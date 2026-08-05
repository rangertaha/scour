// SPDX-License-Identifier: GPL-3.0-or-later

package extract_test

import (
	"strings"
	"testing"

	"github.com/rangertaha/scour/internal/engine"
	"github.com/rangertaha/scour/internal/extract"
)

const pageURL = "https://example.com/news/2026/story.html"

func spec(t *testing.T, items string) *engine.Spec {
	t.Helper()

	src := "job \"news\" {\n  start = [\"https://example.com/\"]\n\n" + items + "\n}\n"
	doc, err := engine.Parse([]byte(src), "job.hcl")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := doc.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	return doc.Jobs[0].Spec()
}

func run(t *testing.T, items, body string) *extract.Result {
	t.Helper()

	e, err := extract.New(spec(t, items))
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	result, err := e.Page(pageURL, []byte(body))
	if err != nil {
		t.Fatalf("page: %v", err)
	}
	return result
}

func one(t *testing.T, items, body string) *extract.Item {
	t.Helper()

	result := run(t, items, body)
	if len(result.Items) != 1 {
		t.Fatalf("extracted %d items, want 1", len(result.Items))
	}
	return result.Items[0]
}

// A page as a publisher writes one: the same facts said three ways, which is
// the situation extraction actually faces.
const article = `<!doctype html>
<html>
<head>
  <title>Something happened yesterday | The Example</title>
  <meta property="og:title" content="Something happened yesterday">
  <meta property="article:published_time" content="2026-08-04T09:15:00Z">
  <meta name="description" content="A short summary of the thing.">
  <link rel="canonical" href="https://example.com/news/2026/story.html">
  <script type="application/ld+json">
  {"@type":"NewsArticle","headline":"Something happened yesterday",
   "author":{"@type":"Person","name":"Alex Doe"},
   "wordCount":812}
  </script>
</head>
<body>
  <article>
    <h1 class="headline">Something happened yesterday</h1>
    <div class="byline">By <span itemprop="author">Alex Doe</span></div>
    <time datetime="2026-08-04T09:15:00Z">yesterday</time>
    <div class="article-body">
      <p>First paragraph.</p>
      <p>Second paragraph.</p>
    </div>
    <a href="/news/2026/other.html">Another story</a>
    <a href="https://elsewhere.example/x">Elsewhere</a>
    <a href="#top">Top</a>
  </article>
</body>
</html>`

// TestATaughtLocatorWins. A locator in the document is something a person read
// and committed; semantics are a guess that is usually right. A guess must
// never quietly beat an instruction, or correcting a wrong extraction would be
// impossible.
func TestATaughtLocatorWins(t *testing.T) {
	item := one(t, `
  item "article" {
    property "title" {
      type = str
      css  = [".byline"]
    }
  }
`, article)

	title, ok := item.Get("title")
	if !ok {
		t.Fatal("nothing found")
	}
	if title.How != extract.ByCSS {
		t.Errorf("found by %s, want the taught selector to win", title.How)
	}
	if !strings.Contains(title.Raw, "Alex Doe") {
		t.Errorf("raw = %q, want what the selector actually points at", title.Raw)
	}
}

// TestSemanticsFindWhatNobodyTaught, which is what makes a job document short
// enough to write.
func TestSemanticsFindWhatNobodyTaught(t *testing.T) {
	item := one(t, `
  item "article" {
    property "title" {
      type = str
    }

    property "description" {
      type = str
    }

    property "author" {
      type = str
    }
  }
`, article)

	for name, want := range map[string]string{
		"title":       "Something happened yesterday",
		"description": "A short summary of the thing.",
		"author":      "Alex Doe",
	} {
		got, ok := item.Get(name)
		if !ok {
			t.Errorf("%s found nothing", name)
			continue
		}
		if got.Text != want {
			t.Errorf("%s = %q, want %q (from %s)", name, got.Text, want, got.From)
		}
		if got.How != extract.BySemantics {
			t.Errorf("%s was found by %s", name, got.How)
		}
	}
}

// TestEveryValueSaysWhereItCameFrom. A value on its own does not tell you
// whether the locator will hold on the next page; the node it came from does.
func TestEveryValueSaysWhereItCameFrom(t *testing.T) {
	item := one(t, `
  item "article" {
    property "title" {
      type = str
    }
  }
`, article)

	title, _ := item.Get("title")
	if title.From == "" {
		t.Fatal("a value with no provenance")
	}
	if !strings.Contains(title.From, "og:title") && !strings.Contains(title.From, "h1") {
		t.Errorf("From = %q, want it to name what it read", title.From)
	}
}

