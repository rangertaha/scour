// SPDX-License-Identifier: GPL-3.0-or-later

package e2e

import (
	"crypto/subtle"
	"fmt"
	"net/http"
	"strings"
)

// The credentials for the press area.
//
// They are here and in /files/press-credentials.pdf, and a test extracts them
// from that PDF and checks they still open the door. That is the point of the
// section: the way in is not on the page that refuses you, it is in a document
// somebody has to read. A crawler that cannot pull text out of a PDF cannot get
// past the 401, and one that can has to notice that what it pulled out is a
// credential rather than prose.
const (
	PressUser = "press"
	PressPass = "quay-gate-1841"
	// PressRealm is what a browser shows in its prompt, and what a client keys
	// its stored credentials on.
	PressRealm = "Gazette press area"
)

// registerPrivate serves the pages behind basic auth.
//
// Basic auth rather than a login form because it is the one scheme a crawler
// can satisfy without running a browser or keeping a session, so it is the
// case worth being able to reproduce. A form would be testing something else.
func registerPrivate(mux *http.ServeMux) {
	mux.HandleFunc("GET /private/", requirePress(pressIndex))
	mux.HandleFunc("GET /private/board-minutes.html", requirePress(boardMinutes))
	mux.HandleFunc("GET /private/tender.html", requirePress(tenderPage))
}

// requirePress refuses anything without the right credentials.
//
// The refusal is a page rather than an empty body, and it says where the
// credentials are. A 401 that explains itself is what a real press area does,
// and it is what makes this a puzzle with an answer rather than a wall.
func requirePress(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		// Compared in constant time, which costs nothing here and is the habit
		// worth having in the code people copy from.
		valid := ok &&
			subtle.ConstantTimeCompare([]byte(user), []byte(PressUser)) == 1 &&
			subtle.ConstantTimeCompare([]byte(pass), []byte(PressPass)) == 1
		if !valid {
			w.Header().Set("WWW-Authenticate", fmt.Sprintf("Basic realm=%q, charset=\"UTF-8\"", PressRealm))
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `<!-- The refusal. It names the media pack rather than the credentials, so
     getting in means reading a PDF rather than reading this page. -->
<html lang="en"><head><meta charset="utf-8"><title>Press area</title></head><body>
<h1>Press area</h1>
<p>This section is for accredited press. Your credentials are in the
<a href="/files/press-credentials.pdf">media pack</a>.</p>
<p>Not press? <a href="/news/">The public news index</a> is open to everyone.</p>
</body></html>`)
			return
		}
		next(w, r)
	}
}

func pressIndex(w http.ResponseWriter, _ *http.Request) {
	writeHTML(w, `<!-- The press index, behind basic auth. Everything linked from here is also
     behind it, so a crawler that got in once has somewhere to go and one that
     did not sees none of it. -->
<html lang="en"><head><meta charset="utf-8"><title>Press area</title>
<link rel="canonical" href="`+BaseToken+`/private/">
</head><body>
<h1>Press area</h1>
<ul>
  <li><a href="/private/board-minutes.html">Harbour board minutes, July</a></li>
  <li><a href="board-minutes.html">The same, by a relative path</a></li>
  <li><a href="`+BaseToken+`/private/tender.html">Dredging tender, absolute path</a></li>
</ul>
<p>The public side is <a href="../news/">here</a>.</p>
</body></html>`)
}

func boardMinutes(w http.ResponseWriter, _ *http.Request) {
	writeHTML(w, `<!-- A page only an authenticated crawl ever sees. It carries the same shape as
     a public article, so nothing about the markup says it was privileged; the
     only difference is that getting here took a credential. -->
<html lang="en"><head><meta charset="utf-8">
<title>Harbour board minutes, July</title>
<meta property="article:published_time" content="2025-07-22T14:00:00Z">
</head><body>
<article>
  <h1 class="headline">Harbour board minutes, July</h1>
  <div class="byline">By <a rel="author" href="/people/jane-okafor.html">Jane Okafor</a></div>
  <time datetime="2025-07-22T14:00:00Z">22 July 2025</time>
  <p class="standfirst">The board agreed to reopen the standby clause.</p>
  <p>Members noted the engineer's memos of October and January had not been
  answered, and asked for the correspondence to be tabled in full.</p>
</article>
<a href="/private/">Back to the press area</a>
</body></html>`)
}

func tenderPage(w http.ResponseWriter, _ *http.Request) {
	writeHTML(w, `<!-- A second protected page, so the section is not one URL. -->
<html lang="en"><head><meta charset="utf-8">
<title>Dredging tender, 2025-26</title>
<meta property="article:published_time" content="2025-07-23T09:00:00Z">
</head><body>
<article>
  <h1 class="headline">Dredging tender, 2025-26</h1>
  <div class="byline">By <a rel="author" href="/people/tomas-lindqvist.html">Tomas Lindqvist</a></div>
  <time datetime="2025-07-23T09:00:00Z">23 July 2025</time>
  <p class="standfirst">Three bidders, and a standby rate that has not moved.</p>
  <p>The tender closes at noon on the last Friday of August.</p>
</article>
<a href="/private/">Back to the press area</a>
</body></html>`)
}

// credentialsInPDF reports whether the media pack still carries the credentials
// the door expects. Used by the test that keeps the two in step.
func credentialsInPDF(text string) bool {
	return strings.Contains(text, PressUser) && strings.Contains(text, PressPass)
}
