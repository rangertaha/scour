// SPDX-License-Identifier: GPL-3.0-or-later

package extract_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rangertaha/scour/internal/decode"
	"github.com/rangertaha/scour/internal/engine"
	"github.com/rangertaha/scour/internal/extract"
)

const corpusDir = "testdata/corpus"

// corpusURL is what a page of the corpus would have been fetched from. The
// canonical links in the pages agree with it, so a page that is measured is
// measured against the address it claims to have.
const corpusURL = "https://corpus.example/pages/"

// corpus loads the hand-written pages, decoded the way the spider decodes them.
//
// Through internal/decode rather than by reading the bytes as UTF-8, because
// one of the pages is windows-1251 and the whole point of keeping it is that it
// is measured as the crawler would see it. A corpus read as UTF-8 would score
// that page on mojibake and nobody would notice.
func corpus(t *testing.T) []extract.Sample {
	t.Helper()

	entries, err := os.ReadDir(corpusDir)
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}

	var samples []extract.Sample
	for _, entry := range entries {
		name := entry.Name()
		if filepath.Ext(name) != ".html" {
			continue
		}

		body, err := os.ReadFile(filepath.Join(corpusDir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}

		// No content type, so the encoding comes from the document, which is
		// the harder case and the one the corpus is for.
		text, err := decode.Bytes(body, "")
		if err != nil {
			t.Fatalf("decode %s: %v", name, err)
		}

		samples = append(samples, extract.Sample{
			URL:  corpusURL + strings.TrimSuffix(name, ".html"),
			Body: text.Text,
		})
	}
	return samples
}

// corpusJob is the job the corpus is measured with, parsed from the same file a
// person would run. Validated here as well, so a corpus job that could not be
// submitted cannot quietly be the one the numbers came from.
func corpusJob(t *testing.T) *engine.Spec {
	t.Helper()

	src, err := os.ReadFile(filepath.Join(corpusDir, "job.hcl"))
	if err != nil {
		t.Fatalf("read job: %v", err)
	}

	doc, err := engine.Parse(src, "job.hcl")
	if err != nil {
		t.Fatalf("parse job: %v", err)
	}
	if err := doc.Validate(); err != nil {
		t.Fatalf("validate job: %v", err)
	}
	return doc.Jobs[0].Spec()
}

func rates(t *testing.T) *extract.Report {
	t.Helper()

	report, err := extract.Rates(corpusJob(t), corpus(t))
	if err != nil {
		t.Fatalf("rates: %v", err)
	}
	return report
}

// TestFillRatesOverTheCorpusClearTheFloor.
//
// The floors are a ratchet. Each one is what extraction actually achieves over
// the corpus, minus a small margin, and it is here so that a change which makes
// extraction worse fails the build instead of being noticed a quarter later.
// Raise a floor when extraction improves. Never lower one to make a change
// pass: a floor that moves down to accommodate a regression is not a
// measurement, it is a record of what somebody was willing to accept.
//
// They are floors and not exact numbers on purpose. An exact assertion would
// fail on every improvement as well as every regression, which is how a test
// teaches people to edit it without reading it.
func TestFillRatesOverTheCorpusClearTheFloor(t *testing.T) {
	report := rates(t)
	t.Log("\n" + report.String())

	if report.Pages < 12 {
		t.Fatalf("the corpus has %d pages; below about a dozen it cannot be diverse enough to mean anything", report.Pages)
	}

	article, ok := report.Item("article")
	if !ok {
		t.Fatal("no article rates at all")
	}

	for _, floor := range []struct {
		property string
		least    float64
	}{
		// Measured 2026-08-05, and each floor is about one page of the corpus
		// below what was measured, which is written beside it.
		{"title", 0.85},     // measured 93.3%
		{"url", 0.65},       // measured 73.3%
		{"body", 0.80},      // measured 86.7%
		{"published", 0.65}, // measured 73.3%
		{"summary", 0.45},   // measured 53.3%
	} {
		got, ok := article.Property(floor.property)
		if !ok {
			t.Errorf("%s is not in the report", floor.property)
			continue
		}
		if got.Rate() < floor.least {
			t.Errorf("%s filled on %.1f%% of pages, floor is %.1f%%",
				floor.property, got.Rate()*100, floor.least*100)
		}
	}

	// Measured 100%: every page in the corpus produces an article of some
	// sort, the error page and the client-rendered shell included, because a
	// page only has to yield one property to be one.
	if got := article.Rate(); got < 0.90 {
		t.Errorf("an article came out of %.1f%% of pages, floor is 90%%", got*100)
	}

	// Measured 54.7%, over every declared property including the ones a page
	// rarely carries at all, such as a modified date.
	if got := report.Overall(); got < 0.50 {
		t.Errorf("overall fill rate %.1f%%, floor is 50%%", got*100)
	}
}

