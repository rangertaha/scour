// SPDX-License-Identifier: GPL-3.0-or-later

// Package entity is what the things a crawl extracts refer to.
//
// A byline is text. The author is a person, and the same person appears in a
// thousand articles under four spellings. This is where that person is, so that
// "which authors has this publisher published on this topic" is a query rather
// than a grep.
//
// # Everything is an assertion
//
// Nothing here is a fact. Every row says: this job, on this page, under this
// item fingerprint, at this time, said this. That is what makes a wrong entity
// correctable rather than something that has quietly become part of the data,
// and it is what lets one job's contribution be removed with a single delete.
//
// An entity store fed by extraction and feeding extraction is a loop that can
// teach itself something wrong and keep confirming it. Provenance is what makes
// that recoverable.
//
// # What this is, and what it is not yet
//
// This is the contained piece: typed entities, typed relations, and assertions
// with provenance. It answers which authors a publisher has published for
// nothing.
//
// Identity resolution, deciding that "A. Doe" and "Alex Doe" are one person, is
// the middle piece and is here in the only shape that does not put the rest at
// risk: nothing merges by itself. [Store.Assert] still keys on the name exactly
// as it was written, so two spellings are two rows until somebody calls
// [Store.Merge], and what a merge writes is one alias row rather than a rewrite
// of the ones already there. See resolve.go for the argument.
//
// Recognition and linking, deciding that a span of text is a name at all, is
// the large piece and is not here. Built as one thing this is a year that never
// ships, so it is staged, and saying which stage this is matters more than the
// stage being large.
package entity

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/rangertaha/scour/internal/storage"

	_ "modernc.org/sqlite" // the pure-Go driver, so this cross-compiles
)

// File is what the database is called inside the configured directory.
const File = "entities.db"

// Entity is a thing in the world.
type Entity struct {
	// ID identifies it, derived from its kind and its normalised name so that
	// two jobs asserting the same person converge on one row without
	// coordinating.
	ID string

	// Kind is what sort of thing it is: "person", "company", "exchange".
	Kind string

	// Name is what it is called, as first seen.
	Name string

	// First and Last are when it was first and most recently asserted.
	First time.Time
	Last  time.Time

	// Assertions is how many times something said it exists.
	Assertions int
}

// Relation is an edge between two entities, or between an item and an entity.
type Relation struct {
	// ID identifies the edge, derived from its ends, its type and its topic by
	// [RelationID], so that it can carry properties of its own.
	ID string

	// From and To are entity IDs.
	From string
	To   string

	// Kind is the property's own name: an author property on an article makes
	// an "author" edge. There is nothing else to write, which is deliberate: a
	// relation name each document chose for itself would give a shared graph
	// two words for one thing.
	Kind string

	// Topic is the subject this edge was asserted under, if any, so that
	// "which authors has this publisher published on climate" is answerable.
	Topic string

	// Position is where the job document declared this relation, and what
	// [Store.Relations] and [Store.Related] order by.
	Position int

	// Assertions is how many times something said this edge exists.
	Assertions int

	First time.Time
	Last  time.Time
}

// Provenance is who said something, and on what evidence.
//
// Every assertion carries one. Without it a wrong entity is a fact, and the
// only way to remove one job's mistakes is to rebuild the store.
type Provenance struct {
	// Job is which job said it.
	Job string

	// URL is the page it was read from.
	URL string

	// Spec is the fingerprint of the item shape it was read under, so an
	// assertion made under a shape that has since changed can be found.
	Spec string

	// At is when.
	At time.Time
}

// graph is the SQLite implementation of [Store].
//
// Shared between jobs, which is the whole of its value: two jobs crawling
// different sites should agree about who Acme is. That is also why it is behind
// a service in a cluster rather than a file each node opens.
type graph struct {
	db *sql.DB

	// sql is the little that differs between one database and another. The
	// queries are written once and this renders the handful of places where
	// SQLite and Postgres disagree. See [storage.Dialect].
	sql storage.Dialect
}

