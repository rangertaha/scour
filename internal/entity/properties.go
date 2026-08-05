// SPDX-License-Identifier: GPL-3.0-or-later

package entity

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// What is known about an entity beyond its name, and what kinds of thing the
// graph holds.
//
// # Why a value is part of the key
//
// Because two sources will disagree, and a store that let the last crawl win
// would answer differently depending on what was crawled most recently, with
// nothing to show that anything had changed. Two spellings of a company's
// domain are two rows with a count and a provenance trail each. Deciding
// between them is what a person does with the counts in front of them, and it
// is the same reason a merge here is a row rather than a rewrite.
//
// # Why there is no schema for them
//
// A property name is whatever the job document called the property. Requiring
// a declaration would mean two jobs that both know a person's role could not
// say so until somebody registered "role", and the graph's value is that jobs
// which have never met agree about who Acme is without coordinating.

// Property is one thing said about an entity or about a relation.
type Property struct {
	// Name is what was said about it: "role", "domain", "since".
	Name string

	// Value is what was said.
	Value string

	// Position is where the job document declared it, and what
	// [Store.Properties] orders by.
	//
	// Ordering is part of what a shape says. A person's role before their beat
	// is somebody's judgement about which matters, and a graph that returned
	// them by how often each had been asserted would answer a question about
	// the shape with a popularity contest. Two jobs that disagree keep the
	// earlier position, so the answer does not depend on which crawled last.
	Position int

	// Assertions is how many times something said it.
	Assertions int

	First time.Time
	Last  time.Time
}

// Kind is one sort of thing the graph holds, and how much of it there is.
//
// The list is derived rather than declared, for the reason above: a kind exists
// because something asserted an entity of it.
type Kind struct {
	Name string

	// Entities is how many distinct entities have this kind, counted after
	// merges, so two spellings of one person are one.
	Entities int
}

// RelationKind is one sort of edge, and how many there are of it.
//
// Edges are typed the way nodes are, and for the same reason: "author" and
// "publisher" are different relations between the same two kinds of thing, and
// a graph that could not say which was which could not answer anything. The
// type is the property's own name, so two jobs cannot invent two words for one
// relation.
type RelationKind struct {
	Name string

	// Relations is how many distinct edges have this type, counted after
	// merges at both ends.
	Relations int
}

// Describe records something known about an entity or about a relation.
//
// The subject is an entity id or a relation id, and it has to already exist:
// describing something nothing has asserted would create a row with a name
// nobody said, and this store's whole claim is that every row came from
// somewhere. [Store.Assert] and [Store.Relate] are what bring one into
// existence.
//
// One call for both because a property of an edge is the same kind of statement
// as a property of a node: a name, a value, who said it and how often. The
// position is where the job document declared it, and two jobs that disagree
// keep the earlier one, so the order does not depend on which crawled last.
//
// An entity subject is written against its canonical id, so describing either
// spelling of a merged pair describes the pair.
func (s *graph) Describe(ctx context.Context, subject, name, value string, position int, said Provenance) error {
	if name == TopicProperty {
		// Reserved, so that what a job declares and what a classifier decided
		// cannot end up in the same rows meaning different things. Refused here
		// rather than documented, because a convention nobody enforces is one
		// somebody eventually writes through.
		return fmt.Errorf(
			"entity: %q is reserved for what a topic classifier decided. Use Tag, or rename the property",
			TopicProperty)
	}
	return s.describe(ctx, subject, name, value, position, said)
}

