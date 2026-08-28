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

	Candidates(ctx context.Context, kind, name string) ([]entity.Candidate, error)
	Merge(ctx context.Context, from, to, rule string, said entity.Provenance) error
	Unmerge(ctx context.Context, alias string) error
	Aliases(ctx context.Context, id string) ([]entity.Alias, error)
	Tag(ctx context.Context, subject, topic string, said entity.Provenance) error
	Topics(ctx context.Context, subject string) ([]entity.Property, error)
	About(ctx context.Context, topic string) ([]*entity.Entity, error)
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
	t.Run("TypesAndEntitiesCarryTopics", func(t *testing.T) { testTopics(t, open) })
	t.Run("AMergeIsProposedRecordedAndUndoable", func(t *testing.T) { testResolution(t, open) })
	t.Run("AProposedMergeIsAcceptedByTheStore", func(t *testing.T) { testAProposedMergeIsAcceptedByTheStore(t, open) })
	t.Run("RetractKeepsWhatIsStillAsserted", func(t *testing.T) { testRetractKeepsWhatIsStillAsserted(t, open) })
	t.Run("RetractSweepsTheEvidenceWithTheFact", func(t *testing.T) { testRetractSweepsTheEvidenceWithTheFact(t, open) })
	t.Run("AKindIsTheSameKindWhoeverSpelledIt", func(t *testing.T) { testAKindIsTheSameKindWhoeverSpelledIt(t, open) })
	t.Run("AMergeGoesToTheNameTheRuleActuallyFound", func(t *testing.T) { testAMergeGoesToTheNameTheRuleFound(t, open) })
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

// testTopics: a type and an entity can both be about something.
//
// "person" being about climate is a different claim from any particular person
// being about it, and both are worth making, so a type gets a derived id and is
// a subject like anything else.
func testTopics(t *testing.T, open Open) {
	ctx := context.Background()
	g := open(t)
	from := said("news", "https://a.example/1")

	author, err := g.Assert(ctx, "person", "Alex Doe", from)
	if err != nil {
		t.Fatal(err)
	}
	other, err := g.Assert(ctx, "person", "Sam Roe", from)
	if err != nil {
		t.Fatal(err)
	}

	// Twice, so the count means something and the ordering has work to do.
	for _, url := range []string{"https://a.example/1", "https://a.example/2"} {
		if err := g.Tag(ctx, author, "climate@7", said("news", url)); err != nil {
			t.Fatal(err)
		}
	}
	if err := g.Tag(ctx, author, "energy@2", from); err != nil {
		t.Fatal(err)
	}
	if err := g.Tag(ctx, other, "climate@7", from); err != nil {
		t.Fatal(err)
	}

	// A type carries one too.
	if err := g.Tag(ctx, entity.KindID("person"), "climate@7", from); err != nil {
		t.Fatal(err)
	}

	topics, err := g.Topics(ctx, author)
	if err != nil {
		t.Fatal(err)
	}
	if len(topics) != 2 {
		t.Fatalf("Topics = %+v, want both", topics)
	}
	if topics[0].Value != "climate@7" || topics[0].Assertions != 2 {
		t.Errorf("topics[0] = %+v, want the most-asserted first", topics[0])
	}

	if kind, err := g.Topics(ctx, entity.KindID("person")); err != nil {
		t.Fatal(err)
	} else if len(kind) != 1 || kind[0].Value != "climate@7" {
		t.Errorf("the type's topics = %+v", kind)
	}

	// The reverse: what is about this.
	about, err := g.About(ctx, "climate@7")
	if err != nil {
		t.Fatal(err)
	}
	if len(about) != 2 {
		t.Fatalf("About = %+v, want both people and not the type", about)
	}
	if about[0].Name != "Alex Doe" {
		t.Errorf("about[0] = %+v, want the most-asserted first", about[0])
	}

	// A topic without a version is refused: the graph would be saying an entity
	// is about a subject without saying which training decided it.
	if err := g.Tag(ctx, author, "climate", from); err == nil {
		t.Error("a topic with no version was accepted")
	}

	// And the reserved name cannot be written as an ordinary property.
	if err := g.Describe(ctx, author, entity.TopicProperty, "climate@7", 0, from); err == nil {
		t.Error("the reserved topic name was writable as a property")
	}
}

