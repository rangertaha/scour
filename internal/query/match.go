// SPDX-License-Identifier: GPL-3.0-or-later

package query

import (
	"sort"
	"strings"
)

// How well one term answers one field. The gaps matter more than the numbers:
// a record whose make is exactly "Ford" should come above one whose
// description happens to mention Ford, however good the rest of it is.
const (
	scoreExact     = 4.0
	scoreWholeWord = 3.0
	scorePrefix    = 2.0
	scoreSubstring = 1.0
)

// Match is how well one record answers a query.
type Match struct {
	// Score is the sum over terms of the best field each found. Comparable
	// only between records answering the same query.
	Score float64
	// Fields names what matched, best term first, so a result can say where it
	// was found rather than leaving someone to guess which column to read.
	Fields []string
}

// Match scores a record against the query.
//
// Every term must find something, because terms narrow. The score is the sum
// of each term's best field rather than its first, so a query naming a word
// that appears both as a whole value and buried in a description is answered by
// the record where it is the value.
//
// The url counts as a field for a bare word. It is how people search for
// records from one site without knowing which property carries the domain, and
// it is the only field every item has.
func (q Query) Match(values map[string]string, url string) (Match, bool) {
	if q.Empty() {
		return Match{}, false
	}

	var m Match
	type hit struct {
		field string
		score float64
	}
	var hits []hit

	for _, t := range q.Terms {
		best, field := 0.0, ""

		if t.Any() {
			// Deterministic order, so two fields scoring the same report the
			// same one every run rather than whichever the map yielded first.
			for _, name := range sortedKeys(values) {
				if s := score(values[name], t.Text); s > best {
					best, field = s, name
				}
			}
			if s := score(url, t.Text); s > best {
				best, field = s, URLField
			}
		} else if t.Field == URLField {
			best, field = score(url, t.Text), URLField
		} else {
			best, field = score(values[t.Field], t.Text), t.Field
		}

		if best == 0 {
			return Match{}, false
		}
		m.Score += best
		hits = append(hits, hit{field, best})
	}

	// Best first, so the field named first is the one that answers the query
	// most directly. Stable so equal scores keep the order the terms were
	// typed in.
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	seen := map[string]bool{}
	for _, h := range hits {
		if h.field == "" || seen[h.field] {
			continue
		}
		seen[h.field] = true
		m.Fields = append(m.Fields, h.field)
	}
	return m, true
}

// score rates one field against one term, zero for no match.
func score(field, term string) float64 {
	if field == "" || term == "" {
		return 0
	}
	f, t := strings.ToLower(field), strings.ToLower(term)
	switch {
	case f == t:
		return scoreExact
	case strings.HasPrefix(f, t):
		// A prefix of a longer value, but a whole word within it is worth
		// more, so "ford" scores higher against "Ford Motor Company" than
		// against "Fordham".
		if wholeWord(f, t) {
			return scoreWholeWord
		}
		return scorePrefix
	case wholeWord(f, t):
		return scoreWholeWord
	case strings.Contains(f, t):
		return scoreSubstring
	}
	return 0
}

// wholeWord reports whether the term appears in the field bounded by something
// other than a letter or a digit.
//
// The distinction is what stops a search for a short word being drowned by the
// records that merely contain those letters: "cab" is in "crew cab" and also in
// "cabinet", and only one of those is what was asked for.
func wholeWord(field, term string) bool {
	for i := 0; ; {
		j := strings.Index(field[i:], term)
		if j < 0 {
			return false
		}
		start := i + j
		end := start + len(term)
		if !alnumAt(field, start-1) && !alnumAt(field, end) {
			return true
		}
		i = start + 1
		if i > len(field)-len(term) {
			return false
		}
	}
}

// alnumAt reports whether the byte at an index is a letter or a digit, treating
// anything outside the string as a boundary.
func alnumAt(s string, i int) bool {
	if i < 0 || i >= len(s) {
		return false
	}
	c := s[i]
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
