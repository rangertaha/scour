// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/rangertaha/scour/e2e"
)

// The fixture site is the corpus that has actually broken extraction, served
// locally. These tests drive the real command line against it and record what
// scour does, which on several pages is the wrong thing.
//
// Where the answer is wrong the assertion says so and pins today's behaviour,
// with a comment naming the right answer. A test that documents a wrong answer
// keeps the fault visible and fails the day it changes, in either direction. A
// test weakened until it passed would only hide it.

// e2eDir prepares a data directory for a crawl against the fixture.
//
// It is crawlDir's settings plus one: the browser is off. The fixture serves a
// page whose content is written by script, and escalating to a real browser for
// it cost between four and thirteen seconds a run. What the browser does is
// worth testing and is not what any of these tests are about.
func e2eDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	body := "[crawl]\nrate = \"0s\"\nrobots = false\nconcurrency = 4\n\n[browser]\nenabled = false\n"
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// everyType is every content type scour claims to be able to read. The fixture
// serves all of them, so a crawl that names fewer is choosing not to look.
//
// xml is in the list because without it the feeds are not fetched at all: see
// TestFixtureFeedsAreSkippedUnlessXMLIsAllowed for why that is a fault rather
// than a setting.
var everyType = []string{"html", "feed", "xml", "pdf", "json", "text", "image"}

// addTypes tells an item which content types its crawl may traverse.
func addTypes(t *testing.T, dir, item string, types []string) {
	t.Helper()
	args := []string{"item", "add", item}
	for _, ty := range types {
		args = append(args, "--type", ty)
	}
	runOK(t, dir, args...)
}

// e2ePage is one row of `scour job log --json`: what a crawl did with one URL.
type e2ePage struct {
	URL         string
	Status      string
	StatusCode  int
	ContentType string
}

// fetchedPages reads a job's last run and returns its pages keyed by path,
// including the query string, which is what tells /search?q=harbour apart from
// the bare /search the site never links to.
func fetchedPages(t *testing.T, dir, job string) map[string]e2ePage {
	t.Helper()
	var log struct {
		Pages []e2ePage `json:"pages"`
	}
	out := runOK(t, dir, "--json", "job", "log", job)
	if err := json.Unmarshal([]byte(out), &log); err != nil {
		t.Fatalf("job log --json: %v\n%s", err, out)
	}

	pages := make(map[string]e2ePage, len(log.Pages))
	for _, p := range log.Pages {
		u, err := url.Parse(p.URL)
		if err != nil {
			t.Fatalf("crawled url %q does not parse: %v", p.URL, err)
		}
		pages[u.RequestURI()] = p
	}
	return pages
}

// e2eRecord is one row of `scour record ls --json`.
type e2eRecord struct {
	Format     string
	Confidence float64
	URL        string
	Values     map[string]string
}

// allExtracted reads an item's records in the order the listing gives them.
func allExtracted(t *testing.T, dir, item string) []e2eRecord {
	t.Helper()
	var rows []e2eRecord
	out := runOK(t, dir, "--json", "record", "ls", item)
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("record ls --json: %v\n%s", err, out)
	}
	return rows
}

// extracted keys an item's records by the path of the page each came out of.
//
// Only useful where one page yields one record, which is every HTML page on
// this site and none of the feeds: a feed is one URL holding several items, so
// its records all share a key and all but one would be lost.
func extracted(t *testing.T, dir, item string) map[string]e2eRecord {
	t.Helper()
	byPath := map[string]e2eRecord{}
	for _, r := range allExtracted(t, dir, item) {
		u, err := url.Parse(r.URL)
		if err != nil {
			t.Fatalf("record url %q does not parse: %v", r.URL, err)
		}
		byPath[u.RequestURI()] = r
	}
	return byPath
}

// paths lists what was fetched, sorted, for a failure message that says what
// the crawl did see rather than only what it missed.
func paths(pages map[string]e2ePage) string {
	out := make([]string, 0, len(pages))
	for p := range pages {
		out = append(out, p)
	}
	sort.Strings(out)
	return strings.Join(out, "\n")
}

