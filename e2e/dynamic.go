// SPDX-License-Identifier: GPL-3.0-or-later

package e2e

import (
	"fmt"
	"net/http"
	"sync/atomic"
	"time"
)

// slowDelay is how long /dynamic/slow takes. Long enough to be measurably slow
// against a local server, short enough that a test suite does not notice.
const slowDelay = 250 * time.Millisecond

// fetches counts requests to /dynamic/changes, so the second fetch can differ
// from the first.
var fetches atomic.Int64

// registerDynamic adds the routes that cannot be files.
//
// A static file cannot lie about its own Content-Type, take too long, fail, or
// answer differently the second time. Each of those has broken something, so
// each gets a route. Everything else in this site is a file, because a file is
// easier to read and to add to than a handler that prints one.
func registerDynamic(mux *http.ServeMux) {
	// The extension says one thing and the header says another. The header is
	// the authority, and reading the extension as if it were the answer is what
	// made a crawl asked for feeds skip feed.xml by its filename.
	mux.HandleFunc("GET /odd/feed-as-plain-xml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		serveFile(w, r, "feeds/rss.xml")
	})
	mux.HandleFunc("GET /odd/article.pdf", func(w http.ResponseWriter, r *http.Request) {
		// An .pdf extension over an HTML body. Refusing on the extension alone
		// would drop a real article.
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		serveFile(w, r, "news/ordinary.html")
	})
	mux.HandleFunc("GET /odd/image-as-html", func(w http.ResponseWriter, r *http.Request) {
		// Deliberately mislabelled the other way: a PNG announced as HTML. The
		// body must not end up parsed as a document.
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		serveFile(w, r, "files/chart.png")
	})
	mux.HandleFunc("GET /odd/no-content-type", func(w http.ResponseWriter, r *http.Request) {
		// No Content-Type at all. Rejecting on a missing header would drop
		// pages that are otherwise fine.
		w.Header()["Content-Type"] = nil
		serveFile(w, r, "news/ordinary.html")
	})

	// A page whose content is written by script. Nothing is in the HTML, so
	// this is what the browser escalation exists for: fetched plainly it is an
	// empty shell, and rendered it is an article.
	mux.HandleFunc("GET /dynamic/js-rendered", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<html><head><title>Loading…</title></head><body>
<div id="app"></div>
<script>
document.getElementById('app').innerHTML =
  '<article><h1 class="headline">Tide gates fitted at the north berth</h1>' +
  '<div class="byline">Ana Duarte</div>' +
  '<time datetime="2025-07-16T09:00:00Z">16 July 2025</time>' +
  '<p class="standfirst">The gates were craned in overnight.</p></article>';
</script>
</body></html>`)
	})

	// The three ways a fetch goes wrong, so a crawl's error handling is
	// exercised rather than assumed.
	mux.HandleFunc("GET /dynamic/500", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "the database is on fire", http.StatusInternalServerError)
	})
	mux.HandleFunc("GET /dynamic/404", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no such page", http.StatusNotFound)
	})
	mux.HandleFunc("GET /dynamic/redirect", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/news/ordinary.html", http.StatusFound)
	})
	mux.HandleFunc("GET /dynamic/slow", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(slowDelay)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		serveFile(w, r, "news/ordinary.html")
	})

	// Answers differently the second time, which is what a refresh policy and a
	// revisit rule are for: a page that changed is the reason to come back, and
	// one that did not is the reason not to.
	mux.HandleFunc("GET /dynamic/changes", func(w http.ResponseWriter, _ *http.Request) {
		n := fetches.Add(1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<html><head><title>Berth status</title></head><body>
<article><h1>Berth status</h1>
<div class="byline">The Harbourmaster</div>
<time datetime="2025-07-1%dT09:00:00Z">fetch %d</time>
<p class="summary">Two berths occupied, as of fetch %d.</p></article>
</body></html>`, n%10, n, n)
	})
}

// serveFile writes one of the embedded files, leaving whatever Content-Type
// the caller already set.
func serveFile(w http.ResponseWriter, _ *http.Request, name string) {
	body, err := readFile(name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if _, err := w.Write(body); err != nil {
		return // the client hung up, which is not this server's problem
	}
}

// Reset puts the counters back, so a test that cares how many times a changing
// page has been fetched starts from a known place.
func Reset() { fetches.Store(0) }
