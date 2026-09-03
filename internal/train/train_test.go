// SPDX-License-Identifier: GPL-3.0-or-later

package train_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rangertaha/scour/internal/engine"
	"github.com/rangertaha/scour/internal/train"
)

func job(t *testing.T, src string) *engine.Job {
	t.Helper()

	doc, err := engine.Parse([]byte(src), "job.hcl")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := doc.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	return doc.Jobs[0]
}

// corpus is a site's pages, all built the same way, which is the situation
// induction is for: the class is the same everywhere and nothing says so.
func corpus(t *testing.T, count int) []train.Page {
	t.Helper()

	pages := make([]train.Page, 0, count)
	for i := range count {
		pages = append(pages, train.Page{
			URL: fmt.Sprintf("https://example.com/story/%d", i),
			Body: []byte(fmt.Sprintf(`<!doctype html>
<html><head><title>Story %d | The Example</title></head>
<body>
  <nav><h1 class="site-name">The Example</h1></nav>
  <article>
    <h1 class="headline">Story %d</h1>
    <div class="byline"><span class="author-name">Alex Doe</span></div>
    <div class="article-body"><p>Words about story %d.</p></div>
  </article>
</body></html>`, i, i, i)),
		})
	}
	return pages
}

const shape = `
job "news" {
  start = ["https://example.com/"]

  item "article" {
    property "headline" {
      type = str
    }

    property "author_name" {
      type = str
    }
  }
}
`

// TestALocatorIsInducedFromTheCorpus, and it is one that works on all of it
// rather than on the page it was learnt from.
func TestALocatorIsInducedFromTheCorpus(t *testing.T) {
	proposals, err := train.Learn(job(t, shape), corpus(t, 5), train.Options{})
	if err != nil {
		t.Fatalf("learn: %v", err)
	}
	if len(proposals) == 0 {
		t.Fatal("nothing was proposed")
	}

	found := map[string]train.Proposal{}
	for _, p := range proposals {
		found[p.Property] = p
	}

	headline, ok := found["headline"]
	if !ok {
		t.Fatalf("no locator for the headline: %v", proposals)
	}
	if headline.Pages != 5 || headline.Total != 5 {
		t.Errorf("the headline locator worked on %d of %d pages", headline.Pages, headline.Total)
	}
	if !strings.Contains(headline.Selector, "headline") {
		t.Errorf("selector = %q, want the class the page uses", headline.Selector)
	}
	if headline.Rate() != 1 {
		t.Errorf("rate = %v", headline.Rate())
	}
}

// TestTheSelectorIsSpecificEnoughToBeRight. Two h1 elements on the page, and
// the one in the navigation is not the headline.
func TestTheSelectorIsSpecificEnoughToBeRight(t *testing.T) {
	proposals, err := train.Learn(job(t, shape), corpus(t, 4), train.Options{})
	if err != nil {
		t.Fatal(err)
	}

	for _, p := range proposals {
		if p.Property != "headline" {
			continue
		}
		if p.Selector == "h1" {
			t.Errorf("proposed %q, which finds the site name rather than the headline", p.Selector)
		}
		if !strings.Contains(p.Example, "Story") {
			t.Errorf("the proposal's own example is %q", p.Example)
		}
	}
}

