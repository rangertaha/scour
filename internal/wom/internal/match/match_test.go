// SPDX-License-Identifier: MIT

package match

import (
	"context"
	"testing"

	"github.com/rangertaha/scour/internal/wom/internal/graph"
	"github.com/rangertaha/scour/internal/wom/internal/schema"
)

// node builds a text node inside an element carrying the given class.
func node(t *testing.T, class, text string) *graph.Node {
	t.Helper()
	doc := graph.NewDocument(graph.FormatHTML)
	el := doc.Append(graph.New(graph.KindElement, "span", ""))
	if class != "" {
		el.Append(graph.New(graph.KindAttribute, "class", class))
	}
	return el.Append(graph.New(graph.KindText, "", text))
}

// Setting a weight to zero is the obvious way to switch a signal off, so zero
// must mean zero. Only the wholly zero value means "use the defaults".
func TestZeroWeightDisablesASignal(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	n := node(t, "make", "Toyota")
	p := schema.Prop{Name: "make", Examples: []string{"Toyota"}}

	zeroValue := Heuristic{}.Score(ctx, p, n)
	defaults := DefaultHeuristic().Score(ctx, p, n)
	if zeroValue != defaults {
		t.Errorf("Heuristic{} scored %v but DefaultHeuristic() scored %v; the zero value must mean defaults",
			zeroValue, defaults)
	}

	off := DefaultHeuristic()
	off.ExampleWeight = 0
	if got := off.Score(ctx, p, n); got == defaults {
		t.Errorf("ExampleWeight = 0 scored %v, the same as the default; the signal was not disabled", got)
	}

	// Adjusting one weight must not silently reset the others.
	tweaked := DefaultHeuristic()
	tweaked.DescriptionWeight = 0
	if tweaked.ExampleWeight != defaultExampleWeight || tweaked.LabelWeight != defaultLabelWeight {
		t.Fatal("DefaultHeuristic() does not carry the other weights")
	}
	if tweaked.Score(ctx, p, n) == 0 {
		t.Error("zeroing one weight should not disable scoring entirely")
	}
}

// A node whose text is the field's own name is a label, not the value.
func TestLabelsScoreBelowValues(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	p := schema.Prop{Name: "make", Aliases: []string{"manufacturer"}}
	h := DefaultHeuristic()

	value := h.Score(ctx, p, node(t, "make", "Toyota"))
	label := h.Score(ctx, p, node(t, "label", "Make"))
	if label >= value {
		t.Errorf("the label %q scored %v, at or above the value %q at %v", "Make", label, "Toyota", value)
	}
}

// Containment matching must not fire on one-character candidates: an SVG
// circle's @r attribute is not an "author".
func TestShortCandidatesDoNotMatchByContainment(t *testing.T) {
	t.Parallel()

	doc := graph.NewDocument(graph.FormatHTML)
	circle := doc.Append(graph.New(graph.KindElement, "circle", ""))
	r := circle.Append(graph.New(graph.KindAttribute, "r", "24"))

	got := DefaultHeuristic().Score(context.Background(),
		schema.Prop{Name: "authors", Aliases: []string{"author"}}, r)
	if got > 0.2 {
		t.Errorf("@r scored %v against \"authors\"; containment matched a single character", got)
	}
}

// Utility frameworks put layout vocabulary on every element, and it was being
// read as a label at 0.8. Measured on nineteen news sites, class:text,
// class:font, class:hover and class:flex attached to every field alike, and
// class:brand, class:blue and class:dark looked perfectly discriminating for
// author only because five of those sites shared a theme.
func TestPresentationalClassesAreNotLabels(t *testing.T) {
	tests := map[string]bool{
		"text-sm":         true,
		"font-bold":       true,
		"flex":            true,
		"items-center":    true,
		"hover":           true,
		"bg-brand-blue":   true,
		"rounded-lg":      true,
		"sr-only":         true,
		"max-w-full":      true,
		"entry-title":     false,
		"byline":          false,
		"post-date":       false,
		"article-summary": false,
		"text-title":      false,
		"published":       false,
		"category-name":   false,
		"c-9f3a1b":        false,
	}
	for cls, want := range tests {
		if got := presentational(cls); got != want {
			t.Errorf("presentational(%q) = %v, want %v", cls, got, want)
		}
	}
}

