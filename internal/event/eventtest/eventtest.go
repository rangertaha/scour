// SPDX-License-Identifier: GPL-3.0-or-later

// Package eventtest is the contract an event log has to keep, whatever is
// behind it.
//
// The reasons are [entity/entitytest]'s: a second backend is only believable if
// something holds it to the first one's promises, and this repository has twice
// been repaid for putting those promises in one place. What is asserted here is
// what a caller can rely on, not how any of it is stored.
package eventtest

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/rangertaha/scour/internal/event"
)

// Log is the surface this suite exercises.
type Log interface {
	Put(ctx context.Context, e event.Event) (string, error)
	Get(ctx context.Context, id string) (*event.Event, error)
	List(ctx context.Context, q event.Query) ([]*event.Event, error)
	Delete(ctx context.Context, id string) error
	Retract(ctx context.Context, job string) (int64, error)
	Names(ctx context.Context) ([]event.Series, error)
}

// Open builds a log for one test, empty.
type Open func(t *testing.T) Log

// Run puts a log through the contract.
func Run(t *testing.T, open Open) {
	t.Helper()

	t.Run("TheSameObservationIsOnePoint", func(t *testing.T) { testIdentity(t, open) })
	t.Run("AQueryNarrowsAndIsNewestFirst", func(t *testing.T) { testQuery(t, open) })
	t.Run("ATagFilterIsNotCutShortByTheLimit", func(t *testing.T) { testTagLimit(t, open) })
	t.Run("DeleteRemovesOneAndMissingIsNotAnError", func(t *testing.T) { testDelete(t, open) })
	t.Run("OneJobsEventsAreOneDelete", func(t *testing.T) { testRetract(t, open) })
	t.Run("AnEventNeedsANameAndATime", func(t *testing.T) { testRefusals(t, open) })
	t.Run("TwoJobsOneObservationKeepBoth", func(t *testing.T) { testTwoJobsOneObservation(t, open) })
	t.Run("ACorrectionStillOverwrites", func(t *testing.T) { testACorrectionStillOverwrites(t, open) })
}

func at(hour int) time.Time { return time.Date(2026, 8, 5, hour, 0, 0, 0, time.UTC) }

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

