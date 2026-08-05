// SPDX-License-Identifier: GPL-3.0-or-later

// Package sqlite is the frontier a crawl actually runs on.
//
// # Why a database, and why this one
//
// Bodies are in the cache and the job is in KV. The frontier needs a database
// because of what it has to do at once: dedup by URL, hand out the
// highest-scoring entry whose host is not cooling, lease it with a timeout, and
// survive a restart with all of that intact.
//
// NATS cannot do it. JetStream is a work queue and work queues are FIFO; a
// focused crawl is ranking, and the ranking changes as the model learns, so the
// one thing the frontier must do is the one thing a broker does not.
//
// Politeness decides the rest. A rate limit is per host and shared between
// jobs, so the frontier is single-writer per host by construction, and SQLite is
// what a single writer wants. When one process can no longer keep up, the answer
// is to shard by host, which makes each shard single-writer again.
//
// # One database, not one per job
//
// Per-job files would be tidier and would make dropping a job a delete. Host
// state cannot be partitioned per job without two jobs on one site each getting
// their own allowance, which is exactly what must not happen. So the urls table
// is keyed by job and the hosts table deliberately is not.
//
// # Hand-written SQL
//
// There are half a dozen queries and every one is shaped by an index. An ORM
// would hide the thing most worth looking at, and the lease is a transaction
// with an ordering in it rather than a row fetched by id.
//
// # Locking, and what porting would cost
//
// The lease selects and then updates, and the two have to be one act: two
// schedulers that both chose before either claimed would fetch the same page
// twice. SQLite has no `SELECT ... FOR UPDATE`; it rejects the syntax outright.
// What it has instead is `BEGIN IMMEDIATE`, which takes the write lock when the
// transaction opens rather than when it first writes, and that is what this
// uses. There is one writer by construction, so nothing else can be choosing at
// the same time.
//
// A port to Postgres is the same statements with `FOR UPDATE SKIP LOCKED` added
// to the select, because there the readers are concurrent and the row has to be
// locked rather than the database. That is a line, not a rewrite, and it is
// worth knowing which line.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // the pure-Go driver, so this cross-compiles

	"github.com/rangertaha/scour/internal/frontier"
)

// Name is what this implementation registers as.
const Name = "sqlite"

// File is what the database is called inside the configured directory.
const File = "frontier.db"

// Status values a row can hold. Strings rather than integers because the first
// thing anybody does with this file is open it in a shell and look.
//
// There is no "leased" status, and that is a performance decision rather than a
// modelling one. A leased row is a waiting row that is not ready yet, which it
// says with ready_at. Splitting it into a second status would make the lease
// ask for one status OR another, and an OR over the leading column of an index
// is an index the query cannot use: measured, that was a full scan of the
// frontier on every single lease.
const (
	waiting   = "waiting"
	finished  = "done"
	abandoned = "abandoned"
)

// Frontier is a job's waiting URLs, in SQLite.
type Frontier struct {
	db     *sql.DB
	policy frontier.Policy
	order  string
	rate   time.Duration
}

// Open returns a frontier backed by a database under cfg.Dir, creating both if
// they are not there.
//
// An empty Dir opens a private in-memory database, which is what a test wants
// and what nothing else should use: it is discarded with the handle, and a
// frontier that does not survive a restart is not doing its job.
func Open(cfg frontier.Config) (*Frontier, error) {
	policy, err := frontier.Policies(cfg.Policy)
	if err != nil {
		return nil, err
	}

	order, err := ordering(policy.Name())
	if err != nil {
		return nil, err
	}

	if err := ensureDir(cfg.Dir); err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite", dsn(cfg.Dir))
	if err != nil {
		return nil, fmt.Errorf("frontier/sqlite: open: %w", err)
	}

	// One connection. Every operation here but Len is a write, so pooling buys
	// nothing and costs the whole class of "database is locked" failures that
	// concurrent writers produce. WAL is still what lets another process read
	// the file while a crawl is running.
	db.SetMaxOpenConns(1)

	if err := schema(db); err != nil {
		db.Close()
		return nil, err
	}

	return &Frontier{db: db, policy: policy, order: order, rate: cfg.Rate}, nil
}