// testResolution: propose, record, undo.
//
// In the contract because every method in it is a promise a second backend has
// to keep, and these three were the ones nothing exercised: they sat in the
// interface while only the SQLite store's own tests touched them, so the bus
// client's versions had never run at all. That is the same hole a shared suite
// exists to close, one level in.
//
// The rule itself is deliberately conservative. Merging two people who are not
// the same person corrupts the graph in a way nobody notices: the rows still
// look right and the counts go up. Failing to merge is visible, because the
// same person is in the list twice and somebody says so.
func testResolution(t *testing.T, open Open) {
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

	// One full name it could belong to, so the initial has a candidate.
	proposed, err := g.Candidates(ctx, "person", "A. Doe")
	if err != nil {
		t.Fatal(err)
	}
	if len(proposed) != 1 {
		t.Fatalf("Candidates = %+v, want the one unambiguous proposal", proposed)
	}
	if proposed[0].From != initial || proposed[0].To != full {
		t.Errorf("Candidates proposed %s into %s, want %s into %s",
			proposed[0].From, proposed[0].To, initial, full)
	}
	if proposed[0].Rule == "" {
		t.Error("a proposal that does not say which rule made it cannot be undone as a class")
	}

	// Proposing writes nothing: the two are still two.
	if kinds, err := g.Kinds(ctx); err != nil {
		t.Fatal(err)
	} else if kinds[0].Entities != 2 {
		t.Errorf("Candidates changed the graph: %+v", kinds)
	}

	// A second full name makes it ambiguous, and nothing is proposed rather
	// than the more-asserted one, which would be a popularity contest dressed
	// as evidence.
	if _, err := g.Assert(ctx, "person", "Alan Doe", from); err != nil {
		t.Fatal(err)
	}
	ambiguous, err := g.Candidates(ctx, "person", "A. Doe")
	if err != nil {
		t.Fatal(err)
	}
	if len(ambiguous) != 0 {
		t.Errorf("Candidates = %+v, want nothing proposed while two names compete", ambiguous)
	}

	// A person saying so is still obeyed, and it is recorded rather than
	// applied: both spellings keep their rows.
	if err := g.Merge(ctx, initial, full, entity.RuleManual, from); err != nil {
		t.Fatal(err)
	}

	aliases, err := g.Aliases(ctx, full)
	if err != nil {
		t.Fatal(err)
	}
	if len(aliases) != 1 || aliases[0].ID != initial {
		t.Fatalf("Aliases = %+v, want the spelling that lost its identity", aliases)
	}
	if aliases[0].Rule != entity.RuleManual {
		t.Errorf("the alias does not say which rule made it: %+v", aliases[0])
	}
	if aliases[0].Said.Job == "" {
		t.Errorf("the alias carries no provenance: %+v", aliases[0])
	}

	// And undoing it is one delete, after which they are two people again.
	if err := g.Unmerge(ctx, initial); err != nil {
		t.Fatal(err)
	}
	if after, err := g.Aliases(ctx, full); err != nil {
		t.Fatal(err)
	} else if len(after) != 0 {
		t.Errorf("Aliases after Unmerge = %+v", after)
	}

	one, err := g.Get(ctx, initial)
	if err != nil {
		t.Fatalf("the spelling did not survive being unmerged: %v", err)
	}
	if one.Name != "A. Doe" {
		t.Errorf("Get(initial) = %+v, want it back as itself", one)
	}
}