// Open returns the store, creating the database if it is not there.
//
// An empty directory opens an in-memory database, which is what a test wants
// and what nothing else should use: the value of this store is that it
// accumulates.
// anonymous numbers the in-memory stores, so each Open("") gets one of its own.
var anonymous atomic.Uint64

// memoryName is a name no other store in this process is using.
//
// The cache stays shared because a store's own handle pool needs it, and only
// the name keeps two stores apart.
func memoryName() string {
	return "entities-" + strconv.FormatUint(anonymous.Add(1), 10)
}

func Open(dir string) (Store, error) {
	if dir != "" {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, fmt.Errorf("entity: %w", err)
		}
	}

	const pragmas = "?_pragma=journal_mode(wal)" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=synchronous(normal)" +
		"&_pragma=foreign_keys(on)" +
		"&_txlock=immediate"

	// A name of its own for each in-memory store, so that two of them are two
	// databases.
	//
	// They used to share one: the name was the constant "entities" and the
	// cache was shared, so every Open("") in a process returned a handle on the
	// same database. Two entities steps in one wave run in parallel goroutines
	// with a handle each, and a shared-cache table lock is not what
	// busy_timeout retries, so the second one failed at once with "database
	// table is locked". A step error makes the pipeline return nothing, so the
	// run discarded every record the crawl had produced. Two unrelated jobs in
	// one process silently wrote into one graph and read each other's entities,
	// which is the quieter half of the same mistake.
	dsn := "file:" + memoryName() + "?mode=memory&cache=shared&" + pragmas[1:]
	if dir != "" {
		dsn = "file:" + filepath.Join(dir, File) + pragmas
	}

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("entity: open: %w", err)
	}
	db.SetMaxOpenConns(1)

	if err := schema(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &graph{db: db, sql: storage.SQLite{}}, nil
}

