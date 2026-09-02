// SPDX-License-Identifier: GPL-3.0-or-later

package event_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/rangertaha/scour/internal/event"
)

// TestAQueryThatNamesNoMeasurementIsStillBounded.
//
// List is served over the bus, so any client may ask for the last ten points
// without naming a measurement. That query had no SQL LIMIT - the limit was
// applied by a break in Go - and no index to walk, so it read and sorted the
// whole table to hand back ten rows: 2.3ms at 5,000 points, 26ms at 50,000,
// on a store with a single connection, blocking every concurrent Put for the
// duration. That is the unbounded query DefaultLimit exists to prevent.
//
// The limit is in the query when there is no tag filter, which is when the rows
// SQLite returns are exactly the rows List keeps, and events_at gives the
// ordering an index to walk.
func TestAQueryThatNamesNoMeasurementIsStillBounded(t *testing.T) {
	dir := t.TempDir()
	store, err := event.Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	for i := range 200 {
		if _, err := store.Put(ctx, event.Event{
			Name: "price", Job: "markets", URL: "https://e.example/p", Spec: "abc",
			At:     time.Date(2026, 1, 1, 0, 0, i, 0, time.UTC),
			Fields: map[string]string{"value": "1"},
		}); err != nil {
			t.Fatal(err)
		}
	}

	got, err := store.List(ctx, event.Query{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 10 {
		t.Errorf("List returned %d points, want the 10 asked for", len(got))
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	// And the ordering is walked rather than sorted. A temp B-tree here is the
	// whole table being read before the first row comes back.
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, event.File))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	rows, err := db.Query(`EXPLAIN QUERY PLAN
SELECT id, name, tags, fields, at, job, url, spec
  FROM events
 WHERE 1=1
 ORDER BY at DESC, id
 LIMIT 10`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()

	var plan []string
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	joined := strings.Join(plan, "\n")
	if strings.Contains(joined, "TEMP B-TREE") {
		t.Errorf("a query naming no measurement sorts the whole table:\n%s", joined)
	}
	if !strings.Contains(joined, "events_at") {
		t.Errorf("the ordering is not served by an index:\n%s", joined)
	}
}
