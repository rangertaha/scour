// SPDX-License-Identifier: MIT

package schema

import (
	"errors"
	"fmt"
	"strings"
)

// Type is the value type a Prop is expected to hold. It is used both to
// validate candidate values and to bias scoring toward nodes whose text
// actually parses as the declared type.
type Type string

// The supported property types. An empty Type is treated as TypeString.
const (
	TypeString Type = "string"
	TypeNumber Type = "number"
	TypeBool   Type = "bool"
	TypeDate   Type = "date"
	TypeURL    Type = "url"
	TypeEmail  Type = "email"
)

// Valid reports whether t is a known type.
func (t Type) Valid() bool {
	switch t {
	case "", TypeString, TypeNumber, TypeBool, TypeDate, TypeURL, TypeEmail:
		return true
	}
	return false
}

// Normalize maps the zero value to TypeString.
func (t Type) Normalize() Type {
	if t == "" {
		return TypeString
	}
	return t
}

// Prop describes one field to locate in the graph. Name is required; the
// remaining fields are optional signals that improve matching:
//
//   - Aliases are alternative labels the site might use ("car" for "vehicles").
//   - Description is prose about the field, mined for additional label tokens.
//   - Examples are actual values seen on the site and are by far the strongest
//     signal, since they can be matched against node text directly.
//   - Props nests sub-fields, which turns the prop into a record: wom then
//     looks for a repeating container holding all of them.
type Prop struct {
	Name        string   `json:"name"`
	Type        Type     `json:"type,omitempty"`
	Aliases     []string `json:"aliases,omitempty"`
	Description string   `json:"description,omitempty"`
	Examples    []string `json:"examples,omitempty"`
	Props       []Prop   `json:"props,omitempty"`

	// Pattern is what an acceptable value looks like, and optionally where in
	// it the value is.
	//
	// One pattern answers both questions because a regex already does. Text it
	// rejects is not this field, whatever else about it agrees, so it decides
	// which node wins; capture group one decides what that node yields. With no
	// group it only validates, and extracting without validating is incoherent,
	// since a pattern that does not match has nothing to extract.
	//
	// Type already does the validating half for the shapes a type implies, via
	// ShapePrior. Pattern is the same idea where the knowledge is about a site
	// rather than a type.
	Pattern string `json:"pattern,omitempty"`
}

// Schema is an ordered set of props. It exists so a schema can be declared as
// a value and spread into Schema:
//
//	item := wom.Schema{ {Name: "vehicles", Props: []wom.Prop{...}} }
//	items, err := w.Schema(item...)
type Schema []Prop

// IsRecord reports whether the prop describes a nested record rather than a
// single value.
func (p Prop) IsRecord() bool { return len(p.Props) > 0 }

// Labels returns every token that could plausibly appear as a label for this
// prop on a page: its name, its aliases, and the words of its description.
// Results are lowercased and deduplicated, with name and aliases first so
// callers can weight them more heavily by position.
func (p Prop) Labels() []string {
	seen := make(map[string]bool)
	var out []string
	add := func(s string) {
		s = strings.ToLower(strings.TrimSpace(s))
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		out = append(out, s)
	}

	add(p.Name)
	for _, a := range p.Aliases {
		add(a)
	}
	// Split the name on the usual identifier separators so "fuel_type" also
	// matches a "fuel" or "type" label.
	for _, part := range splitIdent(p.Name) {
		add(part)
	}
	for _, w := range strings.Fields(p.Description) {
		w = strings.Trim(w, ".,;:!?()[]\"'")
		if len(w) > 2 && !stopWords[strings.ToLower(w)] {
			add(w)
		}
	}
	return out
}

// StrongLabels returns only the high-confidence labels: the name, its
// identifier parts, and the explicit aliases. Description words are excluded
// because they are noisy.
func (p Prop) StrongLabels() []string {
	seen := make(map[string]bool)
	var out []string
	add := func(s string) {
		s = strings.ToLower(strings.TrimSpace(s))
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		out = append(out, s)
	}
	add(p.Name)
	for _, part := range splitIdent(p.Name) {
		add(part)
	}
	for _, a := range p.Aliases {
		add(a)
	}
	return out
}

// splitIdent breaks an identifier into its words, handling snake_case,
// kebab-case, dotted paths, spaces, and camelCase.
func splitIdent(s string) []string {
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return r == '_' || r == '-' || r == '.' || r == ' ' || r == '/'
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		out = append(out, splitCamel(f)...)
	}
	if len(out) == 1 && strings.EqualFold(out[0], s) {
		// Nothing was actually split; the caller already has this token.
		return nil
	}
	return out
}

func splitCamel(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	start := 0
	rs := []rune(s)
	for i := 1; i < len(rs); i++ {
		upper := rs[i] >= 'A' && rs[i] <= 'Z'
		prevLower := rs[i-1] >= 'a' && rs[i-1] <= 'z'
		prevDigit := rs[i-1] >= '0' && rs[i-1] <= '9'
		if upper && (prevLower || prevDigit) {
			out = append(out, string(rs[start:i]))
			start = i
		}
	}
	return append(out, string(rs[start:]))
}

// stopWords are description words too common to carry any signal.
var stopWords = map[string]bool{
	"the": true, "and": true, "for": true, "with": true, "that": true,
	"this": true, "from": true, "are": true, "was": true, "its": true,
	"has": true, "have": true, "not": true, "any": true, "all": true,
	"can": true, "will": true, "may": true, "each": true, "such": true,
	"which": true, "when": true, "where": true, "into": true, "onto": true,
}

// ErrEmptySchema is returned when Schema is called with no props.
var ErrEmptySchema = errors.New("wom: schema has no props")

// validate checks a prop tree for the mistakes that would otherwise surface as
// silently empty results: missing names, unknown types, duplicate siblings,
// and runaway nesting.
func Validate(props []Prop, depth int) error {
	const maxDepth = 16
	if depth > maxDepth {
		return fmt.Errorf("wom: schema nested deeper than %d levels", maxDepth)
	}
	seen := make(map[string]bool, len(props))
	for i, p := range props {
		name := strings.TrimSpace(p.Name)
		if name == "" {
			return fmt.Errorf("wom: prop %d has no name", i)
		}
		if !p.Type.Valid() {
			return fmt.Errorf("wom: prop %q has unknown type %q", name, p.Type)
		}
		key := strings.ToLower(name)
		if seen[key] {
			return fmt.Errorf("wom: duplicate prop %q", name)
		}
		seen[key] = true
		if err := Validate(p.Props, depth+1); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}
