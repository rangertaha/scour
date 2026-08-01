// SPDX-License-Identifier: GPL-3.0-or-later

package webdriver

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rangertaha/scour/internal/transport"
)

// The page a crawler without a browser sees: two script tags and an empty
// mount point. Everything a crawl wants arrives only after the script runs.
const spa = `<!doctype html>
<html><head><title>cars</title></head>
<body>
<div id="root"></div>
<script>
  document.getElementById('root').innerHTML =
    '<h1>Ford F-Series</h1><p>A full-size pickup.</p><a href="/cars/2/">next</a>';
</script>
</body></html>`

// requireChrome skips when no browser is installed, so the suite still passes
// on a machine that has none.
func requireChrome(t *testing.T) string {
	t.Helper()
	for _, name := range []string{"google-chrome", "chromium", "chromium-browser", "google-chrome-stable"} {
		if path, err := exec.LookPath(name); err == nil {
			return path
		}
	}
	t.Skip("no chrome on this machine")
	return ""
}

func TestRendersAPageThatPlainHTTPCannotRead(t *testing.T) {
	requireChrome(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, spa)
	}))
	defer srv.Close()

	rt, err := New(Options{Timeout: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/cars/1/", nil)
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

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "html") {
		t.Errorf("content type = %q", ct)
	}
	// The whole reason the browser exists: content and a link that the served
	// HTML never contained.
	if !strings.Contains(string(body), "Ford F-Series") {
		t.Errorf("the script never ran:\n%s", body)
	}
	if !strings.Contains(string(body), `href="/cars/2/"`) {
		t.Errorf("the rendered link is missing, so the crawl would stop here:\n%s", body)
	}
}

// End to end: the escalating transport in front of a real browser, against a
// server that serves a shell to HTTP and nothing else.
func TestEscalationRendersForReal(t *testing.T) {
	requireChrome(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, spa)
	}))
	defer srv.Close()

	base, err := transport.New("http", transport.Config{})
	if err != nil {
		t.Fatal(err)
	}
	browser, err := transport.New("webdriver", transport.Config{Timeout: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	tr, ok := browser.(*Transport)
	if !ok {
		t.Fatalf("New(webdriver) returned %T", browser)
	}
	defer tr.Close()

	var escalated []string
	e := &transport.Escalating{
		Base: base, Browser: browser, Policy: transport.Auto,
		OnEscalate: func(host string) { escalated = append(escalated, host) },
	}

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/cars/1/", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := e.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if !strings.Contains(string(body), "Ford F-Series") {
		t.Errorf("escalation did not happen against a real browser:\n%s", body)
	}
	if len(escalated) != 1 {
		t.Errorf("escalations recorded: %v, want the host once", escalated)
	}
}

// The pool is the only thing standing between a wide crawl and a machine full
// of browser tabs, so it is worth proving it holds.
func TestPoolBoundsConcurrentRenders(t *testing.T) {
	requireChrome(t)

	var mu sync.Mutex
	var live, peak int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		live++
		if live > peak {
			peak = live
		}
		mu.Unlock()

		time.Sleep(150 * time.Millisecond)

		mu.Lock()
		live--
		mu.Unlock()

		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, spa)
	}))
	defer srv.Close()

	rt, err := New(Options{Pool: 2, Timeout: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	var wg sync.WaitGroup
	for i := range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/cars/1/", nil)
			if err != nil {
				return
			}
			resp, err := rt.RoundTrip(req)
			if err != nil {
				t.Errorf("render %d: %v", i, err)
				return
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}()
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if peak > 2 {
		t.Errorf("%d renders ran at once, the pool allows 2", peak)
	}
	if peak == 0 {
		t.Error("nothing rendered")
	}
}

func TestOnlyGETIsRendered(t *testing.T) {
	rt, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	// No browser starts here: the method is rejected before the allocator is
	// ever touched, which is what makes this test cheap enough to always run.
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://example.com/", strings.NewReader("a=1"))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := rt.RoundTrip(req)
	if err == nil {
		resp.Body.Close()
		t.Error("a POST was quietly turned into a page load")
	}
}

func TestACancelledRequestDoesNotStartABrowser(t *testing.T) {
	rt, err := New(Options{Pool: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	// Fill the only slot, so the next request has to wait on a context that is
	// already dead.
	rt.slot <- struct{}{}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://example.com/", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := rt.RoundTrip(req)
	if err == nil {
		resp.Body.Close()
		t.Error("a cancelled request still queued for a tab")
	}
}

func TestRegisteredUnderWebdriver(t *testing.T) {
	if !transport.Has("webdriver") {
		t.Fatal("importing this package should register the transport")
	}
	rt, err := transport.New("webdriver", transport.Config{UserAgent: "scour/test"})
	if err != nil {
		t.Fatalf("New(webdriver): %v", err)
	}
	tr, ok := rt.(*Transport)
	if !ok {
		t.Fatalf("New(webdriver) returned %T", rt)
	}
	tr.Close()
}

func TestSynthesiseWrapsBareFragments(t *testing.T) {
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://example.com/", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp := synthesise(req, "just text")
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(body), "<html>") {
		t.Errorf("a bare fragment was not made parseable: %q", body)
	}
	if resp.ContentLength != int64(len(body)) {
		t.Errorf("ContentLength = %d, body is %d", resp.ContentLength, len(body))
	}
	if resp.Request != req {
		t.Error("the response lost its request, which colly needs to resolve links")
	}
}