// crawlWholeSite points an item at the fixture's front page and lets it run.
//
// Depth 3 is what reaches every section that is linked from the root: 47 pages
// of the fixture's 52, in about a second. The five it leaves are the second
// page of a search, two more parts of the long-form story and the long-form
// index, none of which say anything the first of each does not.
func crawlWholeSite(t *testing.T) string {
	t.Helper()
	srv := e2e.Server(t)
	dir := e2eDir(t)

	runOK(t, dir, "item", "add", "gazette", "--template", "article")
	runOK(t, dir, "item", "add", "gazette", "-u", srv.URL+"/")
	addTypes(t, dir, "gazette", everyType)
	runOK(t, dir, "start", "gazette", "--depth", "3")
	return dir
}

// One crawl, many questions. Starting the site and walking it costs about a
// second, and asking it seven things costs nothing, so the subtests share one.
func TestFixtureSiteCrawl(t *testing.T) {
	dir := crawlWholeSite(t)
	pages := fetchedPages(t, dir, "gazette")

	t.Run("reaches every section linked from the root", func(t *testing.T) {
		// One page from each section, named rather than counted, so a failure
		// says which part of the site went dark.
		for _, want := range []string{
			"/news/", "/news/ordinary.html",
			"/products/", "/products/ordinary.html",
			"/people/", "/people/jane-okafor.html",
			"/places/", "/places/harbour.html",
			"/listings/", "/listings/page-2.html",
			// The long-form section is reached through a search result rather
			// than from the index, so the story arrives before its own index
			// page does. /longform/ itself is a level further on than this
			// crawl goes, which is why the story and not the index is named.
			"/longform/dredging-contract/1",
			"/feeds/rss.xml", "/files/report.pdf",
		} {
			if p, ok := pages[want]; !ok || p.Status != "fetched" {
				t.Errorf("%s was not fetched, out of:\n%s", want, paths(pages))
			}
		}
	})

	t.Run("does not reach the live wire", func(t *testing.T) {
		// KNOWN GAP, and it is the fixture's rather than scour's. The right
		// answer is that a crawl from the root reaches /live/ like every other
		// section. Nothing on the site links to it: the index lists news,
		// products, people, places and the archive, and the live wire only
		// links to itself. No crawler can find a page nothing points at, so
		// this asserts the absence rather than pretending scour missed it.
		//
		// When a link to /live/ is added to the fixture this fails, which is
		// the moment to move /live/ into the list above.
		if p, ok := pages["/live/"]; ok {
			t.Errorf("/live/ was reached (%s): something now links to it, so it "+
				"belongs in the section list above", p.Status)
		}
	})

	t.Run("resolves absolute, root-relative and document-relative links", func(t *testing.T) {
		// Every fixture page carries all three in one <nav class="crosslinks">,
		// because resolving all three is the crawler's job and a page that only
		// ever links one way never tests the other two.
		for form, want := range map[string]string{
			"absolute":          "/search?q=harbour",
			"root-relative":     "/feeds/rss.xml",
			"document-relative": "/files/report.pdf",
		} {
			if p, ok := pages[want]; !ok || p.Status != "fetched" {
				t.Errorf("the %s link to %s was not followed, out of:\n%s",
					form, want, paths(pages))
			}
		}
	})

	t.Run("reads a content type from its header, not its extension", func(t *testing.T) {
		// The header is the authority. /odd/ serves each of these with a
		// Content-Type that contradicts the URL, and the recorded format is
		// what scour will later try to parse the body as.
		for path, want := range map[string]string{
			// HTML at a .pdf address. Refusing on the extension would drop a
			// real article, and reading it as a PDF would fail to parse one.
			"/odd/article.pdf": "html",
			// A PNG announced as HTML. Believing the header is right; what
			// must not happen is a record coming out of it, asserted below.
			"/odd/image-as-html": "html",
			// A feed served as application/xml, which is how the plainer half
			// of the web serves one.
			"/odd/feed-as-plain-xml": "xml",
			// And the honest ones, so the test says the header is read
			// correctly rather than only that the extension is ignored.
			"/files/report.pdf": "pdf",
			"/files/data.json":  "json",
			"/files/notes.txt":  "text",
			"/files/chart.png":  "image",
		} {
			p, ok := pages[path]
			if !ok {
				t.Errorf("%s was never fetched, out of:\n%s", path, paths(pages))
				continue
			}
			if p.ContentType != want {
				t.Errorf("%s was recorded as %q, want %q", path, p.ContentType, want)
			}
		}
	})

	t.Run("records no format at all for a response without a content type", func(t *testing.T) {
		// KNOWN GAP. /odd/no-content-type is an ordinary news article served
		// with the header removed, and the right answer is to sniff the body,
		// or to fall back to HTML, and extract from it like any other article.
		// What happens is that the format is stored empty, and parse.Load
		// treats an empty format as not extractable, so the page is fetched,
		// cached, counted, and then never read. An article is lost in silence.
		p, ok := pages["/odd/no-content-type"]
		if !ok {
			t.Fatalf("/odd/no-content-type was never fetched, out of:\n%s", paths(pages))
		}
		if p.Status != "fetched" {
			t.Errorf("/odd/no-content-type came back %q, want it fetched", p.Status)
		}
		if p.ContentType != "" {
			t.Errorf("/odd/no-content-type now has format %q: if it is being read, "+
				"this gap is closed and the test should say so", p.ContentType)
		}
	})

	t.Run("counts a redirect as a failure", func(t *testing.T) {
		// KNOWN GAP. /dynamic/redirect answers 302 to /news/ordinary.html,
		// which is an ordinary way for a site to move a page. The right answer
		// is to follow it and record the destination. What is recorded is a
		// failure with no status code at all, so a site that redirects reads
		// as a site that is broken. /listings/index.html goes the same way,
		// there through the file server's own redirect to /listings/.
		for _, path := range []string{"/dynamic/redirect", "/listings/index.html"} {
			p, ok := pages[path]
			if !ok {
				continue // not linked from anywhere the crawl went
			}
			if p.Status != "failed" {
				t.Errorf("%s came back %q rather than failed: redirects are being "+
					"followed now, so this gap is closed", path, p.Status)
			}
		}
	})

	t.Run("reports the failures the fixture serves on purpose", func(t *testing.T) {
		for path, code := range map[string]int{
			"/dynamic/404": 404,
			"/dynamic/500": 500,
			"/private/":    401, // the press area, whose key is in a PDF
		} {
			p, ok := pages[path]
			if !ok {
				t.Errorf("%s was never tried, out of:\n%s", path, paths(pages))
				continue
			}
			if p.Status != "failed" || p.StatusCode != code {
				t.Errorf("%s came back %s/%d, want failed/%d", path, p.Status, p.StatusCode, code)
			}
		}
	})
}

