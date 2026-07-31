// SPDX-License-Identifier: MIT

// Package pattern turns a set of observed values, URLs, or node paths into the
// generalized patterns that make up a Locator, and evaluates those patterns
// back against concrete nodes.
package pattern

import (
	"net/url"
	"regexp"
	"strings"
	"unicode"
)

// AnyRegex is the pattern returned when a set of values is too varied to
// generalize. It still captures, so callers can apply it uniformly.
const AnyRegex = `^(.*)$`

// SynthesizeRegex derives a regular expression matching every observed value.
// It returns an anchored pattern with exactly one capture group around the
// value. Identical values yield a literal; values sharing a shape yield that
// shape ("2019", "2020" become ^(\d{4})$); anything else falls back to
// AnyRegex.
func SynthesizeRegex(values []string) string {
	vals := dedupe(values)
	if len(vals) == 0 {
		return AnyRegex
	}
	if len(vals) == 1 {
		return `^(` + regexp.QuoteMeta(vals[0]) + `)$`
	}

	shapes := make([]([]runRec), 0, len(vals))
	for _, v := range vals {
		shapes = append(shapes, shapeOf(v))
	}

	// Every value must decompose into the same sequence of run classes for a
	// shape-based pattern to be meaningful.
	first := shapes[0]
	for _, s := range shapes[1:] {
		if len(s) != len(first) {
			return AnyRegex
		}
		for i := range s {
			if s[i].class != first[i].class {
				return AnyRegex
			}
			if s[i].class == runLiteral && s[i].text != first[i].text {
				return AnyRegex
			}
		}
	}

	// Runs whose text is identical in every value are boilerplate, not data.
	// "Make: Toyota" and "Make: Honda" share the label and differ only in the
	// name, so the capture group belongs around the name alone — the caller
	// wants the value, not the value with its label glued to the front.
	fixed := make([]bool, len(first))
	firstVar, lastVar := -1, -1
	for i := range first {
		// A digit run is data by nature. Agreeing across the sample is
		// coincidence — three articles published the same year would
		// otherwise freeze that year into the pattern and stop matching in
		// January. Letters and punctuation that agree really are boilerplate.
		fixed[i] = first[i].class != runDigit
		for _, s := range shapes[1:] {
			if s[i].text != first[i].text {
				fixed[i] = false
				break
			}
		}
		if !fixed[i] {
			if firstVar < 0 {
				firstVar = i
			}
			lastVar = i
		}
	}
	if firstVar < 0 {
		// Every run agreed, so the values were all the same after dedupe.
		return `^(` + regexp.QuoteMeta(literalOf(first)) + `)$`
	}

	var b strings.Builder
	b.WriteByte('^')
	b.WriteString(regexp.QuoteMeta(literalOf(first[:firstVar])))
	b.WriteByte('(')
	for i := firstVar; i <= lastVar; i++ {
		if fixed[i] {
			b.WriteString(regexp.QuoteMeta(first[i].text))
			continue
		}
		lo, hi := first[i].length, first[i].length
		for _, s := range shapes[1:] {
			if s[i].length < lo {
				lo = s[i].length
			}
			if s[i].length > hi {
				hi = s[i].length
			}
		}
		b.WriteString(first[i].pattern(lo, hi, len(shapes)))
	}
	b.WriteByte(')')
	b.WriteString(regexp.QuoteMeta(literalOf(first[lastVar+1:])))
	b.WriteByte('$')
	return b.String()
}

// literalOf concatenates the text of a run of shape records.
func literalOf(runs []runRec) string {
	var b strings.Builder
	for _, r := range runs {
		b.WriteString(r.text)
	}
	return b.String()
}

// runClass is the character class of one homogeneous run within a value.
type runClass uint8

const (
	runDigit runClass = iota
	runAlpha
	runSpace
	runLiteral
)

type runRec struct {
	class  runClass
	text   string
	length int
}

// minWidthEvidence is how many distinct values must agree on a width before it
// is treated as a property of the field rather than of the sample. Two names
// that happen to be the same length say nothing about the third.
const minWidthEvidence = 3

// pattern renders the run as a regex fragment covering lengths lo..hi, given
// how many distinct values were observed.
func (r runRec) pattern(lo, hi, samples int) string {
	if r.class == runLiteral {
		return regexp.QuoteMeta(r.text)
	}
	var base string
	switch r.class {
	case runDigit:
		base = `\d`
	case runAlpha:
		base = `[A-Za-z]`
	case runSpace:
		base = `\s`
	}
	// A width that enough samples agree on is evidence of a fixed-width field:
	// a year really is four digits. A width that varies, or that only one or
	// two values happen to share, is just the sample — three author names of
	// 4, 6 and 7 letters say nothing about the fourth. Since a locator whose
	// regex fails now drops the value rather than returning it raw, guessing a
	// bound here loses real data, so the bar for guessing is set high.
	switch {
	case lo == 1 && hi == 1:
		return base
	case lo == hi && samples >= minWidthEvidence:
		return base + `{` + itoa(lo) + `}`
	default:
		return base + `+`
	}
}

