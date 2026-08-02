// SPDX-License-Identifier: GPL-3.0-or-later

package e2e

import (
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// searchable is one thing the site search can find. It is a flat index rather
// than a walk of the file system, so what the search returns is a fact about
// this file and not about whatever happens to be on disk.
type searchable struct {
	Title, URL, Kind, Text string
}

var index = []searchable{
	{"Harbour plan approved after long inquiry", "/news/ordinary.html", KindArticle,
		"councillors backed the scheme dredging harbour planning"},
	{"Rail strike called off hours before deadline", "/news/per-page-id.html", KindArticle,
		"union pay offer rail transport"},
	{"River levels fall for a third straight week", "/news/utility-classes.html", KindArticle,
		"reservoirs water river levels"},
	{"Bridge repairs to close the north lane", "/news/attribute-outscores.html", KindArticle,
		"bridge roadworks closure"},
	{"Ferry service resumes after engine repair", "/news/canonical-only.html", KindArticle,
		"ferry sailing engine harbour"},
	{"Inquiry reopens into the 2019 landslip", "/news/published-vs-modified.html", KindArticle,
		"landslip inquiry highways"},
	{"The dredging contract nobody read", "/longform/dredging-contract/print", KindLongform,
		"dredging contract standby clause council harbour"},
	{"Kestrel 2 Trail Shoe", "/products/ordinary.html", KindProduct, "footwear trail shoe fellstride"},
	{"Harrier Down Jacket", "/products/json-ld.html", KindProduct, "jacket down winter fellstride"},
	{"Jane Okafor", "/people/jane-okafor.html", KindPerson, "planning correspondent reporter"},
	{"North Harbour", PageHarbour, KindPlace, "harbour berths ardmore argyll quay"},
}

// resultsPerPage is small so paging is reachable with the corpus there is.
const resultsPerPage = 3

// registerSearch adds an HTML search that reads its query from the URL.
//
// It exists because a query string is a part of a URL that changes what comes
// back, and almost everything about a crawl assumes the opposite: that a URL
// names a document. Here /search?q=harbour and /search?q=ferry are two pages at
// one path, /search?q=harbour&page=2 is a third, and /search with no query is a
// fourth that lists nothing. A crawler has to decide which of those are worth
// keeping, and it cannot decide without meeting them.
func registerSearch(mux *http.ServeMux) {
	mux.HandleFunc("GET /search", searchPage)
}

func searchPage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	term := strings.ToLower(strings.TrimSpace(q.Get("q")))
	kind := strings.ToLower(strings.TrimSpace(q.Get("kind")))
	page := intParam(q.Get("page"), 1)
	if page < 1 {
		page = 1
	}

	var hits []searchable
	for _, s := range index {
		if kind != "" && s.Kind != kind {
			continue
		}
		if term != "" && !strings.Contains(strings.ToLower(s.Title+" "+s.Text+" "+s.Kind), term) {
			continue
		}
		hits = append(hits, s)
	}

	total := len(hits)
	start := (page - 1) * resultsPerPage
	if start > total {
		start = total
	}
	end := min(start+resultsPerPage, total)
	shown := hits[start:end]

	var b strings.Builder
	b.WriteString(`<!-- Site search. The query string decides the page, so one path serves an
     unbounded number of documents, and the paging links below are more of the
     same path with a different query. A crawler that treats every distinct URL
     as a page to keep will keep all of them. -->
<html lang="en"><head><meta charset="utf-8">`)
	fmt.Fprintf(&b, "<title>Search%s</title>\n", titleSuffix(term))

	// Only a real result set is worth indexing. An empty one, or a deep page,
	// says so, which is the signal a crawler is entitled to use.
	if total == 0 || page > 1 {
		b.WriteString(`<meta name="robots" content="noindex">` + "\n")
	}
	fmt.Fprintf(&b, "<link rel=\"canonical\" href=\"%s/search?q=%s\">\n", BaseToken, url.QueryEscape(term))
	b.WriteString(`</head><body>
<form action="/search" method="get">
  <input type="search" name="q" value="` + html.EscapeString(term) + `">
  <select name="kind">
    <option value="">anything</option>
    <option value="article">articles</option>
    <option value="product">products</option>
    <option value="person">people</option>
    <option value="place">places</option>
  </select>
  <button type="submit">Search</button>
</form>
`)

	switch {
	case term == "" && kind == "":
		b.WriteString("<p>Type something. Nothing is listed until you do.</p>\n")
	case total == 0:
		fmt.Fprintf(&b, "<p>Nothing matched %q.</p>\n", html.EscapeString(term))
	default:
		fmt.Fprintf(&b, "<p>%d result(s), showing %d to %d.</p>\n<ol start=\"%d\">\n",
			total, start+1, end, start+1)
		for _, s := range shown {
			fmt.Fprintf(&b, "  <li><a href=\"%s\">%s</a> <span class=\"kind\">%s</span></li>\n",
				s.URL, html.EscapeString(s.Title), s.Kind)
		}
		b.WriteString("</ol>\n")
	}

	// Paging is more of the same URL with a different query, which is exactly
	// how a crawl ends up with a thousand near-identical pages.
	if start > 0 {
		fmt.Fprintf(&b, "<a rel=\"prev\" href=\"%s\">Previous</a>\n", searchURL(term, kind, page-1))
	}
	if end < total {
		fmt.Fprintf(&b, "<a rel=\"next\" href=\"%s\">Next</a>\n", searchURL(term, kind, page+1))
	}

	// A JSON view of the same query, so the two surfaces agree and a client can
	// pick either.
	fmt.Fprintf(&b, "<p><a href=\"/api/products?q=%s\">The same query against the product API</a></p>\n",
		url.QueryEscape(term))
	b.WriteString("<a href=\"/\">Home</a>\n</body></html>")

	writeHTML(w, b.String())
}

func searchURL(term, kind string, page int) string {
	q := url.Values{}
	if term != "" {
		q.Set("q", term)
	}
	if kind != "" {
		q.Set("kind", kind)
	}
	if page > 1 {
		q.Set("page", strconv.Itoa(page))
	}
	return "/search?" + q.Encode()
}

func titleSuffix(term string) string {
	if term == "" {
		return ""
	}
	return " for " + html.EscapeString(term)
}