// A crawl asked for feeds does not fetch a single one of the fixture's four.
//
// This is the fault the results table calls "a feed skipped by its filename",
// one layer further on than where it was fixed. content.AllowsPath was taught
// that .xml can be a feed, so the request goes out; content.AllowsMIME was not,
// and the feed shorthand expands only to application/rss+xml, atom+xml, rdf+xml
// and feed+json. A file server serves .xml as text/xml, and the fixture serves
// /odd/feed-as-plain-xml as application/xml on purpose, so all four are
// abandoned on their headers.
//
// The right answer is that `--type feed` fetches the feeds. Pointing a crawl at
// a feed URL and getting nothing back is the ordinary way to use one.
func TestFixtureFeedsAreSkippedUnlessXMLIsAllowed(t *testing.T) {
	srv := e2e.Server(t)
	dir := e2eDir(t)

	runOK(t, dir, "item", "add", "gazette", "--template", "article")
	for _, feed := range []string{"/feeds/rss.xml", "/feeds/atom.xml", "/feeds/rdf.xml"} {
		runOK(t, dir, "item", "add", "gazette", "-u", srv.URL+feed)
	}
	addTypes(t, dir, "gazette", []string{"feed", "html"})

	out := runOK(t, dir, "start", "gazette", "--depth", "2")
	if !strings.Contains(out, "nothing fetched") {
		t.Errorf("the feeds are being fetched on --type feed alone now, which closes "+
			"this gap:\n%s", out)
	}
	if got := visitedCount(t, dir, "gazette"); got != 0 {
		t.Errorf("visited %d pages, want 0: today --type feed fetches none of the "+
			"three feeds it was pointed at", got)
	}

	// All three were skipped rather than failed, which is the tell: the request
	// went out and the body was abandoned once the header arrived.
	pages := fetchedPages(t, dir, "gazette")
	for _, feed := range []string{"/feeds/rss.xml", "/feeds/atom.xml", "/feeds/rdf.xml"} {
		if p, ok := pages[feed]; !ok || p.Status != "skipped" {
			t.Errorf("%s was %q, want skipped", feed, p.Status)
		}
	}
}

