// SPDX-License-Identifier: MIT

package schema

import (
	"fmt"
	"strings"

	"github.com/rangertaha/scour/internal/wom/internal/graph"
)

// Locator is the address of a value in the graph, expressed in every dialect
// that applies to the document it was found in. It is what makes one Item able
// to point into any kind of document: markup fills XPath and Selector, while
// Path is always populated in the format's native dialect.
//
// A zero-valued field means "not applicable to this format", not "not found".
type Locator struct {
	// Format is the document dialect the locator addresses.
	Format graph.Format `json:"format"`

	// URI is a regular expression matching the URLs the value was found at.
	// It generalizes over the observed URLs, so a value found on many product
	// pages yields a pattern rather than one literal URL.
	URI string `json:"uri,omitempty"`

	// XPath addresses the value inside a markup document. Empty for
	// non-markup formats.
	XPath string `json:"xpath,omitempty"`

	// Selector is a CSS selector for the element holding the value. Because
	// CSS cannot select attributes or text nodes it resolves to the owning
	// element; pair it with Regex to reach the value itself. Empty for
	// non-markup formats.
	Selector string `json:"selector,omitempty"`

	// Path addresses the value in the format's native dialect: XPath for
	// markup, JSONPath for JSON, a rule/property path for CSS, a scope path
	// for JavaScript, and page[n].line[n] for PDF. Always populated.
	Path string `json:"path,omitempty"`

	// Regex extracts the value from the text at the located node. It is
	// synthesized from the values actually observed, and is "^(.*)$" when they
	// were too varied to generalize.
	Regex string `json:"regex,omitempty"`
}

// String renders the locator compactly for logs and test failures.
func (l Locator) String() string {
	parts := []string{l.Format.String()}
	if l.URI != "" {
		parts = append(parts, "uri="+l.URI)
	}
	if l.XPath != "" {
		parts = append(parts, "xpath="+l.XPath)
	} else if l.Path != "" {
		parts = append(parts, "path="+l.Path)
	}
	if l.Selector != "" {
		parts = append(parts, "css="+l.Selector)
	}
	if l.Regex != "" {
		parts = append(parts, "regex="+l.Regex)
	}
	return strings.Join(parts, " ")
}

// Item is an inferred location for one schema Prop. Items mirror the shape of
// the schema that produced them: a Prop with nested Props yields an Item whose
// Locator addresses the repeating container and whose Items address the fields
// within it, relative to that container.
type Item struct {
	// Name is the Prop name this item answers.
	Name string `json:"name"`

	// Probability is the confidence that the locator really addresses the
	// requested field, in [0,1].
	Probability float64 `json:"probability"`

	// Locator is the address of the value, in every applicable dialect.
	Locator `json:"locator"`

	// Support is the number of distinct nodes in the graph that agreed on
	// this locator. Higher support means the pattern repeated rather than
	// matching once by chance.
	Support int `json:"support,omitempty"`

	// Values samples the text actually found at the locator, capped at a
	// handful of entries. Useful for eyeballing whether a match is real.
	Values []string `json:"values,omitempty"`

	// Items holds the located sub-fields when the Prop was a record.
	Items []Item `json:"items,omitempty"`
}

// String renders the item as a single line, without its children.
func (i Item) String() string {
	return fmt.Sprintf("%s p=%.2f support=%d %s", i.Name, i.Probability, i.Support, i.Locator)
}

// Tree renders the item and its descendants as an indented block, which is
// the readable way to inspect a Schema result.
func (i Item) Tree() string {
	var b strings.Builder
	i.tree(&b, 0)
	return b.String()
}

func (i Item) tree(b *strings.Builder, depth int) {
	b.WriteString(strings.Repeat("  ", depth))
	b.WriteString(i.String())
	b.WriteByte('\n')
	for _, c := range i.Items {
		c.tree(b, depth+1)
	}
}

// Flatten returns the item and all of its descendants in depth-first order,
// with each name prefixed by its ancestors, e.g. "vehicles.make".
func (i Item) Flatten() []Item {
	return i.flatten("")
}

func (i Item) flatten(prefix string) []Item {
	name := i.Name
	if prefix != "" {
		name = prefix + "." + name
	}
	flat := i
	flat.Name = name
	flat.Items = nil
	out := make([]Item, 0, 1+len(i.Items))
	out = append(out, flat)
	for _, c := range i.Items {
		out = append(out, c.flatten(name)...)
	}
	return out
}