func schema(db *sql.DB) error {
	const ddl = `
CREATE TABLE IF NOT EXISTS entities (
	id         TEXT PRIMARY KEY,
	kind       TEXT    NOT NULL,
	name       TEXT    NOT NULL,
	first_seen INTEGER NOT NULL,
	last_seen  INTEGER NOT NULL,
	assertions INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS entities_kind ON entities (kind, name);

CREATE TABLE IF NOT EXISTS relations (
	-- id is derived from the four columns below, the way an entity's is
	-- derived from its kind and name. An edge that can be described has to be
	-- nameable, and a composite key is not something a caller can hold.
	id         TEXT    NOT NULL,
	from_id    TEXT    NOT NULL REFERENCES entities (id) ON DELETE CASCADE,
	to_id      TEXT    NOT NULL REFERENCES entities (id) ON DELETE CASCADE,
	kind       TEXT    NOT NULL,
	topic      TEXT    NOT NULL DEFAULT '',
	-- position is where the job document declared this relation. Ordering is
	-- part of what a shape says: an author before a publisher is the author
	-- reading first, and a graph that reordered by how often something was
	-- asserted would answer a question about the shape with a popularity
	-- contest.
	position   INTEGER NOT NULL DEFAULT 0,
	first_seen INTEGER NOT NULL,
	last_seen  INTEGER NOT NULL,
	assertions INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (from_id, to_id, kind, topic)
);

CREATE INDEX IF NOT EXISTS relations_id ON relations (id);

CREATE INDEX IF NOT EXISTS relations_to ON relations (to_id, kind, topic);

-- What is known about an entity beyond its name: a person's role, a company's
-- domain, a place's country.
--
-- The value is part of the key, so two sources that disagree are two rows with
-- a count each rather than one row that flips depending on who was crawled
-- last. This store records what was said and does not decide, which is the same
-- reason a merge is a row rather than a rewrite: deciding is what a person does
-- with the counts in front of them.
CREATE TABLE IF NOT EXISTS properties (
	-- subject is an entity id or a relation id. One table for both, because a
	-- property of an edge is the same kind of statement as a property of a
	-- node: a name, a value, who said it, and how often. Two tables would be
	-- two of every query and two places for the next rule to be forgotten.
	--
	-- No foreign key, because the subject is one of two things and SQLite
	-- cannot say that. Retract deletes what has no subject left, which is the
	-- same sweep it already does for relations and entities.
	subject    TEXT    NOT NULL,
	name       TEXT    NOT NULL,
	value      TEXT    NOT NULL,
	-- position is where the job document declared it, for the reason relations
	-- carry one.
	position   INTEGER NOT NULL DEFAULT 0,
	first_seen INTEGER NOT NULL,
	last_seen  INTEGER NOT NULL,
	assertions INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (subject, name, value)
);

CREATE INDEX IF NOT EXISTS properties_name ON properties (name, value);

-- Provenance for the table above, kept separately from the assertions table
-- rather than sharing it.
--
-- Sharing would have meant a convention: a property row distinguished from an
-- entity row by one of its columns happening to be empty. The count queries
-- there are written against exactly those emptiness tests, so a property whose
-- value happened to equal an entity id would have been counted as a relation.
-- Unlikely is not impossible, and a graph that miscounts is a graph nobody can
-- use as evidence.
CREATE TABLE IF NOT EXISTS property_assertions (
	job     TEXT    NOT NULL,
	url     TEXT    NOT NULL,
	spec    TEXT    NOT NULL DEFAULT '',
	said_at INTEGER NOT NULL,
	subject TEXT    NOT NULL,
	name    TEXT    NOT NULL,
	value   TEXT    NOT NULL
);

CREATE INDEX IF NOT EXISTS property_assertions_job ON property_assertions (job);
CREATE INDEX IF NOT EXISTS property_assertions_subject ON property_assertions (subject);

-- Every assertion, with who said it and on what evidence. This is the table
-- that makes a wrong entity correctable: one job's contribution is one delete,
-- and the counts above are rebuilt from what is left.
CREATE TABLE IF NOT EXISTS assertions (
	job      TEXT    NOT NULL,
	url      TEXT    NOT NULL,
	spec     TEXT    NOT NULL DEFAULT '',
	said_at  INTEGER NOT NULL,
	subject  TEXT    NOT NULL,
	object   TEXT    NOT NULL DEFAULT '',
	kind     TEXT    NOT NULL DEFAULT '',
	topic    TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS assertions_job ON assertions (job);
CREATE INDEX IF NOT EXISTS assertions_subject ON assertions (subject);

-- A merge is an assertion too, so it is a row here rather than a rewrite of
-- the rows above. Both entities keep their assertions and their provenance,
-- and undoing a merge made on bad evidence is deleting one row.
--
-- The foreign keys are what keep this honest without a sweeper: retracting the
-- job that asserted a spelling deletes the entity, and the merge that spelling
-- was part of goes with it.
CREATE TABLE IF NOT EXISTS aliases (
	alias      TEXT PRIMARY KEY REFERENCES entities (id) ON DELETE CASCADE,
	canonical  TEXT    NOT NULL REFERENCES entities (id) ON DELETE CASCADE,
	rule       TEXT    NOT NULL DEFAULT '',
	job        TEXT    NOT NULL,
	url        TEXT    NOT NULL,
	spec       TEXT    NOT NULL DEFAULT '',
	said_at    INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS aliases_canonical ON aliases (canonical);
CREATE INDEX IF NOT EXISTS aliases_job ON aliases (job);

-- What every read goes through, so that following a merge is a join rather
-- than a recursive walk in each of five queries. An alias never points at
-- another alias, which [Store.Merge] maintains on write precisely so that this
-- can be one join deep and stay legible at three in the morning.
CREATE VIEW IF NOT EXISTS resolved AS
SELECT e.id AS id, COALESCE(a.canonical, e.id) AS canonical
  FROM entities e LEFT JOIN aliases a ON a.alias = e.id;
`
	if _, err := db.Exec(ddl); err != nil {
		return fmt.Errorf("entity: schema: %w", err)
	}
	return nil
}

