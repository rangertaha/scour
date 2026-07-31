// SPDX-License-Identifier: GPL-3.0-or-later

package transport

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// fake is a transport that returns a fixed page and counts its calls.
type fake struct {
	body   string
	status int
	ctype  string
	err    error
	calls  atomic.Int64
}

func (f *fake) RoundTrip(req *http.Request) (*http.Response, error) {
	f.calls.Add(1)
	if f.err != nil {
		return nil, f.err
	}
	status := f.status
	if status == 0 {
		status = http.StatusOK
	}
	ctype := f.ctype
	if ctype == "" {
		ctype = "text/html"
	}
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{ctype}},
		Body:       io.NopCloser(strings.NewReader(f.body)),
		Request:    req,
	}, nil
}

func get(t *testing.T, rt http.RoundTripper, url string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp, string(body)
}

// The shell a single-page application serves: scripts, a mount point, and
// nothing a crawler can use.
const shell = `<html><head><script src="/app.js"></script></head><body><div id="root"></div></body></html>`

// What the same URL looks like once the scripts have run.
const rendered = `<html><body><div id="root"><h1>Ford F-Series</h1><a href="/cars/2/">next</a></div></body></html>`

// A page that needs no browser: real text and real links.
const served = `<html><body><h1>Ford F-Series 2026</h1>
<p>` + longText + `</p><a href="/cars/2/">next</a></body></html>`

// Comfortably past substantialText, so the test turns on the signal it means
// to test rather than on the exact length of this sentence.
const longText = "A full-size pickup with a great deal of descriptive copy about it, " +
	"long enough that no one could mistake this page for an empty shell waiting " +
	"on JavaScript to arrive and fill it in. It has a bed, it tows things, and " +
	"the specification table below runs to several hundred rows of trim levels."

func TestAutoEscalatesOnlyWhenThePageIsEmpty(t *testing.T) {
	base := &fake{body: shell}
	browser := &fake{body: rendered}
	e := &Escalating{Base: base, Browser: browser, Policy: Auto}

	_, body := get(t, e, "http://example.com/cars/1/")
	if !strings.Contains(body, "Ford F-Series") {
		t.Errorf("got the shell back, not the rendered page:\n%s", body)
	}
	if browser.calls.Load() != 1 {
		t.Errorf("browser called %d times, want once", browser.calls.Load())
	}
}

func TestAutoLeavesAServerRenderedPageAlone(t *testing.T) {
	base := &fake{body: served}
	browser := &fake{body: rendered}
	e := &Escalating{Base: base, Browser: browser, Policy: Auto}

	_, body := get(t, e, "http://example.com/cars/1/")
	if !strings.Contains(body, "2026") {
		t.Errorf("the served page was replaced:\n%s", body)
	}
	// Paying for a browser on a page that did not need one is the expensive
	// mistake this policy exists to avoid.
	if browser.calls.Load() != 0 {
		t.Errorf("browser called %d times on a page that did not need it", browser.calls.Load())
	}
}

func TestEscalationIsRememberedPerHost(t *testing.T) {
	base := &fake{body: shell}
	browser := &fake{body: rendered}

	var recorded []string
	e := &Escalating{
		Base: base, Browser: browser, Policy: Auto,
		OnEscalate: func(host string) { recorded = append(recorded, host) },
	}

	for range 3 {
		get(t, e, "http://example.com/cars/1/")
	}

	// After the first page proves the host needs a browser, the rest of the
	// crawl should stop paying for an HTTP attempt it will throw away.
	if base.calls.Load() != 1 {
		t.Errorf("plain http called %d times, want once before the host was remembered", base.calls.Load())
	}
	if browser.calls.Load() != 3 {
		t.Errorf("browser called %d times, want every page after the first", browser.calls.Load())
	}
	if len(recorded) != 1 || recorded[0] != "example.com" {
		t.Errorf("escalation recorded as %v, want the host once", recorded)
	}
}

// A crawl should not have to relearn what an earlier one already paid for.
func TestPrimedHostsSkipTheWastedRequest(t *testing.T) {
	base := &fake{body: shell}
	browser := &fake{body: rendered}

	var recorded []string
	e := &Escalating{
		Base: base, Browser: browser, Policy: Auto,
		OnEscalate: func(host string) { recorded = append(recorded, host) },
	}
	e.Prime("example.com", "")

	_, body := get(t, e, "http://example.com/cars/1/")
	if !strings.Contains(body, "Ford F-Series") {
		t.Errorf("a primed host was not rendered:\n%s", body)
	}
	if base.calls.Load() != 0 {
		t.Error("a primed host still made the http attempt it was primed to skip")
	}
	// Nothing was discovered, so nothing should be reported as discovered.
	if len(recorded) != 0 {
		t.Errorf("priming reported an escalation: %v", recorded)
	}

	// An unrelated host is unaffected.
	other := &fake{body: served}
	e2 := &Escalating{Base: other, Browser: browser, Policy: Auto}
	e2.Prime("example.com")
	get(t, e2, "http://elsewhere.com/")
	if other.calls.Load() != 1 {
		t.Error("priming one host changed how another is fetched")
	}
}

