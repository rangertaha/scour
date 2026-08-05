// SPDX-License-Identifier: GPL-3.0-or-later

package entity_test

import (
	"context"
	"strings"
	"testing"

	"github.com/rangertaha/scour/internal/entity"
)

// TestAnInitialAndASurnameProposeTheOneFullNameItCouldBe, and only when there
// is one.
func TestAnInitialAndASurnameProposeTheOneFullNameItCouldBe(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	full, _ := s.Assert(ctx, "person", "Alex Doe", from("news", "https://a.example/1"))
	if _, err := s.Assert(ctx, "person", "A. Doe", from("news", "https://a.example/2")); err != nil {
		t.Fatal(err)
	}

	proposed, err := s.Candidates(ctx, "person", "A. Doe")
	if err != nil {
		t.Fatalf("candidates: %v", err)
	}
	if len(proposed) != 1 {
		t.Fatalf("proposed %d candidates, want the one full name it could be", len(proposed))
	}
	if proposed[0].To != full {
		t.Errorf("proposed merging into %s, want %s", proposed[0].To, full)
	}
	if proposed[0].Rule != entity.RuleInitial {
		t.Errorf("rule = %q", proposed[0].Rule)
	}

	// It proposed and did not merge, which is the whole shape of this.
	if _, err := s.Find(ctx, "person", "A. Doe"); err != nil {
		t.Error("proposing a candidate merged it")
	}
	if got, _ := s.Kind(ctx, "person"); len(got) != 2 {
		t.Errorf("people = %v, want both spellings still standing", names(got))
	}
}

// TestTwoCandidatesProposeNothing. A. Doe is Alex or Anna and the evidence
// cannot say which. Guessing is how the graph gets poisoned, so it does not
// guess, and it does not take the more asserted one either: that is a
// popularity contest dressed as evidence.
func TestTwoCandidatesProposeNothing(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	for _, name := range []string{"Alex Doe", "Alex Doe", "Alex Doe", "Anna Doe"} {
		if _, err := s.Assert(ctx, "person", name, from("news", "https://a.example/1")); err != nil {
			t.Fatal(err)
		}
	}

	proposed, err := s.Candidates(ctx, "person", "A. Doe")
	if err != nil {
		t.Fatal(err)
	}
	if len(proposed) != 0 {
		t.Errorf("proposed %d candidates for an ambiguous initial", len(proposed))
	}
}

// TestAFullNameFindsTheInitialWaitingForIt, so a crawl that meets the spellings
// in the other order gets the same answer.
func TestAFullNameFindsTheInitialWaitingForIt(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	initial, _ := s.Assert(ctx, "person", "A. Doe", from("news", "https://a.example/1"))

	proposed, err := s.Candidates(ctx, "person", "Alex Doe")
	if err != nil {
		t.Fatal(err)
	}
	if len(proposed) != 1 || proposed[0].From != initial {
		t.Fatalf("proposed %d candidates, want the initial it could be", len(proposed))
	}
	// The fuller spelling keeps its identity, because the store's names are
	// read by people.
	if proposed[0].To != entity.ID("person", "Alex Doe") {
		t.Errorf("the initial would have kept its identity")
	}

	// And once a second full name exists the initial is ambiguous again.
	if _, err := s.Assert(ctx, "person", "Anna Doe", from("news", "https://a.example/2")); err != nil {
		t.Fatal(err)
	}
	if proposed, _ := s.Candidates(ctx, "person", "Alex Doe"); len(proposed) != 0 {
		t.Errorf("proposed %d candidates once a second Doe existed", len(proposed))
	}
}

// TestNothingIsProposedOnSpellingAlone, because an edit-distance threshold that
// merges "Jon Smith" with "Jan Smith" is one character from a threshold that
// does not, and the failure is silent.
func TestNothingIsProposedOnSpellingAlone(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	for _, name := range []string{"Jon Smith", "Alex Doe", "Cher"} {
		if _, err := s.Assert(ctx, "person", name, from("news", "https://a.example/1")); err != nil {
			t.Fatal(err)
		}
	}

	for _, name := range []string{"Jan Smith", "Alexander Doe", "Chertoff", "Alex Doh"} {
		proposed, err := s.Candidates(ctx, "person", name)
		if err != nil {
			t.Fatal(err)
		}
		if len(proposed) != 0 {
			t.Errorf("%q proposed a merge on nothing but spelling", name)
		}
	}
}

