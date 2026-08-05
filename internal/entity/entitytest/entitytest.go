// SPDX-License-Identifier: GPL-3.0-or-later

// Package entitytest is the contract an entity graph has to keep, whatever is
// behind it.
//
// # Why this exists
//
// Because a second backend is only believable if something holds it to the
// first one's promises. This repository has learned that twice: moving a
// single-backend test into [cache/cachetest] found a write that committed a
// truncated body over a good one, and [exporter/exportertest] failed for three
// formats the first time it ran. Both defects had been there as long as the code
// had, and both were invisible while each implementation was tested on its own.
//
// So the promises are here rather than in the SQLite store's own tests. A
// Postgres backend, or a client that reaches the graph over a bus, keeps them or
// fails.
//
// # What is asserted
//
// What a caller can rely on: that an assertion is idempotent and counted, that
// reads follow merges, that order comes from the shape rather than from how
// often something was said, that an edge is a thing with properties of its own,
// and that one job's contribution is one delete. Not how any of it is stored:
// that is each backend's business, and a suite that asserted it would be a
// second copy of one implementation.
package entitytest

import (
	"context"
	"testing"
	"time"

	"github.com/rangertaha/scour/internal/entity"
)

// Graph is the surface this suite exercises.
//
// Declared here rather than imported, so that the suite says what it needs and
// a backend is measured against that rather than against whatever one
// implementation happens to expose.
type Graph interface {
	Assert(ctx context.Context, kind, name string, said entity.Provenance) (string, error)
	Describe(ctx context.Context, subject, name, value string, position int, said entity.Provenance) error
	Relate(ctx context.Context, from, to, kind, topic string, position int, said entity.Provenance) (string, error)

	Get(ctx context.Context, id string) (*entity.Entity, error)
	Find(ctx context.Context, kind, name string) (*entity.Entity, error)
	Kind(ctx context.Context, kind string) ([]*entity.Entity, error)
	Kinds(ctx context.Context) ([]entity.Kind, error)
	RelationKinds(ctx context.Context) ([]entity.RelationKind, error)
	Properties(ctx context.Context, subject string) ([]entity.Property, error)
	Relations(ctx context.Context, id string) ([]entity.Relation, error)
	Related(ctx context.Context, id, kind, topic string) ([]*entity.Entity, error)
	Provenances(ctx context.Context, id string) ([]entity.Provenance, error)

	Merge(ctx context.Context, from, to, rule string, said entity.Provenance) error
	Retract(ctx context.Context, job string) (int64, error)
}

// Open builds a graph for one test, empty.
type Open func(t *testing.T) Graph

// Run puts a graph through the contract.
func Run(t *testing.T, open Open) {
	t.Helper()

	t.Run("AssertingTwiceIsOneEntityCountedTwice", func(t *testing.T) { testAssert(t, open) })
	t.Run("PropertiesComeBackInTheShapesOrder", func(t *testing.T) { testPropertyOrder(t, open) })
	t.Run("AnEdgeHasATypeAndPropertiesOfItsOwn", func(t *testing.T) { testEdge(t, open) })
	t.Run("ReadsFollowAMerge", func(t *testing.T) { testMerge(t, open) })
	t.Run("OneJobsContributionIsOneDelete", func(t *testing.T) { testRetract(t, open) })
}

func said(job, url string) entity.Provenance {
	return entity.Provenance{
		Job: job, URL: url, Spec: "abc",
		At: time.Date(2026, 8, 5, 9, 0, 0, 0, time.UTC),
	}
}