func TestNeverAndAlways(t *testing.T) {
	base := &fake{body: shell}
	browser := &fake{body: rendered}

	never := &Escalating{Base: base, Browser: browser, Policy: Never}
	get(t, never, "http://example.com/")
	if browser.calls.Load() != 0 {
		t.Error("never used the browser anyway")
	}

	base2 := &fake{body: served}
	browser2 := &fake{body: rendered}
	always := &Escalating{Base: base2, Browser: browser2, Policy: Always}
	get(t, always, "http://example.com/")
	if base2.calls.Load() != 0 {
		t.Error("always still made a plain http request first")
	}
	if browser2.calls.Load() != 1 {
		t.Error("always did not use the browser")
	}
}

// A browser that will not start is a degraded crawl, not a failed one.
func TestBrowserFailureKeepsThePlainResponse(t *testing.T) {
	base := &fake{body: shell}
	browser := &fake{err: errors.New("no chrome here")}
	e := &Escalating{Base: base, Browser: browser, Policy: Auto}

	resp, body := get(t, e, "http://example.com/")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d", resp.StatusCode)
	}
	if !strings.Contains(body, "root") {
		t.Errorf("the plain response was lost when the browser failed:\n%s", body)
	}
}

func TestNoBrowserConfiguredIsPlainHTTP(t *testing.T) {
	base := &fake{body: shell}
	e := &Escalating{Base: base, Policy: Auto}

	if _, body := get(t, e, "http://example.com/"); !strings.Contains(body, "root") {
		t.Errorf("body = %s", body)
	}
	if base.calls.Load() != 1 {
		t.Errorf("plain http called %d times", base.calls.Load())
	}
}

func TestNonHTMLIsNeverEscalated(t *testing.T) {
	base := &fake{body: `{"a":1}`, ctype: "application/json"}
	browser := &fake{body: rendered}
	e := &Escalating{Base: base, Browser: browser, Policy: Auto}

	get(t, e, "http://example.com/data.json")
	if browser.calls.Load() != 0 {
		t.Error("a json response was sent to the browser")
	}
}

func TestErrorResponsesAreNeverEscalated(t *testing.T) {
	base := &fake{body: shell, status: http.StatusNotFound}
	browser := &fake{body: rendered}
	e := &Escalating{Base: base, Browser: browser, Policy: Auto}

	// Rendering a 404 would turn a failure into a page, hiding it from the
	// status-class counters the crawl reports.
	get(t, e, "http://example.com/missing")
	if browser.calls.Load() != 0 {
		t.Error("an error response was sent to the browser")
	}
}

// The body has to survive being inspected, or every page would arrive empty.
func TestBodyIsStillReadableAfterInspection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, served)
	}))
	defer srv.Close()

	base, err := NewHTTP(Config{})
	if err != nil {
		t.Fatal(err)
	}
	e := &Escalating{Base: base, Browser: &fake{body: rendered}, Policy: Auto}

	_, body := get(t, e, srv.URL)
	if !strings.Contains(body, "F-Series") {
		t.Errorf("body was consumed by the inspection:\n%q", body)
	}
}

func TestParsePolicy(t *testing.T) {
	tests := map[string]Policy{
		"":         Auto,
		"auto":     Auto,
		" AUTO ":   Auto,
		"never":    Never,
		"Always":   Always,
		"nonsense": Auto,
	}
	for in, want := range tests {
		if got := ParsePolicy(in); got != want {
			t.Errorf("ParsePolicy(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestClientRenderedNeedsAllThreeSignals(t *testing.T) {
	htmlResp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/html"}},
	}

	tests := []struct {
		name string
		body string
		want bool
	}{
		{"empty shell with scripts", shell, true},
		{"no scripts, so nothing to wait for", `<html><body><div id="root"></div></body></html>`, false},
		{"scripts but real text", `<html><script src=a.js></script><body>` + longText + `</body></html>`, false},
		{"scripts but real links", `<html><script src=a.js></script><body><a href="/x">x</a></body></html>`, false},
		{"anchors without href do not count", `<html><script src=a.js></script><body><a>x</a></body></html>`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := clientRendered(htmlResp, []byte(tt.body)); got != tt.want {
				t.Errorf("clientRendered = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRegistry(t *testing.T) {
	if !Has("http") {
		t.Error("the plain transport should always be registered")
	}
	if _, err := New("http", Config{}); err != nil {
		t.Errorf("New(http): %v", err)
	}
	if _, err := New("", Config{}); err != nil {
		t.Errorf("an empty name should give the plain transport: %v", err)
	}
	if _, err := New("nonexistent", Config{}); err == nil {
		t.Error("an unknown transport must be an error rather than a silent default")
	}
}

// Only the first megabyte of a page is inspected, because judging one does not
// need more. Handing back only that megabyte is a different thing, and is what
// used to happen: every page over a megabyte was silently truncated for the
// cache, the parser and extraction alike. A real corpus had 32 of 1,436 pages
// at exactly 1,048,576 bytes.
func TestReadBodyLeavesTheWholeBodyReadable(t *testing.T) {
	const size = (1 << 20) + 4096
	full := bytes.Repeat([]byte("a"), size)
	copy(full[size-4:], "tail")

	resp := &http.Response{Body: io.NopCloser(bytes.NewReader(full))}
	head, ok := readBody(resp)
	if !ok {
		t.Fatal("readBody reported failure on a readable body")
	}
	if len(head) != 1<<20 {
		t.Errorf("inspected %d bytes, want the first megabyte only", len(head))
	}

	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != size {
		t.Fatalf("caller could read %d bytes of a %d byte page", len(got), size)
	}
	if !bytes.Equal(got, full) {
		t.Error("the body the caller read is not the body that arrived")
	}
}