// TestANameIsMatchedInEverySpellingMarkupUsesForIt. `published_at` in a
// document is `publishedAt` in JSON-LD, `published-at` in a class and
// `article:published_time` in Open Graph.
func TestANameIsMatchedInEverySpellingMarkupUsesForIt(t *testing.T) {
	item := one(t, `
  item "article" {
    property "published_time" {
      type       = date
      transforms = [datetime]
    }

    property "word_count" {
      type = int
    }
  }
`, article)

	published, ok := item.Get("published_time")
	if !ok {
		t.Fatal("the published time was not found under any spelling")
	}
	if published.Text != "2026-08-04T09:15:00Z" {
		t.Errorf("published = %q", published.Text)
	}

	if count, ok := item.Get("word_count"); !ok || count.Text != "812" {
		t.Errorf("word count = %+v; wordCount in JSON-LD was not matched", count)
	}
}

// TestATaughtXPath, for what CSS cannot express.
func TestATaughtXPath(t *testing.T) {
	item := one(t, `
  item "article" {
    property "second" {
      type  = str
      xpath = ["//div[@class='article-body']/p[2]"]
    }
  }
`, article)

	got, ok := item.Get("second")
	if !ok {
		t.Fatal("the xpath found nothing")
	}
	if got.Text != "Second paragraph." {
		t.Errorf("got %q", got.Text)
	}
	if got.How != extract.ByXPath {
		t.Errorf("found by %s", got.How)
	}
}

// TestARegexOverTheText, for what is written rather than marked up.
func TestARegexOverTheText(t *testing.T) {
	item := one(t, `
  item "article" {
    property "byline" {
      type    = str
      regexes = ["By ([A-Z][a-z]+ [A-Z][a-z]+)"]
    }
  }
`, article)

	got, ok := item.Get("byline")
	if !ok {
		t.Fatal("the regex found nothing")
	}
	if got.Text != "Alex Doe" {
		t.Errorf("got %q, want the capturing group", got.Text)
	}
	if got.How != extract.ByRegex {
		t.Errorf("found by %s", got.How)
	}
}

