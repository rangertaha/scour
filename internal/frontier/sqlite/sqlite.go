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
		_ = db.Close()
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

-- hosts is politeness, and it is deliberately not keyed by job: a rate limit is
-- per site, so two jobs crawling one host get one allowance between them.
--
-- delay is what the host asked for in its own robots.txt, kept beside next_at
-- rather than folded into it because they are different facts: next_at is when
-- this host may be touched again and delay is how long it wants to be left
-- alone every time. Folding them would mean re-deriving the second from the
-- first, which cannot be done once the first has been overwritten.
CREATE TABLE IF NOT EXISTS hosts (
	host    TEXT PRIMARY KEY,
	next_at INTEGER NOT NULL,
	delay   INTEGER NOT NULL DEFAULT 0
);

-- One index per ordering, because a policy is an ORDER BY and an ORDER BY
-- without an index is a sort of the whole frontier. All three exist rather than
-- only the configured one, so that changing a job's policy is a restart and not
-- a migration.
--
-- Each is (job, status) equality and then the ordering, and nothing else. Both
-- of the other conditions, whether the row is ready and whether its host is
-- cooling, are left as residuals checked per row, and SQLite walks the index in
-- policy order until one passes. Putting ready_at in the index ahead of the
-- ordering columns would turn the walk back into a sort.
--
-- This comment used to claim the walk stops at "nearly always the first row it
-- looks at". That is true when cooling is independent of the ordering and
-- exactly false under the priority policy, where the ordering causes the
-- cooling: the
-- lease hands out the best-scoring row and then cools that row's host, so the
-- head of this index becomes a run of rows whose hosts are all cooling and
-- which grows by one host's worth on every lease. Measured at 50,000 URLs over
-- 5,000 hosts with a 30s delay, the per-lease cost is linear in how many leases
-- have happened inside one politeness window: 1.7ms after 250, 3.8ms after 500,
-- 8ms after 1,000, 17ms after 2,000, falling back once the window turns over.
-- Total work inside a window is quadratic.
--
-- The guard at the top of [Frontier.Lease] fixes the case where NOTHING is due,
-- which is the common one and was the worst. The case above, where something is
-- due but the best rows are cooling, is not fixed here: it wants the host to be
-- the unit of scheduling rather than the URL, which is a bigger change than an
-- index.
--
-- The rowid the orderings tie-break on is not named here: SQLite appends it to
-- every index entry already, and naming it is an error.
CREATE INDEX IF NOT EXISTS urls_priority
	ON urls (job, status, score DESC, discovered);
CREATE INDEX IF NOT EXISTS urls_breadth
	ON urls (job, status, depth, discovered);
CREATE INDEX IF NOT EXISTS urls_depth
	ON urls (job, status, depth DESC, discovered DESC);

-- Hosts by when they are next free, so that "is anything due at all" is a seek
-- rather than a walk. See the guard at the top of [Frontier.Lease] for what
-- that question costs when it is asked of the urls table instead.
CREATE INDEX IF NOT EXISTS hosts_next_at ON hosts (next_at);
`
	if _, err := db.Exec(ddl); err != nil {
		return fmt.Errorf("frontier/sqlite: schema: %w", err)
	}
	return migrate(db)
}

// schemaVersion is what the DDL above builds. A database recording anything
// lower is brought up to it by [migrate], and one recording this is left alone.
const schemaVersion = 1

// migrate brings a database made by an older build up to the schema above.
//
// `CREATE TABLE IF NOT EXISTS` does nothing to a table that is already there,
// so a column added later reaches new databases and no existing one. A frontier
// is the state a crawl resumes from and there is no acceptable answer that
// involves deleting it, so it is changed in place.
//
// # Why there is a version now
//
// Adding a column can be asked for idempotently: the database is asked whether
// it has one. Backfilling cannot. `hosts` has to hold a row for every host in
// the frontier, because [Frontier.Lease] now answers "is anything due at all"
// from that table alone and a missing row would read as a host that is not
// there rather than one that is free, which loses URLs rather than slowing
// them down. Filling it is a scan of the urls table, and doing that on every
// open to discover there was nothing to do is the cost this version exists to
// skip.
//
// Every step is still written to be safe if it runs twice, because the version
// is the optimisation and not the guarantee.
func migrate(db *sql.DB) error {
	var have int
	if err := db.QueryRow("PRAGMA user_version").Scan(&have); err != nil {
		return fmt.Errorf("frontier/sqlite: read schema version: %w", err)
	}
	if have >= schemaVersion {
		return nil
	}

	added := []struct{ table, column, spec string }{
		{"hosts", "delay", "INTEGER NOT NULL DEFAULT 0"},
	}
	for _, add := range added {
		has, err := hasColumn(db, add.table, add.column)
		if err != nil {
			return err
		}
		if has {
			continue
		}
		if _, err := db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", add.table, add.column, add.spec)); err != nil {
			return fmt.Errorf("frontier/sqlite: add %s.%s: %w", add.table, add.column, err)
		}
	}

	// A row per host already in the frontier. Zero means free, which is what a
	// host that has never been leased is.
	// `WHERE true` is not a filter. SQLite cannot tell where a SELECT ends and
	// an upsert clause begins without one, and rejects the statement outright.
	if _, err := db.Exec(`
