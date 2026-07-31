// SPDX-License-Identifier: MIT

package graph

import "strings"

// Addressing a node relative to a container is what makes a record's fields
// reusable: ./span[1]/text()[1] means the same thing inside every row, while
// the absolute path means it in only one.

// ValueNodes returns the value-holding descendants of n in document order,
// which is both the candidate set for scoring and the linear sequence the
// chain decodes.
func ValueNodes(n *Node) []*Node {
	var out []*Node
	n.Walk(func(c *Node) bool {
		if c.Kind.HoldsValue() && c.Text() != "" {
			out = append(out, c)
		}
		return true
	})
	return out
}

// HostElement returns the element a nested document hangs from, or nil when
// the node's document is the page itself. JSON-LD embedded in HTML is the case
// this exists for.
func HostElement(n *Node) *Node {
	doc := n.Document()
	if doc == nil || doc.Parent == nil || doc.Parent.Kind != KindElement {
		return nil
	}
	return doc.Parent
}

// RelPath returns the path of n relative to base, in n's native dialect. Each
// dialect joins its segments differently, so a relative path has to be spliced
// the way that dialect expects rather than always prefixed with a dot.
func RelPath(n, base *Node) string {
	full := n.Path()
	if base == nil || base == n {
		return full
	}
	prefix := base.Path()
	if prefix == "" || !strings.HasPrefix(full, prefix) {
		return full
	}
	suffix := full[len(prefix):]
	if suffix == "" {
		return "."
	}
	switch {
	case n.format == FormatJSON:
		return "@" + suffix
	case strings.HasPrefix(suffix, "/"):
		return "." + suffix // XPath
	}
	suffix = strings.TrimPrefix(suffix, ".")   // page[1].line[2], scope.name
	suffix = strings.TrimPrefix(suffix, " > ") // CSS rule > property
	return suffix
}

// RelXPath returns the XPath of n relative to base.
func RelXPath(n, base *Node) string {
	full := n.XPath()
	if base == nil || base == n || full == "" {
		return full
	}
	prefix := base.XPath()
	if prefix == "" || !strings.HasPrefix(full, prefix) {
		return full
	}
	if suffix := full[len(prefix):]; suffix != "" {
		return "." + suffix
	}
	return "."
}

// RelSelector returns the CSS selector of n relative to base.
func RelSelector(n, base *Node) string {
	full := n.Selector()
	if base == nil || base == n || full == "" {
		return full
	}
	prefix := base.Selector()
	if prefix == "" || !strings.HasPrefix(full, prefix) {
		return full
	}
	return strings.TrimPrefix(strings.TrimPrefix(full[len(prefix):], " "), "> ")
}
