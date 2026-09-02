// SPDX-License-Identifier: GPL-3.0-or-later

package extract

import (
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/rangertaha/scour/internal/engine"
)

// Sample is one page of a corpus, ready to be measured against.
//
// Not called a page: this package already uses `page` for a parsed document and
// [Extractor.Page] for reading one, and a third meaning of the word would make
// every sentence about it ambiguous.
type Sample struct {
	// URL is where it came from. Relative links and the absurl transform are
	// resolved against it, so a corpus with placeholder URLs measures a
	// different thing from the crawl it stands in for.
	URL string

	// Body is the decoded text, not the bytes the server sent. Whoever loads
	// the corpus decodes it with internal/decode, which is what the downloader
	// and the spider both do, so a page in windows-1251 is measured as the
	// spider would see it rather than as mojibake.
	Body []byte
}

// Report is what a corpus said about a spec.
//
// Fill rates are the only honest way to talk about extraction. Everything
// earlier in a job can be checked against a fixture and comes back pass or
// fail; whether a selector finds the headline on a site nobody has seen is a
// number, and a number needs a corpus and a denominator written down beside it.
type Report struct {
	// Pages is how many samples were run, and the denominator of every rate
	// here. Pages that produced nothing at all are in it: the question a fill
	// rate answers is how often a fetched page yields a value, not how often
	// the pages that already worked worked.
	Pages int

	// Items are one entry per declared shape, in the order the document
	// declares them.
	Items []*ItemRates

	// Unreadable is how many samples could not be parsed at all. They are in
	// Pages, because a page the parser refuses is a page the crawl will get
	// nothing from and a rate that left it out would read better than the
	// corpus does.
	//
	// Counted rather than fatal. x/net/html does not fail on bad markup, which
	// is the point of it, but it does refuse a document nested more than 512
	// elements deep - a quoted forum thread reaches that - and one such page
	// in a corpus used to return no report at all. A report is a measurement
	// over a corpus, and losing the measurement because one sample was strange
	// is the opposite of what it is for.
	Unreadable int
}

// ItemRates is one shape's results over the corpus.
type ItemRates struct {
	// Name is the shape's name, as the job declared it.
	Name string

	// Found is how many pages produced this item at all. An item appears only
	// when at least one of its properties found something, so this is the
	// count of pages that looked like the declared shape.
	Found int

	// Complete is how many pages produced it with every required property
	// filled. The gap between this and Found is the count of records a job
	// would be exporting with a hole in them.
	Complete int

	// Missing is the total number of required properties that found nothing,
	// summed over the pages that produced the item. Taken from [Item.Missing],
	// so it counts what extraction itself calls missing rather than a second
	// opinion about it.
	Missing int

	// Properties are one entry per declared property, in declaration order.
	// An object property's fields follow it, named "author.name".
	Properties []*PropertyRates

	pages int
}

// PropertyRates is one property's results over the corpus.
type PropertyRates struct {
	// Name is the property's name, dotted for the fields of an object.
	Name string

	// Required is what the document said, which decides whether an absence is
	// a hole in a record or a field that was optional anyway.
	Required bool

	// Found is how many pages produced a value.
	Found int

	// Empty is how many of those values were empty once the transforms had
	// run. A found value that transforms emptied is worse than a miss, because
	// nothing about the record says the locator worked and the transform did
	// not: absurl on a fragment link is the usual way it happens.
	Empty int

	// Missing is how many pages produced the item but not this property, while
	// the document called it required.
	Missing int

	// By counts the values by which of the four ways found them, keyed by
	// [ByCSS], [ByXPath], [ByRegex] and [BySemantics].
	//
	// This breakdown is the point of the whole report. A property found by
	// semantics on nine pages in ten and one found by a taught selector on
	// nine in ten are the same number describing two different situations: the
	// taught one breaks loudly when the site changes its markup, and the
	// guessed one drifts onto whatever else on the page answers to the name,
	// silently, while the fill rate stays at ninety per cent.
	By map[string]int

	pages int
}

