// SPDX-License-Identifier: MIT

package wom_test

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rangertaha/scour/internal/wom"
)

// addOne builds a graph holding a single document.
func addOne(t *testing.T, uri, contentType, body string) *wom.WOM {
	t.Helper()
	w := wom.New()
	if err := w.AddBody(uri, contentType, []byte(body)); err != nil {
		t.Fatalf("AddBody(%s): %v", uri, err)
	}
	return w
}

// fetch performs a GET bound to the test's context.
func fetch(t *testing.T, url string) (*http.Response, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	return http.DefaultClient.Do(req)
}

// firstWhere returns the first node in the graph satisfying pred.
func firstWhere(w *wom.WOM, pred func(*wom.Node) bool) *wom.Node {
	found := w.Find(pred)
	if len(found) == 0 {
		return nil
	}
	return found[0]
}

func TestGraphShape(t *testing.T) {
	t.Parallel()

	w := wom.New()
	for _, u := range []string{
		"https://example.com/a",
		"https://example.com/b",
		"https://other.com/c",
	} {
		if err := w.AddBody(u, "text/html", []byte("<html><body><p>hi</p></body></html>")); err != nil {
			t.Fatalf("AddBody(%s): %v", u, err)
		}
	}

	if got := w.Len(); got != 3 {
		t.Errorf("Len() = %d, want 3", got)
	}
	if got := len(w.Domains()); got != 2 {
		t.Errorf("Domains() = %d, want 2", got)
	}
	if got := len(w.Documents()); got != 3 {
		t.Errorf("Documents() = %d, want 3", got)
	}

	// root -> domain -> uri -> document
	root := w.Root()
	if root.Kind != wom.KindRoot {
		t.Fatalf("root kind = %v, want root", root.Kind)
	}
	domain := root.Children[0]
	if domain.Kind != wom.KindDomain || domain.Name != "example.com" {
		t.Fatalf("domain = %v %q, want domain example.com", domain.Kind, domain.Name)
	}
	uri := domain.Children[0]
	if uri.Kind != wom.KindURI {
		t.Fatalf("uri kind = %v, want uri", uri.Kind)
	}
	if u := uri.URI(); u == nil || u.Path != "/a" {
		t.Fatalf("uri.URI() = %v, want path /a", u)
	}
	doc := uri.Children[0]
	if doc.Kind != wom.KindDocument || doc.Format() != wom.FormatHTML {
		t.Fatalf("document = %v %v, want document html", doc.Kind, doc.Format())
	}
}

func TestAddReplacesDocumentForSameURL(t *testing.T) {
	t.Parallel()

	w := addOne(t, "https://example.com/x", "text/html", "<html><body><p>first</p></body></html>")
	if err := w.AddBody("https://example.com/x", "text/html", []byte("<html><body><p>second</p></body></html>")); err != nil {
		t.Fatalf("AddBody: %v", err)
	}
	if got := w.Len(); got != 1 {
		t.Errorf("Len() = %d, want 1 after re-adding the same url", got)
	}
	if got := len(w.Documents()); got != 1 {
		t.Fatalf("Documents() = %d, want 1", got)
	}
	if text := w.Documents()[0].Text(); !strings.Contains(text, "second") || strings.Contains(text, "first") {
		t.Errorf("document text = %q, want only the replacement", text)
	}
}

func TestAddFromHTTPResponse(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		rw.Header().Set("Content-Type", "text/html")
		fmt.Fprint(rw, "<html><body><h1>Title</h1></body></html>")
	}))
	defer srv.Close()

	resp, err := fetch(t, srv.URL+"/page")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	w := wom.New()
	if err := w.Add(resp); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if w.Len() != 1 {
		t.Errorf("Len() = %d, want 1", w.Len())
	}
	if h1 := firstWhere(w, func(n *wom.Node) bool { return n.Name == "h1" }); h1 == nil {
		t.Error("no h1 node in the graph")
	} else if h1.Text() != "Title" {
		t.Errorf("h1 text = %q, want %q", h1.Text(), "Title")
	}
}

func TestAddRejectsResponseWithoutURL(t *testing.T) {
	t.Parallel()

	w := wom.New()
	if err := w.Add(nil); !errors.Is(err, wom.ErrNoURL) {
		t.Errorf("Add(nil) = %v, want ErrNoURL", err)
	}
	if err := w.Add(&http.Response{}); !errors.Is(err, wom.ErrNoURL) {
		t.Errorf("Add(response without request) = %v, want ErrNoURL", err)
	}
}

