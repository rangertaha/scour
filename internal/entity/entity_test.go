// SPDX-License-Identifier: GPL-3.0-or-later

package entity_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rangertaha/scour/internal/entity"
	"github.com/rangertaha/scour/internal/entity/entitytest"
)

var seen = time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)

func open(t *testing.T) *entity.Store {
	t.Helper()

	s, err := entity.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func from(job, url string) entity.Provenance {
	return entity.Provenance{Job: job, URL: url, Spec: "abc123", At: seen}
}

// TestTwoJobsAssertingOnePersonConvergeOnOneRow, without coordinating and
// without either of them having to look first.
func TestTwoJobsAssertingOnePersonConvergeOnOneRow(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	first, err := s.Assert(ctx, "person", "Alex Doe", from("news", "https://a.example/1"))
	if err != nil {
		t.Fatalf("assert: %v", err)
	}
	second, err := s.Assert(ctx, "person", "  alex   doe  ", from("markets", "https://b.example/2"))
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("one person got two ids: %s and %s", first, second)
	}

	got, err := s.Get(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	if got.Assertions != 2 {
		t.Errorf("assertions = %d", got.Assertions)
	}
	if got.Name != "Alex Doe" {
		t.Errorf("name = %q, want the first spelling seen", got.Name)
	}
}

// TestTwoSpellingsAreTwoEntities, which is what this stage does not solve and
// should not pretend to.
func TestTwoSpellingsAreTwoEntities(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	full, _ := s.Assert(ctx, "person", "Alex Doe", from("news", "https://a.example/1"))
	initial, _ := s.Assert(ctx, "person", "A. Doe", from("news", "https://a.example/2"))

	if full == initial {
		t.Error("two spellings were merged, which is identity resolution and is not built")
	}
}

// TestWhichAuthorsHasThisPublisherPublished. The question this store exists to
// answer, and it answers it for nothing once the assertions are in.
func TestWhichAuthorsHasThisPublisherPublished(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	publisher, err := s.Assert(ctx, "company", "The Example", from("news", "https://a.example/"))
	if err != nil {
		t.Fatal(err)
	}

	for _, author := range []string{"Alex Doe", "Sam Smith", "Alex Doe", "Alex Doe"} {
		id, err := s.Assert(ctx, "person", author, from("news", "https://a.example/story"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Relate(ctx, publisher, id, "author", "climate@7", 0, from("news", "https://a.example/story")); err != nil {
			t.Fatal(err)
		}
	}
	// One author on another topic, to prove the topic narrows it.
	other, _ := s.Assert(ctx, "person", "Jo Bloggs", from("news", "https://a.example/sport"))
	if _, err := s.Relate(ctx, publisher, other, "author", "sport@1", 0, from("news", "https://a.example/sport")); err != nil {
		t.Fatal(err)
	}

	all, err := s.Related(ctx, publisher, "author", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("the publisher has %d authors: %v", len(all), names(all))
	}
	// Most asserted first, which is what makes the list useful rather than
	// merely complete.
	if all[0].Name != "Alex Doe" {
		t.Errorf("order = %v", names(all))
	}

	onTopic, err := s.Related(ctx, publisher, "author", "climate@7")
	if err != nil {
		t.Fatal(err)
	}
	if len(onTopic) != 2 {
		t.Errorf("on climate the publisher has %v", names(onTopic))
	}
}

// TestEverythingCarriesProvenance. Without it a wrong entity is a fact.
func TestEverythingCarriesProvenance(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	id, err := s.Assert(ctx, "person", "Alex Doe", from("news", "https://a.example/1"))
	if err != nil {
		t.Fatal(err)
	}

	said, err := s.Provenances(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(said) != 1 {
		t.Fatalf("provenances = %v", said)
	}
	if said[0].Job != "news" || said[0].URL != "https://a.example/1" || said[0].Spec != "abc123" {
		t.Errorf("provenance = %+v", said[0])
	}
}

// TestOneJobsMistakesAreOneDelete. The reason every assertion carries
// provenance: a job extracting the wrong field for a week is recoverable
// without rebuilding the store.
func TestOneJobsMistakesAreOneDelete(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	good, _ := s.Assert(ctx, "person", "Alex Doe", from("news", "https://a.example/1"))
	if _, err := s.Assert(ctx, "person", "Alex Doe", from("broken", "https://b.example/1")); err != nil {
		t.Fatal(err)
	}
	wrong, _ := s.Assert(ctx, "person", "Copyright 2026", from("broken", "https://b.example/2"))

	publisher, _ := s.Assert(ctx, "company", "The Example", from("broken", "https://b.example/"))
	if _, err := s.Relate(ctx, publisher, wrong, "author", "", 0, from("broken", "https://b.example/2")); err != nil {
		t.Fatal(err)
	}

	removed, err := s.Retract(ctx, "broken")
	if err != nil {
		t.Fatalf("retract: %v", err)
	}
	if removed == 0 {
		t.Error("nothing was retracted")
	}

	// What the broken job invented is gone.
	if _, err := s.Get(ctx, wrong); err == nil {
		t.Error("an entity nobody asserts any more survived")
	}

	// What the good job said is untouched, and the count is what it said
	// rather than what both said.
	kept, err := s.Get(ctx, good)
	if err != nil {
		t.Fatalf("the good job's entity was retracted too: %v", err)
	}
	if kept.Assertions != 1 {
		t.Errorf("assertions = %d, want only what the surviving job said", kept.Assertions)
	}

	// And the edge the broken job asserted is gone with it.
	related, err := s.Related(ctx, publisher, "author", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(related) != 0 {
		t.Errorf("the broken job's edges survived: %v", names(related))
	}
}

func TestFindByWhatExtractionHas(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	if _, err := s.Assert(ctx, "person", "Alex Doe", from("news", "https://a.example/1")); err != nil {
		t.Fatal(err)
	}

	got, err := s.Find(ctx, "person", "ALEX DOE")
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if got.Name != "Alex Doe" {
		t.Errorf("name = %q", got.Name)
	}
	if _, err := s.Find(ctx, "person", "Nobody"); err == nil {
		t.Error("found somebody nobody asserted")
	}
}

func TestKindLists(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	for _, name := range []string{"Alex Doe", "Sam Smith", "Alex Doe"} {
		if _, err := s.Assert(ctx, "person", name, from("news", "https://a.example/1")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Assert(ctx, "company", "The Example", from("news", "https://a.example/")); err != nil {
		t.Fatal(err)
	}

	people, err := s.Kind(ctx, "person")
	if err != nil {
		t.Fatal(err)
	}
	if len(people) != 2 || people[0].Name != "Alex Doe" {
		t.Errorf("people = %v", names(people))
	}
	if companies, _ := s.Kind(ctx, "company"); len(companies) != 1 {
		t.Errorf("companies = %v", names(companies))
	}
}

func TestAnAssertionHasToSayWhoSaidIt(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	if _, err := s.Assert(ctx, "person", "Alex Doe", entity.Provenance{}); err == nil {
		t.Error("an assertion with no provenance was accepted")
	}
	if _, err := s.Assert(ctx, "", "Alex Doe", from("news", "https://a.example/")); err == nil {
		t.Error("an entity with no kind was accepted")
	}
	if _, err := s.Assert(ctx, "person", "  ", from("news", "https://a.example/")); err == nil {
		t.Error("an entity with no name was accepted")
	}
	if _, err := s.Relate(ctx, "a", "b", "author", "", 0, entity.Provenance{}); err == nil {
		t.Error("a relation with no provenance was accepted")
	}
	if _, err := s.Relate(ctx, "", "b", "author", "", 0, from("news", "https://a.example/")); err == nil {
		t.Error("a relation from nothing was accepted")
	}
}

// TestItSurvivesARestart, because the value of this store is that it
// accumulates.
func TestItSurvivesARestart(t *testing.T) {
	dir := t.TempDir()

	first, err := entity.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	id, err := first.Assert(context.Background(), "person", "Alex Doe", from("news", "https://a.example/1"))
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := entity.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()

	got, err := second.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("the entity did not survive: %v", err)
	}
	if got.Name != "Alex Doe" {
		t.Errorf("name = %q", got.Name)
	}
}

func names(entities []*entity.Entity) string {
	var out []string
	for _, one := range entities {
		out = append(out, one.Name)
	}
	return strings.Join(out, ", ")
}

// TestTwoInMemoryStoresAreTwoStores.
//
// They used to be one. The name was a constant and the cache was shared, so
// every Open("") in a process was a handle on the same database. Two entities
// steps in one wave run in parallel goroutines with a handle each, and a
// shared-cache table lock is not what busy_timeout retries: the second failed
// at once with "database table is locked", the pipeline returned nothing, and
// the run discarded every record the crawl had produced.
//
// The quieter half of the same mistake is here too: two unrelated jobs in one
// process wrote into one graph and read each other's entities.
func TestTwoInMemoryStoresAreTwoStores(t *testing.T) {
	ctx := context.Background()

	first, err := entity.Open("")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()

	second, err := entity.Open("")
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()

	if _, err := first.Assert(ctx, "person", "Alex Doe", entity.Provenance{
		Job: "one", URL: "https://example.com/a",
	}); err != nil {
		t.Fatalf("assert: %v", err)
	}

	if found, err := second.Find(ctx, "person", "Alex Doe"); err == nil && found != nil {
		t.Error("one job's entity was visible in another job's store")
	}

	// And the two can be written at the same time, which is what a wave does.
	var wg sync.WaitGroup
	problems := make([]error, 2)
	for i, s := range []*entity.Store{first, second} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := range 40 {
				_, err := s.Assert(ctx, "person", fmt.Sprintf("Person %d", n), entity.Provenance{
					Job: fmt.Sprintf("job-%d", i),
					URL: fmt.Sprintf("https://example.com/%d/%d", i, n),
				})
				if err != nil {
					problems[i] = err
					return
				}
			}
		}()
	}
	wg.Wait()

	for i, err := range problems {
		if err != nil {
			t.Errorf("store %d could not be written while the other was: %v", i, err)
		}
	}
}

// TestAPropertyIsRecordedNotDecided.
//
// Two sources that disagree about a company's domain are two rows with a count
// each, not one row that flips depending on who was crawled last. This store
// records what was said; deciding is what a person does with the counts in
// front of them, which is the same reason a merge here is a row rather than a
// rewrite.
func TestAPropertyIsRecordedNotDecided(t *testing.T) {
	ctx := context.Background()

	store, err := entity.Open("")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	id, err := store.Assert(ctx, "company", "Acme", entity.Provenance{
		Job: "news", URL: "https://example.com/a",
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, said := range []struct {
		value string
		url   string
	}{
		{"acme.com", "https://example.com/a"},
		{"acme.com", "https://example.com/b"},
		{"acme.co.uk", "https://example.com/c"},
	} {
		if err := store.Describe(ctx, id, "domain", said.value, 0, entity.Provenance{
			Job: "news", URL: said.url,
		}); err != nil {
			t.Fatalf("describe: %v", err)
		}
	}

	props, err := store.Properties(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(props) != 2 {
		t.Fatalf("len(props) = %d, want both values kept: %+v", len(props), props)
	}

	// Both kept, each with its own count, which is the whole claim: the store
	// records and does not decide.
	counts := map[string]int{}
	for _, p := range props {
		counts[p.Value] = p.Assertions
	}
	if counts["acme.com"] != 2 {
		t.Errorf("acme.com was asserted %d times, want 2: %+v", counts["acme.com"], props)
	}
	if counts["acme.co.uk"] != 1 {
		t.Errorf("acme.co.uk was asserted %d times, want 1: %+v", counts["acme.co.uk"], props)
	}

	// Ordered by position and then by value, not by count. Two values of one
	// property share a position, so the tie breaks on the value and two runs
	// list them the same way. Ordering by count would have made the list
	// reshuffle itself as a crawl went on.
	if props[0].Value != "acme.co.uk" || props[1].Value != "acme.com" {
		t.Errorf("order = %q then %q, want position and then value", props[0].Value, props[1].Value)
	}
}

// TestPropertiesFollowAMerge.
//
// What is known about a person is known about them whichever spelling it was
// said under, the same as every other read here. A property that stayed behind
// on the losing spelling would make a merge lose information, which is the one
// thing this store's merge design exists to avoid.
func TestPropertiesFollowAMerge(t *testing.T) {
	ctx := context.Background()

	store, err := entity.Open("")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	said := entity.Provenance{Job: "news", URL: "https://example.com/a"}

	full, err := store.Assert(ctx, "person", "Alex Doe", said)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := store.Assert(ctx, "person", "A. Doe", said)
	if err != nil {
		t.Fatal(err)
	}

	if err := store.Describe(ctx, full, "role", "correspondent", 0, said); err != nil {
		t.Fatal(err)
	}
	if err := store.Describe(ctx, initial, "beat", "climate", 1, said); err != nil {
		t.Fatal(err)
	}

	if err := store.Merge(ctx, initial, full, entity.RuleManual, said); err != nil {
		t.Fatal(err)
	}

	// Both spellings now answer with both properties.
	for _, id := range []string{full, initial} {
		props, err := store.Properties(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if len(props) != 2 {
			t.Errorf("Properties(%s) = %+v, want both sides of the merge", id, props)
		}
	}
}

// TestKindsIsTheWayIntoAGraphYouDidNotBuild.
//
// Counted after merges, because two spellings of one person are one person and
// a count that said otherwise would be the first thing anybody noticed was
// wrong.
func TestKindsIsTheWayIntoAGraphYouDidNotBuild(t *testing.T) {
	ctx := context.Background()

	store, err := entity.Open("")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	said := entity.Provenance{Job: "news", URL: "https://example.com/a"}
	for _, one := range []struct{ kind, name string }{
		{"person", "Alex Doe"},
		{"person", "A. Doe"},
		{"person", "Sam Roe"},
		{"company", "Acme"},
	} {
		if _, err := store.Assert(ctx, one.kind, one.name, said); err != nil {
			t.Fatal(err)
		}
	}

	kinds, err := store.Kinds(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(kinds) != 2 {
		t.Fatalf("kinds = %+v, want person and company", kinds)
	}
	if kinds[0].Name != "person" || kinds[0].Entities != 3 {
		t.Errorf("kinds[0] = %+v, want 3 people", kinds[0])
	}

	// After a merge, the two spellings are one person.
	if err := store.Merge(ctx,
		entity.ID("person", "A. Doe"), entity.ID("person", "Alex Doe"),
		entity.RuleManual, said); err != nil {
		t.Fatal(err)
	}

	kinds, err = store.Kinds(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if kinds[0].Entities != 2 {
		t.Errorf("after a merge kinds[0] = %+v, want 2 people", kinds[0])
	}
}

// TestRetractingAJobTakesItsPropertiesBack.
//
// One job's contribution is one delete. A property left behind by a job that
// has been withdrawn is a fact with no evidence, which is exactly what the
// provenance trail exists to make impossible.
func TestRetractingAJobTakesItsPropertiesBack(t *testing.T) {
	ctx := context.Background()

	store, err := entity.Open("")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	good := entity.Provenance{Job: "good", URL: "https://example.com/a"}
	bad := entity.Provenance{Job: "bad", URL: "https://elsewhere.example/x"}

	id, err := store.Assert(ctx, "company", "Acme", good)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Describe(ctx, id, "domain", "acme.com", 0, good); err != nil {
		t.Fatal(err)
	}
	if err := store.Describe(ctx, id, "domain", "not-acme.example", 0, bad); err != nil {
		t.Fatal(err)
	}

	if _, err := store.Retract(ctx, "bad"); err != nil {
		t.Fatal(err)
	}

	props, err := store.Properties(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(props) != 1 {
		t.Fatalf("props = %+v, want only what the remaining job said", props)
	}
	if props[0].Value != "acme.com" {
		t.Errorf("props[0] = %+v, want the good job's value", props[0])
	}
}

// TestContract holds the SQLite store to what any entity graph promises.
//
// The suite is where the promises live, so that a second backend, or a client
// reaching the graph over a bus, is measured against the same thing rather than
// against its own tests. See [entitytest].
func TestContract(t *testing.T) {
	entitytest.Run(t, func(t *testing.T) entitytest.Graph {
		s, err := entity.Open("")
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		t.Cleanup(func() { s.Close() })
		return s
	})
}
