// SPDX-License-Identifier: GPL-3.0-or-later

package e2e

import (
	"fmt"
	"html"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// PublishEvery is how often the live section publishes another article.
//
// It is a variable rather than a constant because a test that waits for real
// minutes is a test nobody runs. Set it low, or call [PublishNow], which is
// deterministic and does not wait at all.
var PublishEvery = 30 * time.Second

// wire is the live section: a newsroom that keeps publishing.
//
// Everything else in this fixture is a site that has already happened. This one
// changes while you are crawling it, which is the case a frontier, a refresh
// policy and a recrawl all exist for and none of them can be exercised against
// a corpus that sits still. An article that appeared after the crawl started is
// the thing a second run has to find, and the feed growing is how it finds out.
type wire struct {
	mu      sync.Mutex
	started time.Time
	forced  int
}

var live = &wire{started: time.Now()}

// PublishNow publishes one more article immediately, so a test can advance the
// newsroom without sleeping through the schedule.
func PublishNow() int {
	live.mu.Lock()
	defer live.mu.Unlock()
	live.forced++
	return live.count()
}

// ResetLive puts the newsroom back to one article, so a test starts from a
// known place.
func ResetLive() {
	live.mu.Lock()
	defer live.mu.Unlock()
	live.started = time.Now()
	live.forced = 0
}

// count is how many articles exist now: one at the start, one more per
// interval elapsed, and one more for each forced publication.
//
// Derived from the clock rather than stored, so the section does not need a
// goroutine writing into it and a test that changes the interval sees the
// effect at once.
func (n *wire) count() int {
	published := 1 + n.forced
	if PublishEvery > 0 {
		published += int(time.Since(n.started) / PublishEvery)
	}
	if published > liveMax {
		published = liveMax
	}
	return published
}

func (n *wire) published() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.count()
}

// liveMax bounds the section. A newsroom that publishes forever is a crawl that
// never ends, which is a fixture nobody can write an assertion about.
const liveMax = 40

// liveHeadlines are cycled through, so an article's text is a function of its
// number and the section is reproducible across restarts.
var liveHeadlines = []string{
	"Pilot boat returns to service",
	"Tide gates fitted at the north berth",
	"Crane inspection closes berth three",
	"Ferry timetable changes from Monday",
	"Dredger arrives ahead of schedule",
	"Harbour board meets in public session",
	"Fuel barge delayed by weather",
	"New moorings open to small craft",
}

var liveAuthors = []string{"Ana Duarte", "Marcus Iyer", "Priya Raman", "Tomas Lindqvist"}

// registerLive serves the newsroom, its articles and its growing feed.
func registerLive(mux *http.ServeMux) {
	mux.HandleFunc("GET /live/", liveIndex)
	mux.HandleFunc("GET /live/feed.xml", liveFeed)
	mux.HandleFunc("GET /live/{n}", liveArticle)
}

func liveIndex(w http.ResponseWriter, _ *http.Request) {
	n := live.published()
	var b strings.Builder
	b.WriteString(`<!-- The live section. It publishes another article every PublishEvery, so
     the page you are reading is not the page a crawl saw a minute ago. This is
     the only part of the fixture that changes by itself. -->
<html lang="en"><head><meta charset="utf-8"><title>Live wire</title>
<link rel="alternate" type="application/rss+xml" title="Live wire" href="/live/feed.xml">
</head><body>
<h1>Live wire</h1>
`)
	fmt.Fprintf(&b, "<p>%d published so far, one every %s.</p>\n<ul>\n", n, PublishEvery)
	// Newest first, as a wire is read.
	for i := n; i >= 1; i-- {
		fmt.Fprintf(&b, "  <li><a href=\"/live/%d\">%s</a></li>\n", i, html.EscapeString(liveTitle(i)))
	}
	b.WriteString("</ul>\n<p><a href=\"/live/feed.xml\">The feed</a> grows with it.</p>\n")
	b.WriteString(`<nav class="crosslinks">
  <a href="` + BaseToken + `/search?q=harbour">Search (absolute)</a>
  <a href="/feeds/rss.xml">The static feed (root-relative)</a>
  <a href="../files/report.pdf">The report PDF (relative)</a>
</nav>
</body></html>`)
	writeHTML(w, b.String())
}

