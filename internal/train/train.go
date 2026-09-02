// SPDX-License-Identifier: GPL-3.0-or-later

// Package train works out how to find each property, from the pages already in
// the cache.
//
// # Why a locator and not a model
//
// The result is a CSS selector written into the job document, as text. Not a
// model file, not a row in a database. Induction is a guess, and a guess should
// be readable: `css = [".article-body h1"]` is something a person can look at,
// disagree with, correct and commit. A weights file is something they can only
// retrain.
//
// # A correction is never overwritten
//
// This is the rule that makes the loop converge instead of going in circles.
// What this writes is marked, and only what is marked is ever replaced. A
// person who corrects a locator deletes the marker, and from then on the
// document is right and training leaves it alone.
//
// # It trains on the corpus, not on the web
//
// Every page it looks at is one already fetched. Training is therefore free,
// repeatable and offline: the same corpus produces the same locators, which is
// what makes a change to induction measurable rather than merely different.
package train

import (
	"fmt"
	"sort"
	"strings"

	"golang.org/x/net/html"

	"github.com/rangertaha/scour/internal/engine"
	"github.com/rangertaha/scour/internal/extract"
)

// Mark is what identifies an induced locator, and what is matched.
//
// It carries no command name on purpose. The marker is written into somebody's
// document and stays there for years, so anything in it that can go out of date
// eventually does: this said "induced by scour job train" until that command became
// `scour job train`, at which point every document in the world carried an
// instruction that no longer worked.
//
// Matching a prefix rather than the whole sentence is what makes that a
// one-time problem. The markers already written still start with this, so they
// are still recognised, and the next rename cannot reach it either.
const Mark = "# induced by scour"

// Marker is written beside an induced locator.
//
// It is the whole of how a guess is told from an instruction. Deliberately a
// sentence rather than a token: the person who finds it in a diff should not
// have to look anything up.
const Marker = Mark + "; delete this comment to keep your own"

// Proposal is one locator this would write.
type Proposal struct {
	// Item and Property say what it is for.
	Item     string
	Property string

	// Selector is the CSS selector proposed.
	Selector string

	// Pages is how many of the corpus's pages it worked on, and Total how many
	// were looked at. A locator that works on three pages out of two hundred
	// is not a locator, and the ratio is what says so.
	Pages int
	Total int

	// Example is a value it produced, so a person can see what they are
	// agreeing to rather than only where it came from.
	Example string

	// Replaces is what the property has now, if anything.
	Replaces []string

	// Kept is true when the property already has a locator that was not
	// induced, which is a correction and is left alone.
	Kept bool
}

// Rate is how often the proposal worked, between 0 and 1.
func (p Proposal) Rate() float64 {
	if p.Total == 0 {
		return 0
	}
	return float64(p.Pages) / float64(p.Total)
}

func (p Proposal) String() string {
	if p.Kept {
		return fmt.Sprintf("%s.%s  kept %s", p.Item, p.Property, strings.Join(p.Replaces, ", "))
	}
	return fmt.Sprintf("%s.%s  %s  %d/%d pages", p.Item, p.Property, p.Selector, p.Pages, p.Total)
}

// Page is one document from the corpus.
type Page struct {
	// URL is where it came from, which every relative link is resolved
	// against.
	URL string

	// Body is the decoded text.
	Body []byte
}

// Options are how induction is run.
type Options struct {
	// Least is the fraction of pages a locator has to work on to be proposed.
	// Below it, the property is left alone: a locator that works on three
	// pages out of two hundred is worse than none, because it looks like an
	// answer.
	Least float64

	// Replace allows an induced locator to be replaced by a better one. A
	// locator a person wrote is never replaced whatever this says.
	Replace bool
}

// DefaultLeast is the fraction of pages a locator must work on.
const DefaultLeast = 0.6

// Learn proposes a locator for every property that needs one.
func Learn(job *engine.Job, pages []Page, opts Options) ([]Proposal, error) {
	if job == nil {
		return nil, fmt.Errorf("train: no job")
	}
	if len(pages) == 0 {
		return nil, fmt.Errorf("train: no pages in the cache to learn from")
	}
	if opts.Least <= 0 {
		opts.Least = DefaultLeast
	}

	parsed := make([]*document, 0, len(pages))
	for _, page := range pages {
		root, err := html.Parse(strings.NewReader(string(page.Body)))
		if err != nil {
			continue
		}
		parsed = append(parsed, &document{url: page.URL, root: root})
	}
	if len(parsed) == 0 {
		return nil, fmt.Errorf("train: none of the %d pages could be read", len(pages))
	}

	// What extraction finds now is the target: a property with an example is
	// taught by the example, and one without is taught by whatever the
	// semantic pass already finds. Either way the locator being induced is a
	// way to keep finding it that does not depend on the page saying so.
	reader, err := extract.New(job.Spec())
	if err != nil {
		return nil, err
	}

	var out []Proposal
	for _, item := range job.Items {
		for _, prop := range item.Properties {
			proposal := learnOne(reader, item, prop, parsed, opts)
			if proposal != nil {
				out = append(out, *proposal)
			}
		}
	}
	return out, nil
}

// written is the locators a person put on a property, if any.
//
// In the order extraction tries them, so what comes back is what would actually
// be used and what an induced CSS selector would therefore displace.
func written(prop *engine.Property) []string {
	switch {
	case len(prop.CSS) > 0:
		return prop.CSS
	case len(prop.XPath) > 0:
		return prop.XPath
	case len(prop.Regexes) > 0:
		return prop.Regexes
	}
	return nil
}

