// SPDX-License-Identifier: MIT

package wom_test

import (
	"testing"

	"github.com/rangertaha/scour/internal/wom"
)

// The three ways a news page declares what it is: OpenGraph meta tags,
// schema.org microdata attributes, and a canonical link. Real pages carry some
// mixture of them, which is why the schema aliases cover all three.
const structured = `<!doctype html><html><head>
<title>Council approves new transit line - The Example Times</title>
<meta property="og:title" content="Council approves new transit line">
<meta property="og:site_name" content="The Example Times">
<meta property="og:description" content="The vote clears the way for construction.">
<meta property="article:published_time" content="2026-03-14T09:00:00Z">
<meta property="article:section" content="Politics">
<meta name="author" content="Jane Doe">
</head><body>
<article itemscope itemtype="https://schema.org/NewsArticle">
  <h1 itemprop="headline">Council approves new transit line</h1>
  <span itemprop="author">Jane Doe</span>
  <time itemprop="datePublished" datetime="2026-03-14">14 March 2026</time>
  <div itemprop="articleSection">Politics</div>
  <p itemprop="description">The vote clears the way for construction.</p>
</article>
</body></html>`

func microdataSchema() wom.Prop {
	return wom.Prop{Name: "microdata", Props: []wom.Prop{
		{Name: "headline", Aliases: []string{"og:title", "title"}},
		{Name: "author", Aliases: []string{"byline", "creator"}},
		{Name: "datePublished", Aliases: []string{"article:published_time", "pubdate"}},
		{Name: "articleSection", Aliases: []string{"article:section", "section"}},
		{Name: "description", Aliases: []string{"og:description", "summary"}},
	}}
}

// A page that publishes structured data about itself has already answered the
// question extraction is trying to answer, so the fields it declares should be
// the easiest ones in the whole system to locate.
func TestMicrodataIsLocated(t *testing.T) {
	w := wom.New()
	if err := w.AddBody("https://example.com/news/transit/", "text/html", []byte(structured)); err != nil {
		t.Fatal(err)
	}

	items, err := w.Schema(microdataSchema())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) == 0 {
		t.Fatal("nothing located on a page full of declared structure")
	}

	found := map[string]bool{}
	for _, item := range items {
		for _, child := range item.Items {
			found[child.Name] = true
		}
	}
	for _, want := range []string{"headline", "author", "datePublished"} {
		if !found[want] {
			t.Errorf("%s was not located; found %v", want, found)
		}
	}
}

func TestMicrodataExtraction(t *testing.T) {
	w := wom.New()
	if err := w.AddBody("https://example.com/news/transit/", "text/html", []byte(structured)); err != nil {
		t.Fatal(err)
	}

	model, err := w.Model(microdataSchema())
	if err != nil {
		t.Fatal(err)
	}

	values := map[string]string{}
	for _, rec := range model.Extract(w) {
		for _, field := range rec.Items {
			if field.Value != "" {
				values[field.Name] = field.Value
			}
		}
	}

	if len(values) == 0 {
		t.Fatal("no values extracted")
	}
	if got := values["headline"]; got != "" && got != "Council approves new transit line" {
		t.Errorf("headline = %q", got)
	}
	t.Logf("extracted %v", values)
}