// crawlFeeds seeds the three feeds and scopes the crawl to the whole host, so
// the articles they name are inside it.
//
// The domain target is what makes this work, and it is worth saying why. A URL
// target is deliberately narrower: it keeps the crawl under the seed's own
// directory. Seeding only http://host/feeds/rss.xml therefore scopes the crawl
// to /feeds/, and every article the feed names is out of scope before it is
// scored. That is the shape of a real news crawl, since a site's feeds live in
// one directory and its articles in another, and it means "follow a feed to the
// articles it names" does nothing at all unless the operator also names the
// domain. See the report: this is a gap, not a setting.
func crawlFeeds(t *testing.T) string {
	t.Helper()
	srv := e2e.Server(t)
	dir := e2eDir(t)

	runOK(t, dir, "item", "add", "gazette", "--template", "article")
	runOK(t, dir, "item", "add", "gazette", "-d", srv.Listener.Addr().String())
	for _, feed := range []string{"/feeds/rss.xml", "/feeds/atom.xml", "/feeds/rdf.xml"} {
		runOK(t, dir, "item", "add", "gazette", "-u", srv.URL+feed)
	}
	addTypes(t, dir, "gazette", []string{"feed", "html", "xml"})
	runOK(t, dir, "start", "gazette", "--depth", "2")
	return dir
}

// A feed is a list of URLs. Fetching one and stopping there is not crawling it,
// and each of the three dialects hides the URL somewhere different: RSS and RDF
// put it in a <link> element's text, Atom in that element's href.
func TestFixtureFeedsAreCrawledThroughToTheirArticles(t *testing.T) {
	dir := crawlFeeds(t)
	pages := fetchedPages(t, dir, "gazette")

	for feed, articles := range map[string][]string{
		"/feeds/rss.xml": {
			"/news/ordinary.html",        // <link> holding a root-relative path
			"/news/per-page-id.html",     // another
			"/news/utility-classes.html", // <link> holding an absolute URL
			"/longform/dredging-contract/1",
		},
		"/feeds/atom.xml": {
			"/news/attribute-outscores.html", // link/@href, not link's text
			"/news/canonical-only.html",
		},
		"/feeds/rdf.xml": {
			"/news/published-vs-modified.html", // items beside the channel
		},
	} {
		if p, ok := pages[feed]; !ok || p.Status != "fetched" {
			t.Errorf("%s itself was not fetched, out of:\n%s", feed, paths(pages))
			continue
		}
		for _, article := range articles {
			if p, ok := pages[article]; !ok || p.Status != "fetched" {
				t.Errorf("%s names %s and the crawl did not reach it, out of:\n%s",
					feed, article, paths(pages))
			}
		}
	}
}

