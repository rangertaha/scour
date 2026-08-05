// SPDX-License-Identifier: GPL-3.0-or-later

// Package storage is the little that differs between one SQL database and
// another.
//
// # Why this is small, and why there is no ORM
//
// Because the difference is small. Three stores here are hand-written SQL on
// database/sql, and when the question "what would it take to run on Postgres"
// was actually measured rather than assumed, the answer was: eight scalar
// MAX/MIN calls, the connection string, and the placeholder style. Everything
// else already runs on both. `INSERT ... ON CONFLICT (cols) DO UPDATE SET ...
// excluded.x` is Postgres syntax that SQLite adopted; views, correlated
// subqueries, GROUP BY, TEXT and INTEGER are common ground.
//
// So the portable thing to do was not to put a query builder in front of every
// statement. The queries here are the design: alias resolution goes through a
// view, [entity.Store.Retract] rebuilds counts with correlated subqueries, and
// the upserts are what keep first_seen monotonic while last_seen moves. Under
// an ORM those become raw SQL with a dependency in front of them, which is the
// same code and one more thing to learn.
//
// # What this does not do
//
// It does not make a Postgres backend work. The syntax is the easy half; the
// hard half is that these stores are built around SQLite's single writer, with
// SetMaxOpenConns(1) and an immediate transaction lock, and on Postgres that is
// pessimistic locking nobody needs. The entity merge in particular checks its
// rule inside the transaction that acts on it, which on Postgres wants SELECT
// ... FOR UPDATE rather than a serialised connection.
//
// This package is the syntax, and the conformance suites are what would make a
// second backend believable. Neither is a claim that one exists.
package storage

import (
	"fmt"
	"strconv"
	"strings"
)

// Dialect is what one SQL database does differently from another.
//
// An interface with two implementations rather than a switch on a name, because
// a switch is a thing every new query has to remember to consult and an
// interface is one the compiler asks about. That is the same reason the plugin
// registries exist here.
type Dialect interface {
	// Name is what this dialect is called, for errors and for tests.
	Name() string

	// Greatest and Least are the row-wise maximum and minimum of two
	// expressions.
	//
	// SQLite spells them MAX and MIN, the same words as the aggregates, and
	// tells them apart by how many arguments there are. Postgres refuses that
	// and calls them GREATEST and LEAST. This is the one piece of SQL in these
	// stores that cannot be written once, and it appears only in the upserts
	// that keep first_seen monotonic while last_seen moves forward.
	Greatest(a, b string) string
	Least(a, b string) string

	// MergeJSON is the object made of two JSON objects, the second winning
	// where they name the same key.
	//
	// SQLite spells it json_patch and Postgres spells it `||` on jsonb, and
	// both mean the same thing for flat objects, which is all these stores
	// hold.
	MergeJSON(a, b string) string

	// Rebind turns ? placeholders into whatever this database expects.
	//
	// Written in ? because that is what database/sql's own documentation uses
	// and what SQLite takes, and rewritten for the databases that number their
	// parameters instead.
	Rebind(query string) string
}

// SQLite is the dialect the stores are written in.
type SQLite struct{}

func (SQLite) Name() string { return "sqlite" }

func (SQLite) Greatest(a, b string) string { return "MAX(" + a + ", " + b + ")" }
func (SQLite) Least(a, b string) string    { return "MIN(" + a + ", " + b + ")" }

func (SQLite) MergeJSON(a, b string) string { return "json_patch(" + a + ", " + b + ")" }

// Rebind returns the query unchanged: SQLite takes ? as written.
func (SQLite) Rebind(query string) string { return query }

// Postgres is the dialect a second backend would be written in.
//
// Here so that the seam has two sides. A seam with one implementation is a
// seam nobody has tested, and the shape of the second is what says whether the
// first was actually separable.
type Postgres struct{}

func (Postgres) Name() string { return "postgres" }

func (Postgres) Greatest(a, b string) string { return "GREATEST(" + a + ", " + b + ")" }
func (Postgres) Least(a, b string) string    { return "LEAST(" + a + ", " + b + ")" }

func (Postgres) MergeJSON(a, b string) string {
	return "(" + a + "::jsonb || " + b + "::jsonb)::text"
}

// Rebind numbers the placeholders, which is what Postgres takes.
//
// A question mark inside a string literal is not a placeholder, and rewriting
// one would change what a query means rather than how it is written. Literals
// are rare in these stores and skipping them is cheap, so it is done rather
// than assumed away.
func (Postgres) Rebind(query string) string {
	var b strings.Builder
	b.Grow(len(query) + 8)

	n := 0
	inString := false

	for i := 0; i < len(query); i++ {
		c := query[i]

		switch {
		case c == '\'':
			// Doubled quotes are an escaped quote inside a literal, so they
			// leave the state where it was.
			if inString && i+1 < len(query) && query[i+1] == '\'' {
				b.WriteString("''")
				i++
				continue
			}
			inString = !inString
			b.WriteByte(c)

		case c == '?' && !inString:
			n++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))

		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// Named returns a dialect by name, so a configuration can choose one.
func Named(name string) (Dialect, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "sqlite", "sqlite3":
		return SQLite{}, nil
	case "postgres", "postgresql":
		return Postgres{}, nil
	default:
		return nil, fmt.Errorf("storage: no dialect called %q. Have sqlite and postgres", name)
	}
}
