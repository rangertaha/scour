// SPDX-License-Identifier: GPL-3.0-or-later

package entities_test

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rangertaha/scour/internal/engine"
	"github.com/rangertaha/scour/internal/entity"
	"github.com/rangertaha/scour/internal/pipeline"
	"github.com/rangertaha/scour/internal/record"

	_ "github.com/rangertaha/scour/internal/pipeline/entities"
	_ "github.com/rangertaha/scour/internal/pipeline/steps"
)

var fetched = time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)

// job is the shape the entity store was designed around: a byline wanted in the
// record and worth keeping as a link, and a publisher that is not on the page
// at all because it is the site.
func job(t *testing.T, steps string) *engine.Job {
	t.Helper()

	src := `
job "news" {
  start = ["https://example.com/"]

  item "article" {
    property "title" {
      type     = str
      required = true
    }

    property "author" {
      type   = entity
      entity = "person"
    }

    relation "publisher" {
      entity   = "company"
      property = self.domain
    }
  }

  pipeline {
` + steps + `
  }
}
`
	doc, err := engine.Parse([]byte(src), "job.hcl")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := doc.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	return doc.Jobs[0]
}

// step writes the block under test, always multi-line: HCL allows one attribute
// on a single line and no more.
func step(dir string, merge bool) string {
	block := "    step \"entities\" \"article\" {\n      dir = " + strconv.Quote(dir) + "\n"
	if merge {
		block += "      merge = true\n"
	}
	return block + "    }"
}

func rec(url, title, author string) *record.Record {
	return &record.Record{
		Item:    "article",
		URL:     url,
		Spec:    "abc123",
		Fetched: fetched,
		Values:  map[string]string{"title": title, "author": author},
	}
}

// crawl runs the pipeline once, the way a run does, and gives back what came
// out of it alongside the store it wrote to.
func crawl(t *testing.T, dir string, steps string, records ...*record.Record) ([]*record.Record, entity.Store) {
	t.Helper()

	p, err := pipeline.New(context.Background(), job(t, steps))
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	out, err := p.Run(context.Background(), records)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if err := p.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	s, err := entity.Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return out, s
}