func dsn(dir string) string {
	// Pragmas travel in the DSN because the driver applies them per connection,
	// and a pragma set on one connection of a pool is a pragma the next
	// statement may not see.
	//
	// txlock=immediate takes the write lock when a transaction opens rather
	// than when it first writes. The lease reads and then writes, and a
	// deferred transaction that upgrades mid-way is how two writers deadlock.
	const pragmas = "?_pragma=journal_mode(wal)" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=synchronous(normal)" +
		"&_txlock=immediate"

	if dir == "" {
		// Shared cache, or each connection would get a database of its own.
		return "file:frontier?mode=memory&cache=shared&" + pragmas[1:]
	}
	return "file:" + filepath.Join(dir, File) + pragmas
}

// schema creates the tables and the indexes the lease is shaped by.
func schema(db *sql.DB) error {
	const ddl = `
CREATE TABLE IF NOT EXISTS urls (
	job          TEXT    NOT NULL,
	hash         TEXT    NOT NULL,
	url          TEXT    NOT NULL,
	host         TEXT    NOT NULL,
	depth        INTEGER NOT NULL,
	score        REAL    NOT NULL,
	parent       TEXT    NOT NULL DEFAULT '',
	discovered   INTEGER NOT NULL,
	status       TEXT    NOT NULL,
	ready_at     INTEGER NOT NULL DEFAULT 0,
	attempts     INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (job, hash)
);

CREATE TABLE IF NOT EXISTS hosts (
	host    TEXT PRIMARY KEY,
	next_at INTEGER NOT NULL
);

-- One index per ordering, because a policy is an ORDER BY and an ORDER BY
-- without an index is a sort of the whole frontier. All three exist rather than
-- only the configured one, so that changing a job's policy is a restart and not
-- a migration.
--
-- Each is (job, status) equality and then the ordering, and nothing else. Both
-- of the other conditions, whether the row is ready and whether its host is
-- cooling, are left as residuals checked per row: SQLite walks the index in
-- policy order and stops at the first row that passes, which is nearly always
-- the first row it looks at. Putting ready_at in the index ahead of the
-- ordering columns would turn the walk back into a sort.
--
-- The rowid the orderings tie-break on is not named here: SQLite appends it to
-- every index entry already, and naming it is an error.
CREATE INDEX IF NOT EXISTS urls_priority
	ON urls (job, status, score DESC, discovered);
CREATE INDEX IF NOT EXISTS urls_breadth
	ON urls (job, status, depth, discovered);
CREATE INDEX IF NOT EXISTS urls_depth
	ON urls (job, status, depth DESC, discovered DESC);
`
	if _, err := db.Exec(ddl); err != nil {
		return fmt.Errorf("frontier/sqlite: schema: %w", err)
	}
	return nil
}

// ordering is the ORDER BY a policy means.
//
// Built from a closed set rather than from anything a caller supplies. A policy
// that handed SQL to the database would be handing it an injection point and a
// dependency on this schema at once, which is why [frontier.Policy] is an
// interface with four implementations and not a comparison function.
func ordering(policy string) (string, error) {
	switch policy {
	case "priority":
		return "u.score DESC, u.discovered ASC, u.rowid ASC", nil
	case "breadth":
		return "u.depth ASC, u.discovered ASC, u.rowid ASC", nil
	case "depth":
		return "u.depth DESC, u.discovered DESC, u.rowid DESC", nil
	case "random":
		return "RANDOM()", nil
	default:
		return "", fmt.Errorf("frontier/sqlite: no ordering for policy %q", policy)
	}
}