// The feed is a record source as well as a list of links: an RSS item already
// carries a title, an author, a date and a summary, and this is the one place
// on the whole site where scour fills every field of every record correctly.
func TestFixtureRecordsFromTheFeed(t *testing.T) {
	dir := crawlFeeds(t)
	if out, err := run(t, dir, "model", "train", "gazette"); err != nil {
		t.Fatalf("model train: %v\n%s", err, out)
	}

	// KNOWN GAP, and a small one with the same root cause as the skipping
	// above: a feed served as text/xml is recorded under the xml format rather
	// than feed, so `record ls --type feed` finds nothing on this site.
	var fromFeeds []e2eRecord
	for _, r := range allExtracted(t, dir, "gazette") {
		if r.Format == "xml" {
			fromFeeds = append(fromFeeds, r)
		}
	}
	if len(fromFeeds) == 0 {
		t.Fatal("the three feeds produced no records at all")
	}

	// Each RSS item is named by the article it points at, since that is the
	// value that says which item a record came from.
	for link, want := range map[string]map[string]string{
		"/news/ordinary.html": {
			"title":   "Harbour plan approved after long inquiry",
			"author":  "Jane Okafor",
			"summary": "Councillors backed the scheme by a single vote.",
		},
		"/news/per-page-id.html": {
			"title":   "Rail strike called off hours before deadline",
			"author":  "Tomas Lindqvist",
			"summary": "Union leaders accepted a revised pay offer overnight.",
		},
	} {
		var found bool
		for _, r := range fromFeeds {
			if !strings.HasSuffix(r.Values["link"], link) {
				continue
			}
			found = true
			for prop, value := range want {
				if r.Values[prop] != value {
					t.Errorf("the feed's record for %s has %s=%q, want %q",
						link, prop, r.Values[prop], value)
				}
			}
			if r.Values["published"] == "" {
				t.Errorf("the feed's record for %s has no published date", link)
			}
		}
		if !found {
			t.Errorf("no feed record links to %s, out of %d", link, len(fromFeeds))
		}
	}
}

// crawlNewsSection crawls the news index and the fourteen articles under it.
//
// This is the corpus the fault assertions below are measured against: fifteen
// pages, one section, no products or profiles to dilute what induction sees.
// It is stable, in that repeated runs induce the same rules and extract the
// same values.
func crawlNewsSection(t *testing.T) string {
	t.Helper()
	srv := e2e.Server(t)
	dir := e2eDir(t)

	runOK(t, dir, "item", "add", "article", "--template", "article")
	runOK(t, dir, "item", "add", "article", "-u", srv.URL+"/news/")
	addTypes(t, dir, "article", []string{"html"})
	runOK(t, dir, "start", "article", "--depth", "2")

	if out, err := run(t, dir, "model", "train", "article"); err != nil {
		t.Fatalf("model train: %v\n%s", err, out)
	}
	return dir
}

