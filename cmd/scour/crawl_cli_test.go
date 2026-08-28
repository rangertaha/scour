// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// linked is a two-page site: an index and a story.
func linked(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()

	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			http.NotFound(w, r)
			return
		}
		hits.Add(1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch r.URL.Path {
		case "/":
			fmt.Fprint(w, `<html><head><title>Index</title></head><body><a href="/story">one</a></body></html>`)
		default:
			fmt.Fprint(w, `<html><head><meta property="og:title" content="A story"></head><body></body></html>`)
		}
	}))
	t.Cleanup(server.Close)
	return server, &hits
}

func crawlJob(t *testing.T, server *httptest.Server) string {
	t.Helper()

	host := strings.TrimPrefix(server.URL, "http://")
	return document(t, fmt.Sprintf(`
job "news" {
  domains = ["%s"]
  start   = ["%s/"]

  item "article" {
    property "title" {
      type     = str
      required = true
    }
  }

  scheduler {
    rate = "1ms"
  }

  exporter "jsonlines" "article" {}
}
`, host, server.URL))
}

// TestCrawlSaysAndSaysWhatItDid.
func TestCrawlSaysAndSaysWhatItDid(t *testing.T) {
	server, hits := linked(t)
	path := crawlJob(t, server)

	// The exporter writes relative to the working directory.
	back, _ := os.Getwd()
	if err := os.Chdir(filepath.Dir(path)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(back) })

	out, errOut, code := run(t, "crawl", path)
	if code != 0 {
		t.Fatalf("exit %d\n%s%s", code, out, errOut)
	}

	for _, want := range []string{"finished", "fetched", "items", "exported", "jsonlines.article"} {
		if !strings.Contains(out, want) {
			t.Errorf("the summary does not mention %q:\n%s", want, out)
		}
	}
	if hits.Load() != 2 {
		t.Errorf("the site was asked %d times", hits.Load())
	}

	body, err := os.ReadFile(filepath.Join(filepath.Dir(path), "article.jsonl"))
	if err != nil {
		t.Fatalf("nothing was exported: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(body)), "\n")
	if len(lines) != 2 {
		t.Fatalf("exported %d records:\n%s", len(lines), body)
	}
	for _, line := range lines {
		var r map[string]any
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Errorf("not JSON: %v", err)
		}
	}
}

// TestASecondRunHasNothingLeftToDo, because the frontier remembers.
func TestASecondRunHasNothingLeftToDo(t *testing.T) {
	server, hits := linked(t)
	path := crawlJob(t, server)

	back, _ := os.Getwd()
	os.Chdir(filepath.Dir(path))
	t.Cleanup(func() { os.Chdir(back) })

	if _, _, code := run(t, "crawl", path); code != 0 {
		t.Fatal("first run failed")
	}
	first := hits.Load()

	out, _, code := run(t, "crawl", path)
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if hits.Load() != first {
		t.Errorf("the second run refetched: %d then %d", first, hits.Load())
	}
	if !strings.Contains(out, "fetched   0") {
		t.Errorf("the second run says it fetched something:\n%s", out)
	}
}

// TestFreshForgetsWhatWasQueued.
func TestFreshForgetsWhatWasQueued(t *testing.T) {
	server, hits := linked(t)
	path := crawlJob(t, server)

	back, _ := os.Getwd()
	os.Chdir(filepath.Dir(path))
	t.Cleanup(func() { os.Chdir(back) })

	if _, _, code := run(t, "crawl", path); code != 0 {
		t.Fatal("first run failed")
	}
	first := hits.Load()

	// The cache still holds the bodies, so the site is not asked again even
	// though the frontier has forgotten them. That is the point of the cache.
	out, _, code := run(t, "crawl", "--fresh", path)
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if !strings.Contains(out, "fetched   2") {
		t.Errorf("--fresh did not crawl again:\n%s", out)
	}
	if hits.Load() != first {
		t.Errorf("the site was asked again despite the cache: %d then %d", first, hits.Load())
	}
	if !strings.Contains(out, "from the cache") {
		t.Errorf("the summary does not say the pages came from the cache:\n%s", out)
	}
}
