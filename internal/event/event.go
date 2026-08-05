// SPDX-License-Identifier: GPL-3.0-or-later

// Package event stores what a crawl measured.
//
// # What an event is
//
// A record is a document: properties, some of them nested, as somebody asked
// for them. An event is the same observation in the shape a time series takes:
// a name, the dimensions to group by, the numbers measured, and when. A job
// says which is which by declaring properties as tags or fields, so nothing has
// to be configured twice, and [record.Record.Measure] is what renders one into
// the other.
//
// # Why it is a store of its own and not the entity graph
//
// Because they answer different questions and grow differently. Entities are
// bounded by definition, which is why they are safe to be dimensions; events
// are not bounded at all, and a store shaped for a few thousand people is the
// wrong shape for a price every minute for a year. Keeping them apart means the
// graph stays small enough to reason about while the events grow.
//
// # The time is the event's own
//
// A headline published at nine and crawled at half eleven is an event at nine.
// Getting that wrong makes a replay produce a series that is wrong in a way
// nobody notices for months, so the time comes from the item's declared time
// property when it has one and from the fetch otherwise. That decision is made
// before this store sees anything.
package event

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite" // the pure-Go driver, for the reasons the frontier gives
)

// File is what the database is called inside the configured directory.
const File = "events.db"

// Event is one measurement.
type Event struct {
	// ID identifies it, derived from the name, the tags and the time so that
	// two crawls of one page converge on one row rather than doubling the
	// series. Assigned by the store; anything a caller puts here is ignored.
	ID string `json:"id"`

	// Name is what was measured: the item's name.
	Name string `json:"name"`

	// Tags are the dimensions to group by. Bounded by construction, because a
	// job declares them as entity references or as tags.
	Tags map[string]string `json:"tags,omitempty"`

	// Fields are what was measured.
	Fields map[string]string `json:"fields,omitempty"`

	// At is when it happened, not when it was crawled.
	At time.Time `json:"at"`

	// Job, URL and Spec are who said it, where from, and under which shape.
	// The same provenance the entity graph keeps, and for the same reason: one
	// job's contribution has to be one delete.
	Job  string `json:"job,omitempty"`
	URL  string `json:"url,omitempty"`
	Spec string `json:"spec,omitempty"`
}

// Query narrows a list. Every part is optional, and an empty query is
// everything.
type Query struct {
	// Name limits to one measurement.
	Name string `json:"name,omitempty"`

	// Tags are dimensions that must all match.
	Tags map[string]string `json:"tags,omitempty"`

	// From and Until bound the time, From inclusive and Until exclusive, which
	// is what makes two adjacent windows cover a range without overlapping.
	From  time.Time `json:"from,omitempty"`
	Until time.Time `json:"until,omitempty"`

	// Job limits to one job's contribution.
	Job string `json:"job,omitempty"`

	// Limit caps how many come back. Zero means [DefaultLimit], because an
	// unbounded query against a series is how a service falls over.
	Limit int `json:"limit,omitempty"`
}

// DefaultLimit is how many events a query returns when it does not say.
const DefaultLimit = 1000

// ErrNotFound is what a read returns for an event that is not there.
var ErrNotFound = errors.New("event: no such event")

// Store is the event log.
type Store struct {
	db *sql.DB
}

// anonymous numbers the in-memory stores, so each Open("") gets one of its own.
//
// The entity store learned this the hard way: a shared name meant every Open("")
// in a process returned a handle on one database, two of them in one wave failed
// with "database table is locked", and two unrelated jobs silently wrote into
// one store.
var anonymous atomic.Uint64

