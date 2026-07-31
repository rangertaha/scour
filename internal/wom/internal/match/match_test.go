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
