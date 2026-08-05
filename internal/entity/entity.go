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
// the middle piece and is not here: names are matched exactly, after
// normalisation, and two spellings are two entities until something merges
// them. Recognition and linking is the large piece and is not here either.
// Built as one thing this is a year that never ships, so it is staged, and
// saying which stage this is matters more than the stage being large.
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
	"strings"
	"time"

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

// Store is the entity graph.
//
// Shared between jobs, which is the whole of its value: two jobs crawling
// different sites should agree about who Acme is. That is also why it is behind
// a service in a cluster rather than a file each node opens.
type Store struct {
	db *sql.DB
}

// Open returns the store, creating the database if it is not there.
//
// An empty directory opens an in-memory database, which is what a test wants
// and what nothing else should use: the value of this store is that it
// accumulates.
func Open(dir string) (*Store, error) {
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

	dsn := "file:entities?mode=memory&cache=shared&" + pragmas[1:]
	if dir != "" {
		dsn = "file:" + filepath.Join(dir, File) + pragmas
	}

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("entity: open: %w", err)
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
	from_id    TEXT    NOT NULL REFERENCES entities (id) ON DELETE CASCADE,
	to_id      TEXT    NOT NULL REFERENCES entities (id) ON DELETE CASCADE,
	kind       TEXT    NOT NULL,
	topic      TEXT    NOT NULL DEFAULT '',
	first_seen INTEGER NOT NULL,
	last_seen  INTEGER NOT NULL,
	assertions INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (from_id, to_id, kind, topic)
);

CREATE INDEX IF NOT EXISTS relations_to ON relations (to_id, kind, topic);

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
// normalisation, which is the piece this stage does not do.
func ID(kind, name string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(kind) + "\x00" + normalise(name)))
	return hex.EncodeToString(sum[:12])
}

// normalise is how much of a name is compared: case, surrounding space, and
// runs of space inside it. Nothing cleverer, because anything cleverer is
// identity resolution and is a decision this stage does not make.
func normalise(name string) string {
	return strings.ToLower(strings.Join(strings.Fields(name), " "))
}

// Assert records that something exists, and returns its ID.
func (s *Store) Assert(ctx context.Context, kind, name string, from Provenance) (string, error) {
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

	if _, err := tx.ExecContext(ctx, `
INSERT INTO entities (id, kind, name, first_seen, last_seen, assertions)
VALUES (?, ?, ?, ?, ?, 1)
ON CONFLICT (id) DO UPDATE SET
	last_seen  = MAX(last_seen, excluded.last_seen),
	first_seen = MIN(first_seen, excluded.first_seen),
	assertions = assertions + 1`,
		id, strings.ToLower(kind), strings.TrimSpace(name), at, at); err != nil {
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
func (s *Store) Relate(ctx context.Context, from, to, kind, topic string, said Provenance) error {
	if from == "" || to == "" || strings.TrimSpace(kind) == "" {
		return errors.New("entity: a relation needs two entities and a kind")
	}
	if said.Job == "" || said.URL == "" {
		return errors.New("entity: an assertion needs to say who said it and where")
	}
	if said.At.IsZero() {
		said.At = time.Now().UTC()
	}
	at := said.At.UnixNano()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("entity: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
INSERT INTO relations (from_id, to_id, kind, topic, first_seen, last_seen, assertions)
VALUES (?, ?, ?, ?, ?, ?, 1)
ON CONFLICT (from_id, to_id, kind, topic) DO UPDATE SET
	last_seen  = MAX(last_seen, excluded.last_seen),
	first_seen = MIN(first_seen, excluded.first_seen),
	assertions = assertions + 1`,
		from, to, strings.ToLower(kind), topic, at, at); err != nil {
		return fmt.Errorf("entity: relate: %w", err)
	}

	if err := record(ctx, tx, said, from, to, strings.ToLower(kind), topic); err != nil {
		return err
	}
	return tx.Commit()
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
func (s *Store) Get(ctx context.Context, id string) (*Entity, error) {
	var (
		out         Entity
		first, last int64
	)
	err := s.db.QueryRowContext(ctx, `
SELECT id, kind, name, first_seen, last_seen, assertions FROM entities WHERE id = ?`, id).
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
func (s *Store) Find(ctx context.Context, kind, name string) (*Entity, error) {
	return s.Get(ctx, ID(kind, name))
}

// Related answers the question this store exists for: which entities are on the
// other end of an edge of this kind, most asserted first.
//
// "Which authors has this publisher published, on this topic" is this call.
func (s *Store) Related(ctx context.Context, id, kind, topic string) ([]*Entity, error) {
	query := `
SELECT e.id, e.kind, e.name, e.first_seen, e.last_seen, r.assertions
  FROM relations r JOIN entities e ON e.id = r.to_id
 WHERE r.from_id = ? AND r.kind = ?`
	args := []any{id, strings.ToLower(kind)}

	if topic != "" {
		query += ` AND r.topic = ?`
		args = append(args, topic)
	}
	query += ` ORDER BY r.assertions DESC, e.name ASC`

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
func (s *Store) Kind(ctx context.Context, kind string) ([]*Entity, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, kind, name, first_seen, last_seen, assertions
  FROM entities WHERE kind = ? ORDER BY assertions DESC, name ASC`, strings.ToLower(kind))
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
func (s *Store) Provenances(ctx context.Context, id string) ([]Provenance, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT job, url, spec, said_at FROM assertions
 WHERE subject = ? OR object = ? ORDER BY said_at DESC`, id, id)
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
func (s *Store) Retract(ctx context.Context, job string) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("entity: %w", err)
	}
	defer tx.Rollback()

	result, err := tx.ExecContext(ctx, `DELETE FROM assertions WHERE job = ?`, job)
	if err != nil {
		return 0, fmt.Errorf("entity: retract %s: %w", job, err)
	}
	removed, _ := result.RowsAffected()

	for _, statement := range []string{
		// Counts first, from what is left.
		`UPDATE entities SET assertions =
			(SELECT COUNT(*) FROM assertions a WHERE a.subject = entities.id AND a.object = '')`,
		`UPDATE relations SET assertions =
			(SELECT COUNT(*) FROM assertions a
			  WHERE a.subject = relations.from_id AND a.object = relations.to_id
			    AND a.kind = relations.kind AND a.topic = relations.topic)`,
		// Then what nobody asserts any more.
		`DELETE FROM relations WHERE assertions = 0`,
		`DELETE FROM entities WHERE assertions = 0
		   AND id NOT IN (SELECT from_id FROM relations UNION SELECT to_id FROM relations)`,
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
func (s *Store) Close() error { return s.db.Close() }
