// SPDX-License-Identifier: GPL-3.0-or-later

package train

import "testing"

// TestBetterIsATotalOrder.
//
// It was a preference, not an order: two candidates equal on rank, depth, space
// count and length left better returning false in both directions, and the
// winner of the tie was whichever selector Go's randomised map iteration reached
// first. `<span class="author byline">` is an ordinary thing to write and offers
// exactly that pair, so `scour train --write` on an unchanged corpus produced a
// different document on alternate runs, and a diff on every run, against the
// promise this package makes that the same corpus induces the same locators.
//
// Asserted here rather than through Learn because a tie is what has to be
// constructed, and the seeding rules make that fragile: a corpus contrived to
// produce one today is a corpus that stops producing one when a semantic rule is
// added, and the test would pass for the wrong reason ever after.
func TestBetterIsATotalOrder(t *testing.T) {
	selectors := []string{
		".author", ".byline", ".author-name", "span.byline",
		"div span", "span", "article > span", "#lede", "[itemprop=author]",
	}
	depths := map[string]int{".author": 3, ".byline": 3, ".author-name": 3}

	for _, a := range selectors {
		for _, b := range selectors {
			if a == b {
				if better(a, b, depths) {
					t.Errorf("better(%q, %q) is true for one selector against itself", a, b)
				}
				continue
			}
			if better(a, b, depths) == better(b, a, depths) {
				t.Errorf("better(%q, %q) and better(%q, %q) agree, so neither wins and the tie is left to map order",
					a, b, b, a)
			}
		}
	}
}
