// SPDX-License-Identifier: MIT

package pattern

import "github.com/rangertaha/scour/internal/wom/internal/schema"

// Value shapes are one of the two things wom ships with, the other being the
// chain prior. Unlike a locator they transfer between sites: a year is four
// digits wherever it appears and an email has an @ before a dot.

// shapePriors are the fallback extraction patterns for each declared type,
// used when the observed values were too varied to generalize a pattern from.
// Knowing a prop is a number is enough to do better than "match anything".
var shapePriors = map[schema.Type]string{
	schema.TypeNumber: `^\s*([-+]?[\d,]*\.?\d+)\s*$`,
	schema.TypeBool:   `^\s*(?i:(true|false|yes|no|on|off|1|0))\s*$`,
	schema.TypeURL:    `^\s*((?:https?://|/)[^\s]+)\s*$`,
	schema.TypeEmail:  `^\s*([^\s@]+@[^\s@]+\.[^\s@]+)\s*$`,
	// Dates carry the most format variety of anything with a declared type, and
	// two of the commonest were missing. RFC-822, "Fri, 31 Jul 2026 07:00:00
	// GMT", is what every RSS feed publishes; ISO-8601 with a time and a zone,
	// "2026-03-14T09:00:00Z", is what Atom and JSON-LD publish. Without them
	// the prior found the right node and then rejected every value in it: on
	// sixty real feeds, published located correctly and extracted nothing at
	// all, while every other field came back.
	schema.TypeDate: `^\s*(` +
		// ISO-8601, date alone or with a time and optional zone.
		`\d{4}-\d{2}-\d{2}(?:[T ]\d{2}:\d{2}(?::\d{2})?(?:\.\d+)?(?:Z|[+-]\d{2}:?\d{2})?)?` +
		// RFC-822 and RFC-1123, with or without the leading weekday.
		`|(?:[A-Za-z]{3},\s*)?\d{1,2}\s+[A-Za-z]{3}\s+\d{2,4}` +
		`(?:\s+\d{1,2}:\d{2}(?::\d{2})?(?:\s*(?:[A-Z]{2,4}|[+-]\d{4}))?)?` +
		// Numeric, either order, and the written-out form.
		`|\d{1,2}[/-]\d{1,2}[/-]\d{2,4}` +
		`|[A-Z][a-z]+ \d{1,2},? \d{4}` +
		`)\s*$`,
}

// ShapePrior returns the fallback pattern for a type, or anyRegex when the
// type carries no shape information.
func ShapePrior(t schema.Type) string {
	if p, ok := shapePriors[t.Normalize()]; ok {
		return p
	}
	return AnyRegex
}