// hows is the fixed order the breakdown is reported in: the order the four ways
// are tried, taught before guessed, so a row reads left to right from most
// trustworthy to least.
var hows = []string{ByCSS, ByXPath, ByRegex, BySemantics}

// Rates runs a spec over a corpus and counts what it found.
//
// It returns an error rather than an empty report when the spec will not
// compile, because a job with a selector that is not one would otherwise
// measure as an extraction regression rather than as the typo it is.
func Rates(spec *engine.Spec, samples []Sample) (*Report, error) {
	e, err := New(spec)
	if err != nil {
		return nil, err
	}

	report := &Report{Pages: len(samples)}
	rows := map[string]map[string]*PropertyRates{}

	// Every declared shape gets a row whether or not any page produced it. A
	// property that was never found has to appear as a zero: a report that
	// listed only what worked would look perfect on a corpus where nothing did.
	for _, item := range spec.Items {
		rates := &ItemRates{Name: item.Name, pages: len(samples)}
		index := map[string]*PropertyRates{}
		rates.Properties = declared(item.Properties, "", len(samples), index)

		report.Items = append(report.Items, rates)
		rows[item.Name] = index
	}

	for _, sample := range samples {
		result, err := e.Page(sample.URL, sample.Body)
		if err != nil {
			// Counted, not fatal. See [Report.Unreadable].
			report.Unreadable++
			continue
		}

		for i, item := range spec.Items {
			found, ok := result.Item(item.Name)
			if !ok {
				continue
			}

			rates := report.Items[i]
			rates.Found++
			rates.Missing += len(found.Missing)
			if found.Complete() {
				rates.Complete++
			}
			count(rows[item.Name], item.Properties, found.Values, "")
		}
	}
	return report, nil
}

// declared builds the empty rows, one per property, object fields included.
func declared(props []*engine.Property, prefix string, pages int, index map[string]*PropertyRates) []*PropertyRates {
	var out []*PropertyRates

	for _, prop := range props {
		row := &PropertyRates{
			Name:     prefix + prop.Name,
			Required: prop.Required,
			By:       map[string]int{},
			pages:    pages,
		}
		index[row.Name] = row
		out = append(out, row)
		out = append(out, declared(prop.Properties, row.Name+".", pages, index)...)
	}
	return out
}

// countMissing marks every required row beneath an absent property.
//
// The mirror of [propPlan.missing], which is what put those names in the item's
// own total. The two have to agree or the report says a field is missing
// without saying which.
func countMissing(index map[string]*PropertyRates, props []*engine.Property, prefix string) {
	for _, prop := range props {
		if row := index[prefix+prop.Name]; row != nil && prop.Required {
			row.Missing++
		}
		countMissing(index, prop.Properties, prefix+prop.Name+".")
	}
}

// count folds one page's values into the rows.
//
// Only pages that produced the item are counted here. A page that produced
// nothing at all is a page that was not the declared shape, most often because
// it was a listing or an error, and counting its required properties as missing
// would report a crawl's page mix as an extraction fault.
func count(index map[string]*PropertyRates, props []*engine.Property, values map[string]*Value, prefix string) {
	for _, prop := range props {
		row := index[prefix+prop.Name]
		if row == nil {
			continue
		}

		value := values[prop.Name]
		if value == nil {
			if prop.Required {
				row.Missing++
			}
			// Everything required beneath it too, because an absent group
			// takes its whole subtree with it. The item's own total counts
			// those names - propPlan.missing reports them - so leaving the
			// rows at zero made the report contradict itself: the header said
			// one required property was missing and every row said none was,
			// naming a count without naming the field.
			countMissing(index, prop.Properties, prefix+prop.Name+".")
			continue
		}

		row.Found++
		row.By[value.How]++
		if value.Text == "" {
			row.Empty++
		}
		count(index, prop.Properties, value.Nested, row.Name+".")
	}
}