INSERT INTO hosts (host, next_at, delay)
SELECT DISTINCT host, 0, 0 FROM urls WHERE true
ON CONFLICT (host) DO NOTHING`); err != nil {
		return fmt.Errorf("frontier/sqlite: fill hosts: %w", err)
	}

	if _, err := db.Exec(fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
		return fmt.Errorf("frontier/sqlite: record schema version: %w", err)
	}
	return nil
}

// hasColumn asks the database what it has, rather than inferring it from a
// failed ALTER: a duplicate-column error and a real one are the same string
// from here, and treating every failure as "already there" would swallow the
// second.
func hasColumn(db *sql.DB, table, column string) (bool, error) {
	rows, err := db.Query(fmt.Sprintf("SELECT 1 FROM pragma_table_info(%q) WHERE name = ?", table), column)
	if err != nil {
		return false, fmt.Errorf("frontier/sqlite: read %s: %w", table, err)
	}
	defer rows.Close()

	has := rows.Next()
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("frontier/sqlite: read %s: %w", table, err)
	}
	return has, nil
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

	// Every host in the frontier has a row in hosts, so that "is anything due
	// at all" can be answered from that table alone. A host with no row would
	// be invisible to that question, and invisible reads as "nothing here"
	// rather than "free", which loses a URL instead of delaying it.
	//
	// Zero means free. Written once per distinct host in the batch rather than
	// once per URL, because a page's links are overwhelmingly to the page's own
	// host and this is on the path every discovered link takes.
	//
	// Unconditional, including for the empty host. The scheduler normalises
	// before it queues, so a URL with no host should never arrive, but "should
	// never" is the wrong footing for the thing a guard's correctness rests on:
	// the invariant is that every host in urls has a row here, and one written
	// only for the hosts that look reasonable is an invariant with a caller in
	// it. A row for the empty host costs nothing and makes the guard sound by
	// construction rather than by argument.
	known, err := tx.PrepareContext(ctx, `
INSERT INTO hosts (host, next_at, delay) VALUES (?, 0, 0)
ON CONFLICT (host) DO NOTHING`)
	if err != nil {
		return 0, fmt.Errorf("frontier/sqlite: add: %w", err)
	}
	defer known.Close()

	var added int
	seen := make(map[string]bool, 1)
	for _, r := range reqs {
		if r.Hash == "" {
			continue
		}
		if !seen[r.Host] {
			seen[r.Host] = true
			if _, err := known.ExecContext(ctx, r.Host); err != nil {
				return 0, fmt.Errorf("frontier/sqlite: add %s: %w", r.URL, err)
			}
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
	// attempts is selected because the count of handouts is what identifies the
	// hold about to be taken, and Done and Fail will only act on a report that
	// still names it. Reading it here costs nothing: the row is being fetched
	// for its URL anyway.
	// Is any host free at all? If none is, nothing can be leased, and saying so
	// from the hosts table costs one seek.
	//
	// Asked of the urls table instead, the same question is a walk of the whole
	// frontier: every row fails the politeness residual, so SQLite reads all of
	// them before concluding there is nothing. Measured on one host with its
	// delay running, that was 0.55ms at a thousand URLs, 5.3ms at ten thousand
	// and 69ms at a hundred thousand, and it is not asked once. A crawl of one
	// site is the commonest shape there is and every worker asks this again
	// every [run.Idle] for the whole of the delay, holding the write lock each
	// time, which also blocks the Add that would queue what the crawl is
	// finding. At a hundred thousand URLs that is more than a core spent
	// proving there is nothing to do.
	//
	// Sound because every host in the frontier has a row here, which
	// [Frontier.Add] maintains and [migrate] backfilled. A ready host does not
	// mean a leasable URL, so this only ever answers "no" early; the query
	// below still decides everything else.
	var free int
	switch err := tx.QueryRowContext(ctx,
		`SELECT 1 FROM hosts WHERE next_at <= ? LIMIT 1`, now.UnixNano()).Scan(&free); {
	case errors.Is(err, sql.ErrNoRows):
		return nil, frontier.ErrEmpty
	case err != nil:
		return nil, fmt.Errorf("frontier/sqlite: lease: %w", err)
	}

	query := `
