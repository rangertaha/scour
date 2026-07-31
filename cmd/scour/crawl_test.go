// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testSite serves a two-level site plus one image, which is enough to exercise
// link following, the content-type filter and the summary table.
func testSite(t *testing.T) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()
	html := func(pattern, body string) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, body)
		})
	}
	html("/", `<a href="/cars/">cars</a> <a href="/logo.png">logo</a>`)
	html("/cars/", `<a href="/cars/one/">one</a>`)
	html("/cars/one/", `<h1>Ford F-Series 2026</h1>`)
	mux.HandleFunc("/logo.png", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		fmt.Fprint(w, "not really a png")
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// crawlDir prepares a data directory whose config makes crawls fast and
// offline-safe: no delay between requests, and robots.txt ignored because
// httptest serves none.
func crawlDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	body := "[crawl]\nrate = \"0s\"\nrobots = false\nconcurrency = 4\n"
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestCrawlEndToEnd(t *testing.T) {
	srv := testSite(t)
	dir := crawlDir(t)

	runOK(t, dir, "add", "vehicle", "-u", srv.URL+"/")
	out := runOK(t, dir, "crawl", "vehicle", "--depth", "5")

	for _, want := range []string{"PROBABILITY", "MATCHES", "SPEED", "LATENCY", "RATE", "URL"} {
		if !strings.Contains(out, want) {
			t.Errorf("summary table missing the %s column:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "fetched") {
		t.Errorf("no crawl summary line:\n%s", out)
	}
	if !strings.Contains(out, "/cars/") {
		t.Errorf("the crawl did not report reaching /cars/:\n%s", out)
	}
}

func TestCrawlThenStatus(t *testing.T) {
	srv := testSite(t)
	dir := crawlDir(t)

	runOK(t, dir, "add", "vehicle", "-u", srv.URL+"/")
	runOK(t, dir, "crawl", "vehicle", "--depth", "5")

	out := runOK(t, dir, "list", "vehicle")
	for _, want := range []string{"item", "targets", "frontier", "visited", "cache", "matches", "model"} {
		if !strings.Contains(out, want) {
			t.Errorf("status missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "not trained yet") {
		t.Errorf("status should say the model is untrained:\n%s", out)
	}
	if !strings.Contains(out, "html") {
		t.Errorf("status should report the formats crawled:\n%s", out)
	}
}

func TestCrawlSkipsImagesByDefault(t *testing.T) {
	srv := testSite(t)
	dir := crawlDir(t)

	runOK(t, dir, "add", "vehicle", "-u", srv.URL+"/")
	out := runOK(t, dir, "crawl", "vehicle", "--depth", "5")

	if !strings.Contains(out, "skipped") {
		t.Errorf("the image should have been reported as skipped:\n%s", out)
	}
	if strings.Contains(out, "logo.png") {
		t.Errorf("an image should not appear in the summary:\n%s", out)
	}
}

func TestCrawlMaxPages(t *testing.T) {
	srv := testSite(t)
	dir := crawlDir(t)

	runOK(t, dir, "add", "vehicle", "-u", srv.URL+"/")
	out := runOK(t, dir, "crawl", "vehicle", "--depth", "5", "--max-pages", "1")

	if strings.Contains(out, "/cars/one/") {
		t.Errorf("a one-page budget should not have reached the third level:\n%s", out)
	}
}

func TestCrawlJSON(t *testing.T) {
	srv := testSite(t)
	dir := crawlDir(t)

	runOK(t, dir, "add", "vehicle", "-u", srv.URL+"/")
	out := runOK(t, dir, "crawl", "vehicle", "--depth", "5", "--json")

	if !strings.Contains(out, `"URL"`) || !strings.Contains(out, `"Score"`) {
		t.Errorf("json output missing the frontier fields:\n%s", out)
	}
}

func TestCrawlWithoutTargetsExplainsItself(t *testing.T) {
	dir := crawlDir(t)
	runOK(t, dir, "add", "vehicle")

	out, err := run(t, dir, "crawl", "vehicle")
	if err == nil {
		t.Fatal("crawling an item with no targets must fail")
	}
	if !strings.Contains(err.Error()+out, "scour add") {
		t.Errorf("the error should say how to add a target: %v\n%s", err, out)
	}
}

func TestCrawlResumesAndResets(t *testing.T) {
	srv := testSite(t)
	dir := crawlDir(t)

	runOK(t, dir, "add", "vehicle", "-u", srv.URL+"/")
	runOK(t, dir, "crawl", "vehicle", "--depth", "5")

	// A second crawl sees the same pages again; the frontier persists.
	before := runOK(t, dir, "list", "vehicle")
	runOK(t, dir, "crawl", "vehicle", "--depth", "5", "--reset")
	after := runOK(t, dir, "list", "vehicle")

	if !strings.Contains(before, "visited") || !strings.Contains(after, "visited") {
		t.Errorf("status should report visits before and after:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestStatusOnUnknownItem(t *testing.T) {
	dir := crawlDir(t)
	if _, err := run(t, dir, "list", "absent"); err == nil {
		t.Error("status on an unknown item must fail")
	}
}
