// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// article is one story, in the feed and on its own page.
type article struct{ slug, title, author, date, summary string }

var stories = []article{
	{"harbour-plan-approved", "Harbour plan approved after long inquiry",
		"Jane Okafor", "Mon, 14 Jul 2025 09:12:00 GMT",
		"Councillors backed the scheme by a single vote."},
	{"rail-strike-called-off", "Rail strike called off hours before deadline",
		"Tomas Lindqvist", "Mon, 14 Jul 2025 07:40:00 GMT",
		"Union leaders accepted a revised pay offer overnight."},
	{"river-levels-fall", "River levels fall for a third straight week",
		"Priya Raman", "Sun, 13 Jul 2025 18:05:00 GMT",
		"The drop eases pressure on the upstream reservoirs."},
}

// newsroom serves an RSS feed and the articles it links to, which is the shape
// of nearly every news source worth crawling: one URL that is a list of URLs,
// and the thing you actually want one hop further on.
//
// The feed is ordinary RSS 2.0. Each item names its article in a <link>
// element, because that is where RSS puts it, and nowhere in the feed is there
// an <a href>. That is the whole difficulty, and inventing an <a href> here to
// make the crawl succeed would be testing a page no news site serves.
func newsroom(t *testing.T) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()
	var srv *httptest.Server

	mux.HandleFunc("/feed.xml", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
		var b strings.Builder
		b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
		b.WriteString(`<rss version="2.0" xmlns:dc="http://purl.org/dc/elements/1.1/">` + "\n")
		b.WriteString("<channel>\n<title>The Gazette</title>\n")
		fmt.Fprintf(&b, "<link>%s/</link>\n", srv.URL)
		b.WriteString("<description>Local news</description>\n")
		for _, a := range stories {
			b.WriteString("<item>\n")
			fmt.Fprintf(&b, "<title>%s</title>\n", a.title)
			fmt.Fprintf(&b, "<link>%s/news/%s</link>\n", srv.URL, a.slug)
			fmt.Fprintf(&b, "<guid>%s/news/%s</guid>\n", srv.URL, a.slug)
			fmt.Fprintf(&b, "<dc:creator>%s</dc:creator>\n", a.author)
			fmt.Fprintf(&b, "<pubDate>%s</pubDate>\n", a.date)
			fmt.Fprintf(&b, "<description>%s</description>\n", a.summary)
			b.WriteString("</item>\n")
		}
		b.WriteString("</channel>\n</rss>\n")
		fmt.Fprint(w, b.String())
	})

	for _, a := range stories {
		mux.HandleFunc("/news/"+a.slug, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprintf(w, `<html><head>
<meta property="og:title" content="%s">
<meta property="og:description" content="%s">
<meta property="article:published_time" content="2025-07-14T09:12:00Z">
</head><body>
<article>
  <h1 class="headline">%s</h1>
  <div class="byline">By <a rel="author" href="/staff/x">%s</a></div>
  <time datetime="2025-07-14T09:12:00Z">14 July 2025</time>
  <p class="standfirst">%s</p>
  <p>The full report runs to some hundreds of words which need not be repeated.</p>
</article>
</body></html>`, a.title, a.summary, a.title, a.author, a.summary)
		})
	}

	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// newsDir points an article item at a feed. The template carries the property
// names, examples and descriptions, which is how an operator would start.
func newsDir(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	dir := crawlDir(t)
	runOK(t, dir, "item", "add", "article", "--template", "article")
	runOK(t, dir, "item", "add", "article", "-u", srv.URL+"/feed.xml")
	runOK(t, dir, "item", "add", "article", "--type", "feed", "--type", "html")
	return dir
}

// A feed is a list of URLs. Fetching one and stopping there is not crawling it.
func TestCrawlingAFeedReachesItsArticles(t *testing.T) {
	srv := newsroom(t)
	dir := newsDir(t, srv)

	out := runOK(t, dir, "start", "article", "--depth", "5")
	t.Logf("start article:\n%s", out)

	// The feed, and the three articles it names.
	if got := visitedCount(t, dir, "article"); got < len(stories)+1 {
		t.Fatalf("visited %d pages, want %d: the feed plus the %d articles it links to",
			got, len(stories)+1, len(stories))
	}
}

// Having reached the articles, the point is to get records out of them.
func TestExtractingRecordsFromNewsArticles(t *testing.T) {
	srv := newsroom(t)
	dir := newsDir(t, srv)

	runOK(t, dir, "start", "article", "--depth", "5")

	out, err := run(t, dir, "model", "train", "article")
	if err != nil {
		t.Fatalf("model train: %v\n%s", err, out)
	}
	t.Logf("model train:\n%s", out)

	records := runOK(t, dir, "record", "ls", "article")
	t.Logf("record ls:\n%s", records)

	for _, a := range stories {
		head := strings.SplitN(a.title, " ", 4)
		want := strings.Join(head[:3], " ")
		if !strings.Contains(records, want) {
			t.Errorf("no record carries %q:\n%s", want, records)
		}
	}

	// Both shapes contribute. The titles above could be satisfied by the feed
	// alone, and were until induction learned to model more than one format.
	for _, format := range []string{"feed", "html"} {
		if !strings.Contains(records, format) {
			t.Errorf("nothing was extracted from the %s pages:\n%s", format, records)
		}
	}
}

// The whole reason to follow a feed to its articles is that the article page
// carries what the feed's summary leaves out.
//
// This did not work while rules were induced per item rather than per format.
// One corpus got one rule set, and a corpus holding both a feed and the HTML it
// links to got rules for whichever shape induction found first: three <item>
// elements repeating inside one document beats three pages sharing a template,
// so the feed won and its XPaths matched nothing in an article. Three fetched
// articles yielded zero records, which is the ordinary shape of a news crawl
// failing rather than a corner case.
func TestArticlePagesYieldRecordsOfTheirOwn(t *testing.T) {
	srv := newsroom(t)
	dir := newsDir(t, srv)

	runOK(t, dir, "start", "article", "--depth", "5")
	if out, err := run(t, dir, "model", "train", "article"); err != nil {
		t.Fatalf("model train: %v\n%s", err, out)
	}

	fromPages := runOK(t, dir, "record", "ls", "article", "-t", "html")
	t.Logf("records from the article pages:\n%s", fromPages)
	if strings.Contains(fromPages, "no records") {
		t.Errorf("the three article pages produced no records of their own:\n%s", fromPages)
	}
	for _, a := range stories {
		if !strings.Contains(fromPages, a.author) {
			t.Errorf("no article-page record carries the byline %q:\n%s", a.author, fromPages)
		}
	}
}

// The feed itself is a record source, not only a list of links: an RSS item
// already carries a title, an author, a date and a summary. Extracting those
// works today and is what makes the missing link discovery the only gap.
func TestTheFeedItselfYieldsRecords(t *testing.T) {
	srv := newsroom(t)
	dir := newsDir(t, srv)

	runOK(t, dir, "start", "article", "--depth", "1")
	if out, err := run(t, dir, "model", "train", "article"); err != nil {
		t.Fatalf("model train: %v\n%s", err, out)
	}

	records := runOK(t, dir, "record", "ls", "article", "-t", "feed")
	t.Logf("records from the feed:\n%s", records)

	if strings.Contains(records, "no records") {
		t.Fatalf("the feed produced no records at all:\n%s", records)
	}
}