SELECT u.hash, u.url, u.host, u.depth, u.score, u.parent, u.discovered, u.attempts,
       COALESCE(h.delay, 0)
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
		attempts   int
		delay      int64
	)
	err = tx.QueryRowContext(ctx, query, job, stamp, stamp).Scan(
		&req.Hash, &req.URL, &req.Host, &req.Depth, &req.Score, &req.Parent, &discovered, &attempts, &delay)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, frontier.ErrEmpty
	case err != nil:
		return nil, fmt.Errorf("frontier/sqlite: lease: %w", err)
	}
	req.Discovered = time.Unix(0, discovered).UTC()
	req.Attempt = attempts + 1

	// Held rather than moved to a status of its own: a lease is a row that is
	// waiting and not ready yet.
	//
	// attempts is written as a number rather than as attempts + 1, so that what
	// the caller was handed and what the row says are the same value by
	// construction and not by two expressions agreeing. The transaction took
	// the write lock when it opened, so nothing has changed the row since it
	// was read.
	if _, err := tx.ExecContext(ctx, `
UPDATE urls SET ready_at = ?, attempts = ?
 WHERE job = ? AND hash = ?`,
		now.Add(hold).UnixNano(), req.Attempt, job, req.Hash); err != nil {
		return nil, fmt.Errorf("frontier/sqlite: lease: %w", err)
	}

	// The longer of what this job configured and what the site asked for. Read
	// from the row above rather than from f.rate alone, because a crawl-delay
	// belongs to the host and the rate belongs to the job, and honouring only
	// the second is how `Crawl-delay` came to be parsed and ignored.
	//
	// delay alone can be the reason to write the row, so the guard is on the
	// wait rather than on the rate: a job with no rate of its own still owes a
	// site the delay it asked for.
	if wait := max(f.rate, time.Duration(delay)); wait > 0 {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO hosts (host, next_at) VALUES (?, ?)
ON CONFLICT (host) DO UPDATE SET next_at = excluded.next_at`,
			req.Host, now.Add(wait).UnixNano()); err != nil {
			return nil, fmt.Errorf("frontier/sqlite: lease: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("frontier/sqlite: lease: %w", err)
	}
	return &req, nil
}

// Pace implements [frontier.Frontier].
//
// One upsert, doing both halves at once: it records what the site asked for and
// takes the hold that number implies. Splitting them into a read and a write
// would be a read-then-write across two statements on a row two jobs share,
// which is the shape that has already produced two defects here.
//
// next_at only ever moves forward. `MAX(next_at, ?)` rather than an assignment,
// because a host cooling for longer than this delay is a host somebody else is
// already being polite to, and the shorter of two politeness rules is never the
// answer.
func (f *Frontier) Pace(ctx context.Context, host string, now time.Time, delay time.Duration) error {
	if delay < 0 {
		delay = 0
	}

	// The site's number alone, not the longer of the two.
	//
	// The job's rate has already been applied, by the lease that led here, and
	// measured from when that lease was taken. Applying it again from now would
	// measure it from after the fetch instead, so every host that asked for
	// nothing, which is nearly all of them, would be left alone for a rate plus
	// however long its page took to arrive. That is a slower crawl for every
	// ordinary site, bought by a feature about the unusual ones.
	//
	// A delay shorter than the rate therefore falls out correctly rather than
	// needing a case: it pushes next_at to a moment already passed, and MAX
	// keeps the hold the lease took. The floor lives in [Frontier.Lease], which
	// is where the two numbers are compared for the next handout.
	until := now.Add(delay).UnixNano()

	if _, err := f.db.ExecContext(ctx, `
INSERT INTO hosts (host, next_at, delay) VALUES (?, ?, ?)
ON CONFLICT (host) DO UPDATE SET
	delay   = excluded.delay,
	next_at = MAX(hosts.next_at, excluded.next_at)`,
		host, until, int64(delay)); err != nil {
		return fmt.Errorf("frontier/sqlite: pace %q: %w", host, err)
	}
	return nil
}

// Done implements [frontier.Frontier].
//
// The attempt is in the WHERE clause rather than checked first in Go, so the
// match and the write are one statement and no other connection can slip a
// lease between them.
func (f *Frontier) Done(ctx context.Context, job, hash string, attempt int) error {
	_, err := f.db.ExecContext(ctx,
		`UPDATE urls SET status = '`+finished+`'
		  WHERE job = ? AND hash = ? AND attempts = ?`, job, hash, attempt)
	if err != nil {
		return fmt.Errorf("frontier/sqlite: done: %w", err)
	}
	return nil
}

// Fail implements [frontier.Frontier].
//
// One statement rather than a read and a write, so two workers reporting the
// same request cannot both read "two attempts" and both put it back.
//
// The attempt in the WHERE clause is what stops a late report freeing a URL
// somebody else is fetching. Without it this statement clears ready_at for
// whoever asks, so a worker that stalled past its hold could put back a URL
// that had since been leased again, and the attempt count it drives would be
// coming from a worker that is no longer the holder. No row matching is the
// ordinary outcome for a late report and not an error: the holder is still
// working, and will report for itself.
func (f *Frontier) Fail(ctx context.Context, job, hash string, attempt int) error {
	_, err := f.db.ExecContext(ctx, `
UPDATE urls
   SET status   = CASE WHEN attempts >= ? THEN '`+abandoned+`' ELSE status END,
       ready_at = 0
 WHERE job = ? AND hash = ? AND attempts = ?`,
		frontier.MaxAttempts, job, hash, attempt)
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
