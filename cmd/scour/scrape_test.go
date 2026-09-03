// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

const story = `<!doctype html>
<html>
<head>
  <meta property="og:title" content="Something happened yesterday">
  <meta property="article:published_time" content="2026-08-04T09:15:00Z">
</head>
<body>
  <article class="article-body"><p>The body of it.</p></article>
  <a href="/news/other.html">next</a>
</body>
</html>`

// site counts what it was asked for, which is the only way to prove the second
// run did not reach it.
func site(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()

	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			http.NotFound(w, r)
			return
		}
		hits.Add(1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(story))
	}))
	t.Cleanup(server.Close)
	return server, &hits
}

func document(t *testing.T, body string) string {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "job.hcl")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

const tryJob = `
job "news" {
  start = ["https://example.com/"]

  item "article" {
    property "title" {
      type     = str
      required = true
    }

    property "published_time" {
      type       = date
      transforms = [datetime]
    }

    property "price" {
      type     = str
      required = true
      css      = [".price"]
    }
  }
}
`

// TestScrapeShowsWhatEachPropertyFoundAndWhere. A value on its own does not tell
// you whether the locator will hold on the next page.
func TestScrapeShowsWhatEachPropertyFoundAndWhere(t *testing.T) {
	server, _ := site(t)
	path := document(t, tryJob)

	out, _, code := run(t, "scrape", path, server.URL+"/news/story.html")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}

	for _, want := range []string{
		"200",
		"text/html",
		"article",
		"Something happened",
		"og:title",
		"2026-08-04T09:15:00Z",
		"price",
		"required, found nothing",
		"2 of 3 properties found",
		"1 links",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the output does not mention %q:\n%s", want, out)
		}
	}
}

// TestTheSecondRunNeverReachesTheSite. That is the whole reason this is usable
// as a development loop.
func TestTheSecondRunNeverReachesTheSite(t *testing.T) {
	server, hits := site(t)
	path := document(t, tryJob)
	url := server.URL + "/news/story.html"

	if _, _, code := run(t, "scrape", path, url); code != 0 {
		t.Fatal("first run failed")
	}
	if hits.Load() != 1 {
		t.Fatalf("the first run asked the site %d times", hits.Load())
	}

	out, _, code := run(t, "scrape", path, url)
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if hits.Load() != 1 {
		t.Errorf("the second run asked the site again: %d hits", hits.Load())
	}
	if !strings.Contains(out, "cached") {
		t.Errorf("the second run does not say it came from the cache:\n%s", out)
	}
}

// TestTheCacheGoesBesideTheDocument, so a job that has not decided where bodies
// live is still tryable and the answer is somewhere findable.
func TestTheCacheGoesBesideTheDocument(t *testing.T) {
	server, _ := site(t)
	path := document(t, tryJob)

	if _, _, code := run(t, "scrape", path, server.URL+"/a"); code != 0 {
		t.Fatal("run failed")
	}

	cache := filepath.Join(filepath.Dir(path), ".scour", "cache")
	if _, err := os.Stat(cache); err != nil {
		t.Errorf("no cache at %s: %v", cache, err)
	}
}

// TestStrictIsForCI: a job whose required properties stopped matching has
// broken, and that should fail a build rather than quietly export nothing.
func TestStrictIsForCI(t *testing.T) {
	server, _ := site(t)
	path := document(t, tryJob)

	out, errOut, code := run(t, "scrape", "--strict", path, server.URL+"/a")
	if code == 0 {
		t.Fatalf("a missing required property passed --strict:\n%s", out)
	}
	if !strings.Contains(errOut+out, "price") {
		t.Errorf("the failure does not name the property:\n%s%s", out, errOut)
	}

	// And with --json, which is the pair a build actually uses: machine
	// readable output and a failing exit code. The JSON was written and the
	// function returned before the strict check ran, so the one combination CI
	// reaches for was the one that always passed.
	jsonOut, jsonErr, jsonCode := run(t, "scrape", "--strict", "--json", path, server.URL+"/a")
	if jsonCode == 0 {
		t.Errorf("--strict passed a missing required property when --json was given too:\n%s", jsonOut)
	}
	if !strings.Contains(jsonErr+jsonOut, "price") {
		t.Errorf("the failure does not name the property:\n%s%s", jsonOut, jsonErr)
	}
	// The JSON is still written, because a build wants both.
	if !strings.Contains(jsonOut, `"url"`) {
		t.Errorf("--strict --json printed no JSON:\n%s", jsonOut)
	}
}

