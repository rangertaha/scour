// SPDX-License-Identifier: GPL-3.0-or-later

package extract

import (
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/antchfx/htmlquery"
	"golang.org/x/net/html"

	"github.com/rangertaha/scour/internal/engine"
)

// extract reads one shape out of a page.
//
// An item with no values at all is not reported: a page that is not an article
// should not produce an empty article, and every downstream count would be
// wrong if it did.
func (plan *itemPlan) extract(p *page) *Item {
	item := &Item{Name: plan.item.Name, Values: map[string]*Value{}}

	for _, prop := range plan.props {
		value := prop.find(p, p.root)
		if value == nil {
			if prop.prop.Required {
				item.Missing = append(item.Missing, prop.prop.Name)
			}
			continue
		}
		// A relation's pseudo-property contributes its fields and never the
		// name itself.
		//
		// A relation may share a name with a property on purpose: the schema
		// says a relation with no `property` is asserted from an extracted
		// property of the same name, and the entities step reads exactly that.
		// So the two are one name by design, and letting the pseudo-property
		// win overwrote the real value with an empty one — the record's column
		// went blank, and the entities step then skipped the entity, every edge
		// to it and everything the page said about it, silently.
		if prop.relation {
			merge(item, prop.prop.Name, value)
			item.Missing = append(item.Missing, value.Missing...)
			continue
		}
		item.Missing = append(item.Missing, value.Missing...)
		item.Values[prop.prop.Name] = value
	}

	if len(item.Values) == 0 {
		return nil
	}
	return item
}

// merge adds a relation's fields to whatever is already under its name,
// keeping the value that was extracted for the name itself.
func merge(item *Item, name string, from *Value) {
	if len(from.Nested) == 0 {
		return
	}

	into, ok := item.Values[name]
	if !ok {
		// Nothing was extracted under the name, so the fields stand alone: the
		// relation's far end comes from `self` in that case, and the step
		// resolves it without needing a value here.
		item.Values[name] = from
		return
	}
	if into.Nested == nil {
		into.Nested = map[string]*Value{}
	}
	for field, value := range from.Nested {
		// What the item declared wins, because a property is a claim about the
		// page and a relation's field is a claim about the edge.
		if _, taken := into.Nested[field]; !taken {
			into.Nested[field] = value
		}
	}
}

// find looks for one property's value, taught locators first.
func (p *propPlan) find(page *page, within *html.Node) *Value {
	for _, selector := range p.css {
		if node := selector.MatchFirst(within); node != nil {
			return p.value(page, nodeValue(node), describe(node), ByCSS, node)
		}
	}

	for _, expr := range p.xpath {
		if node := htmlquery.QuerySelector(within, expr); node != nil {
			return p.value(page, nodeValue(node), describe(node), ByXPath, node)
		}
	}

	for _, pattern := range p.regex {
		if match := pattern.FindStringSubmatch(page.text); match != nil {
			// The first capturing group if there is one, because a pattern
			// with a group was written to say which part is the value.
			found := match[0]
			if len(match) > 1 {
				found = match[1]
			}
			return p.value(page, found, "page text", ByRegex, nil)
		}
	}

	// Guessed, and therefore last. Everything above it was written down by
	// somebody who had looked at the page.
	if found := p.semantic(page, within); found != nil {
		return found
	}

	// A property declared purely to group fields may still have fields that
	// find something, and the fields are the point of it.
	//
	// Only when it declares no locators of its own. A property that said where
	// to look and did not find it there has not been found, whatever its
	// children matched: allowing the children to stand in for it made
	// `required` unenforceable on any property with nested fields, because the
	// parent was never recorded as missing, and a site that changed its markup
	// read as a healthy crawl while a child matched something unrelated
	// elsewhere on the page. It also let a group with nothing in it overwrite a
	// real value of the same name.
	//
	// A relation's pseudo-property has no locators by construction, since its
	// far end comes from `self`, so this is the case it needs and the case that
	// is safe.
	if len(p.nested) > 0 && !p.located() {
		return p.value(page, "", "", BySemantics, nil)
	}
	return nil
}

// located reports whether this property says where to look.
//
// A property with a locator is a claim about the page; one without is a name
// and a shape. The difference decides whether its fields may stand in for it.
func (p *propPlan) located() bool {
	return len(p.css) > 0 || len(p.xpath) > 0 || len(p.regex) > 0
}

