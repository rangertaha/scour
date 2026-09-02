// SPDX-License-Identifier: GPL-3.0-or-later

package robots

import (
	"strings"
	"testing"
)

// The matcher is where a mistake is silent: too permissive and we crawl what a
// site asked us not to, too strict and we skip pages nobody objected to. It is
// pinned directly rather than only through Allowed, because Allowed's
// longest-match arithmetic can hide a matcher that says yes to the wrong thing.
func TestMatches(t *testing.T) {
	for name, tc := range map[string]struct {
		pattern, path string
		want          bool
	}{
		"prefix":              {"/private", "/private/page", true},
		"prefix falls short":  {"/private", "/public", false},
		"whole path":          {"/", "/anything", true},
		"star in the middle":  {"/a/*/b", "/a/x/b", true},
		"star spans slashes":  {"/a/*/b", "/a/x/y/b", true},
		"star needs nothing":  {"/a/*b", "/a/b", true},
		"anchor exact":        {"/page$", "/page", true},
		"anchor rejects more": {"/page$", "/page/more", false},
		"anchor with star":    {"/*.pdf$", "/deep/x.pdf", true},
		"anchor sees query":   {"/*.pdf$", "/x.pdf?v=1", false},
		"no reuse":            {"/a*a$", "/a", false},
		"consecutive stars":   {"/a**b", "/axb", true},
		"star at the end":     {"/a*", "/a", true},

		// Never stored by the parser, which drops empty values, and here so
		// that a pattern which arrived some other way cannot become a rule
		// matching every path on the site.
		"empty matches nothing": {"", "/anything", false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := matches(tc.pattern, tc.path); got != tc.want {
				t.Errorf("matches(%q, %q) = %v, want %v", tc.pattern, tc.path, got, tc.want)
			}
		})
	}
}

// naive matches the same rule as [matches], written for obviousness rather than
// for speed.
//
// Exponential, and that is the point: it tries every place a `*` could end
// instead of taking the earliest and moving on, so it cannot share a mistake
// with the greedy implementation it is checking.
func naive(pattern, path string) bool {
	if pattern == "" {
		return false
	}

	anchored := strings.HasSuffix(pattern, "$")
	if anchored {
		pattern = pattern[:len(pattern)-1]
	}

	var walk func(p, s string) bool
	walk = func(p, s string) bool {
		if p == "" {
			// A pattern is a prefix unless it was anchored, in which case it
			// has to have consumed the whole path.
			return !anchored || s == ""
		}
		if p[0] == '*' {
			for i := 0; i <= len(s); i++ {
				if walk(p[1:], s[i:]) {
					return true
				}
			}
			return false
		}
		return len(s) > 0 && s[0] == p[0] && walk(p[1:], s[1:])
	}
	return walk(pattern, path)
}

// FuzzMatchesTheObviousImplementation.
//
// This decides whether a site's robots.txt refuses a URL, so a pattern that
// matches one character too few is a page fetched against the site's wishes,
// and one that matches too many is a crawl that quietly fetches nothing. It is
// a greedy matcher - it takes the earliest place each part can sit and moves
// on - which is right for a prefix pattern with no end anchor and is exactly
// the shape that goes wrong once `$` makes the end matter.
func FuzzMatchesTheObviousImplementation(f *testing.F) {
	for _, seed := range []struct{ pattern, path string }{
		{"/", "/"}, {"/a", "/a"}, {"/a", "/b"}, {"/a", "/ab"},
		{"/a$", "/a"}, {"/a$", "/ab"},
		{"/*", "/a"}, {"/*a", "/ba"}, {"/*a$", "/ba"},
		{"/a*b", "/axxb"}, {"/a*b$", "/axxb"}, {"/a*b$", "/axxbc"},
		{"/a*b*c$", "/abcbc"}, {"/*.pdf$", "/a/b.pdf"},
		{"/private", "/private/x"}, {"/private$", "/private/x"},
		{"*", "/anything"}, {"$", "/a"}, {"/a**b", "/ab"},
	} {
		f.Add(seed.pattern, seed.path)
	}

	f.Fuzz(func(t *testing.T, pattern, path string) {
		// Bounded, because the oracle is exponential in the number of stars.
		if len(pattern) > 16 || len(path) > 16 || strings.Count(pattern, "*") > 5 {
			return
		}
		// upperEscapes is applied by matches and not by the oracle, and it is
		// the identity for printable ASCII without a percent. This is about the
		// matching rule; upperEscapes has its own tests.
		for _, s := range []string{pattern, path} {
			for i := range len(s) {
				if s[i] <= 0x20 || s[i] >= 0x7f || s[i] == '%' {
					return
				}
			}
		}

		if got, want := matches(pattern, path), naive(pattern, path); got != want {
			t.Errorf("matches(%q, %q) = %v, and the obvious implementation says %v",
				pattern, path, got, want)
		}
	})
}
