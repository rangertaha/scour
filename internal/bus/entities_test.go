// SPDX-License-Identifier: GPL-3.0-or-later

package bus_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/rangertaha/scour/internal/bus"
	"github.com/rangertaha/scour/internal/entity"
	"github.com/rangertaha/scour/internal/entity/entitytest"
)

// fill runs the same sequence of operations against whatever it is given, so
// that the direct store and the client are asked to do exactly one thing.
//
// Written against [bus.Graph] rather than twice, because a test that described
// the work twice would be a test that could drift the same way the two
// implementations can.
func fill(t *testing.T, g bus.Graph) {
	t.Helper()

	ctx := context.Background()
	said := entity.Provenance{
		Job: "news", URL: "https://example.com/a", Spec: "abc",
		At: time.Date(2026, 8, 5, 9, 0, 0, 0, time.UTC),
	}

	paper, err := g.Assert(ctx, "publisher", "The Chronicle", said)
	if err != nil {
		t.Fatalf("assert publisher: %v", err)
	}
	author, err := g.Assert(ctx, "person", "Alex Doe", said)
	if err != nil {
		t.Fatalf("assert person: %v", err)
	}
	if _, err := g.Assert(ctx, "person", "Sam Roe", said); err != nil {
		t.Fatalf("assert person: %v", err)
	}

	if err := g.Describe(ctx, author, "role", "correspondent", 0, said); err != nil {
		t.Fatalf("describe: %v", err)
	}
	if err := g.Describe(ctx, author, "beat", "climate", 1, said); err != nil {
		t.Fatalf("describe: %v", err)
	}
	edge, err := g.Relate(ctx, paper, author, "author", "climate", 0, said)
	if err != nil {
		t.Fatalf("relate: %v", err)
	}
	// A property of the edge itself, which is neither end's.
	if err := g.Describe(ctx, edge, "role", "correspondent", 0, said); err != nil {
		t.Fatalf("describe the edge: %v", err)
	}

	// A second spelling and a merge, so resolution is exercised too.
	initial, err := g.Assert(ctx, "person", "A. Doe", said)
	if err != nil {
		t.Fatalf("assert initial: %v", err)
	}
	if err := g.Merge(ctx, initial, author, entity.RuleManual, said); err != nil {
		t.Fatalf("merge: %v", err)
	}
}

// snapshot is everything readable about a graph, rendered so two of them can be
// compared as text.
func snapshot(t *testing.T, g bus.Graph) string {
	t.Helper()

	ctx := context.Background()
	var out string

	edgeKinds, err := g.RelationKinds(ctx)
	if err != nil {
		t.Fatalf("relation kinds: %v", err)
	}
	for _, k := range edgeKinds {
		out += fmt.Sprintf("relation kind %s %d\n", k.Name, k.Relations)
	}

	kinds, err := g.Kinds(ctx)
	if err != nil {
		t.Fatalf("kinds: %v", err)
	}
	for _, k := range kinds {
		out += fmt.Sprintf("kind %s %d\n", k.Name, k.Entities)

		people, err := g.Kind(ctx, k.Name)
		if err != nil {
			t.Fatalf("kind %s: %v", k.Name, err)
		}
		for _, e := range people {
			out += fmt.Sprintf("  entity %s %s assertions=%d\n", e.Kind, e.Name, e.Assertions)

			props, err := g.Properties(ctx, e.ID)
			if err != nil {
				t.Fatalf("properties: %v", err)
			}
			for _, p := range props {
				out += fmt.Sprintf("    property %s=%s position=%d x%d\n", p.Name, p.Value, p.Position, p.Assertions)
			}

			related, err := g.Related(ctx, e.ID, "", "")
			if err != nil {
				t.Fatalf("related: %v", err)
			}
			for _, r := range related {
				out += fmt.Sprintf("    related %s %s\n", r.Kind, r.Name)
			}

			edges, err := g.Relations(ctx, e.ID)
			if err != nil {
				t.Fatalf("relations: %v", err)
			}
			for _, edge := range edges {
				out += fmt.Sprintf("    edge %s topic=%s position=%d x%d\n",
					edge.Kind, edge.Topic, edge.Position, edge.Assertions)

				props, err := g.Properties(ctx, edge.ID)
				if err != nil {
					t.Fatalf("edge properties: %v", err)
				}
				for _, p := range props {
					out += fmt.Sprintf("      edge property %s=%s position=%d\n", p.Name, p.Value, p.Position)
				}
			}

			provs, err := g.Provenances(ctx, e.ID)
			if err != nil {
				t.Fatalf("provenances: %v", err)
			}
			for _, p := range provs {
				out += fmt.Sprintf("    said by %s at %s\n", p.Job, p.URL)
			}

			aliases, err := g.Aliases(ctx, e.ID)
			if err != nil {
				t.Fatalf("aliases: %v", err)
			}
			for _, a := range aliases {
				out += fmt.Sprintf("    alias %s by %s\n", a.Name, a.Rule)
			}
		}
	}
	return out
}

