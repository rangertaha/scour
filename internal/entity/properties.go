// SPDX-License-Identifier: GPL-3.0-or-later

package entity

import (
	"context"
	"errors"
	"fmt"
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

// Property is one thing said about an entity.
type Property struct {
	// Name is what was said about it: "role", "domain", "country".
	Name string

	// Value is what was said.
	Value string

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

// Describe records something known about an entity.
//
// The entity has to exist: describing something nothing has asserted would
// create a row with a name nobody said, and this store's whole claim is that
// every row came from somewhere. [Store.Assert] is what brings one into
// existence.
//
// Written against the entity's canonical id, so describing either spelling of a
// merged pair describes the pair.
func (s *Store) Describe(ctx context.Context, id, name, value string, said Provenance) error {
	if id == "" || name == "" {
		return errors.New("entity: a property needs an entity and a name")
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

	canonical, _, err := canonical(ctx, tx, id)
	if err != nil {
		return err
	}

	at := said.At.UnixNano()
	if _, err := tx.ExecContext(ctx, `
INSERT INTO properties (entity, name, value, first_seen, last_seen, assertions)
VALUES (?, ?, ?, ?, ?, 1)
ON CONFLICT (entity, name, value) DO UPDATE SET
	last_seen  = MAX(properties.last_seen, excluded.last_seen),
	first_seen = MIN(properties.first_seen, excluded.first_seen),
	assertions = properties.assertions + 1`,
		canonical, name, value, at, at); err != nil {
		return fmt.Errorf("entity: describe %s: %w", id, err)
	}

	if _, err := tx.ExecContext(ctx, `
INSERT INTO property_assertions (job, url, spec, said_at, entity, name, value)
VALUES (?, ?, ?, ?, ?, ?, ?)`,
		said.Job, said.URL, said.Spec, at, canonical, name, value); err != nil {
		return fmt.Errorf("entity: describe %s: %w", id, err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("entity: %w", err)
	}
	return nil
}

// Properties is everything said about an entity, most-asserted first.
//
// Across the alias family, like every other read here: what is known about a
// person is known about them whichever spelling it was said under. Ordered by
// how often it was said and then by name and value, so two runs over one corpus
// list them the same way.
func (s *Store) Properties(ctx context.Context, id string) ([]Property, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT p.name, p.value, SUM(p.assertions), MIN(p.first_seen), MAX(p.last_seen)
  FROM properties p
  JOIN resolved r ON r.id = p.entity
 WHERE r.canonical = (SELECT canonical FROM resolved WHERE id = ?)
 GROUP BY p.name, p.value
 ORDER BY SUM(p.assertions) DESC, p.name, p.value`, id)
	if err != nil {
		return nil, fmt.Errorf("entity: properties %s: %w", id, err)
	}
	defer rows.Close()

	var out []Property
	for rows.Next() {
		var p Property
		var first, last int64
		if err := rows.Scan(&p.Name, &p.Value, &p.Assertions, &first, &last); err != nil {
			return nil, fmt.Errorf("entity: properties %s: %w", id, err)
		}
		p.First = time.Unix(0, first).UTC()
		p.Last = time.Unix(0, last).UTC()
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("entity: properties %s: %w", id, err)
	}
	return out, nil
}

// Kinds is every sort of thing in the graph, with how many of each.
//
// The way in for somebody who has a graph and does not yet know what is in it,
// which until now was nobody, because nothing could read this store but the
// step that filled it.
func (s *Store) Kinds(ctx context.Context) ([]Kind, error) {
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