// shapeOf splits a value into runs of digits, letters, whitespace, and
// literal punctuation.
func shapeOf(s string) []runRec {
	var out []runRec
	rs := []rune(s)
	for i := 0; i < len(rs); {
		c := classOf(rs[i])
		j := i
		for j < len(rs) && classOf(rs[j]) == c {
			j++
		}
		out = append(out, runRec{class: c, text: string(rs[i:j]), length: j - i})
		i = j
	}
	return out
}

func classOf(r rune) runClass {
	switch {
	case unicode.IsDigit(r):
		return runDigit
	case unicode.IsLetter(r):
		return runAlpha
	case unicode.IsSpace(r):
		return runSpace
	default:
		return runLiteral
	}
}

// SynthesizeURI derives a regular expression matching every URL a value was
// found at. Path segments that agree across all URLs stay literal; segments
// that vary become [^/]+, which is what turns a set of product pages into a
// single pattern.
func SynthesizeURI(uris []string) string {
	vals := dedupe(uris)
	if len(vals) == 0 {
		return AnyRegex
	}
	if len(vals) == 1 {
		return `^` + regexp.QuoteMeta(vals[0]) + `$`
	}

	type parsed struct {
		scheme, host string
		segs         []string
		// slash records whether the URL ended in one. Trimming it away when
		// splitting the path would otherwise produce a pattern anchored with
		// $ that cannot match the very URLs it was synthesized from, which is
		// most of them: directory-style URLs end in a slash.
		slash bool
	}
	all := make([]parsed, 0, len(vals))
	for _, raw := range vals {
		u, err := url.Parse(raw)
		if err != nil {
			return AnyRegex
		}
		segs := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(segs) == 1 && segs[0] == "" {
			segs = nil
		}
		all = append(all, parsed{
			scheme: u.Scheme, host: u.Host, segs: segs,
			slash: len(u.Path) > 1 && strings.HasSuffix(u.Path, "/"),
		})
	}

	var b strings.Builder
	b.WriteByte('^')

	// Scheme: collapse http/https to one alternation rather than dropping it.
	if allSame(all, func(p parsed) string { return p.scheme }) {
		b.WriteString(regexp.QuoteMeta(all[0].scheme))
	} else {
		b.WriteString(`https?`)
	}
	b.WriteString("://")

	if allSame(all, func(p parsed) string { return p.host }) {
		b.WriteString(regexp.QuoteMeta(all[0].host))
	} else {
		b.WriteString(`[^/]+`)
	}

	// Paths of differing depth cannot be aligned segment by segment; keep the
	// segments they agree on as a prefix and generalize the rest.
	segCount := len(all[0].segs)
	sameDepth := true
	for _, p := range all[1:] {
		if len(p.segs) != segCount {
			sameDepth = false
			break
		}
	}
	if !sameDepth {
		prefix := commonSegments(all[0].segs, all[1:], func(p parsed) []string { return p.segs })
		for _, s := range prefix {
			b.WriteByte('/')
			b.WriteString(regexp.QuoteMeta(s))
		}
		b.WriteString(`(?:/.*)?$`)
		return b.String()
	}

	for i := range all[0].segs {
		b.WriteByte('/')
		same := true
		for _, p := range all[1:] {
			if p.segs[i] != all[0].segs[i] {
				same = false
				break
			}
		}
		if same {
			b.WriteString(regexp.QuoteMeta(all[0].segs[i]))
		} else {
			b.WriteString(`[^/]+`)
		}
	}
	switch {
	case segCount == 0:
		b.WriteString(`/?`)
	case allTrue(all, func(p parsed) bool { return p.slash }):
		b.WriteByte('/')
	case anyTrue(all, func(p parsed) bool { return p.slash }):
		b.WriteString(`/?`)
	}
	b.WriteByte('$')
	return b.String()
}

// allTrue reports whether every item satisfies the predicate.
func allTrue[T any](items []T, pred func(T) bool) bool {
	for _, it := range items {
		if !pred(it) {
			return false
		}
	}
	return true
}

// anyTrue reports whether any item satisfies the predicate.
func anyTrue[T any](items []T, pred func(T) bool) bool {
	for _, it := range items {
		if pred(it) {
			return true
		}
	}
	return false
}

func allSame[T any](items []T, key func(T) string) bool {
	if len(items) == 0 {
		return true
	}
	first := key(items[0])
	for _, it := range items[1:] {
		if key(it) != first {
			return false
		}
	}
	return true
}

// commonSegments returns the longest leading run of path segments shared by
// every item.
func commonSegments[T any](first []string, rest []T, key func(T) []string) []string {
	n := len(first)
	for _, it := range rest {
		segs := key(it)
		if len(segs) < n {
			n = len(segs)
		}
		for i := 0; i < n; i++ {
			if segs[i] != first[i] {
				n = i
				break
			}
		}
	}
	return first[:n]
}

// dedupe returns the distinct non-empty entries of vals, preserving order.
func dedupe(vals []string) []string {
	seen := make(map[string]bool, len(vals))
	out := make([]string, 0, len(vals))
	for _, v := range vals {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