func TestAddRejectsUnknownFormat(t *testing.T) {
	t.Parallel()

	w := wom.New()
	err := w.AddBody("https://example.com/blob", "", []byte("\x00\x01\x02"))
	if !errors.Is(err, wom.ErrUnknownFormat) {
		t.Errorf("AddBody with unidentifiable body = %v, want ErrUnknownFormat", err)
	}
	if w.Len() != 0 {
		t.Errorf("Len() = %d, want the rejected document not to be stored", w.Len())
	}
}

func TestParseHTMLPathsAndSelectors(t *testing.T) {
	t.Parallel()

	const body = `<html><body>
		<ul id="list">
			<li><a href="/one">One</a></li>
			<li><a href="/two">Two</a></li>
		</ul>
	</body></html>`

	w := addOne(t, "https://example.com/", "text/html", body)

	links := w.Find(func(n *wom.Node) bool { return n.Name == "a" })
	if len(links) != 2 {
		t.Fatalf("found %d links, want 2", len(links))
	}

	second := links[1]
	if got, want := second.XPath(), "/html[1]/body[1]/ul[1]/li[2]/a[1]"; got != want {
		t.Errorf("xpath = %q, want %q", got, want)
	}
	// The <ul> carries an id, which terminates the selector chain.
	if got, want := second.Selector(), "#list > li:nth-of-type(2) > a"; got != want {
		t.Errorf("selector = %q, want %q", got, want)
	}
	// Path mirrors XPath for markup.
	if second.Path() != second.XPath() {
		t.Errorf("path = %q, want it to equal xpath %q", second.Path(), second.XPath())
	}
	if href, ok := second.Attr("href"); !ok || href != "/two" {
		t.Errorf("href = %q %v, want /two true", href, ok)
	}

	attr := firstWhere(w, func(n *wom.Node) bool {
		return n.Kind == wom.KindAttribute && n.Name == "href" && n.Value == "/two"
	})
	if attr == nil {
		t.Fatal("href attribute node not found")
	}
	if got, want := attr.XPath(), "/html[1]/body[1]/ul[1]/li[2]/a[1]/@href"; got != want {
		t.Errorf("attribute xpath = %q, want %q", got, want)
	}
}

func TestParseFeed(t *testing.T) {
	t.Parallel()

	const body = `<?xml version="1.0"?>
	<rss version="2.0"><channel>
		<title>News</title>
		<item><title>First</title><link>https://example.com/1</link></item>
		<item><title>Second</title><link>https://example.com/2</link></item>
	</channel></rss>`

	w := addOne(t, "https://example.com/feed.xml", "application/rss+xml", body)
	if got := w.Documents()[0].Format(); got != wom.FormatFeed {
		t.Errorf("format = %v, want feed", got)
	}

	items := w.Find(func(n *wom.Node) bool { return n.Name == "item" })
	if len(items) != 2 {
		t.Fatalf("found %d items, want 2", len(items))
	}
	if got, want := items[1].XPath(), "/rss[1]/channel[1]/item[2]"; got != want {
		t.Errorf("item xpath = %q, want %q", got, want)
	}
	if got := items[0].Text(); !strings.Contains(got, "First") {
		t.Errorf("item text = %q, want it to contain First", got)
	}
}

func TestParseJSONPaths(t *testing.T) {
	t.Parallel()

	const body = `{"data":{"items":[{"name":"a"},"loose",{"name":"b"}],"total":2}}`

	w := addOne(t, "https://example.com/api", "application/json", body)

	nameB := firstWhere(w, func(n *wom.Node) bool {
		return n.Kind == wom.KindValue && n.Value == "b"
	})
	if nameB == nil {
		t.Fatal(`value "b" not found`)
	}
	// The third array element, even though the second is a bare string: array
	// indices are positional across the whole array.
	if got, want := nameB.Path(), "$.data.items[2].name"; got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
	// JSON is not addressable by XPath or CSS.
	if nameB.XPath() != "" || nameB.Selector() != "" {
		t.Errorf("xpath = %q, selector = %q, want both empty for json", nameB.XPath(), nameB.Selector())
	}

	total := firstWhere(w, func(n *wom.Node) bool {
		return n.Kind == wom.KindValue && n.Value == "2"
	})
	if total == nil {
		t.Fatal("total value not found")
	}
	if got, want := total.Path(), "$.data.total"; got != want {
		t.Errorf("path = %q, want %q", got, want)
	}
}