// What comes out of the news section, on the page built to have nothing wrong
// with it and on the twelve built to have something wrong.
func TestFixtureRecordsFromTheNewsPages(t *testing.T) {
	dir := crawlNewsSection(t)
	records := extracted(t, dir, "article")

	// Every article yields a record, which is the first thing to establish:
	// the faults below are wrong values, not missing pages.
	for _, article := range []string{
		"/news/ordinary.html", "/news/per-page-id.html", "/news/utility-classes.html",
		"/news/attribute-outscores.html", "/news/canonical-only.html",
		"/news/published-vs-modified.html", "/news/constant-section-1.html",
		"/news/greek.html", "/news/russian.html", "/news/arabic.html", "/news/turkish.html",
	} {
		if _, ok := records[article]; !ok {
			t.Errorf("no record came out of %s", article)
		}
	}

	// The non-English pages keep their own text rather than being dropped or
	// mangled, which is what "English-only vectors fall back" has to mean if it
	// is to mean anything.
	for article, title := range map[string]string{
		"/news/greek.html":   "Νέο λιμάνι για τη Θεσσαλονίκη",
		"/news/russian.html": "Новый мост откроют осенью",
		"/news/arabic.html":  "افتتاح الميناء الجديد",
		"/news/turkish.html": "İstanbul'da yeni tramvay hattı",
	} {
		if got := records[article].Values["title"]; got != title {
			t.Errorf("%s has title %q, want %q", article, got, title)
		}
	}

	t.Run("the ordinary page", func(t *testing.T) {
		// This one is the control: every field is where a well-built news page
		// puts it, in og: meta tags, in schema.org itemprops, in an <h1>, in a
		// <time>, and behind rel="author".
		r := records["/news/ordinary.html"]

		if got, want := r.Values["title"], "Harbour plan approved after long inquiry"; got != want {
			t.Errorf("title = %q, want %q", got, want)
		}
		if got, want := r.Values["published"], "14 July 2025"; got != want {
			t.Errorf("published = %q, want %q", got, want)
		}

		// KNOWN GAP. The summary is the standfirst, "Councillors backed the
		// scheme by a single vote.", which the page carries three times over:
		// in og:description, in itemprop="description", and in
		// <p class="standfirst">. What is extracted is the <title> element,
		// which is the headline with the masthead stuck on the end. summary
		// and title end up saying the same thing, so one of the two fields is
		// worthless on every page of the site.
		if got, want := r.Values["summary"],
			"Harbour plan approved after long inquiry - The Gazette"; got != want {
			t.Errorf("summary = %q, want the wrong-but-current %q", got, want)
		}

		// KNOWN GAP. The byline is <a rel="author" itemprop="author">Jane
		// Okafor</a>, which is as clearly marked as a byline ever gets, and no
		// author is extracted at all. The induced author rule is pinned by a
		// regex to one literal name taken from the constant-section pages, so
		// it matches those three and nothing else on the site. Overfitting a
		// value regex to the only name induction happened to settle on turns a
		// field that works on three pages into a field that is empty on eleven.
		if got := r.Values["author"]; got != "" {
			t.Errorf("author = %q: a byline is being read now, which closes this gap", got)
		}
	})

	t.Run("a per-page id", func(t *testing.T) {
		// The trap: the article's wrapper is <div id="asset-59da10e1-...">,
		// an id that appears on this page and no other. The headline, byline,
		// time and summary are all inside it and are all correctly marked up.
		//
		// The right answer is title "Rail strike called off hours before
		// deadline", author "Tomas Lindqvist", published 14 July 2025.
		//
		// KNOWN GAP. Induction settled on locators rooted at <article>, which
		// eleven of the fourteen pages have and this one does not, so nothing
		// inside the div is found. The only value extracted is the summary,
		// and it is the summary rule reading <head><title>, which is the wrong
		// rule producing a right-looking string: it is the headline, in the
		// summary field.
		r := records["/news/per-page-id.html"]

		if got, want := r.Values["summary"],
			"Rail strike called off hours before deadline"; got != want {
			t.Errorf("summary = %q, want the wrong-but-current %q", got, want)
		}
		for _, prop := range []string{"title", "author", "published"} {
			if got := r.Values[prop]; got != "" {
				t.Errorf("%s = %q: the per-page id no longer costs this field, "+
					"which closes part of this gap", prop, got)
			}
		}
	})

	t.Run("utility classes", func(t *testing.T) {
		// The trap: Tailwind-style utility classes and nothing else, so
		// class:text, class:font, class:flex and the rest attach themselves to
		// every field indiscriminately. The only real labels are the <h1> and
		// the <time>, and both are present and correct.
		//
		// The right answer is title "River levels fall for a third straight
		// week", author "Priya Raman", published 13 July 2025.
		//
		// KNOWN GAP, and the same shape as the per-page id: this page wraps its
		// article in <div class="mx-auto ...">, not <article>, so the induced
		// locators miss everything. The <time> element carries the date in a
		// datetime attribute and is not read either. One value survives, and it
		// is the <head><title> arriving in the summary field.
		r := records["/news/utility-classes.html"]

		if got, want := r.Values["summary"],
			"River levels fall for a third straight week"; got != want {
			t.Errorf("summary = %q, want the wrong-but-current %q", got, want)
		}
		for _, prop := range []string{"title", "author", "published"} {
			if got := r.Values[prop]; got != "" {
				t.Errorf("%s = %q: utility classes no longer cost this field, "+
					"which closes part of this gap", prop, got)
			}
		}
	})

	t.Run("a constant section", func(t *testing.T) {
		// The trap, and the one the results table calls out on its own: all
		// three pages carry the same <p class="kicker">Other items that may
		// interest you</p>. "kicker" is a real name for a section line, so the
		// label is correct; what marks it is that the value never changes. A
		// field describes its record and so changes from one to the next, and a
		// value that never changes is describing the site.
		//
		// The right answer is that no section is extracted from these pages.
		//
		// KNOWN GAP. All three carry the kicker as their section, exactly as
		// the 211 records in the measured corpus did. The constant test is not
		// reached here: three samples is not enough for it, or it is not
		// applied to this locator at all. Either way the fixture reproduces the
		// fault the corpus found.
		const kicker = "Other items that may interest you"
		for _, page := range []string{
			"/news/constant-section-1.html",
			"/news/constant-section-2.html",
			"/news/constant-section-3.html",
		} {
			if got := records[page].Values["section"]; got != kicker {
				t.Errorf("%s has section %q, want the wrong-but-current %q",
					page, got, kicker)
			}
		}

		// What these three do get right is everything else: they are the only
		// pages on the site with a byline, a date and a headline all extracted,
		// because the induced rules were fitted to them.
		for page, author := range map[string]string{
			"/news/constant-section-1.html": "Priya Raman",
			"/news/constant-section-2.html": "Priya Raman",
			"/news/constant-section-3.html": "Priya Raman",
		} {
			if got := records[page].Values["author"]; got != author {
				t.Errorf("%s has author %q, want %q", page, got, author)
			}
			if records[page].Values["published"] == "" {
				t.Errorf("%s has no published date", page)
			}
		}
	})

	t.Run("a short title", func(t *testing.T) {
		// The trap: this is a section index, not an article, and the only thing
		// that says so is that its title is three characters long. 118 of 867
		// titles in the measured corpus were shorter than twenty characters:
		// "Page A1", "Ads", "Community".
		//
		// The right answer is that no record comes out of it at all, or that
		// one comes out marked as something other than an article. This is the
		// open case internal/classify exists for, so it is a known gap rather
		// than a regression.
		r, ok := records["/news/short-title.html"]
		if !ok {
			t.Fatal("no record from /news/short-title.html: the section index is " +
				"being told apart from an article now, which closes this gap")
		}
		if got, want := r.Values["summary"], "Ads"; got != want {
			t.Errorf("summary = %q, want the wrong-but-current %q", got, want)
		}

		// It is indistinguishable from a real article by confidence, which is
		// what makes it expensive: nothing downstream can filter it out.
		ordinary := records["/news/ordinary.html"]
		if r.Confidence != ordinary.Confidence {
			t.Errorf("the section index scores %.2f and a real article %.2f: they are "+
				"being told apart now, which closes this gap",
				r.Confidence, ordinary.Confidence)
		}
	})

	t.Run("published and modified", func(t *testing.T) {
		// The page carries both dates four times over: article:published_time
		// and article:modified_time in meta tags, and two <time> elements
		// marked itemprop="dateCreated" and itemprop="dateModified". 273 of 660
		// records in the measured corpus carried both with different values, so
		// an extractor that collapses them into one field is wrong here.
		//
		// The right answer is published 2025-07-02 and modified 2025-07-19.
		//
		// KNOWN GAP. Neither date is read. The page is not collapsing the two
		// into one, which was the fault being watched for; it is losing both,
		// because the induced published rule is a regex fitted to the "%d July
		// 2025" spelling the constant-section pages use and this page's first
		// <time> reads "2 July 2025", one digit short.
		r := records["/news/published-vs-modified.html"]
		for _, prop := range []string{"published", "modified"} {
			if got := r.Values[prop]; got != "" {
				t.Errorf("%s = %q: a date is being read out of this page now, "+
					"which closes part of this gap", prop, got)
			}
		}
		// What it does get is the headline, twice, in two different fields.
		if got, want := r.Values["title"], "Inquiry reopens into the 2019 landslip"; got != want {
			t.Errorf("title = %q, want %q", got, want)
		}
	})
}