// ID is what a kind and a name resolve to.
//
// Derived rather than allocated, so two jobs asserting the same person converge
// on one row without coordinating and without either of them having to look
// first. The cost is that identity is exactly name equality after
// normalisation, and everything beyond that is a merge somebody made: see
// [Store.Merge]. Deliberately not resolution-aware, because an ID that changed
// when a merge happened would mean an assertion could no longer be found by the
// spelling it was made under, and that is what [Store.Retract] needs.
func ID(kind, name string) string {
	// The kind is trimmed as well as lowered, so that `entity = "person "` in a
	// document is the same type as `entity = "person"`. It was not, and KindID
	// trims, so an entity asserted with a stray space could never be tagged:
	// Tag said there was no such kind while the caller had used the identical
	// string.
	sum := sha256.Sum256([]byte(normaliseKind(kind) + "\x00" + normalise(name)))
	return hex.EncodeToString(sum[:12])
}

// normaliseKind is how a type name is compared: trimmed and lowered.
//
// # Every method that takes a kind goes through this one
//
// Not "should": there is no other spelling of the rule anywhere in the package,
// and a new method that lowercases a kind by hand is a bug even though it will
// compile, pass review and look exactly like the line above it.
//
// Four methods did lowercase by hand. `Assert` and [KindID] trimmed as well, so
// an entity asserted from `entity = "person "` was stored under `person`, and
// `Kind`, `Related`, `Candidates` and [RelationID] then could not see it: they
// asked for `person ` and the store answered, correctly, that it had nothing
// of that type. Nothing failed anywhere. A job's entire entity graph came back
// empty and the document that produced it was perfectly valid, so the only
// visible symptom was a feature that appeared not to work.
//
// The suite holds it now: `AKindIsTheSameKindWhoeverSpelledIt` walks every
// method taking a kind and asks each one with a stray space, so the next method
// added is caught by the build rather than by somebody's empty graph.
func normaliseKind(kind string) string {
	return strings.ToLower(strings.TrimSpace(kind))
}

// normalise is how much of a name is compared: case, surrounding space, and
// runs of space inside it. Nothing cleverer, because anything cleverer is
// identity resolution and is a decision this stage does not make.
func normalise(name string) string {
	return strings.ToLower(strings.Join(strings.Fields(name), " "))
}