// Open returns the store in a directory, creating it if it is not there.
//
// An empty directory opens an in-memory store, which is what a test wants and
// what nothing else should use: an event log that disappears when the process
// does is one every writer believes it wrote to.
func Open(dir string) (*Store, error) {
	if dir != "" {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, fmt.Errorf("event: %w", err)
		}
	}

	const pragmas = "?_pragma=journal_mode(wal)" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=synchronous(normal)" +
		"&_txlock=immediate"

	dsn := "file:events-" + strconv.FormatUint(anonymous.Add(1), 10) +
		"?mode=memory&cache=shared&" + pragmas[1:]
	if dir != "" {
		dsn = "file:" + filepath.Join(dir, File) + pragmas
	}

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("event: open: %w", err)
	}
	db.SetMaxOpenConns(1)

	if err := schema(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func schema(db *sql.DB) error {
	const ddl = `
CREATE TABLE IF NOT EXISTS events (
	id     TEXT PRIMARY KEY,
	name   TEXT    NOT NULL,
	tags   TEXT    NOT NULL DEFAULT '{}',
	fields TEXT    NOT NULL DEFAULT '{}',
	at     INTEGER NOT NULL,
	job    TEXT    NOT NULL DEFAULT '',
	url    TEXT    NOT NULL DEFAULT '',
	spec   TEXT    NOT NULL DEFAULT ''
);

-- Name and time together, because every query starts with "this measurement,
-- over this window" and an index on either alone leaves the other a scan.
CREATE INDEX IF NOT EXISTS events_name_at ON events (name, at);
CREATE INDEX IF NOT EXISTS events_job ON events (job);
`
	if _, err := db.Exec(ddl); err != nil {
		return fmt.Errorf("event: schema: %w", err)
	}
	return nil
}

// ID is what a name, a set of tags and a time resolve to.
//
// Derived rather than allocated, for the reason the entity store gives: two
// crawls of one page have to converge on one row without coordinating, and a
// series that doubled every time somebody re-ran a crawl would be worse than no
// series. The fields are deliberately outside it, so a re-crawl that reads a
// corrected number updates the point rather than adding a second one at the
// same instant.
func ID(name string, tags map[string]string, at time.Time) string {
	var b strings.Builder
	b.WriteString(name)
	b.WriteByte(0)

	keys := make([]string, 0, len(tags))
	for k := range tags {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(tags[k])
		b.WriteByte(0)
	}
	b.WriteString(strconv.FormatInt(at.UTC().UnixNano(), 10))

	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:12])
}

// Put records an event, or updates the one it is the same as.
//
// The create and the update of CRUD are one call, because the identity is
// derived from the observation rather than allocated: a caller that had to know
// whether a point already existed would have to read before every write, and a
// crawl re-reading a page is the ordinary case rather than the exception. The
// id is returned so a caller can read it back.
func (s *Store) Put(ctx context.Context, e Event) (string, error) {
	if strings.TrimSpace(e.Name) == "" {
		return "", errors.New("event: an event needs a name")
	}
	if e.At.IsZero() {
		return "", errors.New("event: an event needs a time, and it is the event's own time and not now")
	}

	id := ID(e.Name, e.Tags, e.At)

	tags, err := encode(e.Tags)
	if err != nil {
		return "", err
	}
	fields, err := encode(e.Fields)
	if err != nil {
		return "", err
	}

	if _, err := s.db.ExecContext(ctx, `
INSERT INTO events (id, name, tags, fields, at, job, url, spec)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (id) DO UPDATE SET
	fields = excluded.fields,
	job    = excluded.job,
	url    = excluded.url,
	spec   = excluded.spec`,
		id, e.Name, tags, fields, e.At.UTC().UnixNano(), e.Job, e.URL, e.Spec); err != nil {
		return "", fmt.Errorf("event: put %s: %w", e.Name, err)
	}
	return id, nil
}