// testAssert: the same name twice is one entity that has been said twice.
//
// Convergence without coordination is the graph's central claim: two jobs that
// have never met agree about who Acme is because the id comes from the kind and
// the name rather than from a sequence.
func testAssert(t *testing.T, open Open) {
	ctx := context.Background()
	g := open(t)

	first, err := g.Assert(ctx, "person", "Alex Doe", said("news", "https://a.example/1"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := g.Assert(ctx, "person", "Alex Doe", said("wire", "https://b.example/2"))
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("two jobs asserting one person got two ids: %s and %s", first, second)
	}

	one, err := g.Get(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	if one.Assertions != 2 {
		t.Errorf("Assertions = %d, want both sightings counted", one.Assertions)
	}

	found, err := g.Find(ctx, "person", "Alex Doe")
	if err != nil {
		t.Fatal(err)
	}
	if found.ID != first {
		t.Errorf("Find returned %s, want %s", found.ID, first)
	}

	kinds, err := g.Kinds(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(kinds) != 1 || kinds[0].Name != "person" || kinds[0].Entities != 1 {
		t.Errorf("Kinds = %+v, want one person", kinds)
	}

	provs, err := g.Provenances(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	if len(provs) != 2 {
		t.Errorf("Provenances = %+v, want both jobs", provs)
	}
}

// testPropertyOrder: properties come back where the shape put them.
//
// Ordering is part of what a shape says, and a graph that returned the
// most-asserted first would answer a question about the shape with a popularity
// contest that reshuffles as a crawl goes on.
func testPropertyOrder(t *testing.T, open Open) {
	ctx := context.Background()
	g := open(t)

	id, err := g.Assert(ctx, "person", "Alex Doe", said("news", "https://a.example/1"))
	if err != nil {
		t.Fatal(err)
	}

	// Declared first, said once. Declared second, said three times.
	if err := g.Describe(ctx, id, "role", "correspondent", 0, said("news", "https://a.example/1")); err != nil {
		t.Fatal(err)
	}
	for i, url := range []string{"https://a.example/2", "https://a.example/3", "https://a.example/4"} {
		if err := g.Describe(ctx, id, "beat", "climate", 1, said("news", url)); err != nil {
			t.Fatalf("describe %d: %v", i, err)
		}
	}

	props, err := g.Properties(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(props) != 2 {
		t.Fatalf("Properties = %+v, want two", props)
	}
	if props[0].Name != "role" || props[1].Name != "beat" {
		t.Errorf("order = %s then %s, want the shape's order and not the counts",
			props[0].Name, props[1].Name)
	}
	if props[1].Assertions != 3 {
		t.Errorf("beat was asserted %d times, want 3", props[1].Assertions)
	}
}

// testEdge: an edge is typed, ordered, and has something to say of its own.
//
// "Alex Doe wrote for The Chronicle" is the edge; "as a correspondent" belongs
// to the edge rather than to either end, where it would attach to every other
// edge that end has.
func testEdge(t *testing.T, open Open) {
	ctx := context.Background()
	g := open(t)

	from := said("news", "https://a.example/1")

	paper, err := g.Assert(ctx, "publisher", "The Chronicle", from)
	if err != nil {
		t.Fatal(err)
	}
	author, err := g.Assert(ctx, "person", "Alex Doe", from)
	if err != nil {
		t.Fatal(err)
	}

	edge, err := g.Relate(ctx, paper, author, "author", "", 0, from)
	if err != nil {
		t.Fatal(err)
	}
	if edge == "" {
		t.Fatal("an edge with no id cannot be described")
	}
	if err := g.Describe(ctx, edge, "role", "correspondent", 0, from); err != nil {
		t.Fatal(err)
	}

	edges, err := g.Relations(ctx, paper)
	if err != nil {
		t.Fatal(err)
	}
	if len(edges) != 1 || edges[0].Kind != "author" || edges[0].ID != edge {
		t.Fatalf("Relations = %+v, want the one author edge", edges)
	}

	props, err := g.Properties(ctx, edge)
	if err != nil {
		t.Fatal(err)
	}
	if len(props) != 1 || props[0].Value != "correspondent" {
		t.Errorf("the edge's own properties = %+v", props)
	}

	// The property is the edge's, not either end's.
	if ends, err := g.Properties(ctx, author); err != nil {
		t.Fatal(err)
	} else if len(ends) != 0 {
		t.Errorf("the edge's property attached to the person: %+v", ends)
	}

	kinds, err := g.RelationKinds(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(kinds) != 1 || kinds[0].Name != "author" || kinds[0].Relations != 1 {
		t.Errorf("RelationKinds = %+v, want one author edge", kinds)
	}

	related, err := g.Related(ctx, paper, "author", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(related) != 1 || related[0].Name != "Alex Doe" {
		t.Errorf("Related = %+v", related)
	}
}

// testMerge: what is known about a person is known whichever spelling it was
// said under.
func testMerge(t *testing.T, open Open) {
	ctx := context.Background()
	g := open(t)

	from := said("news", "https://a.example/1")

	full, err := g.Assert(ctx, "person", "Alex Doe", from)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := g.Assert(ctx, "person", "A. Doe", from)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Describe(ctx, full, "role", "correspondent", 0, from); err != nil {
		t.Fatal(err)
	}
	if err := g.Describe(ctx, initial, "beat", "climate", 1, from); err != nil {
		t.Fatal(err)
	}

	if err := g.Merge(ctx, initial, full, entity.RuleManual, from); err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{full, initial} {
		props, err := g.Properties(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if len(props) != 2 {
			t.Errorf("Properties(%s) = %+v, want both sides of the merge", id, props)
		}
	}

	// And the two spellings are one person now.
	kinds, err := g.Kinds(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(kinds) != 1 || kinds[0].Entities != 1 {
		t.Errorf("Kinds = %+v, want one person after the merge", kinds)
	}
}

// testRetract: one job's contribution is one delete, counts rebuilt from what
// is left.
//
// Without it the only way to remove a job's mistakes is to rebuild the store,
// which is why every row carries provenance in the first place.
func testRetract(t *testing.T, open Open) {
	ctx := context.Background()
	g := open(t)

	good := said("good", "https://a.example/1")
	bad := said("bad", "https://b.example/2")

	id, err := g.Assert(ctx, "company", "Acme", good)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.Assert(ctx, "company", "Acme", bad); err != nil {
		t.Fatal(err)
	}
	if err := g.Describe(ctx, id, "domain", "acme.com", 0, good); err != nil {
		t.Fatal(err)
	}
	if err := g.Describe(ctx, id, "domain", "not-acme.example", 0, bad); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Assert(ctx, "company", "Ghost", bad); err != nil {
		t.Fatal(err)
	}

	if _, err := g.Retract(ctx, "bad"); err != nil {
		t.Fatal(err)
	}

	one, err := g.Get(ctx, id)
	if err != nil {
		t.Fatalf("the entity the remaining job asserted is gone: %v", err)
	}
	if one.Assertions != 1 {
		t.Errorf("Assertions = %d, want the count rebuilt from what is left", one.Assertions)
	}

	props, err := g.Properties(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(props) != 1 || props[0].Value != "acme.com" {
		t.Errorf("Properties = %+v, want only what the remaining job said", props)
	}

	// What only the withdrawn job asserted is gone entirely.
	if _, err := g.Find(ctx, "company", "Ghost"); err == nil {
		t.Error("an entity only the withdrawn job asserted is still there")
	}
}
