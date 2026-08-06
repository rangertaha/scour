// SPDX-License-Identifier: GPL-3.0-or-later

// Package extract turns a page into the items a job declared.
//
// This is where the job document stops being a document. Everything before it
// could be checked against fixtures; here the question is not whether the code
// is right but whether it found the title, and the answer is a number rather
// than a pass or a fail.
//
// # Four ways to find a value, tried in order
//
//  1. **CSS selectors**, which a person wrote or `scour train` induced.
//  2. **XPath**, for the cases CSS cannot express.
//  3. **Regexes**, over the page's text, for what is written rather than
//     marked up.
//  4. **Semantics**, from the property's name and aliases: Open Graph, JSON-LD,
//     microdata, schema.org itemprops, and the elements that mean something on
//     their own like `<title>` and `<time datetime>`.
//
// Taught beats guessed, which is the whole ordering, and it is why the regex
// comes third rather than last: a pattern in the document was written by
// somebody who had looked at the page, and semantics are a guess that is only
// usually right. A guess must never quietly win over an instruction, or
// correcting a wrong extraction would be impossible.
//
// # Every value says where it came from
//
// A value on its own does not tell you whether the locator will hold on the
// next page. [Value.From] carries the node it was read out of, which is what
// makes `scour try` worth running and what `scour train` compares against when
// it proposes a locator.
package extract

import (
	"fmt"
	"strings"

	"golang.org/x/net/html"

	"github.com/rangertaha/scour/internal/engine"
)

// Result is everything one page produced.
type Result struct {
	// URL is the page it came from, which every relative link is resolved
	// against and every value's provenance points at.
	URL string

	// Items are what was extracted, one per declared shape that produced
	// anything.
	Items []*Item

	// Links are the URLs the page points at, absolute and in document order.
	Links []string
}

// Item is one extracted shape.
type Item struct {
	// Name is the shape's name, as the job declared it.
	Name string

	// Values are what each property found, keyed by property name. A property
	// that found nothing is absent rather than empty, because "not there" and
	// "there and blank" are different facts about a page.
	Values map[string]*Value

	// Missing lists the required properties that found nothing. A job with a
	// required property that stopped matching has broken, and that is worth
	// saying rather than exporting a record with a hole in it.
	Missing []string
}

// Value is one extracted value and where it came from.
type Value struct {
	// Text is the value after every transform has run.
	Text string

	// Raw is what was found before the transforms, which is what a person
	// debugging a transform actually wants to see.
	Raw string

	// From describes the node it was read out of: "<h1 class=headline>",
	// "<meta property=og:title>". Not a selector, because it says what was
	// found rather than what would find it again.
	From string

	// How says which of the four ways found it, so a person can tell a locator
	// they wrote from a guess that happened to work.
	How string

	// Missing names the required fields of this value that were not found,
	// dotted. Collected by the item, so a required field is reported the way a
	// required property is.
	Missing []string

	// outside marks a nested value that came from the page's own metadata
	// rather than from inside its parent, so the parent can say so in the
	// provenance. Cleared once it has.
	outside bool

	// Nested holds the fields of an object property.
	Nested map[string]*Value
}

// How a value was found. Ordered as they are tried: taught first, guessed
// after, so a value's provenance says how much to trust it.
const (
	// ByCSS is a selector the document gave.
	ByCSS = "css"
	// ByXPath is an xpath the document gave.
	ByXPath = "xpath"
	// ByRegex is a pattern over the page's text.
	ByRegex = "regex"
	// BySemantics is the property's name or an alias, matched against what a
	// page says about itself. The only one of the four that was guessed.
	BySemantics = "semantics"
)

// Extractor reads one job's shapes out of pages.
//
// Built once per spec rather than per page: every selector is compiled here, so
// a job with a selector that is not one is refused at the start of a run rather
// than on whichever page happens to reach it.
type Extractor struct {
	spec  *engine.Spec
	items []*itemPlan
}

// New compiles a spec's locators.
func New(spec *engine.Spec) (*Extractor, error) {
	if spec == nil {
		return nil, fmt.Errorf("extract: no spec")
	}

	e := &Extractor{spec: spec}
	for _, item := range spec.Items {
		plan, err := planItem(item)
		if err != nil {
			return nil, err
		}
		e.items = append(e.items, plan)
	}
	return e, nil
}

// Spec is what this was built from, which a caller reporting on extraction
// needs in order to say which shape it was reporting on.
func (e *Extractor) Spec() *engine.Spec { return e.spec }

// Page reads every shape out of one page.
//
// The body is the decoded text, not the bytes: what encoding a page arrived in
// is the downloader's business and this one has enough to worry about.
func (e *Extractor) Page(url string, body []byte) (*Result, error) {
	doc, err := html.Parse(strings.NewReader(string(body)))
	if err != nil {
		// x/net/html does not fail on bad markup, which is the point of it, so
		// this is a read error rather than a parse error.
		return nil, fmt.Errorf("extract: %s: %w", url, err)
	}

	page := newPage(url, doc)

	result := &Result{URL: url, Links: page.links()}
	for _, plan := range e.items {
		if item := plan.extract(page); item != nil {
			result.Items = append(result.Items, item)
		}
	}
	return result, nil
}

// Item returns one extracted shape by name.
func (r *Result) Item(name string) (*Item, bool) {
	for _, item := range r.Items {
		if item.Name == name {
			return item, true
		}
	}
	return nil, false
}

// Get returns one property's value.
func (i *Item) Get(name string) (*Value, bool) {
	v, ok := i.Values[name]
	return v, ok
}

// Text returns one property's value as text, or "" if it found nothing. For
// the callers that want a value rather than a story about it.
func (i *Item) Text(name string) string {
	if v, ok := i.Values[name]; ok {
		return v.Text
	}
	return ""
}

// Complete reports whether every required property found something.
func (i *Item) Complete() bool { return len(i.Missing) == 0 }
