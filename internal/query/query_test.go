// SPDX-License-Identifier: GPL-3.0-or-later

package query

import (
	"strings"
	"testing"
)

var props = []string{"make", "model", "year", "title"}

func TestParse(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"a bare word", []string{"f-150"}, "f-150"},
		{"a field", []string{"make:Ford"}, "make:Ford"},
		{"several terms narrow", []string{"make:Ford", "year:2026"}, "make:Ford year:2026"},
		{"the shell already split the phrase", []string{"crew cab"}, `"crew cab"`},
		{"a phrase in a field", []string{"model:crew cab"}, `model:"crew cab"`},
		{"the url is a field on every item", []string{"url:example.com"}, "url:example.com"},
		{"field names are matched case insensitively", []string{"Make:Ford"}, "make:Ford"},
		// The colon in a url is not a field separator, and requiring it to be
		// escaped would be a rule to remember for the commonest search there is.
		{"a url searched for as a word", []string{"https://example.com/a"}, "https://example.com/a"},
		{"a time is not a field", []string{"10:30"}, "10:30"},
		{"a ratio is not a field", []string{"16:9"}, "16:9"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q, err := Parse(tc.args, props)
			if err != nil {
				t.Fatalf("Parse(%q): %v", tc.args, err)
			}
			if got := q.String(); got != tc.want {
				t.Errorf("Parse(%q) = %q, want %q", tc.args, got, tc.want)
			}
		})
	}
}

// A mistyped field would otherwise search for a literal nobody meant and report
// nothing found, which reads as an empty database rather than as a typo.
func TestParseReportsAMistypedField(t *testing.T) {
	_, err := Parse([]string{"mak:Ford"}, props)
	if err == nil {
		t.Fatal("mak:Ford was accepted")
	}
	if !strings.Contains(err.Error(), `"make"`) {
		t.Errorf("got %v, want a suggestion of make", err)
	}
}

func TestParseRejectsAFieldWithNothingToFind(t *testing.T) {
	if _, err := Parse([]string{"make:"}, props); err == nil {
		t.Fatal("make: was accepted with nothing to look for")
	}
}

func TestMatch(t *testing.T) {
	values := map[string]string{
		"make":  "Ford",
		"model": "F-150 crew cab",
		"year":  "2026",
		"title": "A Ford for every cabinet maker",
	}
	const url = "https://example.com/trucks/f-150"

	for _, tc := range []struct {
		name  string
		args  []string
		match bool
		first string // the field expected to be named first
	}{
		{"a bare word finds any field", []string{"crew"}, true, "model"},
		{"a field term looks only there", []string{"make:Ford"}, true, "make"},
		{"a field term that does not match fails the query", []string{"make:Honda"}, false, ""},
		{"every term must match", []string{"make:Ford", "year:1999"}, false, ""},
		{"terms narrow", []string{"make:Ford", "year:2026"}, true, "make"},
		{"the url is searchable", []string{"url:trucks"}, true, "url"},
		{"a bare word reaches the url", []string{"trucks"}, true, "url"},
		{"matching is case insensitive", []string{"make:ford"}, true, "make"},
		// "cab" is in "crew cab" and in "cabinet"; the field where it is a word
		// of its own is the one that answers the query.
		{"a whole word beats a substring", []string{"cab"}, true, "model"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q, err := Parse(tc.args, props)
			if err != nil {
				t.Fatal(err)
			}
			m, ok := q.Match(values, url)
			if ok != tc.match {
				t.Fatalf("Match(%q) = %v, want %v", tc.args, ok, tc.match)
			}
			if !ok {
				return
			}
			if len(m.Fields) == 0 || m.Fields[0] != tc.first {
				t.Errorf("matched on %v, want %q named first", m.Fields, tc.first)
			}
			if m.Score <= 0 {
				t.Errorf("score = %v, want above zero for a match", m.Score)
			}
		})
	}
}

// The ordering is the whole difference between search and ls, so the ranking
// has to put the record that answers the query above the one that mentions it.
func TestMatchRanksTheDirectAnswerHighest(t *testing.T) {
	q, err := Parse([]string{"Ford"}, props)
	if err != nil {
		t.Fatal(err)
	}

	exact, ok := q.Match(map[string]string{"make": "Ford"}, "")
	if !ok {
		t.Fatal("the record whose make is Ford did not match")
	}
	word, ok := q.Match(map[string]string{"title": "A Ford for everyone"}, "")
	if !ok {
		t.Fatal("the record mentioning Ford did not match")
	}
	part, ok := q.Match(map[string]string{"title": "Fordham University"}, "")
	if !ok {
		t.Fatal("the record containing the letters did not match")
	}

	if !(exact.Score > word.Score && word.Score > part.Score) {
		t.Errorf("scores are %v, %v, %v: want exact above whole word above substring",
			exact.Score, word.Score, part.Score)
	}
}

// An empty query matches nothing rather than everything. Search requires a
// query; listing everything is what ls is for.
func TestAnEmptyQueryMatchesNothing(t *testing.T) {
	var q Query
	if !q.Empty() {
		t.Error("a query with no terms does not report itself empty")
	}
	if _, ok := q.Match(map[string]string{"make": "Ford"}, "u"); ok {
		t.Error("an empty query matched a record")
	}
}