// A stop word that swallowed a property name would cost the field it names.
func TestNoStopWordNamesAValue(t *testing.T) {
	for _, w := range []string{
		"title", "heading", "headline", "author", "byline", "creator",
		"date", "published", "updated", "modified", "summary", "description",
		"excerpt", "content", "section", "category", "topic", "link", "url",
		"price", "make", "model", "year", "name", "caption",
	} {
		if stopLabels[w] {
			t.Errorf("%q is a stop word but could name a value", w)
		}
	}
}

// A pattern says what a valid value looks like. It validates rather than
// transforms: a pattern that rewrote would repair a node that should not have
// been chosen, leaving the wrong node winning and hiding that it did.
func TestPatternVetoesInvalidText(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	name := node(t, "author", "Jane Doe")
	url := node(t, "author", "https://www.facebook.com/RugbyMadsa")

	plain := schema.Prop{Name: "author", Aliases: []string{"byline"}}
	guarded := schema.Prop{Name: "author", Aliases: []string{"byline"}, Pattern: `^[^:/@]+$`}

	h := DefaultHeuristic()
	if got := h.Score(ctx, plain, url); got == 0 {
		t.Fatal("without a pattern the URL should still score, or the test proves nothing")
	}
	if got := h.Score(ctx, guarded, url); got != 0 {
		t.Errorf("a value failing the pattern scored %.3f, want 0", got)
	}
	if got := h.Score(ctx, guarded, name); got == 0 {
		t.Error("a value satisfying the pattern must be unaffected")
	}
}

// A pattern that does not compile must not silently empty a field.
func TestUncompilablePatternValidatesEverything(t *testing.T) {
	t.Parallel()

	p := schema.Prop{Name: "author", Pattern: "^(unclosed"}
	if !validates(p, "anything") {
		t.Error("a broken pattern should let text through, not reject it")
	}
}

// A label pattern says which names count. Aliases list words, which is easy to
// write and imprecise: substring matching finds "title" inside "subtitle" and
// "titlebar". A pattern does not.
func TestLabelPatternVetoesTheWrongName(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	h := DefaultHeuristic()
	p := schema.Prop{Name: "title", Label: `^(og:|twitter:)?title$`}

	right := node(t, "title", "Council approves transit line")
	wrong := node(t, "subtitle", "Council approves transit line")

	if got := h.Score(ctx, p, right); got == 0 {
		t.Error("a name the pattern accepts must still score")
	}
	if got := h.Score(ctx, p, wrong); got != 0 {
		t.Errorf("subtitle scored %.3f against ^(og:|twitter:)?title$, want 0", got)
	}

	// Without the pattern, substring matching cannot tell them apart.
	plain := schema.Prop{Name: "title"}
	if h.Score(ctx, plain, wrong) == 0 {
		t.Error("without a label pattern subtitle should still score, or the test proves nothing")
	}
}

// rel is how <link> and <a> say what they point at, and it was never read as a
// label. That is why <link rel="canonical"> went unused despite appearing on
// ten of thirteen news sites with nothing competing for the meaning.
func TestRelNamesWhatALinkPointsAt(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	h := DefaultHeuristic()
	link := schema.Prop{Name: "link", Type: schema.TypeURL, Aliases: []string{"url", "canonical"}}

	// <link rel="canonical" href="..."> as the graph holds it: the value is the
	// href attribute, and the only thing naming it is the sibling rel.
	doc := graph.NewDocument(graph.FormatHTML)
	el := doc.Append(graph.New(graph.KindElement, "link", ""))
	el.Append(graph.New(graph.KindAttribute, "rel", "canonical"))
	href := el.Append(graph.New(graph.KindAttribute, "href", "https://example.com/news/story"))

	if got := h.Score(ctx, link, href); got < 0.5 {
		t.Errorf("rel=canonical scored %.3f for link, want it recognised", got)
	}

	// A rel naming something else must not lend the href any authority.
	other := graph.NewDocument(graph.FormatHTML)
	pre := other.Append(graph.New(graph.KindElement, "link", ""))
	pre.Append(graph.New(graph.KindAttribute, "rel", "preconnect"))
	cdn := pre.Append(graph.New(graph.KindAttribute, "href", "https://fonts.googleapis.com"))

	if got, want := h.Score(ctx, link, cdn), h.Score(ctx, link, href); got >= want {
		t.Errorf("preconnect scored %.3f against canonical's %.3f", got, want)
	}
}
