// SPDX-License-Identifier: GPL-3.0-or-later

package matcher

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/rangertaha/scour/internal/wom"
)

// A tiny vector space, hand-built so the geometry is readable. The first axis
// is "who made it", the second "what it costs", the third "what it is called".
// Synonyms share an axis; unrelated words are orthogonal, which is the most
// unrelated two real words ever are.
const vectorFile = `make 1.0 0.0 0.0
manufacturer 0.9 0.1 0.0
brand 0.95 0.05 0.0
company 0.85 0.15 0.0
price 0.0 1.0 0.0
cost 0.05 0.95 0.0
title 0.0 0.0 1.0
heading 0.05 0.0 0.95
`

func vectors(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "vectors.txt")
	if err := os.WriteFile(path, []byte(vectorFile), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// newEmbed builds a matcher with an explicit band and weight, so these tests
// describe behaviour rather than track whatever the tuned defaults currently
// are.
func newEmbed(t *testing.T, cfg Config) *Embed {
	t.Helper()
	if cfg.Vectors == "" {
		cfg.Vectors = vectors(t)
	}
	if cfg.Floor == 0 && cfg.Ceiling == 0 {
		cfg.Floor, cfg.Ceiling = 0.2, 0.6
	}
	m, err := NewEmbed(cfg)
	if err != nil {
		t.Fatal(err)
	}
	e, ok := m.(*Embed)
	if !ok {
		t.Fatalf("NewEmbed returned %T", m)
	}
	return e
}

// labelled builds a page whose value carries one class, which is the whole
// label the matcher gets to work from.
func labelled(t *testing.T, class string) *wom.Node {
	t.Helper()
	return page(t, `<html><body><dd class="`+class+`">Ford</dd></body></html>`, "Ford")
}

// The reason this matcher exists: a page that never uses the schema's word.
func TestASynonymLabelLiftsAnUndecidedNode(t *testing.T) {
	e := newEmbed(t, Config{Base: fixed(0.35)})

	got := e.Score(context.Background(), prop(), labelled(t, "manufacturer"))
	if got <= 0.35 {
		t.Errorf("a node labelled \"manufacturer\" scored %v against a prop called \"make\", "+
			"no better than the heuristic's 0.35", got)
	}
	if got < 0.5 {
		t.Errorf("score = %v, want a near-synonym to move the node decisively", got)
	}
}

// The description is training data rather than documentation, so it has to
// carry meaning on its own when there are no aliases to lean on.
func TestTheDescriptionCarriesTheMeaning(t *testing.T) {
	e := newEmbed(t, Config{Base: fixed(0.35)})
	p := wom.Prop{Name: "vendor", Description: "the company that built it"}

	got := e.Score(context.Background(), p, labelled(t, "manufacturer"))
	if got <= 0.35 {
		t.Errorf("score = %v: the description's words never reached the comparison", got)
	}
}

// Orthogonal is not opposite. An unrelated label cannot be evidence against a
// match, only an absence of evidence for one, so it has to leave the score very
// nearly where it found it.
func TestAnUnrelatedLabelBarelyMoves(t *testing.T) {
	e := newEmbed(t, Config{Base: fixed(0.35)})

	got := e.Score(context.Background(), prop(), labelled(t, "price"))
	if got < 0.3 || got > 0.4 {
		t.Errorf("an unrelated label moved the score from 0.35 to %v", got)
	}
}

// The failure mode of a word vector file is an absent answer, not a wrong one,
// and an absent answer has to cost exactly nothing.
func TestUnknownVocabularyChangesNothing(t *testing.T) {
	e := newEmbed(t, Config{Base: fixed(0.35)})

	if got := e.Score(context.Background(), prop(), labelled(t, "wossname")); got != 0.35 {
		t.Errorf("a label the vectors have never seen moved the score to %v", got)
	}
}

// A node with no label at all leaves nothing to compare.
func TestAnUnlabelledNodeChangesNothing(t *testing.T) {
	e := newEmbed(t, Config{Base: fixed(0.35)})

	node := page(t, `<html><body><dd>Ford</dd></body></html>`, "Ford")
	if got := e.Score(context.Background(), prop(), node); got != 0.35 {
		t.Errorf("an unlabelled node moved the score to %v", got)
	}
}

// Outside the band the heuristic has real evidence, and a dictionary that has
// never seen the page does not get to argue with it.
func TestConfidentHeuristicIsNotAdjusted(t *testing.T) {
	node := labelled(t, "manufacturer")

	for _, score := range []float64{0.0, 0.05, 0.2, 0.6, 0.9, 1.0} {
		e := newEmbed(t, Config{Base: fixed(score)})

		if got := e.Score(context.Background(), prop(), node); got != score {
			t.Errorf("heuristic %v was adjusted to %v", score, got)
		}
		if stats := e.Stats(); stats.Calls != 0 {
			t.Errorf("the vectors were consulted about a node scored %v", score)
		}
	}
}

// A site repeats its markup on every page, so the same property meets the same
// label set hundreds of times.
func TestTheSameComparisonIsMadeOnce(t *testing.T) {
	e := newEmbed(t, Config{Base: fixed(0.35)})

	var first float64
	for i := range 20 {
		got := e.Score(context.Background(), prop(), labelled(t, "manufacturer"))
		if i == 0 {
			first = got
		} else if got != first {
			t.Fatalf("the same question answered %v then %v", first, got)
		}
	}

	if stats := e.Stats(); stats.Calls != 1 || stats.Hits != 19 || stats.Misses != 20 {
		t.Errorf("stats = %+v, want 20 undecided, 1 compared and 19 from cache", stats)
	}
}

// The case this matcher exists for, in the markup it actually appears in. The
// label is a neighbouring <dt> rather than an attribute, which is where a
// definition list puts it and where a matcher deriving its own label context
// would never look.
func TestASynonymInANeighbouringLabelIsFound(t *testing.T) {
	e := newEmbed(t, Config{Base: fixed(0.35)})
	node := page(t, `<html><body><dl><dt>Manufacturer</dt><dd>Ford</dd></dl></body></html>`, "Ford")

	if got := e.Score(context.Background(), prop(), node); got <= 0.35 {
		t.Errorf("score = %v: a <dt> synonym never reached the comparison", got)
	}
}

// Layout vocabulary is made of real words with real vectors, so comparing
// against it would produce confident nonsense rather than nothing. Measured
// over nineteen news sites, class:text and class:flex attached themselves to
// every field indiscriminately, which is why wom drops them.
func TestPresentationalClassesAreNotVocabulary(t *testing.T) {
	e := newEmbed(t, Config{Base: fixed(0.35)})
	node := page(t,
		`<html><body><div class="flex items-center gap-2 text-sm"><span>Ford</span></div></body></html>`,
		"Ford")

	if got := e.Score(context.Background(), prop(), node); got != 0.35 {
		t.Errorf("score = %v: CSS utility classes were read as meaning", got)
	}
}

// A node carries several names from several sources, and one of them being
// right is the answer. Averaging them would let a generic wrapper bury the one
// class that says what the value is.
func TestTheBestLabelWinsRatherThanTheAverage(t *testing.T) {
	e := newEmbed(t, Config{Base: fixed(0.35)})
	ctx := context.Background()

	alone := e.Score(ctx, prop(), labelled(t, "manufacturer"))
	buried := e.Score(ctx, prop(), page(t,
		`<html><body><div class="title"><dd class="manufacturer">Ford</dd></div></body></html>`,
		"Ford"))

	if buried != alone {
		t.Errorf("wrapped in an unrelated class the score fell from %v to %v", alone, buried)
	}
}

// The same word is worth less as a label the further its source sits from the
// value. wom already ranks the sources; this pins that the ranking survives the
// comparison instead of being flattened by it.
func TestAWeakerSourceArguesLess(t *testing.T) {
	e := newEmbed(t, Config{Base: fixed(0.35)})
	ctx := context.Background()

	// The value's own element carries the class.
	direct := e.Score(ctx, prop(), labelled(t, "manufacturer"))
	// The same class, one element further away.
	distant := e.Score(ctx, prop(), page(t,
		`<html><body><div class="manufacturer"><span>Ford</span></div></body></html>`,
		"Ford"))

	if distant >= direct {
		t.Errorf("a class on an ancestor argued as hard as one on the value itself: %v vs %v",
			distant, direct)
	}
	if distant <= 0.35 {
		t.Errorf("the distant label scored %v, so it argued nothing at all", distant)
	}
}

// A schema describes the same field differently per site, so two props can
// share a name and still be asking different things. The cache must not hand
// one site's answer to the other.
func TestPropsSharingANameAreDifferentQuestions(t *testing.T) {
	e := newEmbed(t, Config{Base: fixed(0.35)})
	node := labelled(t, "manufacturer")
	ctx := context.Background()

	near := e.Score(ctx, wom.Prop{Name: "vendor", Aliases: []string{"manufacturer"}}, node)
	far := e.Score(ctx, wom.Prop{Name: "vendor", Aliases: []string{"price"}}, node)

	if near == far {
		t.Fatalf("both props named \"vendor\" scored %v: the second was served the first's answer", near)
	}
	if near <= far {
		t.Errorf("the synonym scored %v, no better than the unrelated word's %v", near, far)
	}
	if stats := e.Stats(); stats.Calls != 2 {
		t.Errorf("stats = %+v, want both questions compared", stats)
	}
}

// Induction scores nodes concurrently, so the counters and the cache have to
// hold up under it.
func TestConcurrentEmbedScoring(t *testing.T) {
	e := newEmbed(t, Config{Base: fixed(0.35)})

	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e.Score(context.Background(), prop(), labelled(t, "manufacturer"))
		}()
	}
	wg.Wait()

	if stats := e.Stats(); stats.Misses != 16 {
		t.Errorf("stats = %+v, want 16 undecided", stats)
	}
}

