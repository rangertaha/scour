// SPDX-License-Identifier: MIT

package pattern

import "testing"

// Predicated must only emit a predicate it can read back. A value holding both
// quote characters needs XPath 1.0's concat(), which SplitPredicates cannot
// parse, so emitting one would produce a precise-looking locator that matches
// nothing at all.
func TestPredicatedOnlyEmitsWhatItCanParseBack(t *testing.T) {
	t.Parallel()

	const base = "./meta/@content"

	for _, value := range []string{
		`plain`,
		`article:author`,
		`has "double"`,
		`has 'single'`,
		`has "both" and 'kinds'`,
	} {
		disc := Discriminator{Name: "property", Value: value}
		xpath := Predicated(base, disc)

		if xpath == base {
			// Declined to add a predicate; the fallback must stay usable.
			continue
		}
		bare, preds := SplitPredicates(xpath)
		if len(preds) != 1 {
			t.Errorf("value %q produced %s, which parses back to %d predicates",
				value, xpath, len(preds))
			continue
		}
		if preds[0].Value != value || preds[0].Name != disc.Name {
			t.Errorf("value %q round-tripped to %+v", value, preds[0])
		}
		if bare != base {
			t.Errorf("value %q left residue in the bare path: %q", value, bare)
		}
	}
}

// The quoting itself still has to be valid XPath for whichever values do get a
// predicate.
func TestPredicatedQuoting(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		`plain`:        `./meta[@property="plain"]/@content`,
		`has "double"`: `./meta[@property='has "double"']/@content`,
		`has 'single'`: `./meta[@property="has 'single'"]/@content`,
		`both " and '`: `./meta/@content`, // declined
	}
	for value, want := range tests {
		if got := Predicated("./meta/@content", Discriminator{Name: "property", Value: value}); got != want {
			t.Errorf("Predicated(%q) = %q, want %q", value, got, want)
		}
	}
}

// An index already pins the element down, so no predicate is added.
func TestPredicatedLeavesIndexedPathsAlone(t *testing.T) {
	t.Parallel()

	const indexed = "./meta[3]/@content"
	if got := Predicated(indexed, Discriminator{Name: "property", Value: "x"}); got != indexed {
		t.Errorf("Predicated on an indexed path = %q, want it unchanged", got)
	}
}
