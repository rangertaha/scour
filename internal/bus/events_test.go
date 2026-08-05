// SPDX-License-Identifier: GPL-3.0-or-later

package bus_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/rangertaha/scour/internal/bus"
	"github.com/rangertaha/scour/internal/event"
	"github.com/rangertaha/scour/internal/event/eventtest"
)

// measure runs the same sequence against whatever it is given, so the direct
// store and the client are asked to do exactly one thing.
func measure(t *testing.T, l bus.Log) string {
	t.Helper()
	ctx := context.Background()

	hour := func(h int) time.Time { return time.Date(2026, 8, 5, h, 0, 0, 0, time.UTC) }

	for h := 9; h <= 12; h++ {
		for _, company := range []string{"acme", "beta"} {
			if _, err := l.Put(ctx, event.Event{
				Name:   "price",
				Tags:   map[string]string{"company": company},
				Fields: map[string]string{"value": fmt.Sprintf("%d.50", h)},
				At:     hour(h),
				Job:    "markets",
				URL:    fmt.Sprintf("https://example.com/%s/%d", company, h),
			}); err != nil {
				t.Fatalf("put: %v", err)
			}
		}
	}

	// An update of a point already there, which is the same call.
	id, err := l.Put(ctx, event.Event{
		Name:   "price",
		Tags:   map[string]string{"company": "acme"},
		Fields: map[string]string{"value": "corrected"},
		At:     hour(9),
		Job:    "markets",
		URL:    "https://example.com/acme/9",
	})
	if err != nil {
		t.Fatalf("put: %v", err)
	}

	// One from a job that turns out to have been wrong, then taken back.
	if _, err := l.Put(ctx, event.Event{
		Name: "price", Tags: map[string]string{"company": "ghost"},
		At: hour(9), Job: "misreading", URL: "https://elsewhere.example/x",
	}); err != nil {
		t.Fatalf("put: %v", err)
	}
	if _, err := l.Retract(ctx, "misreading"); err != nil {
		t.Fatalf("retract: %v", err)
	}

	var out string

	names, err := l.Names(ctx)
	if err != nil {
		t.Fatalf("names: %v", err)
	}
	for _, n := range names {
		out += fmt.Sprintf("series %s %d %s..%s\n", n.Name, n.Events,
			n.First.Format(time.RFC3339), n.Last.Format(time.RFC3339))
	}

	one, err := l.Get(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	out += fmt.Sprintf("get %s %s %s\n", one.Name, one.Tags["company"], one.Fields["value"])

	window, err := l.List(ctx, event.Query{
		Name: "price", Tags: map[string]string{"company": "acme"},
		From: hour(9), Until: hour(12),
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, e := range window {
		out += fmt.Sprintf("point %s %s %s\n", e.At.Format(time.RFC3339), e.Tags["company"], e.Fields["value"])
	}

	if err := l.Delete(ctx, id); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := l.Get(ctx, id); err == nil {
		out += "deleted but still there\n"
	} else {
		out += "deleted\n"
	}
	return out
}

// TestTheSameEventsComeBackEitherWay.
//
// The claim the service rests on: where the store is has to be invisible to
// what it holds. A client that rounded a time, dropped a tag or lost a field
// would produce a series that looks right until somebody compares it against
// one built directly.
func TestTheSameEventsComeBackEitherWay(t *testing.T) {
	conn := connect(t)

	direct, err := event.Open("")
	if err != nil {
		t.Fatal(err)
	}
	defer direct.Close()

	remote, err := event.Open("")
	if err != nil {
		t.Fatal(err)
	}
	defer remote.Close()

	service, err := conn.ServeEvents(remote)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	want := measure(t, direct)
	got := measure(t, conn.NewEvents(wait))

	if want == "" {
		t.Fatal("the direct store produced nothing, so this compares nothing")
	}
	if got != want {
		t.Errorf("the events differ over the bus:\n--- direct ---\n%s\n--- bus ---\n%s", want, got)
	}
}

// TestAnEventTheStoreRefusesIsAnAnswer.
//
// A missing event and a service nobody is running are different things, and a
// caller has to be able to tell: the first is an answer and the second is a
// deployment problem.
func TestAnEventTheStoreRefusesIsAnAnswer(t *testing.T) {
	conn := connect(t)

	store, err := event.Open("")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	service, err := conn.ServeEvents(store)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	client := conn.NewEvents(wait)
	ctx := context.Background()

	if _, err := client.Get(ctx, "nosuchevent"); err == nil {
		t.Error("reading an event that is not there succeeded")
	}

	// And a refusal the store makes about the request itself travels too.
	if _, err := client.Put(ctx, event.Event{Name: "price"}); err == nil {
		t.Error("an event with no time was accepted over the bus")
	}
}

// TestNothingServingEventsIsNotATimeout.
func TestNothingServingEventsIsNotATimeout(t *testing.T) {
	conn := connect(t)
	client := conn.NewEvents(wait)

	started := time.Now()
	if _, err := client.Names(context.Background()); err == nil {
		t.Fatal("asking a service nobody serves succeeded")
	}
	if time.Since(started) > 5*time.Second {
		t.Errorf("it waited %s, so it timed out rather than noticing", time.Since(started))
	}
}

// TestTheClientKeepsTheEventContract.
//
// The same suite the SQLite store is held to, run against a log on the other
// side of a bus. It was missing, and its absence hid a real defect: the suite
// asserts errors.Is(err, event.ErrNotFound) after a delete, and an error that
// crossed as a string matched nothing, so a client could not tell "not there"
// from "went wrong". Every implementation of an interface belongs in that
// interface's suite; one that is not there is the one that drifts.
func TestTheClientKeepsTheEventContract(t *testing.T) {
	conn := connect(t)

	eventtest.Run(t, func(t *testing.T) eventtest.Log {
		store, err := event.Open("")
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		t.Cleanup(func() { store.Close() })

		service, err := conn.ServeEvents(store)
		if err != nil {
			t.Fatalf("serve: %v", err)
		}
		t.Cleanup(func() { service.Close() })

		return conn.NewEvents(wait)
	})
}
