// SPDX-License-Identifier: MIT

package graph

import (
	"net/url"
	"strconv"
	"strings"
)

// Kind classifies a node in the graph. The first kinds describe the spine of
// the graph itself (root, domain, uri, document); the rest describe content
// inside a document and vary by Format.
type Kind uint8

// The node kinds. Content kinds are grouped by the format that produces them.
const (
	KindRoot Kind = iota
	KindDomain
	KindURI
	KindDocument

	// Markup: HTML, XML, SVG, feeds.
	KindElement
	KindAttribute
	KindText

	// JSON.
	KindObject
	KindArray
	KindField // an object member; Name is the key
	KindValue // a scalar

	// CSS.
	KindRule   // a ruleset; Name is the selector text
	KindAtRule // an at-rule such as @media
	KindDecl   // a declaration; Name is the property, Value the value

	// JavaScript.
	KindScope   // a function or block scope
	KindBinding // a declared name
	KindLiteral // a literal value

	// PDF.
	KindPage
	KindLine
)

var kindNames = [...]string{
	KindRoot:      "root",
	KindDomain:    "domain",
	KindURI:       "uri",
	KindDocument:  "document",
	KindElement:   "element",
	KindAttribute: "attribute",
	KindText:      "text",
	KindObject:    "object",
	KindArray:     "array",
	KindField:     "field",
	KindValue:     "value",
	KindRule:      "rule",
	KindAtRule:    "atrule",
	KindDecl:      "decl",
	KindScope:     "scope",
	KindBinding:   "binding",
	KindLiteral:   "literal",
	KindPage:      "page",
	KindLine:      "line",
}

// String returns the lowercase name of the kind, e.g. "element".
func (k Kind) String() string {
	if int(k) >= len(kindNames) {
		return unknownName
	}
	return kindNames[k]
}

// HoldsValue reports whether a node carries an extractable value rather than
// only structure. These are the candidates the matcher scores.
func (k Kind) HoldsValue() bool {
	switch k {
	case KindText, KindAttribute, KindValue, KindDecl, KindLiteral, KindLine, KindBinding:
		return true
	case KindRoot, KindDomain, KindURI, KindDocument, KindElement, KindObject,
		KindArray, KindField, KindRule, KindAtRule, KindScope, KindPage:
		return false
	}
	return false
}

// Node is a single vertex of the web object model. Nodes form a tree rooted at
// the graph root: root → domain → uri → document → content. Every node can
// report its own address within its document via XPath, Selector, and Path.
type Node struct {
	Kind  Kind
	Name  string // tag name, attribute name, object key, CSS property, ...
	Value string // text, attribute value, scalar value

	Parent   *Node
	Children []*Node

	format Format
	uri    *url.URL // set on KindURI nodes
	doc    *Node    // nearest enclosing KindDocument, nil above it
	pos    int      // 1-based index among siblings sharing Kind and Name

	// counts tallies children by kind and name so Append can assign a
	// position without rescanning its siblings. It is allocated only once a
	// node is wide enough for the scan to matter, because the overwhelming
	// majority of nodes have a handful of children and a map per node would
	// cost far more than it saves.
	counts map[childKey]int
}

// childKey identifies the sibling group a node's position counts within.
type childKey struct {
	kind Kind
	name string
}

// wideParent is the child count past which Append switches from scanning to
// tallying. Below it the linear scan is cheaper than allocating a map.
const wideParent = 32

// New builds a detached node. Callers attach it with Append.
func New(k Kind, name, value string) *Node {
	return &Node{Kind: k, Name: name, Value: value}
}

// Append adds child to n, assigning its sibling position and inheriting the
// document context. It returns the child so parsers can chain construction.
func (n *Node) Append(child *Node) *Node {
	if child == nil {
		return nil
	}
	child.Parent = n
	// A subtree may be built detached and grafted on afterwards, which is how
	// a failed parse is kept out of the graph entirely. Only fill in context
	// the child does not already carry.
	if child.format == FormatUnknown {
		child.format = n.format
	}
	switch {
	case child.Kind == KindDocument:
		child.doc = child
	case child.doc == nil:
		if n.Kind == KindDocument {
			child.doc = n
		} else {
			child.doc = n.doc
		}
	}

	// Position is 1-based among siblings of the same kind and name, which is
	// what both XPath indices and CSS :nth-of-type() count. Computing it by
	// scanning is quadratic in the number of children, which a 16k-element
	// JSON array or a long PDF page reaches easily, so wide parents switch to
	// a tally.
	key := childKey{kind: child.Kind, name: child.Name}
	switch {
	case n.counts != nil:
		n.counts[key]++
		child.pos = n.counts[key]
	case len(n.Children) >= wideParent:
		n.counts = make(map[childKey]int, len(n.Children)+1)
		for _, sib := range n.Children {
			n.counts[childKey{kind: sib.Kind, name: sib.Name}]++
		}
		n.counts[key]++
		child.pos = n.counts[key]
	default:
		child.pos = 1
		for _, sib := range n.Children {
			if sib.Kind == child.Kind && sib.Name == child.Name {
				child.pos++
			}
		}
	}
	n.Children = append(n.Children, child)
	return child
}