// testRetractKeepsWhatIsStillAsserted: the sweeps take what nobody says any
// more, and nothing else.
//
// Three ways that went wrong, all of them silent. A topic on a type was deleted
// by the next Retract of any job at all, because a type has no row of its own
// and the sweep looked for one. A property whose entity was removed in the same
// Retract survived as an orphan, still readable, and reattached itself if the
// same name was asserted again, since ids come from the name. And a merge the
// store had proposed itself was refused, because the proposal counted people
// and the check counted spellings.
// testRetractSweepsTheEvidenceWithTheFact.
//
// A property whose subject disappears is swept, and the assertions that stated
// it were left behind: the delete that removes evidence removes it by job, and
// the job that described a thing is usually not the job that was retracted.
// Nothing else removed them, and the recount at the top of Retract reads them.
//
// An id is derived from kind and name, so asserting the same name again is the
// same id, and the old evidence was waiting. The next Retract of any job at all
// - including one of a job that does not exist, which is meant to be a no-op -
// then counted the property as stated twice when one job had stated it once.
// Retracting that one job left the count standing on the orphan alone, so the
// store went on serving a fact with no live asserter, which is the one thing
// the provenance trail exists to make impossible.
//
// In the conformance suite because it is a promise every backend makes.
func testRetractSweepsTheEvidenceWithTheFact(t *testing.T, open Open) {
	s := open(t)
	ctx := context.Background()

	// One job asserts the company; another describes it.
	ghost, err := s.Assert(ctx, "company", "Ghost", said("drop", "https://a.example/1"))
	if err != nil {
		t.Fatalf("assert: %v", err)
	}
	if err := s.Describe(ctx, ghost, "domain", "ghost.example", 0, said("keep", "https://b.example/1")); err != nil {
		t.Fatalf("describe: %v", err)
	}

	// The only job asserting the company goes, so the company goes and its
	// properties go with it.
	if _, err := s.Retract(ctx, "drop"); err != nil {
		t.Fatalf("retract: %v", err)
	}

	// The same name asserted again is the same id, and one job states the
	// property once.
	again, err := s.Assert(ctx, "company", "Ghost", said("again", "https://c.example/1"))
	if err != nil {
		t.Fatalf("assert: %v", err)
	}
	if err := s.Describe(ctx, again, "domain", "ghost.example", 0, said("again", "https://c.example/1")); err != nil {
		t.Fatalf("describe: %v", err)
	}

	// A no-op retract recounts everything, which is where the orphan surfaced.
	if _, err := s.Retract(ctx, "nosuchjob"); err != nil {
		t.Fatalf("retract: %v", err)
	}

	props, err := s.Properties(ctx, again)
	if err != nil {
		t.Fatalf("properties: %v", err)
	}
	if len(props) == 0 {
		t.Fatal("the property is gone, though a job asserted it after the sweep")
	}
	for _, p := range props {
		if p.Assertions != 1 {
			t.Errorf("%s = %s is stated by one job and counts %d: the evidence for a "+
				"swept property outlived it", p.Name, p.Value, p.Assertions)
		}
	}
}

func testRetractKeepsWhatIsStillAsserted(t *testing.T, open Open) {
	ctx := context.Background()
	g := open(t)

	keep := said("keep", "https://a.example/1")
	drop := said("drop", "https://b.example/2")

	acme, err := g.Assert(ctx, "company", "Acme", keep)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Tag(ctx, entity.KindID("company"), "markets@1", keep); err != nil {
		t.Fatal(err)
	}

	// An entity only the withdrawn job asserted, described by the job that
	// stays: the property has to go with the entity.
	ghost, err := g.Assert(ctx, "company", "Ghost", drop)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Describe(ctx, ghost, "domain", "ghost.example", 0, keep); err != nil {
		t.Fatal(err)
	}

	if _, err := g.Retract(ctx, "drop"); err != nil {
		t.Fatal(err)
	}

	// The type's topic is still there. Nothing said otherwise.
	topics, err := g.Topics(ctx, entity.KindID("company"))
	if err != nil {
		t.Fatal(err)
	}
	if len(topics) != 1 || topics[0].Value != "markets@1" {
		t.Errorf("the type's topic = %+v, want it kept: no job withdrew it", topics)
	}

	// The withdrawn entity is gone, and so is what was said about it.
	if _, err := g.Get(ctx, ghost); err == nil {
		t.Error("an entity only the withdrawn job asserted is still there")
	}
	if orphans, err := g.Properties(ctx, ghost); err != nil {
		t.Fatal(err)
	} else if len(orphans) != 0 {
		t.Errorf("a property outlived the entity it described: %+v", orphans)
	}

	// And what the remaining job said is untouched.
	if _, err := g.Get(ctx, acme); err != nil {
		t.Errorf("the remaining job's entity was swept: %v", err)
	}

	// Retracting a job that never existed changes nothing, which is what makes
	// the sweeps safe to run every time.
	if _, err := g.Retract(ctx, "nosuchjob"); err != nil {
		t.Fatal(err)
	}
	if again, err := g.Topics(ctx, entity.KindID("company")); err != nil {
		t.Fatal(err)
	} else if len(again) != 1 {
		t.Errorf("retracting a job that never existed deleted a topic: %+v", again)
	}
}

