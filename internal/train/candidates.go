// SPDX-License-Identifier: GPL-3.0-or-later

package train

import (
	"strings"

	"github.com/andybalholm/cascadia"
	"golang.org/x/net/html"
)

// candidates proposes selectors that reach a node holding the wanted value.
//
// A bounded set on purpose. Every node in a page could be described a hundred
// ways, and the ones worth proposing are the ones a person would have written:
// what the page says the element is, then what it is called, then where it sits.
// A selector that describes a node exactly and reads like machine output is one
// nobody will maintain.
func candidates(root *html.Node, want string) map[string]int {
	out := map[string]int{}

	for _, found := range holding(root, want) {
		for _, selector := range describe(found.node) {
			if selector == "" {
				continue
			}
			// Kept only if it compiles: a candidate that does not is noise in
			// every later comparison.
			if _, err := cascadia.Compile(selector); err != nil {
				continue
			}
			// The deepest node this selector could have come from. A container
			// that happens to hold only the value is not the value: a byline
			// div and the span inside it both read "Alex Doe" until the byline
			// gains a reading time.
			if depth, seen := out[selector]; !seen || found.depth > depth {
				out[selector] = found.depth
			}
		}
	}
	return out
}

// at is a node holding the wanted value, and how deep it sits.
type at struct {
	node  *html.Node
	depth int
}

// holding finds the nodes whose value is the one wanted, with their depths.
//
// The depth matters because the innermost element holding a headline is the
// headline and its parents merely contain it. A selector for a container
// happens to work until the container gains a second child.
// collapse reduces a run of whitespace to one space, so the two sides of the
// comparison in [holding] are normalised the same way.
//
// They were not. What extraction found keeps the page's own spacing and gains a
// newline after every block element; the text this package reads off a node had
// its whitespace collapsed already. So a value with a newline or a double space
// in it - a headline a template wrapped, any multi-paragraph body - matched no
// node at all, and no locator was ever proposed for it. Silently: `scour job
// train` printed no line for the property, which reads as "nothing works on
// this corpus" rather than "these could not be compared".
//
// Whitespace-insensitive is the right comparison here anyway: the question is
// whether this node holds that value, and HTML does not preserve the difference
// between a newline and a space in the first place.
func collapse(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func holding(root *html.Node, want string) []at {
	var found []at

	var walk func(*html.Node, int)
	walk = func(n *html.Node, depth int) {
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child, depth+1)
		}
		if n.Type == html.ElementNode && collapse(nodeValue(n)) == collapse(want) {
			found = append(found, at{node: n, depth: depth})
		}
	}
	walk(root, 0)
	return found
}

// describe proposes the ways of naming one node.
func describe(n *html.Node) []string {
	var out []string

	// What the page says the element is. The most durable, because it is a
	// claim the publisher made on purpose rather than a name their stylesheet
	// happened to use.
	for _, key := range []string{"itemprop", "property", "name"} {
		if value := attr(n, key); value != "" {
			out = append(out, n.Data+"["+key+"="+quote(value)+"]")
		}
	}
	if id := attr(n, "id"); id != "" && plain(id) {
		out = append(out, "#"+id)
	}

	// Then what it is called. Every class on its own, then the tag with it,
	// because ".headline" and "h1.headline" fail differently: one survives the
	// element changing and the other survives the class being reused.
	classes := strings.Fields(attr(n, "class"))
	for _, class := range classes {
		if !plain(class) {
			continue
		}
		out = append(out, "."+class)
		out = append(out, n.Data+"."+class)
	}

	// Then where it sits: one step of context, which is what distinguishes the
	// headline in the article from the one in the sidebar.
	if parent := n.Parent; parent != nil && parent.Type == html.ElementNode {
		for _, class := range strings.Fields(attr(parent, "class")) {
			if !plain(class) {
				continue
			}
			out = append(out, "."+class+" "+n.Data)
			if len(classes) > 0 && plain(classes[0]) {
				out = append(out, "."+class+" ."+classes[0])
			}
		}
		if id := attr(parent, "id"); id != "" && plain(id) {
			out = append(out, "#"+id+" "+n.Data)
		}
	}

	// And the bare element, last, because it is only right when there is one
	// of them.
	out = append(out, n.Data)
	return out
}

// valueOf is what a selector produces on a page, if anything.
func valueOf(root *html.Node, selector string) (string, bool) {
	compiled, err := cascadia.Compile(selector)
	if err != nil {
		return "", false
	}
	node := compiled.MatchFirst(root)
	if node == nil {
		return "", false
	}
	return strings.TrimSpace(nodeValue(node)), true
}

// plain reports whether a name can go in a selector without escaping.
//
// A generated class like `css-1x2y3z` is plain and useless, but that is a
// judgement about durability rather than syntax, and the page count already
// makes it: a hashed class that changes per build works on one page.
func plain(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		case r == '-' || r == '_':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

func quote(value string) string {
	if plain(value) || !strings.ContainsAny(value, ` "'[]()`) {
		return `"` + value + `"`
	}
	return `"` + strings.ReplaceAll(value, `"`, `\"`) + `"`
}

// attr returns an attribute's value.
func attr(n *html.Node, name string) string {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, name) {
			return strings.TrimSpace(a.Val)
		}
	}
	return ""
}

// nodeValue is what an element means as a value, matching what extraction does
// with it: the attribute where one lives, and the text otherwise.
func nodeValue(n *html.Node) string {
	if n.Type != html.ElementNode {
		return strings.TrimSpace(textOf(n))
	}

	switch n.Data {
	case "meta":
		return attr(n, "content")
	case "time":
		if v := attr(n, "datetime"); v != "" {
			return v
		}
	case "a", "link", "area":
		if v := attr(n, "href"); v != "" {
			return v
		}
	case "img", "source", "iframe", "embed":
		if v := attr(n, "src"); v != "" {
			return v
		}
	case "input", "data":
		if v := attr(n, "value"); v != "" {
			return v
		}
	}
	if v := attr(n, "content"); v != "" {
		return v
	}
	return strings.TrimSpace(textOf(n))
}

func textOf(n *html.Node) string {
	var b strings.Builder

	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
			return
		}
		if n.Type == html.ElementNode {
			switch n.Data {
			case "script", "style", "noscript", "template":
				return
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(n)
	return strings.Join(strings.Fields(b.String()), " ")
}
