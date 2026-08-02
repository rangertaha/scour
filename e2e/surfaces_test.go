// SPDX-License-Identifier: GPL-3.0-or-later

package e2e

import (
	"context"
	"encoding/json"
	"html"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

// A file cannot know the port it is served on, so it writes a token and the
// server fills it in. Without this, every link in the fixture would have to be
// root-relative and the absolute case would never be exercised.
func TestAbsoluteLinksAreRewritten(t *testing.T) {
	srv := Server(t)

	for _, p := range []string{"/", "/files/data.json", "/files/notes.txt", "/feeds/rss.xml"} {
		_, body := get(t, srv.URL, p)
		if strings.Contains(body, BaseToken) {
			t.Errorf("%s still carries %s", p, BaseToken)
		}
	}

	// The token becomes the address the request arrived on, not a guess.
	_, body := get(t, srv.URL, "/files/data.json")
	if !strings.Contains(body, srv.URL+"/news/ordinary.html") {
		t.Errorf("no absolute link to this server:\n%s", body)
	}

	// A binary is never rewritten, because editing it would move every byte
	// after the token and a PDF's xref table would stop matching.
	resp, pdf := get(t, srv.URL, "/files/report.pdf")
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(pdf, "%PDF-") {
		t.Errorf("the PDF was mangled: %d, %.10q", resp.StatusCode, pdf)
	}
}

// Every content type points somewhere, in all three forms, so no format is a
// dead end and each exercises a different resolution.
func TestEveryContentTypeLinksOnward(t *testing.T) {
	srv := Server(t)

	cases := []struct{ path, absolute, rootRel, rel string }{
		{"/files/data.json", srv.URL + "/news/", "/places/harbour.html", "../products/"},
		{"/files/notes.txt", srv.URL + "/longform/", "/places/harbour.html", "report.pdf"},
		{"/news/ordinary.html", srv.URL + "/search", "/feeds/rss.xml", "../files/report.pdf"},
		{"/products/json-ld.html", srv.URL + "/search", "/api/products/", "./ordinary.html"},
	}
	for _, c := range cases {
		_, body := get(t, srv.URL, c.path)
		for what, want := range map[string]string{
			"absolute": c.absolute, "root-relative": c.rootRel, "relative": c.rel,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("%s carries no %s link (%q)", c.path, what, want)
			}
		}
	}

	// A PDF links through a URI action rather than an href.
	_, pdf := get(t, srv.URL, "/files/report.pdf")
	if !strings.Contains(pdf, "/URI(/longform/dredging-contract/print)") {
		t.Error("the PDF has no link annotation")
	}
}

func TestProductSearchAPI(t *testing.T) {
	srv := Server(t)

	type result struct {
		Total    int       `json:"total"`
		Next     string    `json:"next"`
		Products []Product `json:"products"`
	}
	search := func(query string) result {
		t.Helper()
		resp, body := get(t, srv.URL, "/api/products"+query)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("/api/products%s = %d", query, resp.StatusCode)
		}
		var out result
		if err := json.Unmarshal([]byte(body), &out); err != nil {
			t.Fatalf("decode %s: %v", query, err)
		}
		return out
	}

	if all := search(""); all.Total != len(catalogue) {
		t.Errorf("an empty query found %d of %d", all.Total, len(catalogue))
	}
	if byTerm := search("?q=jacket"); byTerm.Total != 1 || byTerm.Products[0].SKU != "FS-HD-1180" {
		t.Errorf("q=jacket found %d: %+v", byTerm.Total, byTerm.Products)
	}
	if byTag := search("?q=winter"); byTag.Total != 2 {
		t.Errorf("a tag query found %d, want 2", byTag.Total)
	}
	if byBrand := search("?brand=moorwind"); byBrand.Total != 2 {
		t.Errorf("brand=moorwind found %d, want 2", byBrand.Total)
	}
	if stock := search("?in_stock=true"); stock.Total != 3 {
		t.Errorf("in_stock found %d, want 3", stock.Total)
	}

	// Sorting is a real ordering rather than whatever the slice held.
	cheap := search("?sort=price")
	if len(cheap.Products) < 2 || cheap.Products[0].Price > cheap.Products[1].Price {
		t.Errorf("sort=price did not sort: %+v", cheap.Products)
	}

	// Paging hands back the next page as a link, not as arithmetic.
	first := search("?limit=2")
	if len(first.Products) != 2 || first.Next == "" {
		t.Fatalf("limit=2 gave %d products, next=%q", len(first.Products), first.Next)
	}
	if resp, _ := get(t, srv.URL, first.Next); resp.StatusCode != http.StatusOK {
		t.Errorf("the next link does not answer: %d", resp.StatusCode)
	}
}

