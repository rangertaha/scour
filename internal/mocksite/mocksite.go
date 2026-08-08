// SPDX-License-Identifier: GPL-3.0-or-later

// Package mocksite is a website that behaves badly on purpose.
//
// # Why this exists
//
// A crawler is mostly a set of answers to things websites do, and the
// interesting ones are not the pages that are fine. Redirects chain, loop and
// go relative. A status can mean try again, never try again, or the server is
// unwell. An encoding is declared in a header and nowhere else. The same page
// arrives under three URLs. Markup is broken in ways nobody would write on
// purpose. Somewhere there is a body larger than any limit.
//
// Every test in this repository used to stand up a server of its own, serving
// the two or three pages that test needed. That is cheap to write and it
// quietly decides what can be asked: a fixture with one clean page cannot be
// used to ask what happens on a redirect loop, so nobody asks, and the answer
// stays unknown until somebody's crawl finds it.
//
// # It is a program, not only a fixture
//
// [Site] is an http.Handler, so a test wraps it in httptest and a person runs
// `scour-mocksite` and points a real crawl at it. Both get the same site,
// because there is one of it: a fixture that drifts from the thing you can run
// by hand is worse than no fixture, since the tests go on passing while the
// site you are debugging against is a different site.
//
// # It counts what was asked for
//
// Every request is recorded by path. A crawler's most interesting failures are
// things it did rather than things it did not: following a redirect off-site,
// fetching a path robots.txt forbids, asking for one page twice. A test can
// only catch those if the site remembers.
package mocksite

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// DefaultRobots disallows one prefix, so that "the crawler did not fetch what
// it was told not to" is a thing a test can check.
const DefaultRobots = "User-agent: *\nDisallow: /private/\n"

// Options are what a caller varies. The zero value is a reasonable site.
type Options struct {
	// Robots is what /robots.txt serves. Empty means [DefaultRobots]; a file
	// with nothing in it is spelled "\n".
	Robots string

	// Slow is how long /slow takes to answer. Zero means it is not slow, which
	// is what a test that is not about timeouts wants.
	Slow time.Duration
}

// Site is the mock website.
type Site struct {
	opts Options

	mu   sync.Mutex
	hits map[string]int
}

// New returns a site ready to serve.
func New(opts Options) *Site {
	if opts.Robots == "" {
		opts.Robots = DefaultRobots
	}
	return &Site{opts: opts, hits: map[string]int{}}
}

// Asked is how many times one path was requested.
func (s *Site) Asked(path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits[path]
}

// Total is how many requests arrived, robots.txt aside: that one is the
// crawler's own housekeeping rather than a page of the site.
func (s *Site) Total() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	var n int
	for path, count := range s.hits {
		if path != "/robots.txt" {
			n += count
		}
	}
	return n
}

// Paths is every path that was asked for, which is what a failure message
// should print rather than making somebody guess.
func (s *Site) Paths() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make(map[string]int, len(s.hits))
	for path, count := range s.hits {
		out[path] = count
	}
	return out
}

// ServeHTTP implements http.Handler.
func (s *Site) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.hits[r.URL.Path]++
	s.mu.Unlock()

	s.route(w, r)
}