func liveArticle(w http.ResponseWriter, r *http.Request) {
	i, err := strconv.Atoi(r.PathValue("n"))
	if err != nil || i < 1 {
		http.NotFound(w, r)
		return
	}
	// An article that has not been published yet is genuinely not there, which
	// is what makes a second crawl find something a first could not.
	if i > live.published() {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprintf(w, `<html><head><meta charset="utf-8"><title>Not published yet</title></head><body>
<h1>Not published yet</h1>
<p>Article %d has not been written. <a href="/live/">The wire</a> has %d.</p>
</body></html>`, i, live.published())
		return
	}

	title := liveTitle(i)
	author := liveAuthors[(i-1)%len(liveAuthors)]
	when := live.publishedAt(i)

	writeHTML(w, fmt.Sprintf(`<!-- One live article. Its number is its identity: the text is a function of
     it, so the same number is the same article on every restart. -->
<html lang="en"><head>
<meta charset="utf-8">
<title>%s</title>
<meta property="og:title" content="%s">
<meta property="article:published_time" content="%s">
<link rel="canonical" href="%s/live/%d">
</head><body>
<article>
  <h1 class="headline">%s</h1>
  <div class="byline">By <a rel="author" href="/people/jane-okafor.html">%s</a></div>
  <time datetime="%s">%s</time>
  <p class="standfirst">Filed from the harbour office, item %d on the wire.</p>
  <p>The harbourmaster confirmed the change this morning.</p>
</article>
<nav>
  <a href="/live/">The wire</a>
  <a href="/live/feed.xml">Feed</a>
  <a href="%d">Previous item</a>
</nav>
</body></html>`, html.EscapeString(title), html.EscapeString(title), when, BaseToken, i,
		html.EscapeString(title), author, when, when, i, maxInt(1, i-1)))
}

// liveFeed is the wire as RSS, and it grows exactly as the section does.
func liveFeed(w http.ResponseWriter, _ *http.Request) {
	n := live.published()
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!-- The live feed. Every item here is an article that exists; the feed is how a
     crawler finds out that one appeared without polling every article URL. -->
<rss version="2.0" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:atom="http://www.w3.org/2005/Atom">
<channel>
`)
	fmt.Fprintf(&b, "  <atom:link href=\"%s/live/feed.xml\" rel=\"self\" type=\"application/rss+xml\"/>\n", BaseToken)
	b.WriteString("  <title>Live wire</title>\n  <link>/live/</link>\n")
	b.WriteString("  <description>Published on a schedule</description>\n")
	fmt.Fprintf(&b, "  <lastBuildDate>%s</lastBuildDate>\n", live.publishedAt(n))

	// Newest first, and bounded, because a feed is a window rather than an
	// archive and a crawler that reads it as an archive will miss the drop-off.
	for i := n; i >= 1 && i > n-20; i-- {
		fmt.Fprintf(&b, `  <item>
    <title>%s</title>
    <link>%s/live/%d</link>
    <guid isPermaLink="true">%s/live/%d</guid>
    <dc:creator>%s</dc:creator>
    <pubDate>%s</pubDate>
    <description>Filed from the harbour office, item %d on the wire.</description>
  </item>
`, html.EscapeString(liveTitle(i)), BaseToken, i, BaseToken, i,
			liveAuthors[(i-1)%len(liveAuthors)], live.publishedAtRFC(i), i)
	}
	b.WriteString("</channel>\n</rss>\n")

	w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
	_, _ = w.Write([]byte(b.String()))
}

func liveTitle(i int) string {
	return fmt.Sprintf("%s (item %d)", liveHeadlines[(i-1)%len(liveHeadlines)], i)
}

// publishedAt is when item i went out, derived from the interval so the dates
// march forward the way a wire's do.
func (n *wire) publishedAt(i int) string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.started.Add(time.Duration(i-1) * PublishEvery).UTC().Format(time.RFC3339)
}

func (n *wire) publishedAtRFC(i int) string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.started.Add(time.Duration(i-1) * PublishEvery).UTC().Format(time.RFC1123Z)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