func TestTransformsRunInOrder(t *testing.T) {
	const body = `<html><body>
	  <p class="messy">   Lots   of
	  space   </p>
	  <a class="rel" href="/deep/page.html">link</a>
	  <span class="when">4 August 2026</span>
	  <span class="shout">Quiet</span>
	</body></html>`

	item := one(t, `
  item "thing" {
    property "messy" {
      type       = str
      css        = [".messy"]
      transforms = [normalise_space]
    }

    property "rel" {
      type       = url
      css        = [".rel"]
      transforms = [absurl]
    }

    property "when" {
      type       = date
      css        = [".when"]
      transforms = [datetime]
    }

    property "shout" {
      type       = str
      css        = [".shout"]
      transforms = [upper]
    }
  }
`, body)

	for name, want := range map[string]string{
		"messy": "Lots of space",
		"rel":   "https://example.com/deep/page.html",
		"when":  "2026-08-04T00:00:00Z",
		"shout": "QUIET",
	} {
		if got := item.Text(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}

// TestTheRawValueSurvivesTheTransforms, because a person debugging a transform
// wants to see what went into it.
func TestTheRawValueSurvivesTheTransforms(t *testing.T) {
	item := one(t, `
  item "thing" {
    property "when" {
      type       = date
      css        = ["time"]
      transforms = [datetime]
    }
  }
`, article)

	when, _ := item.Get("when")
	if when.Text != "2026-08-04T09:15:00Z" {
		t.Errorf("text = %q", when.Text)
	}
	if when.Raw != "2026-08-04T09:15:00Z" {
		t.Errorf("raw = %q", when.Raw)
	}
}

// TestAMachineReadableAttributeBeatsTheTextBesideIt. A `<time datetime>` says
// August in its attribute and "yesterday" in its text.
func TestAMachineReadableAttributeBeatsTheTextBesideIt(t *testing.T) {
	item := one(t, `
  item "article" {
    property "when" {
      type = date
      css  = ["time"]
    }
  }
`, article)

	if got := item.Text("when"); got != "2026-08-04T09:15:00Z" {
		t.Errorf("got %q, want the datetime attribute rather than the text", got)
	}
}

// TestNestedPropertiesAreLookedForInsideTheirParent, so an author's name comes
// from the byline rather than from whichever name appears first on the page.
func TestNestedPropertiesAreLookedForInsideTheirParent(t *testing.T) {
	const body = `<html><body>
	  <span class="name">Not the author</span>
	  <div class="byline">
	    <span class="name">Alex Doe</span>
	    <a class="profile" href="/people/alex">profile</a>
	  </div>
	</body></html>`

	item := one(t, `
  item "article" {
    property "author" {
      type = object
      css  = [".byline"]

      property "name" {
        type = str
        css  = [".name"]
      }

      property "profile" {
        type       = url
        css        = [".profile"]
        transforms = [absurl]
      }
    }
  }
`, body)

	author, ok := item.Get("author")
	if !ok {
		t.Fatal("the author object was not found")
	}
	if got := author.Nested["name"]; got == nil || got.Text != "Alex Doe" {
		t.Errorf("name = %+v, want the one inside the byline", got)
	}
	if got := author.Nested["profile"]; got == nil || got.Text != "https://example.com/people/alex" {
		t.Errorf("profile = %+v", got)
	}
}

// TestAnEmptyElementIsNotAValue. Saying otherwise would make every fill rate a
// lie.
func TestAnEmptyElementIsNotAValue(t *testing.T) {
	item := one(t, `
  item "thing" {
    property "empty" {
      type = str
      css  = [".empty"]
    }

    property "real" {
      type = str
      css  = [".real"]
    }
  }
`, `<html><body><span class="empty">   </span><span class="real">here</span></body></html>`)

	if _, ok := item.Get("empty"); ok {
		t.Error("an element with nothing in it was reported as a value")
	}
	if item.Text("real") != "here" {
		t.Error("the value beside it was lost")
	}
}

// TestARequiredPropertyThatFoundNothingIsReported. A job whose required
// property stopped matching has broken, and that is worth saying rather than
// exporting a record with a hole in it.
func TestARequiredPropertyThatFoundNothingIsReported(t *testing.T) {
	item := one(t, `
  item "article" {
    property "title" {
      type     = str
      required = true
    }

    property "price" {
      type     = str
      required = true
      css      = [".price"]
    }
  }
`, article)

	if item.Complete() {
		t.Error("an item missing a required property reported itself complete")
	}
	if len(item.Missing) != 1 || item.Missing[0] != "price" {
		t.Errorf("missing = %v", item.Missing)
	}
}

// TestAPageThatIsNotTheShapeProducesNothing, rather than an empty record that
// every downstream count would include.
func TestAPageThatIsNotTheShapeProducesNothing(t *testing.T) {
	result := run(t, `
  item "product" {
    property "price" {
      type = str
      css  = [".price"]
    }

    property "sku" {
      type = str
      css  = [".sku"]
    }
  }
`, article)

	if len(result.Items) != 0 {
		t.Errorf("extracted %d items from a page that has none of them", len(result.Items))
	}
}

// TestLinksAreAbsoluteAndDeduplicated, because whoever queues them should not
// have to know which page they were on.
func TestLinksAreAbsoluteAndDeduplicated(t *testing.T) {
	result := run(t, `
  item "article" {
    property "title" {
      type = str
    }
  }
`, article)

	want := []string{
		"https://example.com/news/2026/other.html",
		"https://elsewhere.example/x",
	}
	if len(result.Links) != len(want) {
		t.Fatalf("links = %v", result.Links)
	}
	for i, link := range want {
		if result.Links[i] != link {
			t.Errorf("link %d = %q, want %q", i, result.Links[i], link)
		}
	}
}

// TestLinksThatCannotBeFetchedAreNotLinks.
func TestLinksThatCannotBeFetchedAreNotLinks(t *testing.T) {
	const body = `<html><body>
	  <a href="mailto:a@example.com">mail</a>
	  <a href="javascript:void(0)">script</a>
	  <a href="tel:+441234">phone</a>
	  <a href="/real">real</a>
	  <a href="">empty</a>
	</body></html>`

	result := run(t, `
  item "thing" {
    property "any" {
      type = str
      css  = ["a"]
    }
  }
`, body)

	if len(result.Links) != 1 || result.Links[0] != "https://example.com/real" {
		t.Errorf("links = %v", result.Links)
	}
}

// TestScriptAndStyleAreNotText. A body that swallowed the analytics snippet is
// a body nobody can classify.
func TestScriptAndStyleAreNotText(t *testing.T) {
	const body = `<html><body>
	  <style>.a{color:red}</style>
	  <script>var tracking = "gtm";</script>
	  <p class="real">Actual words.</p>
	</body></html>`

	item := one(t, `
  item "thing" {
    property "any" {
      type    = str
      regexes = ["(gtm|Actual words)"]
    }
  }
`, body)

	if got := item.Text("any"); got != "Actual words" {
		t.Errorf("got %q; the script's contents were treated as text", got)
	}
}

// TestBlockElementsEndALine, or "one<div>two</div>" reads as "onetwo" and every
// extracted body is a run-on sentence.
func TestBlockElementsEndALine(t *testing.T) {
	item := one(t, `
  item "thing" {
    property "body" {
      type       = str
      css        = [".body"]
      transforms = [normalise_space]
    }
  }
`, `<html><body><div class="body"><p>One.</p><p>Two.</p></div></body></html>`)

	if got := item.Text("body"); got != "One. Two." {
		t.Errorf("got %q", got)
	}
}

// TestABrokenLocatorIsRefusedWhenTheExtractorIsBuilt, not on whichever page
// first reaches it. A crawl that ran for an hour before finding out a selector
// was misspelt has wasted an hour and somebody's bandwidth.
func TestABrokenLocatorIsRefusedWhenTheExtractorIsBuilt(t *testing.T) {
	for name, items := range map[string]string{
		"css":    "\n  item \"a\" {\n    property \"p\" {\n      type = str\n      css = [\"div[\"]\n    }\n  }\n",
		"xpath":  "\n  item \"a\" {\n    property \"p\" {\n      type = str\n      xpath = [\"//div[\"]\n    }\n  }\n",
		"regex":  "\n  item \"a\" {\n    property \"p\" {\n      type = str\n      regexes = [\"(unclosed\"]\n    }\n  }\n",
		"nested": "\n  item \"a\" {\n    property \"p\" {\n      type = object\n\n      property \"q\" {\n        type = str\n        css = [\"div[\"]\n      }\n    }\n  }\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := extract.New(spec(t, items))
			if err == nil {
				t.Fatal("a locator that is not one was accepted")
			}
			if !strings.Contains(err.Error(), "a.p") && !strings.Contains(err.Error(), "p.q") {
				t.Errorf("the error does not say which property: %v", err)
			}
		})
	}
}

