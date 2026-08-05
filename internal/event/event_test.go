// SPDX-License-Identifier: GPL-3.0-or-later

package event_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/rangertaha/scour/internal/event"
)

func store(t *testing.T) *event.Store {
	t.Helper()

	s, err := event.Open("")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func at(hour int) time.Time {
	return time.Date(2026, 8, 5, hour, 0, 0, 0, time.UTC)
}

func price(company, value string, hour int) event.Event {
	return event.Event{
		Name:   "price",
		Tags:   map[string]string{"company": company},
		Fields: map[string]string{"value": value},
		At:     at(hour),
		Job:    "markets",
		URL:    fmt.Sprintf("https://example.com/%s/%d", company, hour),
	}
}

// TestWhatWasPutIsWhatComesBack.
func TestWhatWasPutIsWhatComesBack(t *testing.T) {
	ctx := context.Background()
	s := store(t)

	id, err := s.Put(ctx, price("acme", "9.99", 9))
	if err != nil {
		t.Fatal(err)
	}

	got, err := s.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "price" || got.Tags["company"] != "acme" || got.Fields["value"] != "9.99" {
		t.Errorf("got %+v", got)
	}
	if !got.At.Equal(at(9)) {
		t.Errorf("At = %s, want the event's own time", got.At)
	}
	if got.Job != "markets" {
		t.Errorf("the provenance was lost: %+v", got)
	}
}

// TestRecrawlingUpdatesThePointRatherThanDoublingTheSeries.
//
// The identity is derived from the name, the tags and the time, so two crawls
// of one page converge on one row. A series that doubled every time somebody
// re-ran a crawl would be worse than no series, and re-running a crawl is the
// ordinary case: it is how a corrected number gets in.
func TestRecrawlingUpdatesThePointRatherThanDoublingTheSeries(t *testing.T) {
	ctx := context.Background()
	s := store(t)

	first, err := s.Put(ctx, price("acme", "9.99", 9))
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Put(ctx, price("acme", "10.50", 9))
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("the same observation got two ids: %s and %s", first, second)
	}

	all, err := s.List(ctx, event.Query{Name: "price"})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("len = %d, want one point: %+v", len(all), all)
	}
	if all[0].Fields["value"] != "10.50" {
		t.Errorf("value = %q, want the corrected one", all[0].Fields["value"])
	}
}

// TestADifferentTagOrTimeIsADifferentEvent, which is the other half of the
// identity: converging is only right when it is the same observation.
func TestADifferentTagOrTimeIsADifferentEvent(t *testing.T) {
	ctx := context.Background()
	s := store(t)

	for _, e := range []event.Event{
		price("acme", "9.99", 9),
		price("acme", "9.99", 10),
		price("beta", "9.99", 9),
	} {
		if _, err := s.Put(ctx, e); err != nil {
			t.Fatal(err)
		}
	}

	all, err := s.List(ctx, event.Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Errorf("len = %d, want three distinct events", len(all))
	}
}

