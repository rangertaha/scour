// SPDX-License-Identifier: GPL-3.0-or-later

package engine

import (
	"sort"

	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty"
)

// The vocabulary a document may use as bare words.
//
// `type = str` and `transforms = [datetime]` are written without quotes, which
// in HCL makes them variable references rather than strings. Predeclaring them
// is what makes that legal, and it buys something quoting would not: a
// misspelling is caught by the parser, with a line and a column and a
// suggestion, instead of being carried as a string until something later fails
// to make sense of it.
//
// Quoted forms still work, because "str" and str decode to the same value.

// Type is what a property holds.
type Type string

// The types a property may declare.
const (
	TypeStr    Type = "str"
	TypeInt    Type = "int"
	TypeFloat  Type = "float"
	TypeBool   Type = "bool"
	TypeDate   Type = "date"
	TypeURL    Type = "url"
	TypeObject Type = "object"
	TypeList   Type = "list"
	// TypeEntity is a reference to something in the shared entity store: a
	// person, an organisation, a place. The value extracted is a name; what is
	// stored is a link to the thing that name refers to, resolved against what
	// the store already knows.
	TypeEntity Type = "entity"
)

// Types is every type a property or item may declare.
var Types = []Type{
	TypeStr, TypeInt, TypeFloat, TypeBool,
	TypeDate, TypeURL, TypeObject, TypeList, TypeEntity,
}

// DefaultType is what a property that does not say gets. Text, because most
// of what comes off a page is.
const DefaultType = TypeStr

// Transforms are the registered functions a property may apply to what was
// found, in the order written.
//
// They are named here rather than discovered, because a document naming a
// transform that does not exist should be refused when it is submitted rather
// than when the first page that needs it arrives.
const (
	// TransformText reduces markup to its text.
	TransformText = "text"
	// TransformTrim removes surrounding whitespace.
	TransformTrim = "trim"
	// TransformLower and TransformUpper change case.
	TransformLower = "lower"
	TransformUpper = "upper"
	// TransformDatetime parses a date in whatever shape the page wrote it and
	// renders it as RFC 3339.
	TransformDatetime = "datetime"
	// TransformAbsURL resolves a relative link against the page it was found
	// on.
	TransformAbsURL = "absurl"
	// TransformNormaliseSpace collapses runs of whitespace to one space.
	TransformNormaliseSpace = "normalise_space"
)

// Transforms is every registered transform.
var Transforms = []string{
	TransformText,
	TransformTrim,
	TransformLower,
	TransformUpper,
	TransformDatetime,
	TransformAbsURL,
	TransformNormaliseSpace,
}

// evalContext predeclares the vocabulary, so bare words resolve.
func evalContext() *hcl.EvalContext {
	vars := make(map[string]cty.Value, len(Types)+len(Transforms))

	for _, t := range Types {
		vars[string(t)] = cty.StringVal(string(t))
	}
	for _, t := range Transforms {
		vars[t] = cty.StringVal(t)
	}

	return &hcl.EvalContext{Variables: vars}
}

// Valid reports whether a type is one scour knows.
func (t Type) Valid() bool {
	for _, known := range Types {
		if t == known {
			return true
		}
	}
	return false
}

// TypeNames lists the types, sorted, for an error message.
func TypeNames() []string {
	out := make([]string, 0, len(Types))
	for _, t := range Types {
		out = append(out, string(t))
	}
	sort.Strings(out)
	return out
}

// TransformNames lists the transforms, sorted, for an error message.
func TransformNames() []string {
	out := append([]string(nil), Transforms...)
	sort.Strings(out)
	return out
}
