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
		if err := s.Relate(ctx, publisher, id, "author", "climate@7", from("news", "https://a.example/story")); err != nil {
			t.Fatal(err)
		}
	}
	// One author on another topic, to prove the topic narrows it.
	other, _ := s.Assert(ctx, "person", "Jo Bloggs", from("news", "https://a.example/sport"))
	if err := s.Relate(ctx, publisher, other, "author", "sport@1", from("news", "https://a.example/sport")); err != nil {
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
	if err := s.Relate(ctx, publisher, wrong, "author", "", from("broken", "https://b.example/2")); err != nil {
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
	if err := s.Relate(ctx, "a", "b", "author", "", entity.Provenance{}); err == nil {
		t.Error("a relation with no provenance was accepted")
	}
	if err := s.Relate(ctx, "", "b", "author", "", from("news", "https://a.example/")); err == nil {
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