// Format reports the document format this node belongs to.
func (n *Node) Format() Format { return n.format }

// Position returns the node's 1-based index among siblings sharing its kind
// and name.
func (n *Node) Position() int { return n.pos }

// IsTopDocument reports whether n is a document served at a URI, as opposed to
// one embedded inside another document such as a JSON-LD block. Paths stop at
// any document, but ancestry walks only stop at a top-level one.
func (n *Node) IsTopDocument() bool {
	return n.Kind == KindDocument && (n.Parent == nil || n.Parent.Kind == KindURI)
}

// Document returns the nearest enclosing document node, or nil for nodes on
// the graph spine at or above the document.
func (n *Node) Document() *Node {
	if n.Kind == KindDocument {
		return n
	}
	return n.doc
}

// URI returns the URL of the response this node came from, or nil if the node
// sits above any URI in the graph.
func (n *Node) URI() *url.URL {
	for c := n; c != nil; c = c.Parent {
		if c.Kind == KindURI {
			return c.uri
		}
	}
	return nil
}

// Elements returns the children of n that are element nodes, skipping
// attributes and text.
func (n *Node) Elements() []*Node {
	out := make([]*Node, 0, len(n.Children))
	for _, c := range n.Children {
		if c.Kind == KindElement {
			out = append(out, c)
		}
	}
	return out
}

// Attr returns the value of the named attribute and whether it was present.
func (n *Node) Attr(name string) (string, bool) {
	for _, c := range n.Children {
		if c.Kind == KindAttribute && c.Name == name {
			return c.Value, true
		}
	}
	return "", false
}

// Text returns the concatenated text of n and its descendants with runs of
// whitespace collapsed. For nodes that carry a value directly (attributes,
// scalars, declarations, PDF lines) it returns that value.
func (n *Node) Text() string {
	if n.Kind.HoldsValue() {
		return strings.Join(strings.Fields(n.Value), " ")
	}
	var b strings.Builder
	n.Walk(func(c *Node) bool {
		// Attribute values are not part of the rendered text of an element.
		if c.Kind == KindAttribute {
			return false
		}
		if c.Kind == KindText || c.Kind == KindValue || c.Kind == KindLine {
			if b.Len() > 0 {
				b.WriteByte(' ')
			}
			b.WriteString(c.Value)
		}
		return true
	})
	return strings.Join(strings.Fields(b.String()), " ")
}

// Walk calls fn for n and each descendant in document order. Returning false
// from fn skips that node's children.
func (n *Node) Walk(fn func(*Node) bool) {
	if n == nil || !fn(n) {
		return
	}
	for _, c := range n.Children {
		c.Walk(fn)
	}
}

// Find returns every node at or below n for which pred reports true.
func (n *Node) Find(pred func(*Node) bool) []*Node {
	var out []*Node
	n.Walk(func(c *Node) bool {
		if pred(c) {
			out = append(out, c)
		}
		return true
	})
	return out
}

// Ancestors returns the chain from n's parent up to and including the
// document node, nearest first. It stops at the document because locators are
// document-relative.
func (n *Node) Ancestors() []*Node {
	var out []*Node
	for c := n.Parent; c != nil; c = c.Parent {
		out = append(out, c)
		if c.Kind == KindDocument {
			break
		}
	}
	return out
}

// Depth returns the number of steps from the document node down to n.
func (n *Node) Depth() int {
	d := 0
	for c := n; c != nil && c.Kind != KindDocument; c = c.Parent {
		d++
	}
	return d
}

