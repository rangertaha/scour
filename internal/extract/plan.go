// SPDX-License-Identifier: GPL-3.0-or-later

package extract

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/andybalholm/cascadia"
	"github.com/antchfx/xpath"

	"github.com/rangertaha/scour/internal/engine"
)

// itemPlan is one shape with every locator compiled.
type itemPlan struct {
	item  *engine.Item
	props []*propPlan
}

// propPlan is one property with its locators compiled and its aliases folded
// into the names it answers to.
type propPlan struct {
	prop *engine.Property

	css    []cascadia.Selector
	xpath  []*xpath.Expr
	regex  []*regexp.Regexp
	names  []string // the property's name and its aliases, lowercased
	nested []*propPlan

	// relation marks a plan synthesised from a relation block rather than
	// declared as a property. It contributes its fields and never the name
	// itself, because a relation may share a name with a property on purpose.
	relation bool
}

func planItem(item *engine.Item) (*itemPlan, error) {
	plan := &itemPlan{item: item}
	for _, p := range item.Properties {
		compiled, err := planProperty(item.Name, p)
		if err != nil {
			return nil, err
		}
		plan.props = append(plan.props, compiled)
	}

	// What each relation says about itself, planned as though the relation were
	// a property of that name with those children, so its values land in the
	// record under `publisher.role` the way `author.role` does.
	//
	// Nothing did this, so a relation's declared properties were extracted from
	// nowhere: the document accepted them, the entities step looked for them,
	// and the record never carried one. An edge that could say what it was is
	// the whole reason relations were given properties, and it had no way to
	// find out.
	//
	// The relation itself is not extracted here. Its far end comes from `self`
	// or from an item property, which the entities step resolves; this plans
	// only the children.
	for _, r := range item.Relations {
		if len(r.Properties) == 0 {
			continue
		}
		compiled, err := planProperty(item.Name, &engine.Property{
			Name:       r.Name,
			Type:       string(engine.TypeObject),
			Properties: r.Properties,
		})
		if err != nil {
			return nil, err
		}
		compiled.relation = true
		plan.props = append(plan.props, compiled)
	}

	return plan, nil
}

// planProperty compiles what a property was taught.
//
// Every failure here is a document that cannot work, and it is reported when
// the extractor is built rather than on whichever page first reaches the
// selector. A crawl that ran for an hour before finding out a selector was
// misspelt has wasted an hour and somebody's bandwidth.
func planProperty(item string, p *engine.Property) (*propPlan, error) {
	plan := &propPlan{prop: p, names: names(p)}

	for _, selector := range p.CSS {
		compiled, err := cascadia.Compile(selector)
		if err != nil {
			return nil, fmt.Errorf("extract: %s.%s: css %q: %w", item, p.Name, selector, err)
		}
		plan.css = append(plan.css, compiled)
	}
	for _, expr := range p.XPath {
		compiled, err := xpath.Compile(expr)
		if err != nil {
			return nil, fmt.Errorf("extract: %s.%s: xpath %q: %w", item, p.Name, expr, err)
		}
		plan.xpath = append(plan.xpath, compiled)
	}
	for _, pattern := range p.Regexes {
		compiled, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("extract: %s.%s: regex %q: %w", item, p.Name, pattern, err)
		}
		plan.regex = append(plan.regex, compiled)
	}

	for _, nested := range p.Properties {
		compiled, err := planProperty(item+"."+p.Name, nested)
		if err != nil {
			return nil, err
		}
		plan.nested = append(plan.nested, compiled)
	}
	return plan, nil
}

// names is what a property answers to: its own name and every alias, plus the
// spellings the same word takes in markup.
//
// `published_at` in a document is `publishedAt` in JSON-LD, `published-at` in a
// class and `article:published_time` in Open Graph. A crawler that matched only
// the exact string would find none of them, and asking every job to list four
// spellings of its own field names is asking them to do this by hand.
func names(p *engine.Property) []string {
	seen := map[string]bool{}
	var out []string

	add := func(name string) {
		for _, form := range spellings(name) {
			if form != "" && !seen[form] {
				seen[form] = true
				out = append(out, form)
			}
		}
	}

	add(p.Name)
	for _, alias := range p.Aliases {
		add(alias)
	}
	return out
}

// spellings folds one name into the forms markup writes it in.
func spellings(name string) []string {
	name = strings.TrimSpace(strings.ToLower(name))
	if name == "" {
		return nil
	}

	flat := strings.NewReplacer("_", "", "-", "", ":", "", " ", "").Replace(name)
	snake := strings.NewReplacer("-", "_", ":", "_", " ", "_").Replace(name)
	kebab := strings.NewReplacer("_", "-", ":", "-", " ", "-").Replace(name)

	return []string{name, flat, snake, kebab}
}

// answersTo reports whether a name from a page is one this property goes by.
func (p *propPlan) answersTo(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return false
	}

	// Namespaced properties are matched on the last segment too, so a property
	// called "published" finds `article:published_time` and `og:published`.
	candidates := []string{name}
	if i := strings.LastIndexAny(name, ":."); i >= 0 && i+1 < len(name) {
		candidates = append(candidates, name[i+1:])
	}

	for _, candidate := range candidates {
		for _, form := range spellings(candidate) {
			for _, want := range p.names {
				if form == want {
					return true
				}
			}
		}
	}
	return false
}

// missing is every required name at or beneath this property, dotted under the
// prefix, for a property that found nothing.
//
// The whole subtree, because a property that matched nothing is not a value and
// so has no nested value to carry its children's names up. Reporting only the
// property's own `required` meant an item with a required field two levels down
// said it was complete when the object above it was absent: `scour scrape
// --strict` exited 0 and the export wrote a blank column for a field the job
// had said it could not do without.
//
// A required property reports itself as well as its children, because both are
// true: the group is missing and so is everything in it.
func (p *propPlan) missing(prefix string) []string {
	var out []string
	if p.prop.Required {
		out = append(out, prefix)
	}
	for _, nested := range p.nested {
		out = append(out, nested.missing(prefix+"."+nested.prop.Name)...)
	}
	return out
}