// TestAMergeIsRecordedRatherThanApplied. Both entities keep their assertions
// and their provenance, which is what makes a merge on bad evidence undoable.
func TestAMergeIsRecordedRatherThanApplied(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	full, _ := s.Assert(ctx, "person", "Alex Doe", from("news", "https://a.example/1"))
	initial, _ := s.Assert(ctx, "person", "A. Doe", from("wire", "https://b.example/2"))

	if err := s.Merge(ctx, initial, full, entity.RuleManual, from("editor", "https://a.example/1")); err != nil {
		t.Fatalf("merge: %v", err)
	}

	// Both spellings now answer as one person, under the fuller name.
	for _, spelling := range []string{"A. Doe", "Alex Doe"} {
		got, err := s.Find(ctx, "person", spelling)
		if err != nil {
			t.Fatalf("find %q: %v", spelling, err)
		}
		if got.ID != full || got.Name != "Alex Doe" {
			t.Errorf("%q resolved to %q", spelling, got.Name)
		}
		if got.Assertions != 2 {
			t.Errorf("%q has %d assertions, want what both spellings said", spelling, got.Assertions)
		}
	}

	// One person in the list rather than two spellings.
	people, _ := s.Kind(ctx, "person")
	if len(people) != 1 {
		t.Errorf("people = %v", names(people))
	}

	// Both provenance trails survive, still attributed to whoever produced
	// them, which is what somebody checking the merge needs.
	said, err := s.Provenances(ctx, full)
	if err != nil {
		t.Fatal(err)
	}
	jobs := map[string]bool{}
	for _, one := range said {
		jobs[one.Job] = true
	}
	if !jobs["news"] || !jobs["wire"] {
		t.Errorf("provenance = %v, want both sides", said)
	}

	// And the merge itself is on the record, with who made it.
	aliases, err := s.Aliases(ctx, full)
	if err != nil {
		t.Fatal(err)
	}
	if len(aliases) != 1 || aliases[0].ID != initial || aliases[0].Name != "A. Doe" {
		t.Fatalf("aliases = %+v", aliases)
	}
	if aliases[0].Said.Job != "editor" || aliases[0].Rule != entity.RuleManual {
		t.Errorf("the merge does not say who made it: %+v", aliases[0])
	}
}

// TestAMergeMadeOnBadEvidenceIsUndone, and the entity it collapsed comes back
// with everything it had, because nothing was ever moved.
func TestAMergeMadeOnBadEvidenceIsUndone(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	full, _ := s.Assert(ctx, "person", "Alex Doe", from("news", "https://a.example/1"))
	initial, _ := s.Assert(ctx, "person", "A. Doe", from("wire", "https://b.example/2"))
	if err := s.Merge(ctx, initial, full, entity.RuleInitial, from("resolver", "https://b.example/2")); err != nil {
		t.Fatal(err)
	}

	if err := s.Unmerge(ctx, initial); err != nil {
		t.Fatalf("unmerge: %v", err)
	}

	back, err := s.Get(ctx, initial)
	if err != nil {
		t.Fatalf("the merged spelling did not come back: %v", err)
	}
	if back.Name != "A. Doe" || back.Assertions != 1 {
		t.Errorf("came back as %+v", back)
	}
	if kept, _ := s.Get(ctx, full); kept.Assertions != 1 {
		t.Errorf("the survivor kept %d assertions", kept.Assertions)
	}
	if err := s.Unmerge(ctx, initial); err == nil {
		t.Error("unmerging something that was not merged was accepted")
	}
}