func TestParseJSONIsDeterministic(t *testing.T) {
	t.Parallel()

	const body = `{"z":1,"a":2,"m":3}`
	var first []string
	for i := 0; i < 5; i++ {
		w := addOne(t, "https://example.com/api", "application/json", body)
		var paths []string
		w.Walk(func(n *wom.Node) bool {
			if n.Kind == wom.KindValue {
				paths = append(paths, n.Path())
			}
			return true
		})
		if i == 0 {
			first = paths
			continue
		}
		if strings.Join(paths, ",") != strings.Join(first, ",") {
			t.Fatalf("run %d produced %v, want %v", i, paths, first)
		}
	}
}

func TestParseCSS(t *testing.T) {
	t.Parallel()

	const body = `
	@media (min-width: 40em) {
		.card .title { color: #333; font-weight: bold; }
	}
	.price::before { content: "$"; }`

	w := addOne(t, "https://example.com/app.css", "text/css", body)
	if got := w.Documents()[0].Format(); got != wom.FormatCSS {
		t.Errorf("format = %v, want css", got)
	}

	content := firstWhere(w, func(n *wom.Node) bool {
		return n.Kind == wom.KindDecl && n.Name == "content"
	})
	if content == nil {
		t.Fatal("content declaration not found")
	}
	if got, want := content.Path(), ".price::before > content"; got != want {
		t.Errorf("path = %q, want %q", got, want)
	}

	color := firstWhere(w, func(n *wom.Node) bool {
		return n.Kind == wom.KindDecl && n.Name == "color"
	})
	if color == nil {
		t.Fatal("color declaration not found")
	}
	if !strings.HasPrefix(color.Path(), "@media") {
		t.Errorf("nested path = %q, want it to start at the at-rule", color.Path())
	}
}

func TestParseJS(t *testing.T) {
	t.Parallel()

	const body = `
	const config = {"apiKey": "abc123", "retries": 3};
	function loadVehicles() {
		const first = {make: "Toyota", year: 2019};
		return first;
	}`

	w := addOne(t, "https://example.com/app.js", "application/javascript", body)
	if got := w.Documents()[0].Format(); got != wom.FormatJS {
		t.Errorf("format = %v, want js", got)
	}

	// String literals are unquoted so they can be matched as text.
	toyota := firstWhere(w, func(n *wom.Node) bool {
		return n.Kind == wom.KindLiteral && n.Value == "Toyota"
	})
	if toyota == nil {
		t.Fatal(`literal "Toyota" not found`)
	}
	path := toyota.Path()
	for _, want := range []string{"loadVehicles", "first", "make"} {
		if !strings.Contains(path, want) {
			t.Errorf("path = %q, want it to contain %q", path, want)
		}
	}

	if apiKey := firstWhere(w, func(n *wom.Node) bool {
		return n.Kind == wom.KindLiteral && n.Value == "abc123"
	}); apiKey == nil {
		t.Error(`literal "abc123" not found`)
	}
}

func TestParseInvalidBodies(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		uri         string
		contentType string
		body        string
	}{
		{"malformed json", "https://example.com/a.json", "application/json", `{"a":`},
		{"malformed js", "https://example.com/a.js", "application/javascript", `function ( {`},
		{"truncated pdf", "https://example.com/a.pdf", "application/pdf", "%PDF-1.4\ntruncated"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			w := wom.New()
			// The contract is that a bad body produces an error rather than a
			// panic or a half-built document.
			if err := w.AddBody(tt.uri, tt.contentType, []byte(tt.body)); err == nil {
				t.Errorf("AddBody with %s: want an error", tt.name)
			}
			if w.Len() != 0 {
				t.Errorf("Len() = %d, want the failed document not to be stored", w.Len())
			}
		})
	}
}

func TestParseHTMLToleratesMalformedMarkup(t *testing.T) {
	t.Parallel()

	// The HTML parser is defined to recover from anything, so this must not
	// error the way the stricter formats do.
	w := addOne(t, "https://example.com/", "text/html", "<div><p>unclosed<div>nested")
	if w.Len() != 1 {
		t.Errorf("Len() = %d, want 1", w.Len())
	}
}

func TestMaxBodyTruncates(t *testing.T) {
	t.Parallel()

	long := "<html><body>" + strings.Repeat("<p>x</p>", 1000) + "</body></html>"
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		rw.Header().Set("Content-Type", "text/html")
		fmt.Fprint(rw, long)
	}))
	defer srv.Close()

	resp, err := fetch(t, srv.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	w := wom.New(wom.WithMaxBody(64))
	if err := w.Add(resp); err != nil {
		t.Fatalf("Add: %v", err)
	}
	paras := w.Find(func(n *wom.Node) bool { return n.Name == "p" })
	if len(paras) >= 1000 {
		t.Errorf("found %d paragraphs, want the body truncated well below 1000", len(paras))
	}
}

