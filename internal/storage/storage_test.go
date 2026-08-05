// SPDX-License-Identifier: GPL-3.0-or-later

package storage_test

import (
	"strings"
	"testing"

	"github.com/rangertaha/scour/internal/storage"
)

// TestTheDialectsDifferWhereTheyHaveTo, and nowhere else.
//
// The whole argument for this package is that the difference is small. A test
// that let it grow without anybody noticing would be the first step towards the
// query builder this exists instead of.
func TestTheDialectsDifferWhereTheyHaveTo(t *testing.T) {
	sqlite, postgres := storage.SQLite{}, storage.Postgres{}

	if got := sqlite.Greatest("a", "b"); got != "MAX(a, b)" {
		t.Errorf("sqlite greatest = %q", got)
	}
	if got := postgres.Greatest("a", "b"); got != "GREATEST(a, b)" {
		t.Errorf("postgres greatest = %q", got)
	}
	if got := sqlite.Least("a", "b"); got != "MIN(a, b)" {
		t.Errorf("sqlite least = %q", got)
	}
	if got := postgres.Least("a", "b"); got != "LEAST(a, b)" {
		t.Errorf("postgres least = %q", got)
	}
}

// TestRebindNumbersThePlaceholders.
func TestRebindNumbersThePlaceholders(t *testing.T) {
	const query = `SELECT a FROM t WHERE b = ? AND c = ? ORDER BY d LIMIT ?`

	if got := (storage.SQLite{}).Rebind(query); got != query {
		t.Errorf("sqlite rewrote a query it takes as written:\n%s", got)
	}

	want := `SELECT a FROM t WHERE b = $1 AND c = $2 ORDER BY d LIMIT $3`
	if got := (storage.Postgres{}).Rebind(query); got != want {
		t.Errorf("postgres rebind =\n%s\nwant\n%s", got, want)
	}
}

// TestAQuestionMarkInALiteralIsNotAPlaceholder.
//
// Rewriting one would change what the query means rather than how it is
// written. These stores do hold string literals in SQL: the frontier compares
// a status against 'waiting', and a query that asked about 'why?' would come
// back asking about '$1'.
func TestAQuestionMarkInALiteralIsNotAPlaceholder(t *testing.T) {
	for _, c := range []struct{ query, want string }{
		{`SELECT ? WHERE s = 'why?'`, `SELECT $1 WHERE s = 'why?'`},
		{`SELECT 'a?b', ?, 'c?d', ?`, `SELECT 'a?b', $1, 'c?d', $2`},
		// A doubled quote is an escaped quote inside a literal, so the literal
		// has not ended and the question mark after it is still text.
		{`SELECT 'it''s ?' , ?`, `SELECT 'it''s ?' , $1`},
		{`SELECT ?`, `SELECT $1`},
		{`SELECT 1`, `SELECT 1`},
	} {
		if got := (storage.Postgres{}).Rebind(c.query); got != c.want {
			t.Errorf("Rebind(%q) = %q, want %q", c.query, got, c.want)
		}
	}
}

// TestNamedChoosesADialectAndRefusesWhatItDoesNotHave.
func TestNamedChoosesADialectAndRefusesWhatItDoesNotHave(t *testing.T) {
	for _, name := range []string{"", "sqlite", "SQLite3"} {
		d, err := storage.Named(name)
		if err != nil {
			t.Fatalf("Named(%q): %v", name, err)
		}
		if d.Name() != "sqlite" {
			t.Errorf("Named(%q) = %s", name, d.Name())
		}
	}

	d, err := storage.Named("postgres")
	if err != nil || d.Name() != "postgres" {
		t.Errorf("Named(postgres) = %v, %v", d, err)
	}

	_, err = storage.Named("mysql")
	if err == nil {
		t.Fatal("a dialect nobody wrote was accepted")
	}
	if !strings.Contains(err.Error(), "sqlite") {
		t.Errorf("the refusal does not say what there is: %v", err)
	}
}