// TestMostOfTheFillRateIsGuessedRatherThanTaught is the fact the breakdown
// exists to state. A rate that rests on semantics is a rate that will drift when
// a site changes its markup, and nothing will say so, which is why the number is
// reported per way of finding a value rather than as one total.
func TestMostOfTheFillRateIsGuessedRatherThanTaught(t *testing.T) {
	article, ok := rates(t).Item("article")
	if !ok {
		t.Fatal("no article rates at all")
	}

	title, ok := article.Property("title")
	if !ok {
		t.Fatal("title is not in the report")
	}
	if title.By[extract.BySemantics] == 0 {
		t.Error("no title was found semantically, which cannot be right for a corpus with Open Graph in it")
	}
	if got := title.Guessed(); got < 0.9 {
		t.Errorf("titles guessed %.1f%% of the time; if this has fallen, a locator was taught and the floors above are stale", got*100)
	}

	// The job teaches two selectors and one regex, and each has to be doing
	// something. A breakdown where only one column is ever used would report
	// the same number whatever extraction did with the other three.
	body, _ := article.Property("body")
	if body.By[extract.ByCSS] == 0 {
		t.Error("no body came from the taught selectors, so the css column proves nothing")
	}
	published, _ := article.Property("published")
	if published.By[extract.ByRegex] == 0 {
		t.Error("no date came from the taught regex, so the regex column proves nothing")
	}
}

// TestAValueEmptiedByATransformIsNotCountedAsAMiss, and is counted separately,
// because it is the failure that looks like success: the locator worked, the
// transform threw the value away, and the record carries an empty string that
// nothing downstream can distinguish from a field the page never had.
func TestAValueEmptiedByATransformIsNotCountedAsAMiss(t *testing.T) {
	article, ok := rates(t).Item("article")
	if !ok {
		t.Fatal("no article rates at all")
	}

	profile, ok := article.Property("author.profile")
	if !ok {
		t.Fatal("the fields of an object property are not reported")
	}
	if profile.Empty == 0 {
		t.Error("the corpus has a byline linking to a mailto: address, which absurl empties; nothing counted it")
	}
	if profile.Empty > profile.Found {
		t.Errorf("counted %d empty values out of %d found", profile.Empty, profile.Found)
	}
}

// TestARequiredPropertyThatIsAbsentIsCounted, which is the number that says a
// job is exporting records with holes in them rather than failing outright.
func TestARequiredPropertyThatIsAbsentIsCounted(t *testing.T) {
	article, ok := rates(t).Item("article")
	if !ok {
		t.Fatal("no article rates at all")
	}

	if article.Missing == 0 {
		t.Fatal("the corpus contains a page with no headline and one with no body, and neither was counted")
	}
	if article.Complete >= article.Found {
		t.Error("every page came out complete, which for this corpus means the required properties are not being checked")
	}

	var missing int
	for _, prop := range article.Properties {
		missing += prop.Missing
	}
	if missing != article.Missing {
		t.Errorf("the properties account for %d missing values, the item reports %d", missing, article.Missing)
	}
}

// TestTwoRunsOfTheReportRenderIdentically. A measurement that reorders itself
// cannot be diffed against last month's, which is most of what it is for.
func TestTwoRunsOfTheReportRenderIdentically(t *testing.T) {
	first := rates(t).String()
	for i := 0; i < 5; i++ {
		if got := rates(t).String(); got != first {
			t.Fatalf("run %d differs:\n%s\nwant:\n%s", i, got, first)
		}
	}
}

