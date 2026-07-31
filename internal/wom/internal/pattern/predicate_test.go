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

// A positional index gives way to a predicate.
//
// The index used to win, on the reasoning that it already pinned the element
// down. It pins it down only on the pages that happened to be sampled:
// ./meta[3]/@content says the value is the third <meta>, which is a fact about
// one template on one day, while ./meta[@property="og:title"] says which meta
// and holds anywhere OpenGraph is published. An index survives generalization
// exactly when every sampled page agreed on it, and a narrow sample agreeing is
// when an index looks most reliable and is least so.
//
// Measured on three captured AP articles, this was the difference between
// ./meta[1]/@content and ./meta[@property="og:title"]/@content.
func TestAPredicateReplacesAPositionalIndex(t *testing.T) {
	t.Parallel()

	got := Predicated("./meta[3]/@content", Discriminator{Name: "property", Value: "og:title"})
	if want := `./meta[@property="og:title"]/@content`; got != want {
		t.Errorf("Predicated = %q, want %q", got, want)
	}
}

// A path that already carries a predicate is left alone: a second one would say
// no more than the first.
func TestPredicatedLeavesPredicatedPathsAlone(t *testing.T) {
	t.Parallel()

	const already = `./meta[@property="og:title"]/@content`
	if got := Predicated(already, Discriminator{Name: "property", Value: "og:title"}); got != already {
		t.Errorf("Predicated on a predicated path = %q, want it unchanged", got)
	}
}

// The CSS dialect has to make the same choice, or one locator contradicts the
// other.
func TestPredicatedSelectorReplacesNthOfType(t *testing.T) {
	t.Parallel()

	got := PredicatedSelector("head > meta:nth-of-type(3)", Discriminator{Name: "property", Value: "og:title"})
	if want := `head > meta[property="og:title"]`; got != want {
		t.Errorf("PredicatedSelector = %q, want %q", got, want)
	}
}
