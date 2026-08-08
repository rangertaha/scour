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
	// about the way `scour train` tells it: by reading the markers.
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
// decoding, and every later `scour validate`, `show` or `run` on it failed with
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
