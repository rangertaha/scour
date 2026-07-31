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
	schema.TypeDate:   `^\s*(\d{4}-\d{2}-\d{2}|\d{1,2}[/-]\d{1,2}[/-]\d{2,4}|[A-Z][a-z]+ \d{1,2},? \d{4})\s*$`,
}

// ShapePrior returns the fallback pattern for a type, or anyRegex when the
// type carries no shape information.
func ShapePrior(t schema.Type) string {
	if p, ok := shapePriors[t.Normalize()]; ok {
		return p
	}
	return AnyRegex
}