// page writes a document with a title and a body, which is the shape most of
// these share.
func page(w http.ResponseWriter, title, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<html><head><meta property="og:title" content=%q></head><body>%s</body></html>`, title, body)
}

func (s *Site) route(w http.ResponseWriter, r *http.Request) {
	switch path := r.URL.Path; {

	// What the site permits, read before anything else on the host.
	case path == "/robots.txt":
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, s.opts.Robots)

	// The index links to one of everything, so a crawl started here discovers
	// the whole shape without a test having to seed each URL by hand.
	case path == "/":
		page(w, "Index", `
			<a href="/article/og">og</a>
			<a href="/article/jsonld">json-ld</a>
			<a href="/article/microdata">microdata</a>
			<a href="/article/messy">messy</a>
			<a href="/article/og?utm_source=nav">og again, tracked</a>
			<a href="/moved">moved</a>
			<a href="/chain/1">a chain of redirects</a>
			<a href="/missing">gone missing</a>
			<a href="/boom">broken</a>
			<a href="/private/secret">not allowed</a>
			<a href="/deep/1">deep</a>
			<a href="/not-html">a pdf</a>
			<a href="https://elsewhere.example/x">off site</a>`)

	// Three vocabularies for one fact. A crawler that reads only og: finds a
	// fraction of the web.
	case path == "/article/og":
		page(w, "An Open Graph story", `<article>Words.</article>`)

	case path == "/article/jsonld":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<html><head><script type="application/ld+json">
		  {"@context":"https://schema.org","@type":"NewsArticle","headline":"A JSON-LD story"}
		</script></head><body><article>Words.</article></body></html>`)

	case path == "/article/microdata":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<html><body><div itemscope itemtype="https://schema.org/NewsArticle">
		  <h1 itemprop="headline">A microdata story</h1></div></body></html>`)

	// Markup nobody would write on purpose and every crawler meets: unclosed
	// tags, a stray close, an unquoted attribute.
	case path == "/article/messy":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<html><head><meta property=og:title content="A messy story">
		  <body><div><p>Words.</div></span><article>More.`)

	// An encoding declared in the header and nowhere else, which is the case
	// that turns into mojibake the moment a cache loses the headers.
	case path == "/article/cyrillic":
		w.Header().Set("Content-Type", "text/html; charset=windows-1251")
		w.Write(append([]byte(`<html><head><meta property="og:title" content="`),
			append([]byte{0xcf, 0xf0, 0xe8, 0xe2, 0xe5, 0xf2}, // Привет
				[]byte(`"></head><body><article>Words.</article></body></html>`)...)...))

	// Redirects: permanent, chained, relative, and one that never lands.
	case path == "/moved":
		http.Redirect(w, r, "/article/og", http.StatusMovedPermanently)

	case path == "/chain/1":
		http.Redirect(w, r, "/chain/2", http.StatusFound)
	case path == "/chain/2":
		// Relative, which is legal and common, and is resolved against where
		// the body came from rather than against what was asked for.
		w.Header().Set("Location", "3")
		w.WriteHeader(http.StatusFound)
	case path == "/chain/3":
		page(w, "The end of the chain", `<article>Words.</article>`)

	case path == "/loop":
		http.Redirect(w, r, "/loop", http.StatusFound)

	// Statuses that mean different things: it is not here, it will never be
	// here, we are broken, we are busy.
	case path == "/missing":
		http.NotFound(w, r)
	case path == "/gone":
		w.WriteHeader(http.StatusGone)
	case path == "/boom":
		w.WriteHeader(http.StatusInternalServerError)
	case path == "/busy":
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusServiceUnavailable)

	// Disallowed by the default robots.txt. Served rather than refused on
	// purpose: a crawler that asks has already broken its promise, and
	// answering 403 here would hide that behind a failure that looks ordinary.
	case strings.HasPrefix(path, "/private/"):
		page(w, "Not for crawlers", `<article>Secrets.</article>`)

	// A ladder, for the depth budget.
	case strings.HasPrefix(path, "/deep/"):
		var n int
		fmt.Sscanf(path, "/deep/%d", &n)
		page(w, fmt.Sprintf("Depth %d", n), fmt.Sprintf(`<a href="/deep/%d">deeper</a>`, n+1))

	// Not a page at all.
	case path == "/not-html":
		w.Header().Set("Content-Type", "application/pdf")
		fmt.Fprint(w, "%PDF-1.4 not really")

	// Larger than any sane body limit.
	case path == "/big":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<html><head><meta property="og:title" content="Enormous"></head><body>`)
		fmt.Fprint(w, strings.Repeat("padding ", 200_000))
		fmt.Fprint(w, `</body></html>`)

	case path == "/slow":
		time.Sleep(s.opts.Slow)
		page(w, "Eventually", `<article>Words.</article>`)

	default:
		http.NotFound(w, r)
	}
}