func TestProductDetailAPI(t *testing.T) {
	srv := Server(t)

	resp, body := get(t, srv.URL, "/api/products/FS-K2-0442")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d: %s", resp.StatusCode, body)
	}
	var out struct{ Product Product }
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	if out.Product.Name != "Kestrel 2 Trail Shoe" {
		t.Errorf("got %q", out.Product.Name)
	}
	// A detail document links to its own address, its HTML, and a sibling.
	for _, want := range []string{out.Product.Self, out.Product.Page, out.Product.Related} {
		if want == "" {
			t.Errorf("a link is missing: %+v", out.Product)
		}
	}
	// The sku is matched without regard to case, since a URL is often shouted.
	if resp, _ := get(t, srv.URL, "/api/products/fs-k2-0442"); resp.StatusCode != http.StatusOK {
		t.Errorf("a lowercase sku = %d", resp.StatusCode)
	}
	// A miss says where the index is rather than only that it missed.
	resp, missing := get(t, srv.URL, "/api/products/NOPE")
	if resp.StatusCode != http.StatusNotFound || !strings.Contains(missing, "/api/products") {
		t.Errorf("a missing product = %d: %s", resp.StatusCode, missing)
	}
}

// One path, an unbounded number of documents, which is what a query string is.
func TestSearchPageReadsItsQuery(t *testing.T) {
	srv := Server(t)

	_, empty := get(t, srv.URL, "/search")
	if !strings.Contains(empty, "Type something") {
		t.Errorf("a bare search listed something:\n%s", empty)
	}

	_, hits := get(t, srv.URL, "/search?q=harbour")
	if !strings.Contains(hits, "Harbour plan approved") {
		t.Errorf("q=harbour found nothing:\n%s", hits)
	}
	if !strings.Contains(hits, "result(s)") {
		t.Errorf("no result count:\n%s", hits)
	}

	// Filtering by kind narrows rather than reordering.
	_, people := get(t, srv.URL, "/search?kind=person")
	if !strings.Contains(people, "Jane Okafor") || strings.Contains(people, "Kestrel") {
		t.Errorf("kind=person did not narrow:\n%s", people)
	}

	// A page nobody should index says so, which is a signal a crawler may use.
	_, none := get(t, srv.URL, "/search?q=zzzznothing")
	if !strings.Contains(none, "noindex") {
		t.Errorf("an empty result set is indexable:\n%s", none)
	}

	// Paging is the same path with a different query.
	_, page1 := get(t, srv.URL, "/search?q=harbour")
	if strings.Contains(page1, "rel=\"next\"") {
		_, page2 := get(t, srv.URL, "/search?q=harbour&page=2")
		if !strings.Contains(page2, "noindex") {
			t.Errorf("a deep page is indexable:\n%s", page2)
		}
	}
}

// A story across five URLs is not five stories, and the print view is the one
// worth keeping.
func TestLongformSpansSeveralURLs(t *testing.T) {
	srv := Server(t)

	var seen []string
	for i := 1; i <= len(investigation.Pages); i++ {
		resp, body := get(t, srv.URL, "/longform/dredging-contract/"+itoa(i))
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("part %d = %d", i, resp.StatusCode)
		}
		if !strings.Contains(body, investigation.Title) {
			t.Errorf("part %d does not carry the shared headline", i)
		}
		seen = append(seen, body)
	}

	// Every part shares the headline, byline and date, and differs in the body.
	if seen[0] == seen[1] {
		t.Error("two parts are identical, so there is nothing to tell apart")
	}
	if !strings.Contains(seen[0], `rel="next"`) {
		t.Error("part one does not link forward, which is the only thing joining them")
	}
	if !strings.Contains(seen[len(seen)-1], `rel="prev"`) {
		t.Error("the last part does not link back")
	}

	// The whole article exists at one URL, which is the right answer.
	_, whole := get(t, srv.URL, "/longform/dredging-contract/print")
	for _, page := range investigation.Pages {
		// Escaped, because the page is: an apostrophe in the source is &#39; in
		// the document, and comparing the two is comparing different strings.
		if !strings.Contains(whole, html.EscapeString(page)) {
			t.Errorf("the print view is missing a part: %.40q", page)
		}
	}

	if resp, _ := get(t, srv.URL, "/longform/dredging-contract/99"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("a part that does not exist = %d", resp.StatusCode)
	}
}

func TestServerSentEvents(t *testing.T) {
	srv := Server(t)

	resp, body := get(t, srv.URL, "/stream/events")
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q", ct)
	}
	if n := strings.Count(body, "event: berth"); n != streamEvents {
		t.Errorf("got %d berth events, want %d:\n%s", n, streamEvents, body)
	}
	if !strings.Contains(body, "event: done") {
		t.Error("the stream never says it is done, so a reader waits forever")
	}
	// Even the stream says where to go next.
	if !strings.Contains(body, "/places/harbour.html") {
		t.Error("no link out of the event stream")
	}
}

