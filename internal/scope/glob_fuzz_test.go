// SPDX-License-Identifier: GPL-3.0-or-later

package scope_test

import (
	"strings"
	"testing"

	"github.com/rangertaha/scour/internal/scope"
)

// naive matches the same rule as [scope.Glob], written for obviousness rather
// than for speed.
//
// Exponential, and that is the point: it tries every place a `*` could end
// instead of committing to one and backtracking, so it cannot share a mistake
// with the linear matcher it is checking. The fuzz bounds the input length to
// keep it cheap.
func naive(pattern, s string) bool {
	if pattern == "" {
		return s == ""
	}
	switch pattern[0] {
	case '*':
		for i := 0; i <= len(s); i++ {
			if naive(pattern[1:], s[i:]) {
				return true
			}
		}
		return false
	case '?':
		return len(s) > 0 && naive(pattern[1:], s[1:])
	default:
		return len(s) > 0 && s[0] == pattern[0] && naive(pattern[1:], s[1:])
	}
}

// FuzzGlobMatchesTheObviousImplementation.
//
// glob decides what a crawl may fetch: `excluded` is the job saying never, and
// a pattern that matches one byte too few is a page fetched that was not to be.
// It is a hand-written linear matcher with a backtracking rule, which is the
// kind of code that is right for every example somebody thinks of and wrong for
// one they do not.
func FuzzGlobMatchesTheObviousImplementation(f *testing.F) {
	for _, seed := range []struct{ pattern, s string }{
		{"*", ""}, {"*", "a"}, {"", ""}, {"", "a"},
		{"a", "a"}, {"a", "b"}, {"?", "a"}, {"?", ""},
		{"*a", "aa"}, {"a*", "aa"}, {"*a*", "bab"},
		{"a*b", "ab"}, {"a*b", "axxb"}, {"a*b", "axxc"},
		{"**", "ab"}, {"*?*", "a"}, {"?*", "a"},
		{"*/print/*", "https://e.com/print/a"},
		{"*.example.com", "news.example.com"},
		{"a*a*a", "aaaa"}, {"*a*a*a*", "aaa"},
	} {
		f.Add(seed.pattern, seed.s)
	}

	f.Fuzz(func(t *testing.T, pattern, s string) {
		// Bounded, because the oracle is exponential in the number of stars.
		if len(pattern) > 20 || len(s) > 20 || strings.Count(pattern, "*") > 6 {
			return
		}
		if got, want := scope.Glob(pattern, s), naive(pattern, s); got != want {
			t.Errorf("Glob(%q, %q) = %v, and the obvious implementation says %v", pattern, s, got, want)
		}
	})
}