// testAProposedMergeIsAcceptedByTheStore.
//
// The proposal and the check have to count the same things. They did not: the
// proposal counted people, resolving merges, and the check counted spellings,
// so a graph with one already-merged pair made the store refuse a merge it had
// itself just proposed, and say something had changed since when nothing had.
func testAProposedMergeIsAcceptedByTheStore(t *testing.T, open Open) {
	ctx := context.Background()
	g := open(t)
	from := said("news", "https://a.example/1")

	full, err := g.Assert(ctx, "person", "Alex Doe", from)
	if err != nil {
		t.Fatal(err)
	}
	longer, err := g.Assert(ctx, "person", "Alexander Doe", from)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.Assert(ctx, "person", "A. Doe", from); err != nil {
		t.Fatal(err)
	}

	// Two spellings of one person, said so by hand.
	if err := g.Merge(ctx, longer, full, entity.RuleManual, from); err != nil {
		t.Fatal(err)
	}

	// Now there is one person the initial could be, so it is proposed.
	proposed, err := g.Candidates(ctx, "person", "A. Doe")
	if err != nil {
		t.Fatal(err)
	}
	if len(proposed) != 1 {
		t.Fatalf("Candidates = %+v, want the one proposal", proposed)
	}

	// And what the store proposed, the store accepts.
	if err := g.Merge(ctx, proposed[0].From, proposed[0].To, proposed[0].Rule, from); err != nil {
		t.Fatalf("the store refused a merge it proposed itself: %v", err)
	}
}