// Assert records that something exists, and returns its ID.
func (s *graph) Assert(ctx context.Context, kind, name string, from Provenance) (string, error) {
	if strings.TrimSpace(kind) == "" || strings.TrimSpace(name) == "" {
		return "", errors.New("entity: an entity needs a kind and a name")
	}
	if from.Job == "" || from.URL == "" {
		return "", errors.New("entity: an assertion needs to say who said it and where")
	}
	if from.At.IsZero() {
		from.At = time.Now().UTC()
	}

	id := ID(kind, name)
	at := from.At.UnixNano()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("entity: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, s.sql.Rebind(`
INSERT INTO entities (id, kind, name, first_seen, last_seen, assertions)
VALUES (?, ?, ?, ?, ?, 1)
ON CONFLICT (id) DO UPDATE SET
	last_seen  = `+s.sql.Greatest("last_seen", "excluded.last_seen")+`,
	first_seen = `+s.sql.Least("first_seen", "excluded.first_seen")+`,
	assertions = assertions + 1`),
		id, normaliseKind(kind), strings.TrimSpace(name), at, at); err != nil {
		return "", fmt.Errorf("entity: assert %s: %w", name, err)
	}

	if err := record(ctx, tx, from, id, "", "", ""); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("entity: %w", err)
	}
	return id, nil
}

// Relate records an edge between two entities.
func (s *graph) Relate(ctx context.Context, from, to, kind, topic string, position int, said Provenance) (string, error) {
	if from == "" || to == "" || strings.TrimSpace(kind) == "" {
		return "", errors.New("entity: a relation needs two entities and a kind")
	}
	if said.Job == "" || said.URL == "" {
		return "", errors.New("entity: an assertion needs to say who said it and where")
	}
	if said.At.IsZero() {
		said.At = time.Now().UTC()
	}
	at := said.At.UnixNano()
	kind = normaliseKind(kind)
	id := RelationID(from, to, kind, topic)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("entity: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, s.sql.Rebind(`
INSERT INTO relations (id, from_id, to_id, kind, topic, position, first_seen, last_seen, assertions)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1)
ON CONFLICT (from_id, to_id, kind, topic) DO UPDATE SET
	last_seen  = `+s.sql.Greatest("last_seen", "excluded.last_seen")+`,
	first_seen = `+s.sql.Least("first_seen", "excluded.first_seen")+`,
	position   = `+s.sql.Least("position", "excluded.position")+`,
	assertions = assertions + 1`),
		id, from, to, kind, topic, position, at, at); err != nil {
		return "", fmt.Errorf("entity: relate: %w", err)
	}

	if err := record(ctx, tx, said, from, to, kind, topic); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("entity: %w", err)
	}
	return id, nil
}

// RelationID is what an edge resolves to.
//
// Derived from its two ends, its type and its topic, the way an entity's id is
// derived from its kind and its name, and for the same reason: two jobs
// asserting the same edge have to converge on one row without coordinating. It
// also gives an edge something a caller can hold, which a composite key is not,
// and which is what lets a relation carry properties of its own.
func RelationID(from, to, kind, topic string) string {
	sum := sha256.Sum256([]byte(from + "\x00" + to + "\x00" +
		normaliseKind(kind) + "\x00" + topic))
	return hex.EncodeToString(sum[:12])
}

func record(ctx context.Context, tx *sql.Tx, from Provenance, subject, object, kind, topic string) error {
	_, err := tx.ExecContext(ctx, `
INSERT INTO assertions (job, url, spec, said_at, subject, object, kind, topic)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		from.Job, from.URL, from.Spec, from.At.UnixNano(), subject, object, kind, topic)
	if err != nil {
		return fmt.Errorf("entity: provenance: %w", err)
	}
	return nil
}

// Get returns one entity.
//
// By what it has been merged into, so an id that was merged away answers with
// the entity it is now part of rather than with a row nothing else will ever
// mention again. The counts are summed over the family: after "A. Doe" is
// merged into "Alex Doe", how many times something said this person exists is
// how many times something said either.
func (s *graph) Get(ctx context.Context, id string) (*Entity, error) {
	var (
		out         Entity
		first, last int64
	)
	err := s.db.QueryRowContext(ctx, `
SELECT c.id, c.kind, c.name, MIN(e.first_seen), MAX(e.last_seen), SUM(e.assertions)
  FROM entities e
  JOIN resolved r ON r.id = e.id
  JOIN entities c ON c.id = r.canonical
 WHERE r.canonical = (SELECT canonical FROM resolved WHERE id = ?)
 GROUP BY c.id`, id).
		Scan(&out.ID, &out.Kind, &out.Name, &first, &last, &out.Assertions)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, fmt.Errorf("entity: no such entity %q", id)
	case err != nil:
		return nil, fmt.Errorf("entity: %w", err)
	}

	out.First = time.Unix(0, first).UTC()
	out.Last = time.Unix(0, last).UTC()
	return &out, nil
}

// Find returns an entity by kind and name, which is what extraction has.
//
// A spelling that has been merged away finds the person it was merged into, so
// a crawl reading "A. Doe" off a page gets Alex Doe once somebody has said they
// are the same. Not finding anything is an ordinary answer and never an
// obstacle: see [Store.Candidates] for why nothing here gates on it.
func (s *graph) Find(ctx context.Context, kind, name string) (*Entity, error) {
	return s.Get(ctx, ID(kind, name))
}