// TestALocatorThatOnlyWorksSometimesIsNotProposed. One that works on three
// pages out of two hundred is worse than none, because it looks like an answer.
func TestALocatorThatOnlyWorksSometimesIsNotProposed(t *testing.T) {
	pages := corpus(t, 10)
	// One page in ten is built differently, and its class is unique to it.
	pages[0].Body = []byte(`<html><body><article>
	  <h1 class="one-off-headline">Different</h1>
	</article></body></html>`)

	proposals, err := train.Learn(job(t, shape), pages, train.Options{Least: 0.9})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range proposals {
		if strings.Contains(p.Selector, "one-off") {
			t.Errorf("proposed %q, which works on one page in ten", p.Selector)
		}
	}

	// And the threshold is what refused it, not the presence of a better
	// candidate. This is the only test in the repo that sets Least - the
	// `--min` flag - and the corpus above gives `headline` a nine-in-ten
	// selector alongside the one-off, so the best-scorer wins before the
	// threshold is ever consulted: deleting the threshold entirely left this
	// test passing, and Least: 0.9 and Least: 0.001 produced identical
	// proposals.
	//
	// A corpus where the only candidate works on one page in ten has nothing
	// better to fall back on, so the threshold is the whole of the answer.
	// A different tag and a different class on every page, so no candidate -
	// by tag, by class or by position - works on more than one of them.
	tags := []string{"h1", "h2", "h3", "h4", "h5", "h6", "p", "span", "div", "strong"}
	sparse := corpus(t, len(tags))
	for i, tag := range tags {
		sparse[i].Body = fmt.Appendf(nil, `<html><body><article>
		  <%s class="headline-%d">Story %d</%s>
		</article></body></html>`, tag, i, i, tag)
	}

	strict, err := train.Learn(job(t, shape), sparse, train.Options{Least: 0.9})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range strict {
		if p.Property == "headline" {
			t.Errorf("proposed %q for a headline no selector finds twice, under --min 0.9", p.Selector)
		}
	}

	lax, err := train.Learn(job(t, shape), sparse, train.Options{Least: 0.05})
	if err != nil {
		t.Fatal(err)
	}
	var offered bool
	for _, p := range lax {
		if p.Property == "headline" {
			offered = true
		}
	}
	if !offered {
		t.Error("under --min 0.05 the same corpus proposed nothing, so the threshold is not what decided")
	}
}