// Policy is the ordering this was built with.
func (f *Frontier) Policy() string { return f.policy.Name() }

// Add implements [frontier.Frontier].
//
// Re-discovering a URL is not news: the first sighting is kept, along with the
// crawl path that found it, which a shallower later one would throw away.
func (f *Frontier) Add(ctx context.Context, job string, reqs ...frontier.Request) (int, error) {
	if len(reqs) == 0 {
		return 0, nil
	}

	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("frontier/sqlite: add: %w", err)
	}
	defer tx.Rollback()

	// DO NOTHING rather than DO UPDATE, so the count of changed rows is the
	// count of URLs that were actually new. That number is what tells a crawl
	// whether it is still finding anything.
	stmt, err := tx.PrepareContext(ctx, `
INSERT INTO urls (job, hash, url, host, depth, score, parent, discovered, status)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, '`+waiting+`')
ON CONFLICT (job, hash) DO NOTHING`)
	if err != nil {
		return 0, fmt.Errorf("frontier/sqlite: add: %w", err)
	}
	defer stmt.Close()

	var added int
	for _, r := range reqs {
		if r.Hash == "" {
			continue
		}
		result, err := stmt.ExecContext(ctx, job, r.Hash, r.URL, r.Host,
			r.Depth, r.Score, r.Parent, r.Discovered.UnixNano())
		if err != nil {
			return 0, fmt.Errorf("frontier/sqlite: add %s: %w", r.URL, err)
		}
		if n, err := result.RowsAffected(); err == nil {
			added += int(n)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("frontier/sqlite: add: %w", err)
	}
	return added, nil
}

// Lease implements [frontier.Frontier].
//
// The query every index in the schema exists for. It is one transaction because
// choosing and claiming have to be the same act: two schedulers that both chose
// before either claimed would fetch the same page twice.
func (f *Frontier) Lease(ctx context.Context, job string, now time.Time, hold time.Duration) (*frontier.Request, error) {
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("frontier/sqlite: lease: %w", err)
	}
	defer tx.Rollback()

	// Politeness is in the WHERE clause rather than in the ordering, and it is
	// not negotiable by the policy: a policy that could override it could
	// hammer one server by choosing badly.
	query := `
SELECT u.hash, u.url, u.host, u.depth, u.score, u.parent, u.discovered
  FROM urls u LEFT JOIN hosts h ON h.host = u.host
 WHERE u.job = ? AND u.status = '` + waiting + `'
   AND u.ready_at <= ?
   AND (h.next_at IS NULL OR h.next_at <= ?)
 ORDER BY ` + f.order + `
 LIMIT 1`

	stamp := now.UnixNano()

	var (
		req        frontier.Request
		discovered int64
	)
	err = tx.QueryRowContext(ctx, query, job, stamp, stamp).Scan(
		&req.Hash, &req.URL, &req.Host, &req.Depth, &req.Score, &req.Parent, &discovered)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, frontier.ErrEmpty
	case err != nil:
		return nil, fmt.Errorf("frontier/sqlite: lease: %w", err)
	}
	req.Discovered = time.Unix(0, discovered).UTC()

	// Held rather than moved to a status of its own: a lease is a row that is
	// waiting and not ready yet.
	if _, err := tx.ExecContext(ctx, `
UPDATE urls SET ready_at = ?, attempts = attempts + 1
 WHERE job = ? AND hash = ?`,
		now.Add(hold).UnixNano(), job, req.Hash); err != nil {
		return nil, fmt.Errorf("frontier/sqlite: lease: %w", err)
	}

	if f.rate > 0 {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO hosts (host, next_at) VALUES (?, ?)
ON CONFLICT (host) DO UPDATE SET next_at = excluded.next_at`,
			req.Host, now.Add(f.rate).UnixNano()); err != nil {
			return nil, fmt.Errorf("frontier/sqlite: lease: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("frontier/sqlite: lease: %w", err)
	}
	return &req, nil
}

// Done implements [frontier.Frontier].
func (f *Frontier) Done(ctx context.Context, job, hash string) error {
	_, err := f.db.ExecContext(ctx,
		`UPDATE urls SET status = '`+finished+`' WHERE job = ? AND hash = ?`, job, hash)
	if err != nil {
		return fmt.Errorf("frontier/sqlite: done: %w", err)
	}
	return nil
}

// Fail implements [frontier.Frontier].
//
// One statement rather than a read and a write, so two workers reporting the
// same request cannot both read "two attempts" and both put it back.
func (f *Frontier) Fail(ctx context.Context, job, hash string) error {
	_, err := f.db.ExecContext(ctx, `
UPDATE urls
   SET status   = CASE WHEN attempts >= ? THEN '`+abandoned+`' ELSE status END,
       ready_at = 0
 WHERE job = ? AND hash = ?`,
		frontier.MaxAttempts, job, hash)
	if err != nil {
		return fmt.Errorf("frontier/sqlite: fail: %w", err)
	}
	return nil
}

// Len implements [frontier.Frontier]: how many are still waiting, leased or not.
func (f *Frontier) Len(ctx context.Context, job string) (int, error) {
	var n int
	err := f.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM urls WHERE job = ? AND status = '`+waiting+`'`,
		job).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("frontier/sqlite: len: %w", err)
	}
	return n, nil
}

