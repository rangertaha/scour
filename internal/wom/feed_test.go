// SPDX-License-Identifier: MIT

package wom_test

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
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

// The BBC publishes lastBuildDate on the channel and nothing like it on an
// <item>, so a `modified` property matches once, correctly, above every record.
// Unlike the Guardian's case there is no rival reading inside the item to
// prefer, so the field has to be judged by its absence instead: setting it aside
// lets the record repeat, which is what says it was never part of one.
const feedWithChannelDate = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0">
  <channel>
    <title>Example News</title>
    <link>https://example.com/</link>
    <description>All the news</description>
    <lastBuildDate>Wed, 15 Mar 2026 08:00:00 GMT</lastBuildDate>
    <item>
      <title>Council approves transit line</title>
      <link>https://example.com/transit/</link>
      <pubDate>Tue, 14 Mar 2026 09:00:00 GMT</pubDate>
      <description>The vote clears the way for construction.</description>
    </item>
    <item>
      <title>Budget passes on third reading</title>
      <link>https://example.com/budget/</link>
      <pubDate>Tue, 14 Mar 2026 11:30:00 GMT</pubDate>
      <description>A narrow margin after two hours of debate.</description>
    </item>
    <item>
      <title>Ferry service resumes</title>
      <link>https://example.com/ferry/</link>
      <pubDate>Wed, 15 Mar 2026 07:15:00 GMT</pubDate>
      <description>Sailings return to the summer timetable.</description>
    </item>
  </channel>
</rss>`

// countRecords extracts body against the given schema and returns the records.
func countRecords(t *testing.T, body string, prop wom.Prop) int {
	t.Helper()
	w := wom.New()
	if err := w.AddBody("https://example.com/feed/", "application/rss+xml", []byte(body)); err != nil {
		t.Fatal(err)
	}
	model, err := w.Model(prop)
	if err != nil {
		t.Fatal(err)
	}
	return len(model.Extract(w))
}

// The Guardian's feed carries an <image> describing its own logo, with a
// <title>, a <url> and a <link> inside it. Those outscored the summary and link
// sitting in every <item>, so two fields lived under <image> and the rest under
// <item>, and the only ancestor holding all of them was the channel: one record
// where there were forty-five.
//
// Neither field was uncertain, so dropping weak matches did not touch them, and
// neither could be dropped alone, because removing one leaves the other outside
// the item. They are wrong only together, and only once there is a container to
// judge them against. So a field keeps its rival locations until the container
// is chosen.
//
// The fixture is the real feed rather than an imitation of it. Every attempt to
// write a small one by hand extracted correctly, because what makes the logo win
// is the particular wording of the Guardian's own title against the particular
// shape of its item descriptions, and that is not something worth guessing at.
func TestChannelFurnitureDoesNotWidenTheRecord(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("testdata", "guardian-world-rss.xml"))
	if err != nil {
		t.Fatal(err)
	}
	prop := wom.Prop{Name: "article", Props: []wom.Prop{
		{Name: "title", Aliases: []string{"heading"},
			Description: "the article's title, which a feed publishes as <title>",
			Examples:    []string{"Council approves new transit line"}},
		{Name: "published", Type: wom.TypeDate,
			Aliases:     []string{"date", "date published", "posted", "pubdate", "dc:date"},
			Description: "when it was published", Examples: []string{"2026-03-14"}},
		{Name: "section", Aliases: []string{"category", "topic", "kicker"},
			Description: "where it sits in the publication",
			Examples:    []string{"Politics", "Business"}},
		{Name: "summary",
			Aliases:     []string{"standfirst", "excerpt", "description", "lede", "content", "subtitle"},
			Description: "a one or two sentence summary of the article",
			Examples:    []string{"The vote clears the way for construction to begin in autumn."}},
		{Name: "link", Aliases: []string{"url", "guid", "permalink", "href"},
			Description: "the address of the article itself",
			Examples:    []string{"https://www.example.com/news/transit-line/"}},
	}}

	w := wom.New()
	if err := w.AddBody("https://www.theguardian.com/world/rss", "application/rss+xml", body); err != nil {
		t.Fatal(err)
	}
	model, err := w.Model(prop)
	if err != nil {
		t.Fatal(err)
	}

	const items = 8
	if got := len(model.Extract(w)); got != items {
		t.Errorf("extracted %d records, want %d (the channel's <image> widened the container)", got, items)
	}
}

func TestAChannelOnlyFieldDoesNotWidenTheRecord(t *testing.T) {
	prop := wom.Prop{Name: "article", Props: []wom.Prop{
		{Name: "title"},
		{Name: "published", Aliases: []string{"pubdate"}, Type: wom.TypeDate},
		{Name: "summary", Aliases: []string{"description"}},
		{Name: "link"},
		{Name: "modified", Aliases: []string{"lastBuildDate", "updated"}, Type: wom.TypeDate},
	}}
	if got := countRecords(t, feedWithChannelDate, prop); got != 3 {
		t.Errorf("extracted %d records, want 3 (a channel-level date widened the container)", got)
	}
}

// fieldValues extracts body and returns, per field name, the distinct values.
func fieldValues(t *testing.T, body string, prop wom.Prop) map[string][]string {
	t.Helper()
	w := wom.New()
	if err := w.AddBody("https://example.com/feed/", "application/rss+xml", []byte(body)); err != nil {
		t.Fatal(err)
	}
	model, err := w.Model(prop)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]string{}
	for _, rec := range model.Extract(w) {
		for _, f := range rec.Items {
			if f.Value != "" {
				out[f.Name] = append(out[f.Name], f.Value)
			}
		}
	}
	return out
}

// A namespace declaration is machinery, not data, and it used to beat the value
// it annotates. The parser hangs an xmlns node holding
// "http://purl.org/dc/elements/1.1/" off every <dc:creator>, and since a
// namespaced element lends its name to what is inside it, that URI is labelled
// "creator" exactly as the byline is. The two scored the same and the attribute
// won the tie by sorting before text().
func TestANamespaceIsNotAByline(t *testing.T) {
	prop := wom.Prop{Name: "article", Props: []wom.Prop{
		{Name: "title"},
		{Name: "author", Aliases: []string{"creator", "byline"}},
		{Name: "published", Aliases: []string{"pubdate"}, Type: wom.TypeDate},
		{Name: "summary", Aliases: []string{"description"}},
	}}

	got := fieldValues(t, feed, prop)
	if len(got["author"]) == 0 {
		t.Fatalf("no author extracted; got fields %v", keysOf(got))
	}
	for _, v := range got["author"] {
		if strings.Contains(v, "://") {
			t.Errorf("author came back as a namespace URI: %q", v)
		}
	}
	if got["author"][0] != "Jane Doe" {
		t.Errorf("author = %q, want %q", got["author"][0], "Jane Doe")
	}
}

func keysOf(m map[string][]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