// chain returns the path from the document node down to n, outermost first.
func (n *Node) chain() []*Node {
	var out []*Node
	for c := n; c != nil && c.Kind != KindDocument; c = c.Parent {
		out = append(out, c)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// XPath returns the absolute XPath of n within its document. It is defined
// only for markup formats; other formats return the empty string and should be
// addressed with Path instead.
func (n *Node) XPath() string {
	if !n.format.Markup() || n.Kind == KindDocument {
		return ""
	}
	var b strings.Builder
	for _, c := range n.chain() {
		switch c.Kind {
		case KindElement:
			b.WriteByte('/')
			b.WriteString(c.Name)
			b.WriteByte('[')
			b.WriteString(strconv.Itoa(c.pos))
			b.WriteByte(']')
		case KindAttribute:
			b.WriteString("/@")
			b.WriteString(c.Name)
		case KindText:
			b.WriteString("/text()[")
			b.WriteString(strconv.Itoa(c.pos))
			b.WriteByte(']')
		default:
			// Non-markup kinds cannot appear inside a markup document.
			return ""
		}
	}
	return b.String()
}

// Selector returns a CSS selector addressing n within its document. Attribute
// and text nodes resolve to their owning element, since CSS cannot select
// them; use XPath or Regex to reach the value itself. It returns the empty
// string for non-markup formats.
func (n *Node) Selector() string {
	if !n.format.Markup() {
		return ""
	}
	el := n
	for el != nil && el.Kind != KindElement {
		el = el.Parent
	}
	if el == nil {
		return ""
	}

	var parts []string
	for c := el; c != nil && c.Kind == KindElement; c = c.Parent {
		// An id is unique within a document, so it terminates the chain.
		if id, ok := c.Attr("id"); ok && validIdent(id) {
			parts = append(parts, "#"+id)
			break
		}
		seg := c.Name
		if c.pos > 1 || c.hasNamedSibling() {
			seg += ":nth-of-type(" + strconv.Itoa(c.pos) + ")"
		}
		parts = append(parts, seg)
	}
	for i, j := 0, len(parts)-1; i < j; i, j = i+1, j-1 {
		parts[i], parts[j] = parts[j], parts[i]
	}
	return strings.Join(parts, " > ")
}

// hasNamedSibling reports whether n shares its parent with another element of
// the same tag name, which is when :nth-of-type() becomes necessary.
func (n *Node) hasNamedSibling() bool {
	if n.Parent == nil {
		return false
	}
	count := 0
	for _, sib := range n.Parent.Children {
		if sib.Kind == n.Kind && sib.Name == n.Name {
			count++
			if count > 1 {
				return true
			}
		}
	}
	return false
}

// Path returns the address of n in the native dialect of its document format:
// XPath for markup, JSONPath for JSON, a rule/property path for CSS, a scope
// path for JavaScript, and a page/line path for PDF. Unlike XPath and
// Selector it is defined for every format, which is what lets a single Item
// point into any kind of document.
func (n *Node) Path() string {
	switch n.format {
	case FormatHTML, FormatXML, FormatSVG, FormatFeed:
		return n.XPath()
	case FormatJSON:
		return n.jsonPath()
	case FormatCSS:
		return n.cssPath()
	case FormatJS:
		return n.jsPath()
	case FormatPDF:
		return n.pdfPath()
	case FormatUnknown:
		return ""
	}
	return ""
}

func (n *Node) jsonPath() string {
	var b strings.Builder
	b.WriteByte('$')
	for _, c := range n.chain() {
		switch {
		case c.Parent != nil && c.Parent.Kind == KindArray:
			b.WriteByte('[')
			b.WriteString(strconv.Itoa(c.pos - 1))
			b.WriteByte(']')
		case c.Kind == KindField:
			if validIdent(c.Name) {
				b.WriteByte('.')
				b.WriteString(c.Name)
			} else {
				b.WriteString("['")
				b.WriteString(strings.ReplaceAll(c.Name, `'`, `\'`))
				b.WriteString("']")
			}
		}
	}
	return b.String()
}

func (n *Node) cssPath() string {
	var parts []string
	for _, c := range n.chain() {
		switch c.Kind {
		case KindAtRule, KindRule:
			parts = append(parts, c.Name)
		case KindDecl:
			parts = append(parts, c.Name)
		}
	}
	return strings.Join(parts, " > ")
}

func (n *Node) jsPath() string {
	var parts []string
	for _, c := range n.chain() {
		switch c.Kind {
		case KindScope, KindBinding:
			name := c.Name
			if name == "" {
				name = c.Kind.String() + "[" + strconv.Itoa(c.pos-1) + "]"
			}
			parts = append(parts, name)
		case KindLiteral:
			parts = append(parts, "literal["+strconv.Itoa(c.pos-1)+"]")
		}
	}
	return strings.Join(parts, ".")
}

func (n *Node) pdfPath() string {
	var parts []string
	for _, c := range n.chain() {
		switch c.Kind {
		case KindPage:
			parts = append(parts, "page["+strconv.Itoa(c.pos)+"]")
		case KindLine:
			parts = append(parts, "line["+strconv.Itoa(c.pos)+"]")
		}
	}
	return strings.Join(parts, ".")
}

// validIdent reports whether s can be used unquoted as a CSS id or a dotted
// JSONPath segment.
func validIdent(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_':
		case r == '-' && i > 0:
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	// A leading digit is invalid in both dialects.
	return s[0] < '0' || s[0] > '9'
}

// NewDocument builds a detached document node for a format. Parsers fill it in
// and the caller grafts it onto a URI node once parsing succeeds, so a body
// that fails to parse leaves no trace in the graph.
func NewDocument(format Format) *Node {
	n := New(KindDocument, format.String(), "")
	n.format = format
	n.doc = n
	return n
}

// SetURI records the URL a KindURI node stands for.
func (n *Node) SetURI(u *url.URL) { n.uri = u }

// SetIndex overrides a node's sibling position. Array elements need this:
// their index is positional across the whole array rather than counted per
// kind, so a mixed array such as [{}, "x", {}] numbers the second object 3.
func (n *Node) SetIndex(i int) { n.pos = i }

// Reset detaches every child, used when a URL is re-added and its previous
// document is replaced.
func (n *Node) Reset() {
	n.Children = n.Children[:0]
	n.counts = nil
}

// OwnerElement returns the element a node belongs to, following attributes and
// text up to their element. It stops at a top-level document.
func OwnerElement(n *Node) *Node {
	for c := n; c != nil && !c.IsTopDocument(); c = c.Parent {
		if c.Kind == KindElement {
			return c
		}
	}
	return nil
}