// TestAnUnknownBylineExtractsAsEasilyAsAFamiliarOne.
//
// The proof Phase 5.75 asks for, and the failure mode the whole design has to
// avoid: known entities improving extraction is also known entities crowding
// out new ones. If a byline only comes through cleanly when it matches
// something already stored, discovery stops and the store converges on what it
// already believed, looking exactly like rising accuracy the whole time.
//
// So the guard is that known entities raise confidence and never gate
// extraction. Here that is checkable: the author nobody has ever heard of comes
// out of the pipeline identical to the one asserted a hundred times, is
// asserted just the same, and is linked to the publisher just the same.
func TestAnUnknownBylineExtractsAsEasilyAsAFamiliarOne(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	block := step(dir, false)

	// Make one author familiar. Several pages, so the store is as sure about
	// them as a crawl ever gets.
	familiar := []*record.Record{
		rec("https://example.com/1", "One", "Alex Doe"),
		rec("https://example.com/2", "Two", "Alex Doe"),
		rec("https://example.com/3", "Three", "Alex Doe"),
	}
	crawl(t, dir, block, familiar...)

	// Now a page with a byline belonging to nobody, beside one belonging to
	// somebody.
	known := rec("https://example.com/4", "Four", "Alex Doe")
	unknown := rec("https://example.com/5", "Five", "Nobody Atall")

	out, store := crawl(t, dir, block, known, unknown)

	// Nothing was dropped, nothing was reordered, nothing was rewritten.
	if len(out) != 2 {
		t.Fatalf("%d records came out of the pipeline, want both", len(out))
	}
	if out[0].Get("author") != "Alex Doe" || out[1].Get("author") != "Nobody Atall" {
		t.Fatalf("records = %q and %q", out[0].Get("author"), out[1].Get("author"))
	}
	if out[1].Get("title") != "Five" || out[1].URL != "https://example.com/5" {
		t.Errorf("the unknown byline's record came out changed: %+v", out[1])
	}

	// The unknown author is in the store, on the first page they appeared on.
	stranger, err := store.Find(ctx, "person", "Nobody Atall")
	if err != nil {
		t.Fatalf("an unknown byline was not asserted: %v", err)
	}
	if stranger.Assertions != 1 {
		t.Errorf("assertions = %d", stranger.Assertions)
	}

	// And is linked to the publisher exactly as the familiar one is, so the
	// question the store exists for answers with both.
	publisher, err := store.Find(ctx, "company", "example.com")
	if err != nil {
		t.Fatalf("the publisher was not asserted: %v", err)
	}
	authors, err := store.Related(ctx, publisher.ID, "author", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(authors) != 2 {
		t.Fatalf("the publisher has %v, want the stranger among them", names(authors))
	}
	// Most asserted first, which is the store being surer about the familiar
	// one and not the store preferring it.
	if authors[0].Name != "Alex Doe" || authors[1].Name != "Nobody Atall" {
		t.Errorf("authors = %v", names(authors))
	}

	said, err := store.Provenances(ctx, stranger.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(said) == 0 || said[0].Job != "news" || said[0].URL != "https://example.com/5" {
		t.Errorf("the stranger's provenance = %+v", said)
	}
}

// TestOneJobsAssertionsAreOneDelete, from a crawl rather than from calls, which
// is the other half of what Phase 5.75 is proved by.
func TestOneJobsAssertionsAreOneDelete(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	_, store := crawl(t, dir, step(dir, false),
		rec("https://example.com/1", "One", "Alex Doe"),
		rec("https://example.com/2", "Two", "Sam Smith"))

	if people, _ := store.Kind(ctx, "person"); len(people) != 2 {
		t.Fatalf("people = %v", names(people))
	}

	removed, err := store.Retract(ctx, "news")
	if err != nil {
		t.Fatalf("retract: %v", err)
	}
	if removed == 0 {
		t.Fatal("nothing was retracted")
	}
	if people, _ := store.Kind(ctx, "person"); len(people) != 0 {
		t.Errorf("people = %v after the job was retracted", names(people))
	}
	if companies, _ := store.Kind(ctx, "company"); len(companies) != 0 {
		t.Errorf("companies = %v after the job was retracted", names(companies))
	}
}

// TestTheStepAssertsAnEdgeFromThePublisherToEachByline, which is the shape
// nobody had to name: the relation is the property's own name.
func TestTheStepAssertsAnEdgeFromThePublisherToEachByline(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	_, store := crawl(t, dir, step(dir, false),
		rec("https://www.example.com/1", "One", "Alex Doe"),
		rec("https://news.example.com/2", "Two", "Alex Doe"))

	// Two hosts, one publisher, because the relation is the registrable domain
	// and not the host.
	companies, err := store.Kind(ctx, "company")
	if err != nil {
		t.Fatal(err)
	}
	if len(companies) != 1 || companies[0].Name != "example.com" {
		t.Fatalf("companies = %v", names(companies))
	}

	authors, err := store.Related(ctx, companies[0].ID, "author", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(authors) != 1 || authors[0].Assertions != 2 {
		t.Fatalf("authors = %v", names(authors))
	}
}

// TestARecordWithNoBylineAssertsNoPerson, rather than asserting an empty one.
// An entity named "" would be one row every job on the web converged on.
func TestARecordWithNoBylineAssertsNoPerson(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	out, store := crawl(t, dir, step(dir, false),
		rec("https://example.com/1", "One", "   "))

	if len(out) != 1 {
		t.Fatalf("a record with no byline was dropped")
	}
	if people, _ := store.Kind(ctx, "person"); len(people) != 0 {
		t.Errorf("people = %v", names(people))
	}
	// The publisher is still asserted, because it did not come from the page.
	if companies, _ := store.Kind(ctx, "company"); len(companies) != 1 {
		t.Errorf("companies = %v", names(companies))
	}
}

// TestTheStepLeavesOtherItemsAlone, the same rule every other step kind
// follows: a step is named for the item it works on.
func TestTheStepLeavesOtherItemsAlone(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	comment := rec("https://example.com/1", "One", "Alex Doe")
	comment.Item = "comment"

	out, store := crawl(t, dir, step(dir, false), comment)

	if len(out) != 1 {
		t.Fatal("a record for another item was dropped")
	}
	if people, _ := store.Kind(ctx, "person"); len(people) != 0 {
		t.Errorf("people = %v, want nothing from an item this step is not named for", names(people))
	}
}

// TestAStepNotNamedForAnItemIsRefused, at build time rather than by doing
// nothing at run time: the shape is where the entity kinds are written and
// there is nothing to assert without it.
func TestAStepNotNamedForAnItemIsRefused(t *testing.T) {
	_, err := pipeline.New(context.Background(), job(t, `    step "entities" "graph" {
    }`))
	if err == nil {
		t.Fatal("a step with no shape to work from was built")
	}
	if !strings.Contains(err.Error(), "named for an item") {
		t.Errorf("error = %v", err)
	}
}

// TestMergingIsOffUnlessTheJobAsksForIt. A crawl is the worst place to decide
// two people are one: it would make thousands of them unattended, and a wrong
// one corrupts the graph in a way nobody notices.
func TestMergingIsOffUnlessTheJobAsksForIt(t *testing.T) {
	ctx := context.Background()

	for _, tc := range []struct {
		name  string
		merge bool
		want  int
		wants string
	}{
		{name: "off by default", merge: false, want: 2,
			wants: "two spellings, because nothing merged"},
		{name: "on when asked", merge: true, want: 1,
			wants: "one person, because the initial had exactly one full name to be"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()

			_, store := crawl(t, dir, step(dir, tc.merge),
				rec("https://example.com/1", "One", "Alex Doe"),
				rec("https://example.com/2", "Two", "A. Doe"))

			people, err := store.Kind(ctx, "person")
			if err != nil {
				t.Fatal(err)
			}
			if len(people) != tc.want {
				t.Fatalf("people = %v, want %s", names(people), tc.wants)
			}
			if tc.want == 1 && people[0].Name != "Alex Doe" {
				t.Errorf("the initial kept its identity: %v", names(people))
			}
		})
	}
}

// TestAnAmbiguousInitialIsNotMergedByACrawl, which is the reason the rule
// counts candidates: A. Doe is Alex or Anna, and guessing is how the graph gets
// poisoned.
func TestAnAmbiguousInitialIsNotMergedByACrawl(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	_, store := crawl(t, dir, step(dir, true),
		rec("https://example.com/1", "One", "Alex Doe"),
		rec("https://example.com/2", "Two", "Anna Doe"),
		rec("https://example.com/3", "Three", "A. Doe"))

	people, err := store.Kind(ctx, "person")
	if err != nil {
		t.Fatal(err)
	}
	if len(people) != 3 {
		t.Errorf("people = %v, want the ambiguous initial left alone", names(people))
	}
}

func names(entities []*entity.Entity) string {
	var out []string
	for _, one := range entities {
		out = append(out, one.Name)
	}
	return strings.Join(out, ", ")
}

// TestAPageDescribesTheEntityItNames.
//
// The feature this step was extended for, exercised through a document rather
// than through the store, which is where it turned out not to work: an
// entity-typed property with nested properties is the shape the step reads, and
// validation refused exactly that shape, so nothing anybody could write reached
// the code. The store's own tests passed the whole time, because they called
// Describe directly.
//
// `author.role` is the person's role, not the article's. Putting it on the
// article would attach it to every other article that person wrote.
func TestAPageDescribesTheEntityItNames(t *testing.T) {
	dir := t.TempDir()

	src := `
job "news" {
  start = ["https://example.com/"]

  item "article" {
    property "title" {
      type = str
    }

    property "author" {
      type   = entity
      entity = "person"

      property "role" {
        type = str
      }

      property "beat" {
        type = str
      }
    }
  }

  pipeline {
    step "entities" "article" {
      dir = "` + dir + `"
    }
  }
}
`
	doc, err := engine.Parse([]byte(src), "job.hcl")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := doc.Validate(); err != nil {
		t.Fatalf("a document declaring what a page says about an author was refused: %v", err)
	}

	graph, err := pipeline.New(context.Background(), doc.Jobs[0])
	if err != nil {
		t.Fatal(err)
	}
	defer graph.Close()

	in := &record.Record{
		Item: "article", URL: "https://example.com/a", Spec: "abc",
		Fetched: time.Date(2026, 8, 5, 9, 0, 0, 0, time.UTC),
		Values: map[string]string{
			"title":       "A story",
			"author":      "Alex Doe",
			"author.role": "correspondent",
			"author.beat": "climate",
		},
	}
	if _, err := graph.Run(context.Background(), []*record.Record{in}); err != nil {
		t.Fatalf("run: %v", err)
	}

	store, err := entity.New(context.Background(), entity.Config{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	person, err := store.Find(context.Background(), "person", "Alex Doe")
	if err != nil {
		t.Fatalf("the author was not asserted: %v", err)
	}

	props, err := store.Properties(context.Background(), person.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(props) != 2 {
		t.Fatalf("Properties = %+v, want what the page said about the person", props)
	}
	if props[0].Name != "role" || props[0].Value != "correspondent" {
		t.Errorf("props[0] = %+v, want the role, in the shape's order", props[0])
	}
	if props[1].Name != "beat" || props[1].Value != "climate" {
		t.Errorf("props[1] = %+v, want the beat second", props[1])
	}
}
