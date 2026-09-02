// SPDX-License-Identifier: GPL-3.0-or-later

package event_test

import (
	"context"
	"strings"
	"testing"
	"time"

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
	// The plan of the query List actually builds, asked of the store.
	//
	// This used to run SQL written out in the test with `LIMIT 10` baked into
	// it, which says nothing about whether List puts one there: deleting the
	// limit from the query left this passing, and the Go-side break kept the
	// row count at ten either way. Only the index half of it bound anything.
	planner, ok := store.(interface {
		Plan(context.Context, event.Query) ([]string, error)
	})
	if !ok {
		t.Fatal("the store cannot explain its own query, so this checks nothing")
	}

	plan, err := planner.Plan(ctx, event.Query{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}

	joined := strings.Join(plan, "\n")
	if !strings.Contains(joined, "USING") {
		t.Errorf("the query reads no index at all:\n%s", joined)
	}

	// And the bound is in the query, not only in the loop that reads the rows.
	// A plan cannot show this - the index walk is the same either way - so it
	// is asked of the query text, which is what the store hands to SQLite.
	query, _, _ := event.Listing(event.Query{Limit: 10})
	if !strings.Contains(query, "LIMIT ?") {
		t.Errorf("a query with no tag filter carries no LIMIT, so it reads every row:\n%s", query)
	}

	// With a tag filter it deliberately does not, because the rows SQLite
	// returns are then not the rows List keeps: cutting in SQL would return
	// fewer than asked for whenever the matching rows lay beyond the cut.
	tagged, _, _ := event.Listing(event.Query{Limit: 10, Tags: map[string]string{"company": "acme"}})
	if strings.Contains(tagged, "LIMIT") {
		t.Errorf("a query with a tag filter cuts in SQL, so it can return fewer than asked for:\n%s", tagged)
	}
	if strings.Contains(joined, "TEMP B-TREE") {
		t.Errorf("a query naming no measurement sorts the whole table:\n%s", joined)
	}
	if !strings.Contains(joined, "events_at") {
		t.Errorf("the ordering is not served by an index:\n%s", joined)
	}
}