func TestScrapeAsJSON(t *testing.T) {
	server, _ := site(t)
	path := document(t, tryJob)

	out, _, code := run(t, "scrape", "--json", path, server.URL+"/a")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}

	for _, want := range []string{
		`"url"`, `"status": 200`, `"cached": false`, `"spec"`,
		`"how": "semantics"`, `"from"`, `"missing"`, `"links"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the JSON does not have %s:\n%s", want, out)
		}
	}
}

func TestScrapeOneItemOnly(t *testing.T) {
	server, _ := site(t)
	path := document(t, `
job "news" {
  start = ["https://example.com/"]

  item "article" {
    property "title" {
      type = str
    }
  }

  item "product" {
    property "title" {
      type = str
    }
  }
}
`)

	out, _, code := run(t, "scrape", "--item", "product", path, server.URL+"/a")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if strings.Contains(out, "\narticle\n") {
		t.Errorf("--item printed the other shape too:\n%s", out)
	}
	if !strings.Contains(out, "product") {
		t.Errorf("--item printed nothing:\n%s", out)
	}
}

// TestScrapeFallsBackToTheJobsFirstStartURL, because the common case is running it
// on the page the job already names.
func TestScrapeFallsBackToTheJobsFirstStartURL(t *testing.T) {
	server, hits := site(t)
	path := document(t, strings.Replace(tryJob,
		`start = ["https://example.com/"]`,
		`start = ["`+server.URL+`/from-the-document"]`, 1))

	if _, _, code := run(t, "scrape", path); code != 0 {
		t.Fatal("run failed")
	}
	if hits.Load() != 1 {
		t.Error("the start url was not used")
	}
}

func TestScrapeUsage(t *testing.T) {
	server, _ := site(t)
	path := document(t, tryJob)

	for name, args := range map[string][]string{
		"no document": {"scrape"},
		"too many":    {"scrape", path, server.URL, "extra"},
		"url twice":   {"scrape", "--url", server.URL, path, server.URL},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, code := run(t, args...); code != 2 {
				t.Errorf("exit %d, want a usage error", code)
			}
		})
	}
}

// TestScrapeOnAJobWithNowhereToStart is refused by validation before it gets as
// far as fetching, which is the right place: a job with no start urls is not a
// job, whatever command was pointed at it.
func TestScrapeOnAJobWithNowhereToStart(t *testing.T) {
	path := document(t, `
job "news" {
  start = []

  item "article" {
    property "title" {
      type = str
    }
  }
}
`)

	out, errOut, code := run(t, "scrape", path)
	if code != 1 {
		t.Fatalf("exit %d, want the document to be refused", code)
	}
	if !strings.Contains(out+errOut, "start") {
		t.Errorf("the refusal does not say what is missing:\n%s%s", out, errOut)
	}
}

// TestScrapeOnASiteThatIsNotThere fails rather than pretending.
func TestScrapeOnASiteThatIsNotThere(t *testing.T) {
	path := document(t, tryJob)

	if _, _, code := run(t, "scrape", path, "http://127.0.0.1:1/nothing"); code != 3 {
		t.Errorf("exit %d, want a failure", code)
	}
}

// TestRefreshGoesBackToTheSite.
//
// `--refresh` says "fetch even if it is cached, and replace what is there". It
// printed "refreshing" and then served the cached body: the cache middleware
// answers before the fetch is reached, so nothing came back to replace what the
// key held. Somebody iterating on a selector after correcting the page was
// shown the stale one forever, with a line on stderr telling them otherwise.
func TestRefreshGoesBackToTheSite(t *testing.T) {
	var title atomic.Value
	title.Store("FIRST")

	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			http.NotFound(w, r)
			return
		}
		hits.Add(1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<html><head><title>%s</title></head><body><h1>%s</h1>
		  <span class="price">1.00</span></body></html>`, title.Load(), title.Load())
	}))
	t.Cleanup(server.Close)

	path := document(t, tryJob)

	// Fetched and cached.
	if out, errOut, code := run(t, "scrape", path, server.URL+"/a"); code != 0 {
		t.Fatalf("exit %d\n%s%s", code, out, errOut)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("the site was asked %d times for the first fetch", got)
	}

	// The page changes, and a plain scrape is answered from the cache.
	title.Store("SECOND")
	out, _, code := run(t, "scrape", path, server.URL+"/a")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if !strings.Contains(out, "FIRST") {
		t.Errorf("a second scrape did not come from the cache:\n%s", out)
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("the site was asked %d times, so the cache is not being used", got)
	}

	// And --refresh goes back to the site and shows what is there now.
	out, errOut, code := run(t, "scrape", "--refresh", path, server.URL+"/a")
	if code != 0 {
		t.Fatalf("exit %d\n%s%s", code, out, errOut)
	}
	if got := hits.Load(); got != 2 {
		t.Errorf("--refresh asked the site %d times in total, so it did not refetch", got)
	}
	if !strings.Contains(out, "SECOND") {
		t.Errorf("--refresh showed the stale page:\n%s", out)
	}

	// And what it fetched replaced what the key held, so the next plain scrape
	// sees the new page too.
	out, _, code = run(t, "scrape", path, server.URL+"/a")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if !strings.Contains(out, "SECOND") {
		t.Errorf("--refresh did not replace what the cache held:\n%s", out)
	}
}