func TestAnExtractorNeedsASpec(t *testing.T) {
	if _, err := extract.New(nil); err == nil {
		t.Error("built an extractor with nothing to extract")
	}
}

// TestMalformedMarkupIsStillAPage. Half the web is malformed, and a crawler
// that refused it would be a crawler for a web that does not exist.
func TestMalformedMarkupIsStillAPage(t *testing.T) {
	item := one(t, `
  item "thing" {
    property "title" {
      type = str
    }
  }
`, `<html><body><h1>Unclosed<p>and more`)

	if got := item.Text("title"); got == "" {
		t.Error("nothing was extracted from malformed markup")
	}
}

// TestBrokenJSONLDIsNotAFailedPage: malformed JSON-LD is extremely common, and
// it is one of four ways to find a value.
func TestBrokenJSONLDIsNotAFailedPage(t *testing.T) {
	const body = `<html><head>
	  <script type="application/ld+json">{"headline": not json}</script>
	  <meta property="og:title" content="From Open Graph">
	</head><body></body></html>`

	item := one(t, `
  item "article" {
    property "title" {
      type = str
    }
  }
`, body)

	if got := item.Text("title"); got != "From Open Graph" {
		t.Errorf("got %q; broken JSON-LD stopped the other ways from being tried", got)
	}
}

func TestTheSpecComesBack(t *testing.T) {
	s := spec(t, "\n  item \"a\" {\n    property \"p\" {\n      type = str\n    }\n  }\n")

	e, err := extract.New(s)
	if err != nil {
		t.Fatal(err)
	}
	if e.Spec().Fingerprint() != s.Fingerprint() {
		t.Error("the extractor reports a different spec from the one it was built with")
	}
}

func TestDatesInWhateverShapeThePageWroteThem(t *testing.T) {
	for name, tc := range map[string]struct{ in, want string }{
		"rfc3339":    {"2026-08-04T09:15:00Z", "2026-08-04T09:15:00Z"},
		"date only":  {"2026-08-04", "2026-08-04T00:00:00Z"},
		"slashes":    {"2026/08/04", "2026-08-04T00:00:00Z"},
		"long":       {"4 August 2026", "2026-08-04T00:00:00Z"},
		"american":   {"August 4, 2026", "2026-08-04T00:00:00Z"},
		"http date":  {"Tue, 04 Aug 2026 09:15:00 GMT", "2026-08-04T09:15:00Z"},
		"epoch":      {"1785835200", "2026-08-04T09:20:00Z"},
		"unreadable": {"yesterday", "yesterday"},
		"page count": {"812", "812"},
	} {
		t.Run(name, func(t *testing.T) {
			item := one(t, `
  item "thing" {
    property "when" {
      type       = date
      css        = [".when"]
      transforms = [datetime]
    }
  }
`, `<html><body><span class="when">`+tc.in+`</span></body></html>`)

			if got := item.Text("when"); got != tc.want {
				t.Errorf("%q became %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