// TestMergingIntoSomethingAlreadyMergedFlattens, so an alias never points at
// another alias and every read stays one join deep.
func TestMergingIntoSomethingAlreadyMergedFlattens(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	one, _ := s.Assert(ctx, "person", "A. Doe", from("news", "https://a.example/1"))
	two, _ := s.Assert(ctx, "person", "Alex Doe", from("news", "https://a.example/2"))
	three, _ := s.Assert(ctx, "person", "Alexander Doe", from("news", "https://a.example/3"))

	said := from("editor", "https://a.example/")
	if err := s.Merge(ctx, one, two, entity.RuleManual, said); err != nil {
		t.Fatal(err)
	}
	// Now merge the survivor away. The one already pointing at it must follow.
	if err := s.Merge(ctx, two, three, entity.RuleManual, said); err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{one, two, three} {
		got, err := s.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if got.ID != three {
			t.Errorf("%s resolved to %s, want the last survivor", id, got.ID)
		}
	}
	if got, _ := s.Get(ctx, three); got.Assertions != 3 {
		t.Errorf("assertions = %d, want all three spellings", got.Assertions)
	}
	if aliases, _ := s.Aliases(ctx, three); len(aliases) != 2 {
		t.Errorf("aliases = %+v, want both spellings pointing straight at the survivor", aliases)
	}

	// Merging a pair that is already one entity is not an error, so a caller
	// acting on a proposal it made a moment ago does not need a lock.
	if err := s.Merge(ctx, one, three, entity.RuleManual, said); err != nil {
		t.Errorf("merging an already merged pair: %v", err)
	}
}

// TestAMergeIsAnAssertionSoItHasToSayWhoSaidIt, and never crosses two kinds: a
// person who is also a company is a mistake upstream, and collapsing them would
// spread it.
func TestAMergeIsAnAssertionSoItHasToSayWhoSaidIt(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	person, _ := s.Assert(ctx, "person", "Alex Doe", from("news", "https://a.example/1"))
	company, _ := s.Assert(ctx, "company", "Alex Doe", from("news", "https://a.example/1"))

	if err := s.Merge(ctx, person, company, entity.RuleManual, from("editor", "https://a.example/")); err == nil {
		t.Error("a person was merged into a company")
	}
	if err := s.Merge(ctx, person, person, entity.RuleManual, entity.Provenance{}); err == nil {
		t.Error("a merge with no provenance was accepted")
	}
	if err := s.Merge(ctx, person, "nothing", entity.RuleManual, from("editor", "https://a.example/")); err == nil {
		t.Error("something was merged into an entity that does not exist")
	}
}