// A vector file holds words. Labels are identifiers, and the gap between the
// two is where this matcher would silently find nothing.
func TestLabelsAreSplitIntoWords(t *testing.T) {
	cases := []struct {
		label string
		want  []string
	}{
		{"entry-title", []string{"entry", "title"}},
		{"articleAuthor", []string{"article", "author"}},
		{"og:published_time", []string{"published", "time"}},
		{"model year", []string{"model", "year"}},
		{"dc.creator", []string{"creator"}},
		{"byline__name", []string{"byline", "name"}},
		// Structure rather than vocabulary, and no vector file has it.
		{"h1", nil},
	}

	for _, c := range cases {
		if got := words([]string{c.label}); !reflect.DeepEqual(got, c.want) {
			t.Errorf("words(%q) = %v, want %v", c.label, got, c.want)
		}
	}
}

// An RSS item is named by its element and by nothing else, so a feed with no
// class or itemprop anywhere still has to be comparable.
func TestAFeedElementNamesItsValue(t *testing.T) {
	e := newEmbed(t, Config{Base: fixed(0.35)})

	w := wom.New()
	feed := `<?xml version="1.0"?><rss version="2.0"><channel><item>
<title>Ford</title></item></channel></rss>`
	if err := w.AddBody("http://example.com/feed.xml", "application/rss+xml", []byte(feed)); err != nil {
		t.Fatal(err)
	}

	var node *wom.Node
	w.Root().Walk(func(n *wom.Node) bool {
		if node == nil && n.Kind.HoldsValue() && n.Text() == "Ford" {
			node = n
		}
		return true
	})
	if node == nil {
		t.Fatal("no node holding the item title")
	}

	p := wom.Prop{Name: "heading"}
	if got := e.Score(context.Background(), p, node); got <= 0.35 {
		t.Errorf("score = %v: the <title> element never named the value it holds", got)
	}
}