// A PNG announced as HTML must not become a record. The header says to parse
// it, which is right; what the parse finds must not be mistaken for an article.
func TestFixtureAMislabelledImageYieldsNoRecord(t *testing.T) {
	srv := e2e.Server(t)
	dir := e2eDir(t)

	// The news section comes along so that induction has real articles to learn
	// from. An image on its own teaches nothing and would prove nothing.
	runOK(t, dir, "item", "add", "gazette", "--template", "article")
	runOK(t, dir, "item", "add", "gazette", "-u", srv.URL+"/news/")
	runOK(t, dir, "item", "add", "gazette", "-u", srv.URL+"/odd/image-as-html")
	addTypes(t, dir, "gazette", everyType)
	runOK(t, dir, "start", "gazette", "--depth", "2")

	pages := fetchedPages(t, dir, "gazette")
	if p := pages["/odd/image-as-html"]; p.ContentType != "html" {
		t.Fatalf("/odd/image-as-html was recorded as %q, want html: the header is "+
			"no longer being believed and this test is measuring something else",
			p.ContentType)
	}

	if out, err := run(t, dir, "model", "train", "gazette"); err != nil {
		t.Fatalf("model train: %v\n%s", err, out)
	}
	if r, ok := extracted(t, dir, "gazette")["/odd/image-as-html"]; ok {
		t.Errorf("a PNG served as text/html produced a record: %v", r.Values)
	}
}