// TestRatesRefusesASpecItCannotCompile, rather than reporting zeros. A typo in a
// selector would otherwise measure as an extraction regression and be looked for
// in the wrong place.
func TestRatesRefusesASpecItCannotCompile(t *testing.T) {
	broken := spec(t, `
  item "article" {
    property "title" {
      type = str
      css  = ["h1[["]
    }
  }
`)

	if _, err := extract.Rates(broken, corpus(t)); err == nil {
		t.Fatal("a spec with a selector that is not one was measured rather than refused")
	}
}

// TestAPageThatWillNotParseIsCountedNotFatal.
//
// x/net/html does not fail on bad markup, which is the point of it, but it does
// refuse a document nested more than 512 elements deep - a quoted forum or mail
// thread reaches that. One such page in a corpus made Rates return no report at
// all, so a measurement over a thousand pages was lost to the strangest one of
// them.
//
// It is counted instead, and said: a rate over a corpus that partly would not
// parse is a different number from one over a corpus that did, and dropping the
// difference silently reads as full coverage.
func TestAPageThatWillNotParseIsCountedNotFatal(t *testing.T) {
	deep := []byte("<html><body>" + strings.Repeat("<blockquote>", 600) +
		"hi" + strings.Repeat("</blockquote>", 600) + "</body></html>")
	good := []byte(`<html><head><title>One</title></head><body><h1 class="t">One</h1></body></html>`)

	spec := &engine.Spec{Job: "news", Items: []*engine.Item{{
		Name:       "article",
		Properties: []*engine.Property{{Name: "t", Type: "str", CSS: []string{".t"}}},
	}}}

	report, err := extract.Rates(spec, []extract.Sample{
		{URL: "https://e.example/1", Body: good},
		{URL: "https://e.example/2", Body: deep},
		{URL: "https://e.example/3", Body: good},
	})
	if err != nil {
		t.Fatalf("one unparseable page lost the whole report: %v", err)
	}
	if report.Pages != 3 {
		t.Errorf("pages = %d, want every sample counted", report.Pages)
	}
	if report.Unreadable != 1 {
		t.Errorf("unreadable = %d, want 1", report.Unreadable)
	}
	if got := report.Items[0].Found; got != 2 {
		t.Errorf("found on %d pages, want the two that parsed", got)
	}
	if !strings.Contains(report.String(), "could not be parsed") {
		t.Errorf("the report does not say a page was unreadable:\n%s", report)
	}
}

// TestTheReportsRowsAccountForItsTotal.
//
// The header says how many required properties were missing and the rows say
// which. When an object property matched nothing, the item's total counted
// every required name beneath it and the rows counted none, so the report named
// a number without naming a field - which is the one thing a fill-rate report
// is for.
func TestTheReportsRowsAccountForItsTotal(t *testing.T) {
	spec := &engine.Spec{Job: "news", Items: []*engine.Item{{
		Name: "article",
		Properties: []*engine.Property{
			{Name: "title", Type: "str", CSS: []string{"h1"}},
			{Name: "author", Type: "object", CSS: []string{".byline"}, Properties: []*engine.Property{
				{Name: "name", Type: "str", Required: true, CSS: []string{".byline-name"}},
			}},
		},
	}}}

	report, err := extract.Rates(spec, []extract.Sample{{
		URL:  "https://e.example/1",
		Body: []byte(`<html><body><h1>Something happened</h1></body></html>`),
	}})
	if err != nil {
		t.Fatal(err)
	}

	item := report.Items[0]
	if item.Missing == 0 {
		t.Fatal("the item reports no required property missing, though one is")
	}

	var counted int
	for _, prop := range item.Properties {
		counted += prop.Missing
	}
	if counted != item.Missing {
		t.Errorf("the item reports %d required properties missing and its rows account for %d:\n%s",
			item.Missing, counted, report)
	}
}