// Get reads one event.
func (s *Store) Get(ctx context.Context, id string) (*Event, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, name, tags, fields, at, job, url, spec FROM events WHERE id = ?`, id)

	out, err := scan(row.Scan)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
	case err != nil:
		return nil, fmt.Errorf("event: get %s: %w", id, err)
	}
	return out, nil
}

// List reads the events a query matches, newest first.
//
// Newest first because that is what somebody looking at a series wants without
// asking, and because a bounded query that returned the oldest would answer
// "what happened when this started" to a question about now.
func (s *Store) List(ctx context.Context, q Query) ([]*Event, error) {
	where := []string{"1 = 1"}
	var args []any

	if q.Name != "" {
		where = append(where, "name = ?")
		args = append(args, q.Name)
	}
	if q.Job != "" {
		where = append(where, "job = ?")
		args = append(args, q.Job)
	}
	if !q.From.IsZero() {
		where = append(where, "at >= ?")
		args = append(args, q.From.UTC().UnixNano())
	}
	if !q.Until.IsZero() {
		where = append(where, "at < ?")
		args = append(args, q.Until.UTC().UnixNano())
	}

	limit := q.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}

	// Tags are matched in Go rather than in SQL. They live in one JSON column,
	// so matching them in SQL would mean either a join table this store does
	// not need or json_extract per tag per row, and the time and name bounds
	// above are what actually cut the scan down. The limit is applied after
	// the match, so a tag filter cannot silently return fewer than asked for
	// because the rows it wanted were beyond the cut.
	rows, err := s.db.QueryContext(ctx, `
SELECT id, name, tags, fields, at, job, url, spec
  FROM events
 WHERE `+strings.Join(where, " AND ")+`
 ORDER BY at DESC, id`, args...)
	if err != nil {
		return nil, fmt.Errorf("event: list: %w", err)
	}
	defer rows.Close()

	out := make([]*Event, 0, min(limit, 64))
	for rows.Next() {
		one, err := scan(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("event: list: %w", err)
		}
		if !matches(one.Tags, q.Tags) {
			continue
		}
		out = append(out, one)
		if len(out) >= limit {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("event: list: %w", err)
	}
	return out, nil
}

// Delete removes one event. Removing one that is not there is not an error: the
// caller wanted it gone and it is.
func (s *Store) Delete(ctx context.Context, id string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM events WHERE id = ?`, id); err != nil {
		return fmt.Errorf("event: delete %s: %w", id, err)
	}
	return nil
}

// Retract removes everything one job contributed, and says how much.
//
// The same promise the entity graph makes: one job's contribution is one
// delete. A job that turns out to have been reading a page wrongly is removed
// without rebuilding the store or reasoning about which points it touched.
func (s *Store) Retract(ctx context.Context, job string) (int64, error) {
	if job == "" {
		return 0, errors.New("event: retract needs a job")
	}

	result, err := s.db.ExecContext(ctx, `DELETE FROM events WHERE job = ?`, job)
	if err != nil {
		return 0, fmt.Errorf("event: retract %s: %w", job, err)
	}
	removed, _ := result.RowsAffected()
	return removed, nil
}

// Names is every measurement in the store, with how many points each has.
//
// The way in for somebody who has an event store and does not know what is in
// it, which is the same thing [entity.Store.Kinds] is for the graph.
func (s *Store) Names(ctx context.Context) ([]Series, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT name, COUNT(*), MIN(at), MAX(at)
  FROM events
 GROUP BY name
 ORDER BY COUNT(*) DESC, name`)
	if err != nil {
		return nil, fmt.Errorf("event: names: %w", err)
	}
	defer rows.Close()

	var out []Series
	for rows.Next() {
		var one Series
		var first, last int64
		if err := rows.Scan(&one.Name, &one.Events, &first, &last); err != nil {
			return nil, fmt.Errorf("event: names: %w", err)
		}
		one.First = time.Unix(0, first).UTC()
		one.Last = time.Unix(0, last).UTC()
		out = append(out, one)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("event: names: %w", err)
	}
	return out, nil
}

// Series is one measurement and what the store holds of it.
type Series struct {
	Name   string    `json:"name"`
	Events int       `json:"events"`
	First  time.Time `json:"first"`
	Last   time.Time `json:"last"`
}

// Close releases the store.
func (s *Store) Close() error { return s.db.Close() }

// matches reports whether every wanted tag is present with that value.
func matches(have, want map[string]string) bool {
	for k, v := range want {
		if have[k] != v {
			return false
		}
	}
	return true
}

func encode(m map[string]string) (string, error) {
	if len(m) == 0 {
		return "{}", nil
	}
	out, err := json.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("event: %w", err)
	}
	return string(out), nil
}

// scan reads one row, whichever kind of row-reader it came from.
func scan(into func(...any) error) (*Event, error) {
	var (
		e            Event
		tags, fields string
		at           int64
	)
	if err := into(&e.ID, &e.Name, &tags, &fields, &at, &e.Job, &e.URL, &e.Spec); err != nil {
		return nil, err
	}

	if err := json.Unmarshal([]byte(tags), &e.Tags); err != nil {
		return nil, fmt.Errorf("event: %s: tags: %w", e.ID, err)
	}
	if err := json.Unmarshal([]byte(fields), &e.Fields); err != nil {
		return nil, fmt.Errorf("event: %s: fields: %w", e.ID, err)
	}
	e.At = time.Unix(0, at).UTC()
	return &e, nil
}