// TestOneJobsAssertionsAreStillOneDeleteAfterAMerge.
//
// The property the whole store is built on, checked against the thing most
// likely to break it. A merge records rather than rewrites, so retracting a job
// still finds its assertions under the spelling they were made with, and the
// merge that spelling was part of goes with the spelling.
func TestOneJobsAssertionsAreStillOneDeleteAfterAMerge(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	publisher, _ := s.Assert(ctx, "company", "The Example", from("news", "https://a.example/"))
	full, _ := s.Assert(ctx, "person", "Alex Doe", from("news", "https://a.example/1"))
	if err := s.Relate(ctx, publisher, full, "author", "", from("news", "https://a.example/1")); err != nil {
		t.Fatal(err)
	}

	initial, _ := s.Assert(ctx, "person", "A. Doe", from("wire", "https://b.example/2"))
	if err := s.Relate(ctx, publisher, initial, "author", "", from("wire", "https://b.example/2")); err != nil {
		t.Fatal(err)
	}

	if err := s.Merge(ctx, initial, full, entity.RuleInitial, from("wire", "https://b.example/2")); err != nil {
		t.Fatal(err)
	}

	// One author, credited with both jobs' pages, which is why anybody merges.
	authors, err := s.Related(ctx, publisher, "author", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(authors) != 1 || authors[0].Assertions != 2 {
		t.Fatalf("authors = %v", names(authors))
	}

	if _, err := s.Retract(ctx, "wire"); err != nil {
		t.Fatalf("retract: %v", err)
	}

	// The retracted job's spelling is gone, and so is the merge it was in.
	if _, err := s.Find(ctx, "person", "A. Doe"); err == nil {
		t.Error("a retracted job's spelling survived")
	}
	kept, err := s.Get(ctx, full)
	if err != nil {
		t.Fatalf("the other job's entity went with it: %v", err)
	}
	if kept.Assertions != 1 {
		t.Errorf("assertions = %d, want only what the surviving job said", kept.Assertions)
	}
	if aliases, _ := s.Aliases(ctx, full); len(aliases) != 0 {
		t.Errorf("the merge outlived the evidence for it: %+v", aliases)
	}

	authors, err = s.Related(ctx, publisher, "author", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(authors) != 1 || authors[0].Assertions != 1 {
		t.Errorf("authors = %v with %d assertions", names(authors), authors[0].Assertions)
	}
}

// TestRetractingTheJobThatMergedTakesTheMergeBack, and leaves what the two
// spellings said standing, because a merge is a thing a job said and not a
// thing that became true.
func TestRetractingTheJobThatMergedTakesTheMergeBack(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	full, _ := s.Assert(ctx, "person", "Alex Doe", from("news", "https://a.example/1"))
	initial, _ := s.Assert(ctx, "person", "A. Doe", from("wire", "https://b.example/2"))
	if err := s.Merge(ctx, initial, full, entity.RuleInitial, from("resolver", "https://b.example/2")); err != nil {
		t.Fatal(err)
	}

	removed, err := s.Retract(ctx, "resolver")
	if err != nil {
		t.Fatalf("retract: %v", err)
	}
	if removed != 1 {
		t.Errorf("removed = %d, want the one merge that job made", removed)
	}

	if got, _ := s.Kind(ctx, "person"); len(got) != 2 {
		t.Errorf("people = %v, want both spellings back", names(got))
	}
	for _, spelling := range []string{"A. Doe", "Alex Doe"} {
		got, err := s.Find(ctx, "person", spelling)
		if err != nil {
			t.Fatalf("%q was retracted along with the merge: %v", spelling, err)
		}
		if got.Assertions != 1 {
			t.Errorf("%q has %d assertions", spelling, got.Assertions)
		}
	}
}

// TestAMergeIsRefusedIfTheAmbiguityAppearedAfterItWasProposed.
//
// Candidates counted in a read of its own and Merge took its word for it, so
// the safety rule was a read-then-write across two transactions and anything
// asserted in between defeated it. The entities step calls both per record
// while a crawl is running, so what decided the merge was the order the pages
// arrived in and, in a wave, which goroutine got there first. It decided
// permanently, because a merge is a row.
//
// This is the shape the frontier fence fixed once already, by putting the
// condition in the write rather than in a read before it.
func TestAMergeIsRefusedIfTheAmbiguityAppearedAfterItWasProposed(t *testing.T) {
	ctx := context.Background()

	store, err := entity.Open("")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	said := entity.Provenance{Job: "news", URL: "https://example.com/a"}
	for _, name := range []string{"Alan Doe", "A. Doe"} {
		if _, err := store.Assert(ctx, "person", name, said); err != nil {
			t.Fatalf("assert %s: %v", name, err)
		}
	}

	// Proposed while only one full name was known.
	proposed, err := store.Candidates(ctx, "person", "A. Doe")
	if err != nil {
		t.Fatal(err)
	}
	if len(proposed) != 1 {
		t.Fatalf("len(proposed) = %d, want the one unambiguous candidate", len(proposed))
	}

	// A second full name arrives before the merge is made, which is the whole
	// of what a crawl does between two records.
	if _, err := store.Assert(ctx, "person", "Alex Doe", said); err != nil {
		t.Fatal(err)
	}

	err = store.Merge(ctx, proposed[0].From, proposed[0].To, proposed[0].Rule, said)
	if err == nil {
		t.Fatal("a merge proposed before the ambiguity appeared was made anyway")
	}
	if !strings.Contains(err.Error(), "since it was proposed") {
		t.Errorf("the error does not say what changed: %v", err)
	}

	// All three are still there and none of them reads as another.
	people, err := store.Kind(ctx, "person")
	if err != nil {
		t.Fatal(err)
	}
	if len(people) != 3 {
		t.Errorf("len(people) = %d, want the three spellings left alone", len(people))
	}

	// And a person saying so is still obeyed, because the rule exists to stop
	// the machine guessing and not to overrule somebody who knows.
	if err := store.Merge(ctx, proposed[0].From, proposed[0].To, entity.RuleManual, said); err != nil {
		t.Errorf("a manual merge was refused by the ambiguity rule: %v", err)
	}
}
