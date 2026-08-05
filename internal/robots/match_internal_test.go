// SPDX-License-Identifier: GPL-3.0-or-later

package robots

import "testing"

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