func TestWebSocket(t *testing.T) {
	srv := Server(t)

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	conn, _, _, err := ws.Dialer{}.Dial(ctx, strings.Replace(srv.URL, "http://", "ws://", 1)+"/stream/ws")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	var messages int
	for {
		data, err := wsutil.ReadServerText(conn)
		if err != nil {
			break
		}
		messages++
		if strings.Contains(string(data), `"done":true`) {
			break
		}
		if messages > streamEvents+2 {
			t.Fatal("the socket never finished")
		}
	}
	if messages != streamEvents+1 {
		t.Errorf("read %d frames, want %d plus the done frame", messages, streamEvents)
	}

	// A plain GET, which is what a crawler does, gets a page saying what the
	// URL is rather than a hung connection.
	resp, page := get(t, srv.URL, "/stream/ws")
	if resp.StatusCode != http.StatusUpgradeRequired {
		t.Errorf("an unupgraded GET = %d, want 426", resp.StatusCode)
	}
	if !strings.Contains(page, "/stream/events") {
		t.Errorf("the 426 does not say where the same data is:\n%s", page)
	}
	// And the endpoints are reachable by following a link like anything else.
	if _, index := get(t, srv.URL, "/stream/"); !strings.Contains(index, "/stream/events") {
		t.Error("the streaming endpoints are not linked from anywhere")
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// Everything else in this fixture has already happened. The live section
// changes while you are crawling it, which is the case a refresh policy exists
// for and cannot be exercised against a corpus that sits still.
func TestTheLiveSectionPublishesOverTime(t *testing.T) {
	ResetLive()
	srv := Server(t)

	_, first := get(t, srv.URL, "/live/")
	if !strings.Contains(first, "1 published so far") {
		t.Fatalf("the wire did not start with one:\n%s", first)
	}
	// Item two is genuinely not there, which is what makes a second crawl find
	// something a first could not.
	if resp, _ := get(t, srv.URL, "/live/2"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("an unpublished article = %d, want 404", resp.StatusCode)
	}

	if n := PublishNow(); n != 2 {
		t.Fatalf("publishing gave %d, want 2", n)
	}
	if resp, body := get(t, srv.URL, "/live/2"); resp.StatusCode != http.StatusOK {
		t.Errorf("the published article = %d: %s", resp.StatusCode, body)
	}

	_, second := get(t, srv.URL, "/live/")
	if first == second {
		t.Error("the index did not change when an article was published")
	}
}

// The feed is how a crawler finds out an article appeared without polling every
// article URL, so it has to grow with the section rather than beside it.
func TestTheLiveFeedGrows(t *testing.T) {
	ResetLive()
	srv := Server(t)

	resp, before := get(t, srv.URL, "/live/feed.xml")
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "application/rss+xml") {
		t.Errorf("Content-Type = %q", ct)
	}
	if n := strings.Count(before, "<item>"); n != 1 {
		t.Fatalf("the feed opened with %d items, want 1", n)
	}

	PublishNow()
	PublishNow()

	_, after := get(t, srv.URL, "/live/feed.xml")
	if n := strings.Count(after, "<item>"); n != 3 {
		t.Errorf("after two publications the feed has %d items, want 3:\n%s", n, after)
	}
	// The feed's links are absolute, so a reader that never saw the index can
	// still resolve them.
	if !strings.Contains(after, srv.URL+"/live/3") {
		t.Errorf("the newest item is not addressable:\n%s", after)
	}
	// And every item in the feed is a page that answers.
	if resp, _ := get(t, srv.URL, "/live/3"); resp.StatusCode != http.StatusOK {
		t.Errorf("the newest item 404s: %d", resp.StatusCode)
	}
}

// A schedule nobody can wait for is a schedule nobody tests. The interval is a
// variable so a test can make time pass in milliseconds.
func TestTheScheduleActuallySchedules(t *testing.T) {
	ResetLive()
	original := PublishEvery
	PublishEvery = 40 * time.Millisecond
	t.Cleanup(func() { PublishEvery = original; ResetLive() })

	srv := Server(t)
	time.Sleep(130 * time.Millisecond)

	_, body := get(t, srv.URL, "/live/")
	// Three intervals elapsed, so four articles: the first plus one per tick.
	if !strings.Contains(body, "published so far") {
		t.Fatalf("no count:\n%s", body)
	}
	if strings.Contains(body, "1 published so far") {
		t.Errorf("nothing was published by the clock:\n%s", body)
	}
}