// Item returns one shape's results by name.
func (r *Report) Item(name string) (*ItemRates, bool) {
	for _, item := range r.Items {
		if item.Name == name {
			return item, true
		}
	}
	return nil, false
}

// Overall is the fraction of every opportunity that produced a value: one
// opportunity per declared property per page, object fields included.
//
// One number for the whole corpus, which is what a ratchet in a test can hold.
// It is deliberately unweighted: a job's least reliable property drags it down
// as hard as its title, because a record is only as good as the field somebody
// downstream is about to filter on.
func (r *Report) Overall() float64 {
	var found, chances int
	for _, item := range r.Items {
		for _, prop := range item.Properties {
			found += prop.Found
			chances += r.Pages
		}
	}
	if chances == 0 {
		return 0
	}
	return float64(found) / float64(chances)
}

// Rate is the fraction of pages that produced this item.
func (i *ItemRates) Rate() float64 { return ratio(i.Found, i.pages) }

// Property returns one property's results by name, dotted for the fields of an
// object: "author.name".
func (i *ItemRates) Property(name string) (*PropertyRates, bool) {
	for _, prop := range i.Properties {
		if prop.Name == name {
			return prop, true
		}
	}
	return nil, false
}

// Rate is the fraction of the corpus's pages that produced a value.
func (p *PropertyRates) Rate() float64 { return ratio(p.Found, p.pages) }

// Guessed is the fraction of the values that semantics found rather than a
// locator somebody wrote.
//
// Reported on its own because it is the number that says how much of a fill
// rate rests on a guess, and therefore how much of it would quietly change if a
// site changed its markup.
func (p *PropertyRates) Guessed() float64 { return ratio(p.By[BySemantics], p.Found) }

func ratio(part, whole int) float64 {
	if whole == 0 {
		return 0
	}
	return float64(part) / float64(whole)
}

// String renders the report as a table.
//
// Rows are in the order the document declares them rather than sorted by name
// or by rate: it is as deterministic, two runs produce identical text, and it
// keeps a property next to the one it was written beside. Sorting by rate would
// reorder the table every time extraction changed, which is exactly when two
// reports need comparing.
func (r *Report) String() string {
	var b strings.Builder

	_, _ = fmt.Fprintf(&b, "fill rates over %d pages\n", r.Pages)
	if r.Unreadable > 0 {
		// Said, because a rate computed over a corpus that partly would not
		// parse is a different number from one over a corpus that did, and
		// silently dropping the difference reads as full coverage.
		_, _ = fmt.Fprintf(&b, "%d of them could not be parsed and found nothing\n", r.Unreadable)
	}

	for _, item := range r.Items {
		_, _ = fmt.Fprintf(&b, "\nitem %q: on %d pages (%s), complete on %d, %d required properties missing\n\n",
			item.Name, item.Found, percent(item.Rate()), item.Complete, item.Missing)

		w := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintf(w, "  property\tfound\trate\t%s\tempty\tmissing\n", strings.Join(hows, "\t"))

		for _, prop := range item.Properties {
			name := prop.Name
			if prop.Required {
				name += "*"
			}

			_, _ = fmt.Fprintf(w, "  %s\t%d\t%s", name, prop.Found, percent(prop.Rate()))
			for _, how := range hows {
				_, _ = fmt.Fprintf(w, "\t%d", prop.By[how])
			}
			_, _ = fmt.Fprintf(w, "\t%d\t%d\n", prop.Empty, prop.Missing)
		}
		_ = w.Flush()
	}

	_, _ = fmt.Fprintf(&b, "\noverall %s. * is a required property.\n", percent(r.Overall()))
	return b.String()
}

func percent(rate float64) string { return fmt.Sprintf("%.1f%%", rate*100) }