// semantic looks for what a page says about itself, under any of the names
// this property answers to.
//
// The order is by how deliberate the claim is. Open Graph and JSON-LD are a
// publisher telling machines what the page is; microdata is the same; an
// element carrying the name as a class is a guess about somebody's stylesheet.
func (p *propPlan) semantic(page *page, within *html.Node) *Value {
	// Inside a parent, what the parent contains wins over what the page says
	// about itself.
	//
	// These two were tried last, after the page-global metadata maps, so a
	// nested field escaped its parent whenever the page happened to name the
	// same thing: an article whose JSON-LD carried publisher.name returned "The
	// Chronicle" for `author.name` while "Alex Doe" sat inside the matched
	// byline, and it was not even marked as having come from outside, because
	// that marking is gated on the parent having had no node. The guarantee
	// this function's own callers state is that a field is looked for inside
	// its parent; it now is.
	scoped := within != nil && within != page.root
	if scoped {
		if node := p.byWellKnownElement(within); node != nil {
			return p.value(page, nodeValue(node), describe(node), BySemantics, node)
		}
		if node := p.byClassOrID(within); node != nil {
			return p.value(page, nodeValue(node), describe(node), BySemantics, node)
		}
	}

	// Sorted, because these are maps and a page that says the same thing under
	// two names would otherwise be read differently between runs: the value
	// could change, and the provenance certainly did. Two runs over one corpus
	// producing different records is the property every other part of this
	// crawler is careful to keep.
	// The page's own metadata. Inside a parent this is a statement about the
	// page rather than about the parent, so it is marked, and the caller says
	// so in the provenance.
	for _, one := range []struct {
		from map[string]string
		how  string
	}{
		{page.meta, "<meta "},
		{page.linked, "json-ld "},
		{page.micro, "<itemprop "},
	} {
		for _, name := range sorted(one.from) {
			if !p.answersTo(name) {
				continue
			}
			where := one.how + name
			if one.how != "json-ld " {
				where += ">"
			}
			found := p.value(page, one.from[name], where, BySemantics, nil)
			if found != nil {
				found.outside = scoped
			}
			return found
		}
	}

	if !scoped {
		if node := p.byWellKnownElement(within); node != nil {
			return p.value(page, nodeValue(node), describe(node), BySemantics, node)
		}
		if node := p.byClassOrID(within); node != nil {
			return p.value(page, nodeValue(node), describe(node), BySemantics, node)
		}
	}
	return nil
}

// byWellKnownElement covers the elements that mean something without anybody
// having said so: a page's `<title>`, its `<h1>`, its `<time>`.
//
// Inside the article first, where there is one. Nearly every page has a second
// `<h1>` in its masthead, and taking the first one in document order finds the
// name of the site rather than the name of the story. That was not a
// hypothetical: induction learned it, froze it into a selector, and the test
// that caught it was about the selector.
func (p *propPlan) byWellKnownElement(within *html.Node) *html.Node {
	var tags []string

	switch {
	case p.answersTo("title") || p.answersTo("headline"):
		tags = []string{"h1", "title"}
	case p.answersTo("published") || p.answersTo("date") || p.answersTo("pubdate"):
		tags = []string{"time"}
	case p.answersTo("body") || p.answersTo("content") || p.answersTo("articlebody"):
		tags = []string{"article", "main"}
	default:
		return nil
	}

	for _, container := range []string{"article", "main"} {
		inside := firstElement(within, container)
		if inside == nil {
			continue
		}
		for _, tag := range tags {
			if tag == container {
				continue
			}
			if node := firstElement(inside, tag); node != nil {
				return node
			}
		}
	}

	for _, tag := range tags {
		if node := firstElement(within, tag); node != nil {
			return node
		}
	}
	return nil
}

