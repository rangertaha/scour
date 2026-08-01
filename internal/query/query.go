// SPDX-License-Identifier: GPL-3.0-or-later

// Package query is the search language for records.
//
// A query is a list of terms, all of which must match. A bare word matches any
// field of a record or its url; field:value matches one field. That is the
// whole grammar, and it is deliberately small: the thing being searched is a
// handful of properties somebody defined themselves, so anything more would be
// a language to learn before you can find the row you came for.
//
// # Why the fields are known in advance
//
// Whether a colon separates a field from a value cannot be decided by looking
// at the text. A url is full of colons, and https://example.com would parse as
// the field "https" under any rule that trusts punctuation. So parsing takes
// the fields that exist, and a prefix that is not one of them is part of the
// word rather than a field name. Searching for a url therefore works without
// quoting, and a mistyped field is reported rather than silently matching
// nothing.
package query

import (
	"fmt"
	"strings"

	"github.com/rangertaha/scour/internal/fuzzy"
)

// URLField is the one field every item has, whatever properties it defines.
// The url is not a property, but it is the thing people search by most often
// when they are looking for a record from one site.
const URLField = "url"

// Term is one condition a record has to satisfy.
type Term struct {
	// Field is which field to look in. Empty means any of them, plus the url,
	// which is what a bare word does.
	Field string
	// Text is what to look for, matched case insensitively as a substring.
	Text string
}

// Any reports whether the term looks in every field rather than a named one.
func (t Term) Any() bool { return t.Field == "" }

// String renders the term the way it would be typed.
func (t Term) String() string {
	if t.Any() {
		return quoteIfNeeded(t.Text)
	}
	return t.Field + ":" + quoteIfNeeded(t.Text)
}

func quoteIfNeeded(s string) string {
	if strings.ContainsAny(s, " \t") {
		return `"` + s + `"`
	}
	return s
}

// Query is the whole search: every term must match.
//
// Terms narrow rather than widen because that is what repeated typing means
// everywhere else a search box exists, and because narrowing is recoverable by
// deleting a word while widening is not.
type Query struct {
	Terms []Term
}

// Empty reports whether there is nothing to search for.
func (q Query) Empty() bool { return len(q.Terms) == 0 }

// Fields lists the named fields the query mentions, in the order they appear.
func (q Query) Fields() []string {
	var out []string
	seen := map[string]bool{}
	for _, t := range q.Terms {
		if t.Any() || seen[t.Field] {
			continue
		}
		seen[t.Field] = true
		out = append(out, t.Field)
	}
	return out
}

// String renders the query the way it would be typed.
func (q Query) String() string {
	parts := make([]string, 0, len(q.Terms))
	for _, t := range q.Terms {
		parts = append(parts, t.String())
	}
	return strings.Join(parts, " ")
}

// Parse turns command line arguments into a query.
//
// One argument is one term: the shell has already done the quoting, so an
// argument holding a space is a phrase and needs no further splitting. Fields
// are the item's properties plus url, and a prefix that is not one of them is
// treated as part of the word rather than as a field, so a url can be searched
// for without escaping the colon in it.
//
// An unknown field is only reported when the text before the colon could not
// plausibly be anything else: it has no scheme-like shape and the query names
// no field that is spelled almost the same way. Otherwise a typo would search
// for a literal string nobody meant and report no matches, which reads as an
// empty database rather than as a mistake.
func Parse(args []string, fields []string) (Query, error) {
	known := map[string]bool{URLField: true}
	for _, f := range fields {
		known[strings.ToLower(f)] = true
	}

	var q Query
	for _, arg := range args {
		arg = strings.TrimSpace(arg)
		if arg == "" {
			continue
		}

		name, value, found := strings.Cut(arg, ":")
		switch {
		case !found:
			q.Terms = append(q.Terms, Term{Text: arg})
		case known[strings.ToLower(name)]:
			if value == "" {
				return Query{}, fmt.Errorf("%s: needs something to look for, as %s:<value>", arg, name)
			}
			q.Terms = append(q.Terms, Term{Field: strings.ToLower(name), Text: value})
		case looksLikeAField(name):
			near := fuzzy.Nearest(strings.ToLower(name), append(fieldNames(fields), URLField))
			if near != "" {
				return Query{}, fmt.Errorf("no field %q, did you mean %q? (fields: %s)",
					name, near, strings.Join(append(fieldNames(fields), URLField), ", "))
			}
			return Query{}, fmt.Errorf("no field %q (fields: %s)",
				name, strings.Join(append(fieldNames(fields), URLField), ", "))
		default:
			// A url, a time, a ratio: something whose colon is part of it.
			q.Terms = append(q.Terms, Term{Text: arg})
		}
	}
	return q, nil
}

// fieldNames lowercases the property names for reporting and matching.
func fieldNames(fields []string) []string {
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		out = append(out, strings.ToLower(f))
	}
	return out
}

// looksLikeAField reports whether the text before a colon was meant as a field
// name rather than as part of the word.
//
// A field is a bare identifier starting with a letter. Anything holding a
// slash, a dot or a space came from a url or a sentence, and anything starting
// with a digit is a time or a ratio, so none of them is a misspelled field
// worth complaining about.
func looksLikeAField(s string) bool {
	if s == "" {
		return false
	}
	if c := s[0]; !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z') {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9', r == '_', r == '-':
		default:
			return false
		}
	}
	// A scheme is the one identifier-shaped prefix that is routinely not a
	// field, and it is always followed by the slashes of an authority, which
	// Cut has already removed. Checked by name because the set is short and
	// the alternative is guessing from what comes after.
	switch strings.ToLower(s) {
	case "http", "https", "ftp", "file", "mailto", "data":
		return false
	}
	return true
}