func learnOne(reader *extract.Extractor, item *engine.Item, prop *engine.Property, pages []*document, opts Options) *Proposal {
	// A locator somebody wrote is a correction, and a correction is never
	// overwritten. That is the rule that makes this converge.
	//
	// Any locator, not only a CSS one. Extraction tries CSS before XPath before
	// a regex, so writing an induced `css` beside a hand-written `xpath` does
	// not sit next to it - it replaces it, silently, and the XPath was written
	// precisely because CSS could not express what the person meant. It was not
	// even reported as kept, because nothing looked.
	if hand := written(prop); len(hand) > 0 && !prop.Induced {
		return &Proposal{Item: item.Name, Property: prop.Name, Replaces: hand, Kept: true}
	}
	if len(prop.CSS) > 0 && !opts.Replace {
		return &Proposal{Item: item.Name, Property: prop.Name, Replaces: prop.CSS, Kept: true}
	}

	// What each page should produce for this property.
	targets := map[*document]string{}
	for _, page := range pages {
		if want := target(reader, item.Name, prop, page); want != "" {
			targets[page] = want
		}
	}
	if len(targets) == 0 {
		return nil
	}

	// Every selector that produces the target on any page, scored by how many
	// pages it produces it on. A selector that works everywhere beats one that
	// works on the page it was learnt from, which is the whole of what makes
	// this generalise.
	scores := map[string]int{}
	examples := map[string]string{}
	depths := map[string]int{}

	for page, want := range targets {
		for candidate, depth := range candidates(page.root, want) {
			if existing, seen := depths[candidate]; !seen || depth > existing {
				depths[candidate] = depth
			}
			if _, counted := scores[candidate]; !counted {
				scores[candidate] = 0
			}
		}
	}

	for selector := range scores {
		for page, want := range targets {
			// Collapsed on both sides, for the reason [collapse] gives: what
			// extraction found keeps the page's own spacing and what a node
			// reads as here does not, so a headline a template wrapped never
			// matched itself. Both this comparison and the one in [holding]
			// had it, and fixing one alone changed nothing.
			if got, ok := valueOf(page.root, selector); ok && collapse(got) == collapse(want) {
				scores[selector]++
				if examples[selector] == "" {
					examples[selector] = got
				}
			}
		}
	}

	best, count := "", 0
	for selector, score := range scores {
		switch {
		case score > count:
			best, count = selector, score
		case score == count && score > 0 && better(selector, best, depths):
			best = selector
		}
	}

	if best == "" || float64(count)/float64(len(pages)) < opts.Least {
		return nil
	}
	return &Proposal{
		Item:     item.Name,
		Property: prop.Name,
		Selector: best,
		Pages:    count,
		Total:    len(pages),
		Example:  examples[best],
		Replaces: prop.CSS,
	}
}

// target is what this property should find on this page.
//
// An example the job gave, if one is on the page, and whatever extraction
// already finds otherwise. The first is a person teaching by answer; the second
// is the semantic pass, which is right often enough to be worth freezing into a
// selector that does not depend on the page continuing to say so.
func target(reader *extract.Extractor, item string, prop *engine.Property, page *document) string {
	text := textOf(page.root)
	for _, example := range prop.Examples {
		if strings.Contains(text, example) {
			return example
		}
	}

	result, err := reader.Page(page.url, page.body())
	if err != nil {
		return ""
	}
	found, ok := result.Item(item)
	if !ok {
		return ""
	}
	value, ok := found.Get(prop.Name)
	if !ok {
		return ""
	}
	return value.Raw
}

// document is one parsed page, and its source when something wants it again.
type document struct {
	url  string
	root *html.Node
	raw  []byte
}

func (d *document) body() []byte {
	if d.raw == nil {
		var b strings.Builder
		_ = html.Render(&b, d.root)
		d.raw = []byte(b.String())
	}
	return d.raw
}

// better breaks a tie between two selectors that work on the same pages.
//
// By how much the selector says, not by how short it is. A bare `div` and
// `.author-name` can both find a byline on every page in a corpus, and the
// first one keeps working until the page gains a second div. Shortest-wins
// picked the div, which is how this rule came to be written down.
func better(a, b string, depths map[string]int) bool {
	if rank(a) != rank(b) {
		return rank(a) > rank(b)
	}
	// Then the one that points at the value rather than at something
	// containing it.
	if depths[a] != depths[b] {
		return depths[a] > depths[b]
	}
	if strings.Count(a, " ") != strings.Count(b, " ") {
		return strings.Count(a, " ") < strings.Count(b, " ")
	}
	if len(a) != len(b) {
		return len(a) < len(b)
	}
	// Alphabetical last, so that this is a total order and not merely a
	// preference. Two candidates equal on every rule above are ordinary:
	// `<span class="author byline">` offers `.author` and `.byline`, same node,
	// same rank, same length. Without this the winner was whichever Go's
	// randomised map iteration reached first, so training the same corpus twice
	// produced a different document each time and a diff on every run, against
	// the promise this package makes that induction is repeatable.
	return a < b
}

// rank is how much a selector commits to, highest first.
func rank(selector string) int {
	switch {
	case strings.Contains(selector, "["):
		// What the page says the element is, which is a claim the publisher
		// made on purpose.
		return 5
	case strings.Contains(selector, "#"):
		return 4
	case strings.Contains(selector, "."):
		return 3
	case strings.Contains(selector, " "):
		return 2
	default:
		// A bare tag, which is only right when there is one of them.
		return 1
	}
}

// Report renders proposals the way `scour job train` prints them.
func Report(proposals []Proposal) string {
	sort.Slice(proposals, func(i, j int) bool {
		if proposals[i].Item != proposals[j].Item {
			return proposals[i].Item < proposals[j].Item
		}
		return proposals[i].Property < proposals[j].Property
	})

	var b strings.Builder
	for _, p := range proposals {
		b.WriteString(p.String())
		b.WriteString("\n")
	}
	return b.String()
}
