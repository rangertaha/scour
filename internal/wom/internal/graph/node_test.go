// SPDX-License-Identifier: MIT

package graph

import (
	"fmt"
	"testing"
)

// Append assigns sibling positions. Wide parents switch from scanning to a
// tally, so the two paths must agree exactly — a 16k-element JSON array would
// otherwise be numbered differently from a small one.
func TestAppendPositionsAgreeAcrossTheWideThreshold(t *testing.T) {
	t.Parallel()

	for _, n := range []int{4, wideParent - 1, wideParent, wideParent + 1, wideParent * 4} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			t.Parallel()
			parent := New(KindArray, "", "")

			// Alternate kinds and names so the tally has to key on both.
			wantByKey := map[string]int{}
			for i := 0; i < n; i++ {
				kind, name := KindObject, ""
				switch i % 3 {
				case 1:
					kind, name = KindValue, ""
				case 2:
					kind, name = KindField, fmt.Sprintf("k%d", i%2)
				}
				child := parent.Append(New(kind, name, ""))
				key := fmt.Sprintf("%v/%s", kind, name)
				wantByKey[key]++
				if child.Position() != wantByKey[key] {
					t.Fatalf("child %d (%s): position = %d, want %d",
						i, key, child.Position(), wantByKey[key])
				}
			}
			if len(parent.Children) != n {
				t.Errorf("parent holds %d children, want %d", len(parent.Children), n)
			}
		})
	}
}

// Reset must clear the tally too, or a re-added document keeps numbering from
// where the previous one stopped.
func TestResetClearsPositionState(t *testing.T) {
	t.Parallel()

	parent := New(KindURI, "", "")
	for i := 0; i < wideParent*2; i++ {
		parent.Append(New(KindDocument, "html", ""))
	}
	parent.Reset()

	first := parent.Append(New(KindDocument, "html", ""))
	if first.Position() != 1 {
		t.Errorf("after Reset the first child has position %d, want 1", first.Position())
	}
}

// Grafting a pre-built subtree must not overwrite the context it already
// carries; that is what keeps a failed parse out of the graph.
func TestAppendPreservesExistingContext(t *testing.T) {
	t.Parallel()

	doc := NewDocument(FormatJSON)
	value := doc.Append(New(KindValue, "", "x"))

	uri := New(KindURI, "https://example.com/a", "")
	uri.Append(doc)

	if doc.Format() != FormatJSON {
		t.Errorf("document format = %v after grafting, want json", doc.Format())
	}
	if value.Format() != FormatJSON {
		t.Errorf("child format = %v, want json", value.Format())
	}
	if value.Document() != doc {
		t.Error("child lost its document after the parent was grafted")
	}
}