// TestAQueryNarrowsByNameTagAndWindow.
//
// From is inclusive and Until exclusive, which is what lets two adjacent
// windows cover a range without counting the boundary twice.
func TestAQueryNarrowsByNameTagAndWindow(t *testing.T) {
	ctx := context.Background()
	s := store(t)

	for hour := 9; hour <= 12; hour++ {
		for _, company := range []string{"acme", "beta"} {
			if _, err := s.Put(ctx, price(company, "1", hour)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := s.Put(ctx, event.Event{
		Name: "headline", At: at(9), Job: "news", URL: "https://example.com/a",
	}); err != nil {
		t.Fatal(err)
	}

	byName, err := s.List(ctx, event.Query{Name: "price"})
	if err != nil {
		t.Fatal(err)
	}
	if len(byName) != 8 {
		t.Errorf("by name: %d, want 8", len(byName))
	}

	byTag, err := s.List(ctx, event.Query{Name: "price", Tags: map[string]string{"company": "acme"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(byTag) != 4 {
		t.Errorf("by tag: %d, want 4", len(byTag))
	}

	window, err := s.List(ctx, event.Query{Name: "price", From: at(10), Until: at(12)})
	if err != nil {
		t.Fatal(err)
	}
	if len(window) != 4 {
		t.Errorf("by window: %d, want the two hours 10 and 11 for both companies", len(window))
	}
	for _, one := range window {
		if one.At.Before(at(10)) || !one.At.Before(at(12)) {
			t.Errorf("%s is outside [10, 12)", one.At)
		}
	}
}

// TestListIsNewestFirstAndBounded.
//
// Newest first because that is what somebody looking at a series wants without
// asking: a bounded query that returned the oldest would answer "what happened
// when this started" to a question about now.
func TestListIsNewestFirstAndBounded(t *testing.T) {
	ctx := context.Background()
	s := store(t)

	for hour := range 12 {
		if _, err := s.Put(ctx, price("acme", "1", hour)); err != nil {
			t.Fatal(err)
		}
	}

	got, err := s.List(ctx, event.Query{Name: "price", Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("len = %d, want the limit respected", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i].At.After(got[i-1].At) {
			t.Errorf("out of order: %s came after %s", got[i].At, got[i-1].At)
		}
	}
	if !got[0].At.Equal(at(11)) {
		t.Errorf("first = %s, want the newest", got[0].At)
	}
}

// TestATagFilterIsNotCutShortByTheLimit.
//
// Tags are matched after the rows come back, so applying the limit in SQL would
// have returned fewer than asked for whenever the matching rows were beyond the
// cut. A query that silently under-reports is worse than a slow one, because
// the answer looks complete.
func TestATagFilterIsNotCutShortByTheLimit(t *testing.T) {
	ctx := context.Background()
	s := store(t)

	// Twenty of one company, then five of another, all newer.
	for hour := range 20 {
		if _, err := s.Put(ctx, price("beta", "1", hour)); err != nil {
			t.Fatal(err)
		}
	}
	for hour := 20; hour < 25; hour++ {
		if _, err := s.Put(ctx, price("acme", "1", hour)); err != nil {
			t.Fatal(err)
		}
	}

	got, err := s.List(ctx, event.Query{
		Name:  "price",
		Tags:  map[string]string{"company": "beta"},
		Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 10 {
		t.Errorf("len = %d, want 10: the tag filter was cut short by the limit", len(got))
	}
}

// TestDeleteRemovesOneAndMissingIsNotAnError.
func TestDeleteRemovesOneAndMissingIsNotAnError(t *testing.T) {
	ctx := context.Background()
	s := store(t)

	id, err := s.Put(ctx, price("acme", "9.99", 9))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, id); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Get(ctx, id); !errors.Is(err, event.ErrNotFound) {
		t.Errorf("Get after Delete = %v, want ErrNotFound", err)
	}

	// The caller wanted it gone and it is.
	if err := s.Delete(ctx, id); err != nil {
		t.Errorf("deleting what is already gone: %v", err)
	}
}

// TestOneJobsEventsAreOneDelete, the same promise the entity graph makes.
func TestOneJobsEventsAreOneDelete(t *testing.T) {
	ctx := context.Background()
	s := store(t)

	for hour := range 4 {
		if _, err := s.Put(ctx, price("acme", "1", hour)); err != nil {
			t.Fatal(err)
		}
	}
	bad := price("acme", "1", 9)
	bad.Job = "misreading"
	bad.Tags = map[string]string{"company": "wrong"}
	if _, err := s.Put(ctx, bad); err != nil {
		t.Fatal(err)
	}

	removed, err := s.Retract(ctx, "misreading")
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Errorf("removed = %d, want 1", removed)
	}

	left, err := s.List(ctx, event.Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 4 {
		t.Errorf("len = %d, want the other job's events untouched", len(left))
	}
}

// TestNamesIsTheWayIntoAStoreYouDidNotFill.
func TestNamesIsTheWayIntoAStoreYouDidNotFill(t *testing.T) {
	ctx := context.Background()
	s := store(t)

	for hour := 9; hour <= 11; hour++ {
		if _, err := s.Put(ctx, price("acme", "1", hour)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Put(ctx, event.Event{
		Name: "headline", At: at(9), Job: "news", URL: "https://example.com/a",
	}); err != nil {
		t.Fatal(err)
	}

	names, err := s.Names(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 {
		t.Fatalf("names = %+v, want price and headline", names)
	}
	if names[0].Name != "price" || names[0].Events != 3 {
		t.Errorf("names[0] = %+v, want price with 3", names[0])
	}
	if !names[0].First.Equal(at(9)) || !names[0].Last.Equal(at(11)) {
		t.Errorf("names[0] window = %s to %s", names[0].First, names[0].Last)
	}
}

// TestAnEventNeedsANameAndATime.
//
// The time in particular: a headline published at nine and crawled at half
// eleven is an event at nine, and a store that quietly used now would produce a
// series that is wrong in a way nobody notices for months.
func TestAnEventNeedsANameAndATime(t *testing.T) {
	ctx := context.Background()
	s := store(t)

	if _, err := s.Put(ctx, event.Event{At: at(9)}); err == nil {
		t.Error("an event with no name was accepted")
	}
	if _, err := s.Put(ctx, event.Event{Name: "price"}); err == nil {
		t.Error("an event with no time was accepted, and now is not when it happened")
	}
}

// TestTwoInMemoryStoresAreTwoStores, which the entity store learned the hard
// way: a shared name meant every Open("") in a process was one database.
func TestTwoInMemoryStoresAreTwoStores(t *testing.T) {
	ctx := context.Background()

	first, second := store(t), store(t)

	if _, err := first.Put(ctx, price("acme", "9.99", 9)); err != nil {
		t.Fatal(err)
	}

	got, err := second.List(ctx, event.Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("one store's events were visible in another: %+v", got)
	}
}