// testIdentity: two crawls of one page are one point, and a corrected number
// updates it.
//
// A series that doubled every time somebody re-ran a crawl would be worse than
// no series, and re-running a crawl is the ordinary case: it is how a
// correction gets in.
func testIdentity(t *testing.T, open Open) {
	ctx := context.Background()
	l := open(t)

	first, err := l.Put(ctx, price("acme", "9.99", 9))
	if err != nil {
		t.Fatal(err)
	}
	second, err := l.Put(ctx, price("acme", "10.50", 9))
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("one observation got two ids: %s and %s", first, second)
	}

	one, err := l.Get(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	if one.Fields["value"] != "10.50" {
		t.Errorf("value = %q, want the corrected one", one.Fields["value"])
	}
	if !one.At.Equal(at(9)) {
		t.Errorf("At = %s, want the event's own time", one.At)
	}

	// A different tag or time is a different observation.
	for _, e := range []event.Event{price("acme", "9.99", 10), price("beta", "9.99", 9)} {
		if _, err := l.Put(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	all, err := l.List(ctx, event.Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Errorf("len = %d, want three distinct events", len(all))
	}
}

// testQuery: name, tag and window narrow, and the newest comes first.
//
// From is inclusive and Until exclusive, which is what lets two adjacent
// windows cover a range without counting the boundary twice.
func testQuery(t *testing.T, open Open) {
	ctx := context.Background()
	l := open(t)

	for hour := 9; hour <= 12; hour++ {
		for _, company := range []string{"acme", "beta"} {
			if _, err := l.Put(ctx, price(company, "1", hour)); err != nil {
				t.Fatal(err)
			}
		}
	}

	byTag, err := l.List(ctx, event.Query{Name: "price", Tags: map[string]string{"company": "acme"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(byTag) != 4 {
		t.Errorf("by tag = %d, want 4", len(byTag))
	}

	window, err := l.List(ctx, event.Query{Name: "price", From: at(10), Until: at(12)})
	if err != nil {
		t.Fatal(err)
	}
	if len(window) != 4 {
		t.Errorf("by window = %d, want hours 10 and 11 for both companies", len(window))
	}
	for i, one := range window {
		if one.At.Before(at(10)) || !one.At.Before(at(12)) {
			t.Errorf("%s is outside [10, 12)", one.At)
		}
		if i > 0 && window[i].At.After(window[i-1].At) {
			t.Errorf("out of order at %d", i)
		}
	}

	names, err := l.Names(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names[0].Name != "price" || names[0].Events != 8 {
		t.Errorf("Names = %+v, want price with 8", names)
	}
}

// testTagLimit: a tag filter is not cut short by the limit.
//
// A query that silently under-reports is worse than a slow one, because the
// answer looks complete.
func testTagLimit(t *testing.T, open Open) {
	ctx := context.Background()
	l := open(t)

	for hour := range 20 {
		if _, err := l.Put(ctx, price("beta", "1", hour)); err != nil {
			t.Fatal(err)
		}
	}
	for hour := 20; hour < 25; hour++ {
		if _, err := l.Put(ctx, price("acme", "1", hour)); err != nil {
			t.Fatal(err)
		}
	}

	got, err := l.List(ctx, event.Query{
		Name: "price", Tags: map[string]string{"company": "beta"}, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 10 {
		t.Errorf("len = %d, want 10: the filter was cut short by the limit", len(got))
	}
}

func testDelete(t *testing.T, open Open) {
	ctx := context.Background()
	l := open(t)

	id, err := l.Put(ctx, price("acme", "9.99", 9))
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Delete(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Get(ctx, id); !errors.Is(err, event.ErrNotFound) {
		t.Errorf("Get after Delete = %v, want ErrNotFound", err)
	}
	// The caller wanted it gone and it is.
	if err := l.Delete(ctx, id); err != nil {
		t.Errorf("deleting what is already gone: %v", err)
	}
}

func testRetract(t *testing.T, open Open) {
	ctx := context.Background()
	l := open(t)

	for hour := range 4 {
		if _, err := l.Put(ctx, price("acme", "1", hour)); err != nil {
			t.Fatal(err)
		}
	}
	bad := price("acme", "1", 9)
	bad.Job = "misreading"
	bad.Tags = map[string]string{"company": "wrong"}
	if _, err := l.Put(ctx, bad); err != nil {
		t.Fatal(err)
	}

	removed, err := l.Retract(ctx, "misreading")
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Errorf("removed = %d, want 1", removed)
	}

	left, err := l.List(ctx, event.Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 4 {
		t.Errorf("len = %d, want the other job's events untouched", len(left))
	}
}

// testRefusals: the time in particular.
//
// A headline published at nine and crawled at half eleven is an event at nine,
// and a store that quietly used now would produce a series that is wrong in a
// way nobody notices for months.
func testRefusals(t *testing.T, open Open) {
	ctx := context.Background()
	l := open(t)

	if _, err := l.Put(ctx, event.Event{At: at(9)}); err == nil {
		t.Error("an event with no name was accepted")
	}
	if _, err := l.Put(ctx, event.Event{Name: "price"}); err == nil {
		t.Error("an event with no time was accepted, and now is not when it happened")
	}
}

// testTwoJobsOneObservation: two jobs that measure the same instant keep both
// their numbers, and neither takes the other's row.
//
// The identity is the observation, so two jobs CAN derive one id: same
// measurement, same tags, same instant, different fields. Replacing meant the
// second silently erased the first's numbers and took ownership, so Retract of
// the first returned zero and could not take back what it had contributed,
// which is the one promise this store makes.
func testTwoJobsOneObservation(t *testing.T, open Open) {
	ctx := context.Background()
	l := open(t)

	first := price("acme", "9.99", 9)
	first.Job = "markets"

	second := price("acme", "", 9)
	second.Fields = map[string]string{"volume": "120"}
	second.Job = "volumes"

	a, err := l.Put(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	b, err := l.Put(ctx, second)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("one observation got two ids: %s and %s", a, b)
	}

	one, err := l.Get(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if one.Fields["value"] != "9.99" {
		t.Errorf("the first job's number was erased: %+v", one.Fields)
	}
	if one.Fields["volume"] != "120" {
		t.Errorf("the second job's number did not land: %+v", one.Fields)
	}

	// The job that recorded it first still owns it, so its Retract takes it
	// back rather than reporting nothing to take.
	removed, err := l.Retract(ctx, "markets")
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Errorf("Retract(markets) removed %d, want the point it recorded", removed)
	}
}

// testACorrectionStillOverwrites: merging fields must not stop a re-crawl
// fixing a number, which is the case the identity was designed around.
func testACorrectionStillOverwrites(t *testing.T, open Open) {
	ctx := context.Background()
	l := open(t)

	if _, err := l.Put(ctx, price("acme", "9.99", 9)); err != nil {
		t.Fatal(err)
	}
	id, err := l.Put(ctx, price("acme", "10.50", 9))
	if err != nil {
		t.Fatal(err)
	}

	one, err := l.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if one.Fields["value"] != "10.50" {
		t.Errorf("value = %q, want the corrected one", one.Fields["value"])
	}
}
