// SPDX-License-Identifier: GPL-3.0-or-later

package extract

import (
	"net/url"
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
		item.Values[prop.prop.Name] = value
	}

	if len(item.Values) == 0 {
		return nil
	}
	return item
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
	return p.semantic(page, within)
}

// semantic looks for what a page says about itself, under any of the names
// this property answers to.
//
// The order is by how deliberate the claim is. Open Graph and JSON-LD are a
// publisher telling machines what the page is; microdata is the same; an
// element carrying the name as a class is a guess about somebody's stylesheet.
func (p *propPlan) semantic(page *page, within *html.Node) *Value {
	for name, content := range page.meta {
		if p.answersTo(name) {
			return p.value(page, content, "<meta "+name+">", BySemantics, nil)
		}
	}
	for name, content := range page.linked {
		if p.answersTo(name) {
			return p.value(page, content, "json-ld "+name, BySemantics, nil)
		}
	}
	for name, content := range page.micro {
		if p.answersTo(name) {
			return p.value(page, content, "<itemprop "+name+">", BySemantics, nil)
		}
	}

	if node := p.byWellKnownElement(within); node != nil {
		return p.value(page, nodeValue(node), describe(node), BySemantics, node)
	}
	if node := p.byClassOrID(within); node != nil {
		return p.value(page, nodeValue(node), describe(node), BySemantics, node)
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
	if len(p.nested) > 0 && node != nil {
		v.Nested = map[string]*Value{}
		for _, nested := range p.nested {
			if inner := nested.find(page, node); inner != nil {
				v.Nested[nested.prop.Name] = inner
			}
		}
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