// Training survives a corpus holding a format nothing can read.
//
// It did not until this run. Induction runs once per format, and a format whose
// pages all fail to parse came back as "no cached pages", which was returned
// rather than skipped. One notes.txt alongside forty article pages therefore
// trained on none of them: text is declared extractable and wom has no reader
// for it, so the text pass came back empty and took the html pass down with it.
func TestFixtureOneUnreadableFormatDoesNotStopTraining(t *testing.T) {
	srv := e2e.Server(t)
	dir := e2eDir(t)

	runOK(t, dir, "item", "add", "gazette", "--template", "article")
	runOK(t, dir, "item", "add", "gazette", "-u", srv.URL+"/news/")
	runOK(t, dir, "item", "add", "gazette", "-u", srv.URL+"/files/notes.txt")
	addTypes(t, dir, "gazette", []string{"html", "text"})
	runOK(t, dir, "start", "gazette", "--depth", "2")

	// Both formats are in the corpus, which is what makes this a test.
	pages := fetchedPages(t, dir, "gazette")
	if p := pages["/files/notes.txt"]; p.ContentType != "text" {
		t.Fatalf("/files/notes.txt was recorded as %q, want text", p.ContentType)
	}

	out, err := run(t, dir, "model", "train", "gazette")
	if err != nil {
		t.Fatalf("one unreadable text file stopped training over %d pages: %v\n%s",
			len(pages), err, out)
	}
	if len(extracted(t, dir, "gazette")) == 0 {
		t.Errorf("training reported success and extracted nothing:\n%s", out)
	}
}

// A domain target that names a port scopes the crawl.
//
// It did not until this run: the target was stored as it was given, ports and
// all, and Scope.Allows only ever compared the bare hostname, so a target of
// "127.0.0.1:8099" matched nothing. The crawl fetched its seed and stopped,
// with every link it found dropped as out of scope and nothing said about it.
// Anything served on a port other than 80 or 443 is named that way, which on a
// developer's machine is everything.
func TestFixtureADomainTargetWithAPortScopesTheCrawl(t *testing.T) {
	srv := e2e.Server(t)
	dir := e2eDir(t)

	runOK(t, dir, "item", "add", "gazette", "--template", "article")
	runOK(t, dir, "item", "add", "gazette", "-d", srv.Listener.Addr().String())
	addTypes(t, dir, "gazette", []string{"html"})
	runOK(t, dir, "start", "gazette", "--depth", "2")

	if got := visitedCount(t, dir, "gazette"); got < 5 {
		t.Errorf("visited %d pages from a domain target naming a port, want the "+
			"whole front page's worth: the domain is scoping nothing", got)
	}
	pages := fetchedPages(t, dir, "gazette")
	if p, ok := pages["/news/"]; !ok || p.Status != "fetched" {
		t.Errorf("the crawl did not leave its seed, out of:\n%s", paths(pages))
	}
}