// TestExamplesTeachWithoutASelector. Given the answer, induction can look for
// the node that produces it, which is how a person teaches a locator without
// writing one.
func TestExamplesTeachWithoutASelector(t *testing.T) {
	taught := job(t, `
job "news" {
  start = ["https://example.com/"]

  item "article" {
    property "byline" {
      type     = str
      examples = ["Alex Doe"]
    }
  }
}
`)

	proposals, err := train.Learn(taught, corpus(t, 3), train.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(proposals) != 1 {
		t.Fatalf("proposals = %v", proposals)
	}
	if !strings.Contains(proposals[0].Selector, "author-name") {
		t.Errorf("selector = %q, want the node holding the example", proposals[0].Selector)
	}
	if proposals[0].Example != "Alex Doe" {
		t.Errorf("example = %q", proposals[0].Example)
	}
}

// TestACorrectionIsNeverOverwritten. The rule that makes the loop converge
// instead of going in circles.
func TestACorrectionIsNeverOverwritten(t *testing.T) {
	corrected := job(t, `
job "news" {
  start = ["https://example.com/"]

  item "article" {
    property "headline" {
      type = str
      css  = [".headline"]
    }
  }
}
`)

	proposals, err := train.Learn(corrected, corpus(t, 5), train.Options{Replace: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(proposals) != 1 {
		t.Fatalf("proposals = %v", proposals)
	}
	if !proposals[0].Kept {
		t.Errorf("a locator a person wrote was replaced by %q", proposals[0].Selector)
	}
	if got := strings.Join(proposals[0].Replaces, ","); got != ".headline" {
		t.Errorf("replaces = %q", got)
	}
}

// TestWhatItWroteIsMarked, which is the only reason it can tell its own guess
// from somebody's correction next time.
func TestWhatItWroteIsMarked(t *testing.T) {
	document := []byte(shape)

	proposals, err := train.Learn(job(t, shape), corpus(t, 5), train.Options{})
	if err != nil {
		t.Fatal(err)
	}

	edited, written, err := train.Write(document, "news", proposals)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if written == 0 {
		t.Fatal("nothing was written")
	}
	if !strings.Contains(string(edited), train.Marker) {
		t.Errorf("what it wrote is not marked:\n%s", edited)
	}
	if !strings.Contains(string(edited), "css =") {
		t.Errorf("no locator was written:\n%s", edited)
	}

	// And the edited document is still a job.
	back := job(t, string(edited))
	if len(back.Items[0].Properties[0].CSS) != 1 {
		t.Errorf("the locator did not survive a reparse: %+v", back.Items[0].Properties[0])
	}

	// The marker is findable again, which is what the next run reads.
	induced := train.MarkInduced(edited, "news")
	if !induced["article.headline"] {
		t.Errorf("the marker was not found again: %v", induced)
	}
}

// TestTheRestOfTheFileIsLeftAlone. A diff nobody can read is a diff nobody
// reviews, and reviewing what induction proposed is the entire point.
func TestTheRestOfTheFileIsLeftAlone(t *testing.T) {
	document := []byte(`# A job that crawls the example.
job "news" {
  # Where it starts.
  start = ["https://example.com/"]

  item "article" {
    property "headline" {
      type = str   # what a person reads
    }
  }
}
`)

	proposals, err := train.Learn(job(t, string(document)), corpus(t, 3), train.Options{})
	if err != nil {
		t.Fatal(err)
	}
	edited, written, err := train.Write(document, "news", proposals)
	if err != nil || written == 0 {
		t.Fatalf("write: %d, %v", written, err)
	}

	for _, comment := range []string{
		"# A job that crawls the example.",
		"# Where it starts.",
		"# what a person reads",
	} {
		if !strings.Contains(string(edited), comment) {
			t.Errorf("a comment was lost: %q\n%s", comment, edited)
		}
	}
}

// TestRunningItTwiceReplacesItsOwnGuess, rather than adding a second one.
func TestRunningItTwiceReplacesItsOwnGuess(t *testing.T) {
	document := []byte(shape)

	first, err := train.Learn(job(t, shape), corpus(t, 5), train.Options{})
	if err != nil {
		t.Fatal(err)
	}
	edited, _, err := train.Write(document, "news", first)
	if err != nil {
		t.Fatal(err)
	}

	// The document now carries induced locators, which the next run is told
	// about the way `scour job train` tells it: by reading the markers.
	next := job(t, string(edited))
	for _, item := range next.Items {
		for _, prop := range item.Properties {
			if train.MarkInduced(edited, "news")[item.Name+"."+prop.Name] {
				prop.Induced = true
			}
		}
	}

	second, err := train.Learn(next, corpus(t, 5), train.Options{Replace: true})
	if err != nil {
		t.Fatal(err)
	}
	twice, _, err := train.Write(edited, "news", second)
	if err != nil {
		t.Fatal(err)
	}

	if got := strings.Count(string(twice), "css ="); got != strings.Count(string(edited), "css =") {
		t.Errorf("running twice produced %d locators where once produced %d",
			got, strings.Count(string(edited), "css ="))
	}
	if got := strings.Count(string(twice), train.Marker); got != strings.Count(string(edited), train.Marker) {
		t.Errorf("the markers multiplied: %d then %d",
			strings.Count(string(edited), train.Marker), got)
	}
}

// TestWritingIsAtomic: a job document may be the only copy.
func TestWritingIsAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job.hcl")
	if err := os.WriteFile(path, []byte(shape), 0o600); err != nil {
		t.Fatal(err)
	}

	proposals, err := train.Learn(job(t, shape), corpus(t, 5), train.Options{})
	if err != nil {
		t.Fatal(err)
	}

	written, err := train.WriteFile(path, "news", proposals)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if written == 0 {
		t.Fatal("nothing was written")
	}

	back, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(back), train.Marker) {
		t.Errorf("the file was not edited:\n%s", back)
	}
	if _, err := os.Stat(path + ".scour-train"); err == nil {
		t.Error("the temporary file was left behind")
	}
}

func TestNothingToLearnFrom(t *testing.T) {
	if _, err := train.Learn(job(t, shape), nil, train.Options{}); err == nil {
		t.Error("learned from no pages at all")
	}
	if _, err := train.Learn(nil, corpus(t, 1), train.Options{}); err == nil {
		t.Error("learned for no job")
	}
}

func TestReportReadsAsALine(t *testing.T) {
	report := train.Report([]train.Proposal{
		{Item: "article", Property: "headline", Selector: ".headline", Pages: 5, Total: 5},
		{Item: "article", Property: "author", Replaces: []string{".by"}, Kept: true},
	})

	if !strings.Contains(report, "5/5 pages") || !strings.Contains(report, "kept .by") {
		t.Errorf("report:\n%s", report)
	}
}

// TestAOneLinePropertyIsLeftAloneRatherThanCorrupted.
//
// The regression test for a bug that damaged somebody's job document. A
// property written on one line has no body to insert a locator into, and the
// scan for the block's closing brace latched onto the closing brace of whatever
// came next: the locator landed outside the property, the file stopped
// decoding, and every later `scour job valid`, `show` or `run` on it failed with
// "Unsupported argument". A tool that edits a file people keep in a repository
// has to fail to act rather than act wrongly.
func TestAOneLinePropertyIsLeftAloneRatherThanCorrupted(t *testing.T) {
	for name, document := range map[string]string{
		"empty": `
job "news" {
  start = ["https://example.com/"]

  item "article" {
    property "headline" {}
  }
}
`,
		"with a body on the line": `
job "news" {
  start = ["https://example.com/"]

  item "article" {
    property "headline" { type = str }
  }
}
`,
	} {
		t.Run(name, func(t *testing.T) {
			// The shape has to parse for the fixture to mean anything.
			j := job(t, document)

			proposals, err := train.Learn(j, corpus(t, 5), train.Options{})
			if err != nil {
				t.Fatalf("learn: %v", err)
			}

			edited, written, err := train.Write([]byte(document), "news", proposals)
			if err != nil {
				t.Fatalf("write: %v", err)
			}
			if written != 0 {
				t.Errorf("wrote %d locators into a document it cannot edit safely", written)
			}
			if string(edited) != document {
				t.Errorf("the document was changed:\n%s", edited)
			}

			// The thing that actually matters: whatever came back still parses.
			if _, err := engine.Parse(edited, "job.hcl"); err != nil {
				t.Errorf("the document no longer decodes: %v\n%s", err, edited)
			}
		})
	}
}

// TestTwoJobsWithOneItemNameDoNotCollide.
//
// The search for where a locator goes was line-oriented with no notion of a job
// block, so it took the first `item "x"` in the file. Two jobs each declaring
// an item of the same name, crawling different sites, is an ordinary document:
// training the second wrote its induced selector into the first, so one job
// began extracting with a selector induced from a site it had never fetched and
// the other still had no locator. The reader of that document saw a plausible
// css line under a marker saying scour had put it there.
func TestTwoJobsWithOneItemNameDoNotCollide(t *testing.T) {
	document := []byte(`
job "uk" {
  item "article" {
    property "headline" {
      type = str
    }
  }
}

job "eu" {
  item "article" {
    property "headline" {
      type = str
    }
  }
}
`)

	edited, written, err := train.Write(document, "eu", []train.Proposal{
		{Item: "article", Property: "headline", Selector: ".eu-hed"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if written != 1 {
		t.Fatalf("written = %d, want 1", written)
	}

	uk, eu, _ := strings.Cut(string(edited), `job "eu"`)
	if strings.Contains(uk, ".eu-hed") {
		t.Error("the eu job's selector was written into the uk job")
	}
	if !strings.Contains(eu, ".eu-hed") {
		t.Error("the eu job did not get its selector")
	}
}

// TestAMarkerInAnotherJobIsNotThisJobsMarker.
//
// The other half of the same mistake. MarkInduced keyed on item and property
// alone, so a marker in one job reported the other job's property as induced,
// and a locator a person had written by hand was proposed for replacement.
func TestAMarkerInAnotherJobIsNotThisJobsMarker(t *testing.T) {
	document := []byte(`
job "uk" {
  item "article" {
    property "headline" {
      css = [".uk-hed"] ` + train.Marker + `
    }
  }
}

job "eu" {
  item "article" {
    property "headline" {
      css = [".written-by-hand"]
    }
  }
}
`)

	if train.MarkInduced(document, "eu")["article.headline"] {
		t.Error("a hand-written locator was reported as one scour had induced")
	}
	if !train.MarkInduced(document, "uk")["article.headline"] {
		t.Error("the job's own marker was not seen")
	}
}

// TestALocatorGoesToTheItemsPropertyAndNotARelations.
//
// An item holds property blocks and relation blocks, and a relation holds
// property blocks of its own. Both spellings are `property "name" {`, so a scan
// looking for the item's `role` found the relation's `role` when the relation
// was written first, and the induced locator went onto the edge instead.
//
// Nothing failed. The item's property still had no locator, so extraction went
// on missing it on every page, while the author edge quietly gained a selector
// induced for a different field. MarkInduced then attributed that marker to the
// item's property, so a later run would report a hand-written locator as
// replaceable.
func TestALocatorGoesToTheItemsPropertyAndNotARelations(t *testing.T) {
	document := []byte(`
job "news" {
  domains = ["example.com"]
  start   = ["https://example.com/"]

  item "article" {
    relation "author" {
      entity = "person"

      property "role" {
        type = str
      }
    }

    property "role" {
      type = str
    }
  }
}
`)

	edited, written, err := train.Write(document, "news", []train.Proposal{
		{Item: "article", Property: "role", Selector: ".byline-role", Pages: 5, Total: 5},
	})
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if written != 1 {
		t.Fatalf("wrote %d proposals, want 1", written)
	}

	// The relation comes first in the document, so the wrong answer is the one
	// nearer the top. Comparing the two halves says which block it landed in.
	text := string(edited)
	relation := strings.Index(text, `relation "author"`)
	item := strings.LastIndex(text, `property "role"`)
	locator := strings.Index(text, ".byline-role")

	if locator < 0 {
		t.Fatalf("the locator was not written at all:\n%s", text)
	}
	if locator < item {
		t.Errorf("the locator landed inside relation \"author\" at %d rather than the item's own "+
			"property at %d, so the edge gained a selector induced for a different field "+
			"and article.role still has none:\n%s", relation, item, text)
	}

	// And it still parses, because a document that stops decoding is how this
	// was noticed the last time it went wrong.
	if _, err := engine.Parse(edited, "job.hcl"); err != nil {
		t.Errorf("the edited document no longer parses: %v\n%s", err, text)
	}
}

// TestAMarkerInARelationIsNotTheItemsMarker.
//
// MarkInduced is what tells Learn which locators it may replace, and it tracked
// the last `item` and the last `property` it had seen without noticing that a
// relation's property blocks are spelled the same as an item's. So a marker
// inside relation "author"'s `role` recorded article.role as induced, and a
// locator a person had written by hand on the item was offered for replacement
// on the strength of a marker belonging to a different field.
func TestAMarkerInARelationIsNotTheItemsMarker(t *testing.T) {
	document := []byte(`
job "news" {
  item "article" {
    relation "author" {
      entity = "person"

      property "role" {
        css = [".edge"] ` + train.Marker + `
      }
    }

    property "role" {
      css = [".mine"]
    }
  }
}
`)

	induced := train.MarkInduced(document, "news")
	if induced["article.role"] {
		t.Errorf("a marker inside relation \"author\" reported article.role as induced, "+
			"so the hand-written locator on the item would be replaced: %v", induced)
	}
}

// TestAHandWrittenXPathIsACorrection.
//
// A locator somebody wrote is never overwritten, and the guard only looked at
// CSS. Extraction tries CSS before XPath, so writing an induced `css` beside a
// hand-written `xpath` does not sit next to it: it replaces it, and the XPath
// was written precisely because CSS could not express what the person meant.
// Nothing even reported it as kept, because nothing looked.
func TestAHandWrittenXPathIsACorrection(t *testing.T) {
	doc := `
job "news" {
  domains = ["example.com"]
  start   = ["https://example.com/"]

  item "article" {
    property "headline" {
      type  = str
      xpath = ["//h2[contains(.,'Price')]/following-sibling::span"]
    }
  }
}
`
	parsed, err := engine.Parse([]byte(doc), "job.hcl")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := parsed.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}

	proposals, err := train.Learn(parsed.Jobs[0], []train.Page{
		{URL: "https://example.com/a", Body: []byte("<html><body><h1>A story</h1></body></html>")},
		{URL: "https://example.com/b", Body: []byte("<html><body><h1>Another</h1></body></html>")},
	}, train.Options{Replace: true})
	if err != nil {
		t.Fatalf("learn: %v", err)
	}

	for _, p := range proposals {
		if p.Property != "headline" {
			continue
		}
		if !p.Kept {
			t.Errorf("a hand-written xpath was replaced by an induced css %q, "+
				"and extraction prefers css so the xpath would never run again", p.Selector)
		}
	}
}

// TestAnInducedSelectorIsQuotedAsHCL.
//
// The selector is written into somebody's job document, and %q is Go's syntax
// rather than HCL's. They agree until a value contains `${`, which HCL reads as
// the start of an interpolation: a page carrying an unrendered template
// attribute yields a selector like meta[name="og:title-${id}"], and writing it
// with %q left a document that no longer parses, failing every later command
// with a diagnostic about an unknown variable that named nothing useful.
func TestAnInducedSelectorIsQuotedAsHCL(t *testing.T) {
	doc := []byte(`
job "news" {
  domains = ["example.com"]
  start   = ["https://example.com/"]

  item "article" {
    property "headline" {
      type = str
    }
  }
}
`)

	edited, written, err := train.Write(doc, "news", []train.Proposal{{
		Item:     "article",
		Property: "headline",
		Selector: `meta[name="og:title-${id}"]`,
	}})
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if written != 1 {
		t.Fatalf("wrote %d locators", written)
	}

	if _, err := engine.Parse(edited, "job.hcl"); err != nil {
		t.Errorf("the edited document no longer parses: %v\n%s", err, edited)
	}
}

// TestALocatorGoesToTheItemsPropertyAndNotANestedOne.
//
// The same mistake as the relation case, through nested properties. An item's
// property may hold properties of its own, spelled identically, and the scan
// that finds where a locator goes walked into them: a proposal for the item's
// `name` landed inside `author`'s `name` whenever the object was written
// first.
//
// Nothing failed. The item's property still had no locator, so extraction went
// on missing it on every page, while a nested field quietly gained a selector
// induced for a different one - evaluated inside the author node, where it
// matches nothing.
func TestALocatorGoesToTheItemsPropertyAndNotANestedOne(t *testing.T) {
	document := []byte(`
job "news" {
  item "article" {
    property "author" {
      type = "object"

      property "name" {
        type = "str"
      }
    }

    property "name" {
      type = "str"
    }
  }
}
`)

	edited, written, err := train.Write(document, "news", []train.Proposal{{
		Item: "article", Property: "name", Selector: ".headline",
	}})
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if written != 1 {
		t.Fatalf("wrote %d locators, want 1", written)
	}

	// The item's own property got it, and the nested one did not.
	text := string(edited)
	item := text[strings.LastIndex(text, `property "name"`):]
	if !strings.Contains(item, ".headline") {
		t.Errorf("the item's own property has no locator:\n%s", text)
	}
	author := text[strings.Index(text, `property "author"`):strings.LastIndex(text, `property "name"`)]
	if strings.Contains(author, ".headline") {
		t.Errorf("the locator went into the nested property:\n%s", text)
	}
}

// TestAMarkerInANestedPropertyIsNotTheItemsMarker.
//
// The other half, exactly as with relations. MarkInduced recorded any
// `property "` line it saw, nested ones included, so a marker inside
// `author.name` reported the item's own `name` as induced - and a locator a
// person had written by hand was then overwritten rather than learned from.
func TestAMarkerInANestedPropertyIsNotTheItemsMarker(t *testing.T) {
	document := []byte(`
job "news" {
  item "article" {
    property "author" {
      type = "object"

      property "name" {
        css = [".nested"] ` + train.Marker + `
      }
    }

    property "name" {
      css = [".written-by-hand"]
    }
  }
}
`)

	if train.MarkInduced(document, "news")["article.name"] {
		t.Error("a hand-written locator was reported as one scour had induced, " +
			"on the strength of a marker in a nested property")
	}
}

// TestAHeadlineWrittenOverTwoLinesStillGetsALocator.
//
// Induction compares the value extraction found against the text of each node,
// and the two sides normalised whitespace differently: extraction keeps a
// page's own spacing and adds a newline after every block element, while this
// package collapsed runs of whitespace to single spaces. So a value with a
// newline or a double space in it matched no node at all and no locator was
// ever proposed for it.
//
// Silently: `scour job train` simply printed no line for the property, which
// reads as "nothing works on this corpus" rather than "this cannot be
// compared". Every headline a template wraps, and every multi-paragraph body,
// was in that set.
func TestAHeadlineWrittenOverTwoLinesStillGetsALocator(t *testing.T) {
	pages := corpus(t, 5)
	for i := range pages {
		pages[i].Body = []byte(fmt.Sprintf(`<!doctype html>
<html><head><title>Story %d | The Example</title></head>
<body>
  <nav><h1 class="site-name">The Example</h1></nav>
  <article>
    <h1 class="headline">Story %d
       today</h1>
    <div class="byline"><span class="author-name">Alex Doe</span></div>
  </article>
</body></html>`, i, i))
	}

	proposals, err := train.Learn(job(t, shape), pages, train.Options{})
	if err != nil {
		t.Fatal(err)
	}

	var found bool
	for _, p := range proposals {
		if p.Property == "headline" {
			found = true
			if !strings.Contains(p.Selector, "headline") {
				t.Errorf("proposed %q for a headline wrapped over two lines", p.Selector)
			}
		}
	}
	if !found {
		t.Errorf("no locator was proposed for a headline wrapped over two lines: %v", proposals)
	}
}

// TestAMarkerIsSeenAfterAOneLineProperty.
//
// MarkInduced cleared the property it was inside only on a line that is exactly
// `}`, so a sibling that does not end that way left it set - and the next
// property, the item's own, was then read as nested and stepped over. Its
// marker was never seen.
//
// Both shapes here are ordinary. A one-line property block is a first-class
// form (`property "topic_score" { type = str }` is in this repo's own
// documentation) and a closing brace with a trailing comment is what somebody
// writes when annotating a document.
//
// The consequence is that scour's own locator is read as hand-written, so
// `scour job train` reports it kept and never replaces it: training stops
// converging on that property, silently.
func TestAMarkerIsSeenAfterAOneLineProperty(t *testing.T) {
	for name, before := range map[string]string{
		"one-line block":         `    property "section" { type = "str" }`,
		"brace with a comment":   "    property \"section\" {\n      type = \"str\"\n    } # the section",
		"one-line with a marker": `    property "section" { css = [".sec"] ` + train.Marker + ` }`,
	} {
		t.Run(name, func(t *testing.T) {
			document := []byte(`
job "news" {
  item "article" {
` + before + `

    property "title" {
      css = ["h1"] ` + train.Marker + `
    }
  }
}
`)
			induced := train.MarkInduced(document, "news")
			if !induced["article.title"] {
				t.Errorf("the marker on the item's own title was not seen: %v", induced)
			}
		})
	}
}