// Hosts is how many hosts this frontier is pacing, which is what a benchmark
// needs to know it built the workload it meant to.
func (f *Frontier) Hosts(ctx context.Context) (int, error) {
	var n int
	if err := f.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM hosts`).Scan(&n); err != nil {
		return 0, fmt.Errorf("frontier/sqlite: hosts: %w", err)
	}
	return n, nil
}

// Close implements [frontier.Frontier].
func (f *Frontier) Close() error { return f.db.Close() }

// Remove deletes a job's URLs.
//
// The hosts table is left alone on purpose: it is shared, and another job may
// be on the same site right now. A host row is a few bytes and forgetting one
// costs nothing; forgetting that a site is being paced costs the site.
func (f *Frontier) Remove(ctx context.Context, job string) error {
	if _, err := f.db.ExecContext(ctx, `DELETE FROM urls WHERE job = ?`, job); err != nil {
		return fmt.Errorf("frontier/sqlite: remove %s: %w", job, err)
	}
	return nil
}

// Path is where a directory's database lives, which is what an operator asks
// before they go looking for it.
func Path(dir string) string {
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, File)
}

// ensureDir creates the directory before the driver is asked for a file inside
// it, so a missing parent reads as a missing parent rather than as SQLite
// failing to open something.
func ensureDir(dir string) error {
	if dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("frontier/sqlite: %w", err)
	}
	return nil
}

// Plan returns SQLite's query plan for the lease.
//
// Exported because the lease being served by an index rather than by a sort is
// a property worth a test rather than a benchmark somebody remembers to read. A
// crawl leases once per page, so the difference between walking an index and
// sorting the frontier is the difference between a crawl that scales and one
// that stops.
func (f *Frontier) Plan(ctx context.Context, job string) ([]string, error) {
	query := `
SELECT u.hash FROM urls u LEFT JOIN hosts h ON h.host = u.host
 WHERE u.job = ? AND u.status = '` + waiting + `'
   AND u.ready_at <= ?
   AND (h.next_at IS NULL OR h.next_at <= ?)
 ORDER BY ` + f.order + `
 LIMIT 1`

	rows, err := f.db.QueryContext(ctx, "EXPLAIN QUERY PLAN "+query, job, 0, 0)
	if err != nil {
		return nil, fmt.Errorf("frontier/sqlite: plan: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			return nil, fmt.Errorf("frontier/sqlite: plan: %w", err)
		}
		out = append(out, detail)
	}
	return out, rows.Err()
}