// The shipped weight is what production uses, so it is worth asserting it is
// coherent rather than only that a test-chosen one works.
func TestDefaultWeightIsUsable(t *testing.T) {
	if DefaultWeight <= 0 || DefaultWeight > 1 {
		t.Fatalf("weight %v cannot move a score usefully", DefaultWeight)
	}

	m, err := NewEmbed(Config{
		Vectors: vectors(t),
		Base:    fixed((DefaultFloor + DefaultCeiling) / 2),
	})
	if err != nil {
		t.Fatal(err)
	}

	mid := (DefaultFloor + DefaultCeiling) / 2
	got := m.Score(context.Background(), prop(), labelled(t, "manufacturer"))
	if got <= mid {
		t.Errorf("a synonym mid-band scored %v, no better than the %v it started at", got, mid)
	}
	if got < 0 || got > 1 {
		t.Errorf("score = %v, outside [0,1]", got)
	}
}

func TestEmbedNeedsVectors(t *testing.T) {
	if _, err := NewEmbed(Config{}); err == nil {
		t.Error("a matcher with no vectors should say so rather than fail later")
	}
}

func TestAnInvertedEmbedBandIsRejected(t *testing.T) {
	if _, err := NewEmbed(Config{Vectors: vectors(t), Floor: 0.8, Ceiling: 0.2}); err == nil {
		t.Error("a floor above the ceiling should be an error, not a matcher that never compares")
	}
}

func TestEmbedIsRegistered(t *testing.T) {
	if !Has("embed") {
		t.Errorf("registered matchers = %v", Names())
	}
	if _, err := New("embed", Config{Vectors: vectors(t)}); err != nil {
		t.Errorf("building the registered embed matcher: %v", err)
	}
}