func TestNodeText(t *testing.T) {
	t.Parallel()

	w := addOne(t, "https://example.com/", "text/html",
		`<html><body><div class="c"><span>  Hello   </span> <b>world</b></div></body></html>`)

	div := firstWhere(w, func(n *wom.Node) bool { return n.Name == "div" })
	if div == nil {
		t.Fatal("div not found")
	}
	// Whitespace is collapsed, and attribute values are not part of the text.
	if got, want := div.Text(), "Hello world"; got != want {
		t.Errorf("Text() = %q, want %q", got, want)
	}
	if len(div.Elements()) != 2 {
		t.Errorf("Elements() = %d, want 2 (attributes excluded)", len(div.Elements()))
	}
}

func TestWalkSkipsSubtree(t *testing.T) {
	t.Parallel()

	w := addOne(t, "https://example.com/", "text/html",
		`<html><body><div id="skip"><p>hidden</p></div><p>kept</p></body></html>`)

	var seen []string
	w.Walk(func(n *wom.Node) bool {
		if id, ok := n.Attr("id"); ok && id == "skip" {
			return false
		}
		if n.Kind == wom.KindText {
			seen = append(seen, n.Value)
		}
		return true
	})
	if got := strings.Join(seen, ","); got != "kept" {
		t.Errorf("visited text = %q, want only \"kept\"", got)
	}
}

// buildPDF assembles a minimal PDF with one page per group of lines and a
// correct cross-reference table. Object numbering is: 1 catalog, 2 page tree,
// then a page and a content stream per page, then the shared font.
func buildPDF(pages [][]string) []byte {
	n := len(pages)
	fontObj := 3 + 2*n

	kids := make([]string, n)
	for i := range pages {
		kids[i] = fmt.Sprintf("%d 0 R", 3+2*i)
	}

	objects := make([]string, 0, 2+2*len(pages)+1)
	objects = append(objects,
		"<< /Type /Catalog /Pages 2 0 R >>",
		fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), n),
	)
	for i, lines := range pages {
		var content bytes.Buffer
		content.WriteString("BT /F1 12 Tf\n")
		y := 700
		for _, line := range lines {
			fmt.Fprintf(&content, "1 0 0 1 72 %d Tm (%s) Tj\n", y, line)
			y -= 20
		}
		content.WriteString("ET\n")

		objects = append(objects,
			fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] "+
				"/Resources << /Font << /F1 %d 0 R >> >> /Contents %d 0 R >>",
				fontObj, 4+2*i),
			fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", content.Len(), content.String()),
		)
	}
	objects = append(objects, "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")

	var buf bytes.Buffer
	buf.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objects))
	for i, obj := range objects {
		offsets[i] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", i+1, obj)
	}

	xref := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for _, off := range offsets {
		fmt.Fprintf(&buf, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n",
		len(objects)+1, xref)
	return buf.Bytes()
}

func TestParsePDF(t *testing.T) {
	t.Parallel()

	body := buildPDF([][]string{{"Toyota Corolla", "2019 Hybrid"}})

	w := wom.New()
	if err := w.AddBody("https://example.com/spec.pdf", "application/pdf", body); err != nil {
		t.Fatalf("AddBody: %v", err)
	}
	if got := w.Documents()[0].Format(); got != wom.FormatPDF {
		t.Fatalf("format = %v, want pdf", got)
	}

	pages := w.Find(func(n *wom.Node) bool { return n.Kind == wom.KindPage })
	if len(pages) != 1 {
		t.Fatalf("found %d pages, want 1", len(pages))
	}

	lines := w.Find(func(n *wom.Node) bool { return n.Kind == wom.KindLine })
	if len(lines) == 0 {
		t.Fatal("no text lines extracted from the pdf")
	}
	if got, want := lines[0].Path(), "page[1].line[1]"; got != want {
		t.Errorf("line path = %q, want %q", got, want)
	}

	all := make([]string, 0, len(lines))
	for _, l := range lines {
		all = append(all, l.Value)
	}
	joined := strings.Join(all, " ")
	for _, want := range []string{"Toyota", "2019"} {
		if !strings.Contains(joined, want) {
			t.Errorf("extracted text %q, want it to contain %q", joined, want)
		}
	}
}