// Related answers the question this store exists for: which entities are on the
// other end of an edge of this kind, most asserted first.
//
// "Which authors has this publisher published, on this topic" is this call.
//
// Both ends go through the merges, so an edge asserted against a spelling that
// has since been merged away counts towards the person it was merged into. An
// author who appeared under two bylines is one author here with one count,
// which is the whole reason to merge anything.
func (s *graph) Related(ctx context.Context, id, kind, topic string) ([]*Entity, error) {
	query := `
SELECT c.id, c.kind, c.name, c.first_seen, c.last_seen, SUM(r.assertions)
  FROM relations r
  JOIN resolved rf ON rf.id = r.from_id
  JOIN resolved rt ON rt.id = r.to_id
  JOIN entities c  ON c.id  = rt.canonical
 WHERE rf.canonical = (SELECT canonical FROM resolved WHERE id = ?) AND r.kind = ?`
	args := []any{id, normaliseKind(kind)}

	if topic != "" {
		query += ` AND r.topic = ?`
		args = append(args, topic)
	}
	// By the position the shape declared, then by name. Ordering is part of
	// what a shape says, and returning the most-asserted edge first would
	// answer a question about the shape with a popularity contest.
	query += ` GROUP BY c.id ORDER BY MIN(r.position), c.name ASC`

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("entity: related: %w", err)
	}
	defer rows.Close()

	var out []*Entity
	for rows.Next() {
		var (
			one         Entity
			first, last int64
		)
		if err := rows.Scan(&one.ID, &one.Kind, &one.Name, &first, &last, &one.Assertions); err != nil {
			return nil, fmt.Errorf("entity: related: %w", err)
		}
		one.First, one.Last = time.Unix(0, first).UTC(), time.Unix(0, last).UTC()
		out = append(out, &one)
	}
	return out, rows.Err()
}

