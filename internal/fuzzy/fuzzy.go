// SPDX-License-Identifier: GPL-3.0-or-later

// Package fuzzy answers "did you mean" for names a person typed.
//
// It exists because a name scour does not recognise is nearly always a typo or
// a half-remembered spelling, and "not found" alone leaves someone re-reading a
// listing to spot the one character they got wrong.
package fuzzy

import "strings"

// Nearest returns the candidate closest to want, or "" when none is close
// enough to be worth offering.
//
// Offering a distant match is worse than offering nothing: a suggestion is read
// as "this is probably what you meant", and pointing at something unrelated
// sends someone looking in the wrong place. The threshold allows one edit per
// three characters, and always at least one, so short names still tolerate a
// slip.
func Nearest(want string, from []string) string {
	best, bestDist := "", -1
	for _, c := range from {
		if c == want {
			return c
		}
		d := distance(strings.ToLower(want), strings.ToLower(c))
		if d > allowed(want, c) {
			continue
		}
		if bestDist < 0 || d < bestDist {
			best, bestDist = c, d
		}
	}
	return best
}

// allowed is how many edits still count as the same name.
func allowed(a, b string) int {
	n := max(len(a), len(b)) / 3
	if n < 1 {
		return 1
	}
	return n
}

// distance is Damerau-Levenshtein: insertions, deletions, substitutions, and
// transpositions of adjacent characters.
//
// Transpositions count as one edit rather than two because swapping a pair of
// letters is the typo people actually make. Plain Levenshtein prices "trian"
// for "train" the same as two unrelated mistakes, which is exactly the case
// worth catching.
func distance(a, b string) int {
	rows := make([][]int, len(a)+1)
	for i := range rows {
		rows[i] = make([]int, len(b)+1)
		rows[i][0] = i
	}
	for j := 0; j <= len(b); j++ {
		rows[0][j] = j
	}
	for i := 1; i <= len(a); i++ {
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			d := min(rows[i-1][j]+1, min(rows[i][j-1]+1, rows[i-1][j-1]+cost))
			if i > 1 && j > 1 && a[i-1] == b[j-2] && a[i-2] == b[j-1] {
				d = min(d, rows[i-2][j-2]+1)
			}
			rows[i][j] = d
		}
	}
	return rows[len(a)][len(b)]
}