// describe is the write itself, without the reserved-name check, so [graph.Tag]
// can record the one name Describe refuses.
func (s *graph) describe(ctx context.Context, subject, name, value string, position int, said Provenance) error {
	if subject == "" || name == "" {
		return errors.New("entity: a property needs a subject and a name")
	}
	if said.Job == "" || said.URL == "" {
		return errors.New("entity: an assertion needs to say who said it and where")
	}
	if said.At.IsZero() {
		said.At = time.Now().UTC()
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("entity: %w", err)
	}
	defer tx.Rollback()

	// An entity subject resolves through its merges; a relation id is already
	// what it is, and resolving one would find nothing.
	subject, err = subjectOf(ctx, tx, subject)
	if err != nil {
		return err
	}

	at := said.At.UnixNano()
	if _, err := tx.ExecContext(ctx, s.sql.Rebind(`
INSERT INTO properties (subject, name, value, position, first_seen, last_seen, assertions)
VALUES (?, ?, ?, ?, ?, ?, 1)
ON CONFLICT (subject, name, value) DO UPDATE SET
	last_seen  = `+s.sql.Greatest("properties.last_seen", "excluded.last_seen")+`,
	first_seen = `+s.sql.Least("properties.first_seen", "excluded.first_seen")+`,
	position   = `+s.sql.Least("properties.position", "excluded.position")+`,
	assertions = properties.assertions + 1`),
		subject, name, value, position, at, at); err != nil {
		return fmt.Errorf("entity: describe %s: %w", subject, err)
	}

	if _, err := tx.ExecContext(ctx, `
INSERT INTO property_assertions (job, url, spec, said_at, subject, name, value)
VALUES (?, ?, ?, ?, ?, ?, ?)`,
		said.Job, said.URL, said.Spec, at, subject, name, value); err != nil {
		return fmt.Errorf("entity: describe %s: %w", subject, err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("entity: %w", err)
	}
	return nil
}

// Properties is everything said about an entity or a relation, in the order the
// shape declared.
//
// For an entity, across the alias family like every other read here: what is
// known about a person is known about them whichever spelling it was said
// under.
//
// Ordered by position and then by name and value. By position because ordering
// is part of what a shape says and a graph that returned properties by how
// often each was asserted would answer a question about the shape with a
// popularity contest; by name and value after it so that two properties
// declared at the same position, which is what two jobs disagreeing produces,
// still come back in the same order on every run.
func (s *graph) Properties(ctx context.Context, subject string) ([]Property, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT p.name, p.value, MIN(p.position), SUM(p.assertions), MIN(p.first_seen), MAX(p.last_seen)
  FROM properties p
 WHERE p.subject IN (
       SELECT id FROM resolved
        WHERE canonical = (SELECT canonical FROM resolved WHERE id = ?)
       UNION SELECT ?)
 GROUP BY p.name, p.value
 ORDER BY MIN(p.position), p.name, p.value`, subject, subject)
	if err != nil {
		return nil, fmt.Errorf("entity: properties %s: %w", subject, err)
	}
	defer rows.Close()

	var out []Property
	for rows.Next() {
		var p Property
		var first, last int64
		if err := rows.Scan(&p.Name, &p.Value, &p.Position, &p.Assertions, &first, &last); err != nil {
			return nil, fmt.Errorf("entity: properties %s: %w", subject, err)
		}
		p.First = time.Unix(0, first).UTC()
		p.Last = time.Unix(0, last).UTC()
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("entity: properties %s: %w", subject, err)
	}
	return out, nil
}

// subjectOf resolves an entity subject through its merges, and leaves a
// relation subject alone.
//
// A relation id is derived from its two ends and its type rather than from a
// name, so it is not in the alias family and resolving one would find nothing.
func subjectOf(ctx context.Context, tx *sql.Tx, subject string) (string, error) {
	// A type, which is a subject in its own right: "person" being about
	// climate is a different claim from any particular person being about it.
	// Checked against what has actually been asserted, so a type nobody has
	// ever seen is refused the way a mistyped entity id is.
	if kind, ok := strings.CutPrefix(subject, KindPrefix); ok {
		var seen int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM entities WHERE kind = ?`, kind).Scan(&seen); err != nil {
			return "", fmt.Errorf("entity: describe: %w", err)
		}
		if seen == 0 {
			return "", fmt.Errorf("entity: no kind %q in the graph", kind)
		}
		return subject, nil
	}

	var exists int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM relations WHERE id = ?`, subject).Scan(&exists); err != nil {
		return "", fmt.Errorf("entity: describe: %w", err)
	}
	if exists > 0 {
		return subject, nil
	}

	id, _, err := canonical(ctx, tx, subject)
	return id, err
}

// Relations is every edge out of an entity, in the order the shape declared.
//
// Returns the edges themselves rather than what is at the far end, which
// [Store.Related] does. Both exist because they answer different questions: who
// did this publisher publish is the far end, and what does this edge say is the
// edge, and the second is only askable now that an edge has an id and can carry
// properties of its own.
func (s *graph) Relations(ctx context.Context, id string) ([]Relation, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT r.id, r.from_id, r.to_id, r.kind, r.topic, r.position,
       r.assertions, r.first_seen, r.last_seen
  FROM relations r
  JOIN resolved rf ON rf.id = r.from_id
 WHERE rf.canonical = (SELECT canonical FROM resolved WHERE id = ?)
 ORDER BY r.position, r.kind, r.topic`, id)
	if err != nil {
		return nil, fmt.Errorf("entity: relations %s: %w", id, err)
	}
	defer rows.Close()

	var out []Relation
	for rows.Next() {
		var r Relation
		var first, last int64
		if err := rows.Scan(&r.ID, &r.From, &r.To, &r.Kind, &r.Topic, &r.Position,
			&r.Assertions, &first, &last); err != nil {
			return nil, fmt.Errorf("entity: relations %s: %w", id, err)
		}
		r.First = time.Unix(0, first).UTC()
		r.Last = time.Unix(0, last).UTC()
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("entity: relations %s: %w", id, err)
	}
	return out, nil
}

// RelationKinds is every sort of edge in the graph, with how many of each.
//
// The other half of [Store.Kinds]: edges are typed the way nodes are, and a
// graph you did not build is not readable until you can see both.
func (s *graph) RelationKinds(ctx context.Context) ([]RelationKind, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT r.kind, COUNT(DISTINCT rf.canonical || ':' || rt.canonical)
  FROM relations r
  JOIN resolved rf ON rf.id = r.from_id
  JOIN resolved rt ON rt.id = r.to_id
 GROUP BY r.kind
 ORDER BY COUNT(*) DESC, r.kind`)
	if err != nil {
		return nil, fmt.Errorf("entity: relation kinds: %w", err)
	}
	defer rows.Close()

	var out []RelationKind
	for rows.Next() {
		var k RelationKind
		if err := rows.Scan(&k.Name, &k.Relations); err != nil {
			return nil, fmt.Errorf("entity: relation kinds: %w", err)
		}
		out = append(out, k)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("entity: relation kinds: %w", err)
	}
	return out, nil
}

// Kinds is every sort of thing in the graph, with how many of each.
//
// The way in for somebody who has a graph and does not yet know what is in it,
// which until now was nobody, because nothing could read this store but the
// step that filled it.
func (s *graph) Kinds(ctx context.Context) ([]Kind, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT e.kind, COUNT(DISTINCT r.canonical)
  FROM entities e
  JOIN resolved r ON r.id = e.id
 GROUP BY e.kind
 ORDER BY COUNT(DISTINCT r.canonical) DESC, e.kind`)
	if err != nil {
		return nil, fmt.Errorf("entity: kinds: %w", err)
	}
	defer rows.Close()

	var out []Kind
	for rows.Next() {
		var k Kind
		if err := rows.Scan(&k.Name, &k.Entities); err != nil {
			return nil, fmt.Errorf("entity: kinds: %w", err)
		}
		out = append(out, k)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("entity: kinds: %w", err)
	}
	return out, nil
}