// Kind lists every entity of one kind, most asserted first.
//
// One row per person rather than one per spelling, because a merged name is no
// longer somebody the store can be asked about separately. A caller that wants
// the spellings back asks [Store.Aliases].
func (s *graph) Kind(ctx context.Context, kind string) ([]*Entity, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT c.id, c.kind, c.name, MIN(e.first_seen), MAX(e.last_seen), SUM(e.assertions)
  FROM entities e
  JOIN resolved r ON r.id = e.id
  JOIN entities c ON c.id = r.canonical
 WHERE e.kind = ?
 GROUP BY c.id
 ORDER BY SUM(e.assertions) DESC, c.name ASC`, normaliseKind(kind))
	if err != nil {
		return nil, fmt.Errorf("entity: kind: %w", err)
	}
	defer rows.Close()

	var out []*Entity
	for rows.Next() {
		var (
			one         Entity
			first, last int64
		)
		if err := rows.Scan(&one.ID, &one.Kind, &one.Name, &first, &last, &one.Assertions); err != nil {
			return nil, fmt.Errorf("entity: kind: %w", err)
		}
		one.First, one.Last = time.Unix(0, first).UTC(), time.Unix(0, last).UTC()
		out = append(out, &one)
	}
	return out, rows.Err()
}

// Provenances lists what was said about an entity, and by whom.
//
// Over every spelling merged into it, because a merge keeps both trails rather
// than replacing one with the other. That is what somebody checking a merge
// needs: the evidence for each side, still attributed to whoever produced it.
func (s *graph) Provenances(ctx context.Context, id string) ([]Provenance, error) {
	rows, err := s.db.QueryContext(ctx, `
WITH family AS (
	SELECT id FROM resolved
	 WHERE canonical = (SELECT canonical FROM resolved WHERE id = ?)
)
SELECT job, url, spec, said_at FROM assertions
 WHERE subject IN (SELECT id FROM family) OR object IN (SELECT id FROM family)
 ORDER BY said_at DESC`, id)
	if err != nil {
		return nil, fmt.Errorf("entity: provenance: %w", err)
	}
	defer rows.Close()

	var out []Provenance
	for rows.Next() {
		var (
			one Provenance
			at  int64
		)
		if err := rows.Scan(&one.Job, &one.URL, &one.Spec, &at); err != nil {
			return nil, fmt.Errorf("entity: provenance: %w", err)
		}
		one.At = time.Unix(0, at).UTC()
		out = append(out, one)
	}
	return out, rows.Err()
}

// Retract removes everything one job ever said.
//
// The reason every assertion carries provenance. A job that was extracting the
// wrong field for a week is one delete, and what other jobs said is untouched.
// The counts are rebuilt from the assertions that remain, and an entity nobody
// asserts any more goes with them.
//
// A merge that job made is one of the things it said, so its alias rows go too.
// The alternative, leaving them, would keep two people collapsed into one on
// the strength of evidence that has just been withdrawn, which is the failure
// this whole call exists to make recoverable. A merge somebody else made
// against a spelling this job was the only asserter of is deleted by the
// foreign key rather than by a sweep, since there is no longer anything at one
// end of it.
func (s *graph) Retract(ctx context.Context, job string) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("entity: %w", err)
	}
	defer tx.Rollback()

	var removed int64
	for _, statement := range []string{
		`DELETE FROM assertions WHERE job = ?`,
		`DELETE FROM property_assertions WHERE job = ?`,
		`DELETE FROM aliases WHERE job = ?`,
	} {
		result, err := tx.ExecContext(ctx, statement, job)
		if err != nil {
			return 0, fmt.Errorf("entity: retract %s: %w", job, err)
		}
		count, _ := result.RowsAffected()
		removed += count
	}

	for _, statement := range []string{
		// Counts first, from what is left.
		`UPDATE entities SET assertions =
			(SELECT COUNT(*) FROM assertions a WHERE a.subject = entities.id AND a.object = '')`,
		`UPDATE relations SET assertions =
			(SELECT COUNT(*) FROM assertions a
			  WHERE a.subject = relations.from_id AND a.object = relations.to_id
			    AND a.kind = relations.kind AND a.topic = relations.topic)`,
		`UPDATE properties SET assertions =
			(SELECT COUNT(*) FROM property_assertions p
			  WHERE p.subject = properties.subject AND p.name = properties.name
			    AND p.value = properties.value)`,
		// Then what nobody asserts any more.
		`DELETE FROM relations WHERE assertions = 0`,
		`DELETE FROM properties WHERE assertions = 0`,
		`DELETE FROM entities WHERE assertions = 0
		   AND id NOT IN (SELECT from_id FROM relations UNION SELECT to_id FROM relations)`,
		// And last, what has lost its subject.
		//
		// Last, because the entity delete above is what makes a subject
		// disappear: sweeping first left a property whose entity was removed in
		// the same Retract, still readable, and reattaching silently if the
		// same name was asserted again, since ids are derived from the name.
		//
		// A subject is an entity, a relation, or a type, and SQLite cannot
		// express a foreign key to one of three, so the sweep is here rather
		// than in a cascade. A type is the one that has no row of its own: it
		// is a `kind:` id, so the check is that some entity still has that
		// kind. Without that clause every topic anybody put on a type was
		// deleted by the next Retract of any job at all, while the assertion
		// that made it survived, so nothing could ever restore it.
		`DELETE FROM properties
		  WHERE subject NOT IN (SELECT id FROM entities UNION SELECT id FROM relations)
		    AND subject NOT IN (SELECT ` + "'" + KindPrefix + `' || kind FROM entities)`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return 0, fmt.Errorf("entity: retract %s: %w", job, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("entity: %w", err)
	}
	return removed, nil
}

// Close releases the database.
func (s *graph) Close() error { return s.db.Close() }