// TestTheSameOperationsProduceTheSameGraphEitherWay.
//
// The claim the whole service rests on, and the same one the stages make: where
// the store is has to be invisible to what it holds. A client that dropped a
// tag, rounded a time, or lost an alias would produce a graph that looks right
// until somebody compares it with one built directly, which is what nobody does
// in production and what this does on every run.
func TestTheSameOperationsProduceTheSameGraphEitherWay(t *testing.T) {
	conn := connect(t)

	direct, err := entity.Open("")
	if err != nil {
		t.Fatal(err)
	}
	defer direct.Close()

	remote, err := entity.Open("")
	if err != nil {
		t.Fatal(err)
	}
	defer remote.Close()

	service, err := conn.ServeEntities(remote, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	client := conn.NewEntities(wait)

	fill(t, direct)
	fill(t, client)

	want := snapshot(t, direct)
	got := snapshot(t, client)

	if want == "" {
		t.Fatal("the direct graph is empty, so this compares nothing")
	}
	if got != want {
		t.Errorf("the graph differs over the bus:\n--- direct ---\n%s\n--- bus ---\n%s", want, got)
	}
}

// TestTheStoreSaysNoRatherThanNothingAnswering.
//
// "The store refused" and "nothing is serving" are different things, and a
// caller that could not tell them apart would retry a refusal forever. The
// refusal travels as an answer.
func TestTheStoreSaysNoRatherThanNothingAnswering(t *testing.T) {
	conn := connect(t)

	store, err := entity.Open("")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	service, err := conn.ServeEntities(store, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	client := conn.NewEntities(wait)

	// An entity nothing has asserted.
	if _, err := client.Get(context.Background(), "nosuchentity"); err == nil {
		t.Error("reading an entity that is not there succeeded")
	} else if got := err.Error(); got == "" {
		t.Error("the refusal arrived with nothing in it")
	}
}

// TestNothingServingIsNotATimeoutForTheGraph, the same distinction the stages
// make: a client with no service should say so at once rather than waiting out
// its timeout.
func TestNothingServingIsNotATimeoutForTheGraph(t *testing.T) {
	conn := connect(t)
	client := conn.NewEntities(wait)

	started := time.Now()
	_, err := client.Kinds(context.Background())
	if err == nil {
		t.Fatal("asking a service nobody serves succeeded")
	}
	if time.Since(started) > 5*time.Second {
		t.Errorf("it waited %s, so it timed out rather than noticing", time.Since(started))
	}
}

// TestTheClientKeepsTheGraphContract.
//
// The same suite the SQLite store is held to, run against a graph that is on
// the other side of a bus. This is what "where the store is has to be invisible"
// means in a form that keeps being true: a promise added to the contract later
// is one the client has to keep too, without anybody remembering to test it
// here.
func TestTheClientKeepsTheGraphContract(t *testing.T) {
	conn := connect(t)

	entitytest.Run(t, func(t *testing.T) entitytest.Graph {
		store, err := entity.Open("")
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		t.Cleanup(func() { store.Close() })

		service, err := conn.ServeEntities(store, 0)
		if err != nil {
			t.Fatalf("serve: %v", err)
		}
		t.Cleanup(func() { service.Close() })

		return conn.NewEntities(wait)
	})
}

// slowGraph is a graph whose Assert never returns until its context ends, so a
// test can see what deadline the service gave it.
//
// The embedded interface is nil: every other method panics if called, which is
// what this test wants. It asks for one operation, and a graph that quietly
// answered a second would hide a change in what the service calls.
type slowGraph struct {
	bus.Graph
	saw chan error
}

func (g *slowGraph) Assert(ctx context.Context, kind, name string, said entity.Provenance) (string, error) {
	<-ctx.Done()
	g.saw <- ctx.Err()
	return "", ctx.Err()
}

// TestAServiceBoundsARequestByTheTimeItWasGiven.
//
// `timeout` in a service block was parsed, validated, documented as "how long
// one request may take", and read by nothing: every handler ran on the
// package's own constant instead. A service document asking for a two-second
// bound got the default, so an operator who had shortened it to keep a busy
// store responsive was watching requests they thought were bounded hold a
// connection for the full default.
//
// The handler's context is the observable part, so that is what is asserted:
// what the service promises is not that a slow store is fast, but that it is
// let go of when the time the document named has passed.
func TestAServiceBoundsARequestByTheTimeItWasGiven(t *testing.T) {
	conn := connect(t)

	graph := &slowGraph{saw: make(chan error, 1)}
	service, err := conn.ServeEntities(graph, 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	// The caller waits far longer than the service was given, so what ends the
	// handler is the service's own deadline and not this one.
	go conn.NewEntities(10*time.Second).Assert(
		context.Background(), "company", "acme", entity.Provenance{Job: "j", URL: "u"})

	select {
	case err := <-graph.saw:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("the handler ended with %v, want the deadline the document asked for", err)
		}
	case <-time.After(3 * time.Second):
		t.Error("the handler was never let go: the service used its own timeout, not the document's")
	}
}