// byClassOrID is the last guess: an element whose class or id is one of the
// names this property goes by.
func (p *propPlan) byClassOrID(within *html.Node) *html.Node {
	var found *html.Node

	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if found != nil {
			return
		}
		if n.Type == html.ElementNode {
			for _, key := range []string{"id", "class"} {
				for _, token := range strings.Fields(attr(n, key)) {
					if p.answersTo(token) {
						found = n
						return
					}
				}
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(within)
	return found
}

// sorted lists a map's keys in a fixed order.
func sorted(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for name := range m {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func firstElement(within *html.Node, tag string) *html.Node {
	var found *html.Node

	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if found != nil {
			return
		}
		if n.Type == html.ElementNode && n.Data == tag {
			found = n
			return
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(within)
	return found
}

// value applies the transforms and records where the value came from.
func (p *propPlan) value(page *page, raw, from, how string, node *html.Node) *Value {
	raw = strings.TrimSpace(raw)
	if raw == "" && len(p.nested) == 0 {
		// Found the node and it held nothing, which is not a value. Saying
		// otherwise would make an empty element look like a successful
		// extraction, and every fill rate a lie.
		return nil
	}

	v := &Value{Raw: raw, From: from, How: how}
	v.Text = transform(raw, p.prop, page.url)

	// An object property's fields are looked for inside the node that produced
	// it, so an author's name comes from the byline rather than from whichever
	// name appears first on the page.
	//
	// A value that came from a `<meta>`, from JSON-LD or from an `itemprop`
	// has no node, and the fields were simply left empty: measured over the
	// corpus, `author` filled on 60% of pages and `author.name` on 40%, the
	// difference being entirely pages that describe their author in metadata.
	// So the fields fall back to the whole document, which is the weaker
	// inference and is marked as one.
	//
	// Weaker because it is the "name from anywhere" problem the node exists to
	// avoid: on a page whose masthead carries an itemprop of its own, this
	// finds the site rather than the person. It is done anyway because an
	// empty field is not safer than a wrong one, it is only quieter, and
	// [Value.How] says which of the two you have.
	if len(p.nested) > 0 {
		within, how := node, ""
		if within == nil {
			within, how = page.root, BySemantics
		}

		v.Nested = map[string]*Value{}
		for _, nested := range p.nested {
			inner := nested.find(page, within)
			if inner == nil {
				// A required field that was not found is reported the way a
				// required property is, under its dotted name. Only the top
				// level was collected, so an item with a missing required
				// field said it was complete while the fill-rate report,
				// counting the same pages, said the field was missing on all
				// of them.
				if nested.prop.Required {
					v.Missing = append(v.Missing, p.prop.Name+"."+nested.prop.Name)
				}
				continue
			}
			if how != "" || inner.outside {
				// Found outside the parent, so it is a guess about the page
				// rather than a reading of the parent, whatever found it. The
				// second case is a field the parent DID have a node for, whose
				// value still came from the page's own metadata: that was
				// unmarked, so a caller could not tell it apart from a reading
				// of the parent.
				inner.How = BySemantics
				inner.From = inner.From + " (outside " + p.prop.Name + ")"
				inner.outside = false
			}
			v.Nested[nested.prop.Name] = inner
		}
	}

	// Nothing of its own and nothing beneath it is not a value, for the reason
	// the empty check above gives: saying otherwise makes an absent group look
	// like a successful extraction.
	if raw == "" && len(v.Nested) == 0 {
		return nil
	}
	return v
}

// transform applies a property's transforms, in the order it listed them.
func transform(value string, prop *engine.Property, base string) string {
	for _, name := range prop.Transforms {
		switch name {
		case engine.TransformText:
			value = collapse(value)
		case engine.TransformTrim:
			value = strings.TrimSpace(value)
		case engine.TransformLower:
			value = strings.ToLower(value)
		case engine.TransformUpper:
			value = strings.ToUpper(value)
		case engine.TransformNormaliseSpace:
			value = collapse(value)
		case engine.TransformAbsURL:
			value = resolve(base, value)
		case engine.TransformDatetime:
			if when, ok := parseTime(value); ok {
				value = when
			}
		}
	}
	return value
}

// collapse reduces runs of whitespace to one space, which is what "the text of
// this element" means once markup indentation is out of it.
func collapse(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// resolve makes a URL absolute against the page it was found on. A link that
// cannot be resolved comes back empty rather than as itself, because a relative
// path in a record is a value nothing downstream can use.
func resolve(base, href string) string {
	href = strings.TrimSpace(href)
	if href == "" || strings.HasPrefix(href, "#") {
		return ""
	}

	from, err := url.Parse(base)
	if err != nil {
		return ""
	}
	link, err := from.Parse(href)
	if err != nil {
		return ""
	}
	switch link.Scheme {
	case "http", "https":
	default:
		return ""
	}
	link.Fragment = ""
	return link.String()
}

func formatFloat(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}
