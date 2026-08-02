// SPDX-License-Identifier: GPL-3.0-or-later

package e2e

import (
	"fmt"
	"html"
	"net/http"
	"strconv"
	"strings"
)

// longform is one investigation published across several URLs.
//
// A story that runs to five pages is not five stories, and telling the
// difference is genuinely hard: every page carries the same headline, the same
// byline and the same date, and differs only in a page number and the body.
// Deduplicating on the headline merges them into one record and loses four
// pages of text; treating each page as its own article produces five records
// that are mostly the same. Both are wrong in ways worth being able to
// reproduce.
type longform struct {
	Slug     string
	Title    string
	Author   string
	Date     string
	Standard string
	Pages    []string
}

var investigation = longform{
	Slug:     "dredging-contract",
	Title:    "The dredging contract nobody read",
	Author:   "Jane Okafor",
	Date:     "2025-07-20T07:00:00Z",
	Standard: "A four-year agreement, signed in one afternoon, and what it cost.",
	Pages: []string{
		"The contract was tabled at 2pm and signed by four. Nobody on the committee had seen the schedule of rates, which ran to sixty pages and was circulated as a link that had expired.",
		"The rates themselves were unremarkable. What was not was the mobilisation clause, which allowed the contractor to bill for standby at the full day rate whenever the tide window closed.",
		"Between October and March the tide window closes most afternoons. Over the first winter the standby line came to more than the dredging.",
		"The council's own engineer raised it twice, in writing. Both memos went to a mailbox that had been closed when the post was merged with harbours.",
		"Asked about the clause this month, the authority said the agreement was under review. The contractor said it had been paid what it was owed, which is not in dispute.",
	},
}

// registerLongform serves the investigation, one URL per page.
//
// The pages link forward and back with rel="next" and rel="prev", which is the
// only machine-readable thing saying they are one article, and there is a
// print view carrying the whole of it at a sixth URL. That view is what makes
// this a fair test: the right answer exists, and finding it means preferring
// one URL over the five that also match.
func registerLongform(mux *http.ServeMux) {
	mux.HandleFunc("GET /longform/", func(w http.ResponseWriter, _ *http.Request) {
		writeHTML(w, longformIndex())
	})
	mux.HandleFunc("GET /longform/{slug}/{page}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("slug") != investigation.Slug {
			http.NotFound(w, r)
			return
		}
		n, err := strconv.Atoi(r.PathValue("page"))
		if err != nil || n < 1 || n > len(investigation.Pages) {
			http.NotFound(w, r)
			return
		}
		writeHTML(w, longformPage(n))
	})
	mux.HandleFunc("GET /longform/{slug}/print", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("slug") != investigation.Slug {
			http.NotFound(w, r)
			return
		}
		writeHTML(w, longformPrint())
	})
}

func longformIndex() string {
	var b strings.Builder
	b.WriteString(`<!-- The investigation's front door. One story, six URLs: five pages and a
     print view that holds the whole of it. -->
<html lang="en"><head><meta charset="utf-8"><title>Longform</title></head><body>
<h1>Longform</h1>
<ul>
`)
	fmt.Fprintf(&b, "  <li><a href=\"/longform/%s/1\">%s</a>, in %d parts</li>\n",
		investigation.Slug, html.EscapeString(investigation.Title), len(investigation.Pages))
	fmt.Fprintf(&b, "  <li><a href=\"%s/longform/%s/print\">The same, on one page</a></li>\n",
		BaseToken, investigation.Slug)
	b.WriteString("</ul>\n<a href=\"../\">Home</a>\n</body></html>")
	return b.String()
}

// longformPage is part n. Everything except the body and the page number is
// identical on all five, which is the difficulty stated as markup.
func longformPage(n int) string {
	var b strings.Builder
	a := investigation

	fmt.Fprintf(&b, `<!-- Part %d of %d. The headline, byline and date are the same on every part;
     only the body and the page number differ. rel="next" and rel="prev" are
     the only things saying these are one article. -->
<html lang="en"><head>
<meta charset="utf-8">
<title>%s (part %d of %d)</title>
<meta property="og:title" content="%s">
<meta property="article:published_time" content="%s">
<link rel="canonical" href="%s/longform/%s/%d">
`, n, len(a.Pages), html.EscapeString(a.Title), n, len(a.Pages),
		html.EscapeString(a.Title), a.Date, BaseToken, a.Slug, n)

	if n > 1 {
		fmt.Fprintf(&b, "<link rel=\"prev\" href=\"/longform/%s/%d\">\n", a.Slug, n-1)
	}
	if n < len(a.Pages) {
		fmt.Fprintf(&b, "<link rel=\"next\" href=\"/longform/%s/%d\">\n", a.Slug, n+1)
	}

	fmt.Fprintf(&b, `</head><body>
<article>
  <h1 class="headline">%s</h1>
  <p class="standfirst">%s</p>
  <div class="byline">By <a rel="author" href="/people/jane-okafor.html">%s</a></div>
  <time datetime="%s">20 July 2025</time>
  <p class="page-number">Page %d of %d</p>
  <p>%s</p>
</article>
<nav>
`, html.EscapeString(a.Title), html.EscapeString(a.Standard), a.Author, a.Date,
		n, len(a.Pages), html.EscapeString(a.Pages[n-1]))

	if n > 1 {
		fmt.Fprintf(&b, "  <a rel=\"prev\" href=\"%d\">Previous</a>\n", n-1)
	}
	if n < len(a.Pages) {
		// A document-relative next link, where the canonical above is absolute:
		// the same page carries both forms on purpose.
		fmt.Fprintf(&b, "  <a rel=\"next\" href=\"%d\">Next</a>\n", n+1)
	}
	fmt.Fprintf(&b, "  <a href=\"print\">Read on one page</a>\n  <a href=\"/longform/\">All longform</a>\n</nav>\n</body></html>")
	return b.String()
}

// longformPrint is the whole article at one URL, which is the answer a reader
// wants and the record an extractor should prefer.
func longformPrint() string {
	a := investigation
	var body strings.Builder
	for _, p := range a.Pages {
		fmt.Fprintf(&body, "  <p>%s</p>\n", html.EscapeString(p))
	}
	return fmt.Sprintf(`<!-- The print view: the same article, whole, at one URL. This is the record
     worth having, and preferring it over the five paginated parts that match
     just as well is the thing this fixture is for. -->
<html lang="en"><head>
<meta charset="utf-8">
<title>%s</title>
<meta property="og:title" content="%s">
<meta property="article:published_time" content="%s">
<link rel="canonical" href="%s/longform/%s/print">
</head><body>
<article>
  <h1 class="headline">%s</h1>
  <p class="standfirst">%s</p>
  <div class="byline">By <a rel="author" href="/people/jane-okafor.html">%s</a></div>
  <time datetime="%s">20 July 2025</time>
%s</article>
<a href="1">Back to part one</a>
</body></html>`, html.EscapeString(a.Title), html.EscapeString(a.Title), a.Date,
		BaseToken, a.Slug, html.EscapeString(a.Title), html.EscapeString(a.Standard),
		a.Author, a.Date, body.String())
}

func writeHTML(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(body))
}