// testAKindIsTheSameKindWhoeverSpelledIt: a stray space is not a second type.
//
// KindID trimmed and Assert did not, so an entity asserted from
// `entity = "person "` could never be tagged: Tag said there was no such kind
// while the caller had used the identical string.
func testAKindIsTheSameKindWhoeverSpelledIt(t *testing.T, open Open) {
	ctx := context.Background()
	g := open(t)
	from := said("news", "https://a.example/1")

	spaced, err := g.Assert(ctx, "person ", "Alex Doe", from)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := g.Assert(ctx, "person", "Alex Doe", from)
	if err != nil {
		t.Fatal(err)
	}
	if spaced != plain {
		t.Fatalf("a stray space made a second person: %s and %s", spaced, plain)
	}

	if err := g.Tag(ctx, entity.KindID("person "), "climate@7", from); err != nil {
		t.Fatalf("a type asserted with a stray space could not be tagged: %v", err)
	}
	topics, err := g.Topics(ctx, entity.KindID("person"))
	if err != nil {
		t.Fatal(err)
	}
	if len(topics) != 1 {
		t.Errorf("the type's topics = %+v, want the one it was tagged with", topics)
	}

	// Every method that takes a kind, not just the two that were fixed when
	// this test was written.
	//
	// The store canonicalises a kind by lowercasing AND trimming it, and only
	// some of the methods did both: an entity asserted from `entity = "person "`
	// was stored under `person` and then invisible to every reader that only
	// lowercased. Nothing failed. The store simply reported that a type nobody
	// had misspelled contained nothing, so a job's whole entity graph came back
	// empty and the document that produced it looked fine.
	//
	// Written as a walk over the methods rather than one assertion, because the
	// defect is per method and the next one added is the next one to forget.
	// The same graph, not a fresh one. Opening a second graph inside a subtest
	// is fine for a store on this machine and wrong for one on a bus: the
	// harness there serves each graph on the same subject in one queue group,
	// so a second service means requests land on whichever store answers first
	// and an entity asserted through one is invisible to the other.
	t.Run("EveryMethodThatTakesAKind", func(t *testing.T) {
		alex := plain
		paper, err := g.Assert(ctx, "work", "A Paper", from)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := g.Relate(ctx, paper, alex, "author", "", 0, from); err != nil {
			t.Fatal(err)
		}

		// Each of these is the same question asked with a stray space.
		if got, err := g.Find(ctx, "person ", "Alex Doe"); err != nil || got == nil {
			t.Errorf("Find with a spaced kind found nothing: %v, %v", got, err)
		}
		if got, err := g.Kind(ctx, "person "); err != nil || len(got) != 1 {
			t.Errorf("Kind with a spaced kind returned %d entities, want 1: %v", len(got), err)
		}
		if got, err := g.Related(ctx, paper, "author ", ""); err != nil || len(got) != 1 {
			t.Errorf("Related with a spaced kind returned %d entities, want 1: %v", len(got), err)
		}
		if got, err := g.Candidates(ctx, "person ", "A. Doe"); err != nil || len(got) != 1 {
			t.Errorf("Candidates with a spaced kind returned %d, want the one it would find spelled plainly: %v", len(got), err)
		}

		// And a relation asserted with a stray space is the same relation, so
		// the reader that spells it plainly still finds it.
		if _, err := g.Relate(ctx, paper, alex, "author ", "", 0, from); err != nil {
			t.Fatal(err)
		}
		if got, err := g.Related(ctx, paper, "author", ""); err != nil || len(got) != 1 {
			t.Errorf("relating with a spaced kind made a second relation: %d, %v", len(got), err)
		}
	})
}

// testAMergeGoesToTheNameTheRuleFound: the initial rule may only merge into the
// one full name it actually found.
//
// The rule is "an initial may be merged into a full name when exactly one full
// name shares its surname and its first letter". The check counted those full
// names and required there to be one, and then never looked at whether the
// entity being merged INTO was that one. So the count could be satisfied by
// Alex Doe while the merge went to Bob Roe, and "A. Doe" became Bob Roe
// permanently: an alias is a row, and every article bylined "A. Doe" reads as
// the wrong person from then on.
//
// Reachable from outside the process. `internal/bus` passes a caller's From, To
// and Rule straight into Merge, so this is not only what the store's own
// proposer would ask for.
//
// Merging wrongly is worse than not merging, which is the whole reason this
// rule is conservative in the first place.
func testAMergeGoesToTheNameTheRuleFound(t *testing.T, open Open) {
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
	stranger, err := g.Assert(ctx, "person", "Bob Roe", from)
	if err != nil {
		t.Fatal(err)
	}

	// Exactly one full "Doe" beginning with A exists, so the count is
	// satisfied. It is satisfied by Alex Doe, and this merge is to Bob Roe.
	if err := g.Merge(ctx, initial, stranger, entity.RuleInitial, from); err == nil {
		t.Error("merged an initial into a name that shares neither its surname nor its first letter, " +
			"because the rule counted a different entity entirely")
	}

	// The merge the rule really did find is still allowed, or the fix would
	// have retired the feature rather than the defect.
	if err := g.Merge(ctx, initial, full, entity.RuleInitial, from); err != nil {
		t.Errorf("refused the merge the rule actually found: %v", err)
	}

	// And it took effect: the initial now resolves to the full name.
	got, err := g.Find(ctx, "person", "A. Doe")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.ID != full {
		t.Errorf("after merging, A. Doe resolves to %+v, want Alex Doe", got)
	}
}
