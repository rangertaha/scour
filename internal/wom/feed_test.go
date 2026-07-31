// SPDX-License-Identifier: MIT

package wom_test

import (
	"testing"

	"github.com/rangertaha/scour/internal/wom"
)

// A cut-down RSS feed with the elements every real one has.
const feed = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:dc="http://purl.org/dc/elements/1.1/">
  <channel>
    <title>Example News</title>
    <link>https://example.com/</link>
    <description>All the news</description>
    <item>
      <title>Council approves transit line</title>
      <link>https://example.com/transit/</link>
      <pubDate>Tue, 14 Mar 2026 09:00:00 GMT</pubDate>
      <dc:creator>Jane Doe</dc:creator>
      <description>The vote clears the way for construction.</description>
    </item>
    <item>
      <title>Budget passes on third reading</title>
      <link>https://example.com/budget/</link>
      <pubDate>Tue, 14 Mar 2026 11:30:00 GMT</pubDate>
      <dc:creator>John Roe</dc:creator>
      <description>A narrow margin after two hours of debate.</description>
    </item>
    <item>
      <title>Ferry service resumes</title>
      <link>https://example.com/ferry/</link>
      <pubDate>Wed, 15 Mar 2026 07:15:00 GMT</pubDate>
      <dc:creator>Jane Doe</dc:creator>
      <description>Sailings return to the summer timetable.</description>
    </item>
  </channel>
</rss>`

func feedArticleSchema() wom.Prop {
	return wom.Prop{Name: "article", Props: []wom.Prop{
		{Name: "headline", Aliases: []string{"title"}},
		{Name: "published", Aliases: []string{"pubdate"}},
		{Name: "author", Aliases: []string{"creator"}},
		{Name: "summary", Aliases: []string{"description"}},
	}}
}

// In XML the element name is the label, and for a long time it was the one
// thing the matcher did not look at.
//
// Every field in an RSS item is named by its element and by nothing else: there
// is no class, no id, no itemprop, and no "Label: value" text to split. Reading
// only the attributes meant a feed produced no locators at all, which made the
// whole of RSS and Atom unextractable.
func TestFeedFieldsAreLocatedByElementName(t *testing.T) {
	w := wom.New()
	if err := w.AddBody("https://example.com/feed/", "application/rss+xml", []byte(feed)); err != nil {
		t.Fatal(err)
	}

	items, err := w.Schema(feedArticleSchema())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) == 0 {
		t.Fatal("nothing located in a feed")
	}

	found := map[string]string{}
	for _, item := range items {
		for _, child := range item.Items {
			found[child.Name] = child.Locator.XPath
		}
	}

	for _, want := range []string{"headline", "published", "author", "summary"} {
		if found[want] == "" {
			t.Errorf("%s was not located; located %v", want, found)
		}
	}
}

// Locating the fields is only half of it: the values have to come back.
func TestFeedExtraction(t *testing.T) {
	w := wom.New()
	if err := w.AddBody("https://example.com/feed/", "application/rss+xml", []byte(feed)); err != nil {
		t.Fatal(err)
	}

	model, err := w.Model(feedArticleSchema())
	if err != nil {
		t.Fatal(err)
	}

	records := model.Extract(w)
	if len(records) == 0 {
		t.Fatal("no records extracted from a feed")
	}

	var headlines []string
	for _, rec := range records {
		for _, field := range rec.Items {
			if field.Name == "headline" && field.Value != "" {
				headlines = append(headlines, field.Value)
			}
		}
	}
	if len(headlines) == 0 {
		t.Fatalf("no headlines among %d records", len(records))
	}

	// The channel's own title is not an article, so a headline that is only
	// the feed's name means the container was located one level too high.
	for _, h := range headlines {
		if h == "Example News" {
			t.Errorf("the channel title was extracted as an article headline")
		}
	}
}
