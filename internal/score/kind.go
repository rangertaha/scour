// SPDX-License-Identifier: GPL-3.0-or-later

package score

// Kind is the sort of node a scorer ranks.
//
// scour has more than one graph and they have different nodes. The crawl graph
// is URLs and the paths taken to reach them; a parsed page is elements,
// attributes and text. Both get ranked, and both rankings are pluggable, but
// they are not interchangeable: a scorer of one takes an input the other cannot
// produce, so they are separate registries rather than one with a cast in it.
//
// What the kind gives is a way to ask which scorers rank which nodes, and a
// place to put the next graph. A feed's items and a PDF's regions are nodes
// too, and neither is a URL or an HTML element.
type Kind string

const (
	// KindURL ranks a link in the crawl graph: how likely it is to lead to a
	// match. Implementations satisfy [Scorer] and register here.
	KindURL Kind = "url"

	// KindDocument ranks a node of a parsed page against a property: how
	// strongly this element, attribute or text is that field. Implementations
	// satisfy wom.Matcher and register with internal/matcher, which is the
	// document node scorer under an older name.
	KindDocument Kind = "document"
)

// Kinds lists the node kinds that have a scorer registry, with where to find
// it. It exists so that "what can be scored, and by what" has one answer rather
// than being spread across two packages that do not mention each other.
func Kinds() map[Kind]string {
	return map[Kind]string{
		KindURL:      "internal/score",
		KindDocument: "internal/matcher",
	}
}

// String names the kind as configuration and documentation spell it.
func (k Kind) String() string { return string(k) }
