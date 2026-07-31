// SPDX-License-Identifier: GPL-3.0-or-later

package fuzzy

import "testing"

func TestNearest(t *testing.T) {
	commands := []string{"add", "crawl", "export", "import", "list", "remove",
		"rules", "search", "server", "tag", "templates", "top", "train"}

	cases := []struct {
		name, want string
		from       []string
	}{
		// Ordinary slips: one wrong letter, one missing, one extra.
		{"serach", "search", commands},
		{"craw", "crawl", commands},
		{"exprt", "export", commands},
		{"lst", "list", commands},
		{"improt", "import", commands},

		// Transpositions are one edit, not two, which is the whole reason for
		// Damerau over plain Levenshtein: "trian" is five characters and would
		// otherwise need two edits against a budget of one.
		{"trian", "train", commands},
		{"taemplates", "templates", commands},

		// Nothing close enough. A suggestion pointing somewhere unrelated is
		// worse than none, because it is read as "this is what you meant".
		{"bogus", "", commands},
		{"zzzzzz", "", commands},
		{"deploy", "", commands},

		// An exact match wins outright, even against a shorter near miss.
		{"top", "top", commands},

		// Case is a slip like any other.
		{"Search", "search", commands},
		{"VEHICLE", "vehicle", []string{"vehicle", "news"}},

		// Hyphens and the names people actually give entities.
		{"newshtml", "news-html", []string{"news-html", "news-feeds", "vehicle"}},
		{"news-feed", "news-feeds", []string{"news-html", "news-feeds", "vehicle"}},

		// Nothing to suggest from.
		{"anything", "", nil},
		{"anything", "", []string{}},
	}

	for _, tc := range cases {
		if got := Nearest(tc.name, tc.from); got != tc.want {
			t.Errorf("Nearest(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// Short names have to tolerate a slip too, or a two-letter typo could never be
// corrected at one edit per three characters.
func TestNearestAllowsOneEditOnShortNames(t *testing.T) {
	if got := Nearest("ad", []string{"add", "top"}); got != "add" {
		t.Errorf("Nearest(\"ad\") = %q, want \"add\"", got)
	}
	if got := Nearest("tap", []string{"tag", "top"}); got == "" {
		t.Error("a one-edit miss on a short name should still suggest")
	}
}

func TestDistance(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"abc", "abc", 0},
		{"abc", "", 3},
		{"", "abc", 3},
		{"train", "trian", 1}, // transposition
		{"ab", "ba", 1},       // transposition
		{"crawl", "craw", 1},  // deletion
		{"list", "lst", 1},    // deletion
		{"cat", "cut", 1},     // substitution
		{"kitten", "sitting", 3},
	}
	for _, tc := range cases {
		if got := distance(tc.a, tc.b); got != tc.want {
			t.Errorf("distance(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}
