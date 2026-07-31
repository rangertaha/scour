// SPDX-License-Identifier: MIT

//go:build e2e

// This file holds the live end-to-end tests. They reach the public internet,
// so they are excluded from the default build and run only when asked for:
//
//	go test -tags e2e -run TestLive -v ./...
//
// Everything they check is also covered offline in news_test.go against
// fixtures captured from the same sites. These exist to catch the case the
// fixtures cannot: a site changing its markup underneath us.
package wom_test

import (
	"context"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/rangertaha/scour/internal/wom"
)

const liveUserAgent = "wom-e2e/1.0 (+https://github.com/rangertaha/scour/internal/wom)"

// fetchLive retrieves a URL with a browser-ish user agent and a bounded
// timeout, returning the body and its content type.
func fetchLive(ctx context.Context, t *testing.T, url string) ([]byte, string) {
	t.Helper()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("build request for %s: %v", url, err)
	}
	req.Header.Set("User-Agent", liveUserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")

	client := &http.Client{Timeout: 45 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Skipf("network unavailable for %s: %v", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Skipf("%s returned %s", url, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		t.Fatalf("read %s: %v", url, err)
	}
	return body, resp.Header.Get("Content-Type")
}

// liveSite is one publicly reachable site to exercise.
type liveSite struct {
	name     string
	index    string
	articles *regexp.Regexp
}

func liveSites() []liveSite {
	return []liveSite{
		{
			name:     "apnews",
			index:    "https://apnews.com",
			articles: regexp.MustCompile(`^https://apnews\.com/article/[a-z0-9-]+$`),
		},
		{
			name:     "planetrugby",
			index:    "https://www.planetrugby.com/news",
			articles: regexp.MustCompile(`^https://www\.planetrugby\.com/news/[a-z0-9-]+$`),
		},
	}
}

// TestLiveArticleSchema walks each site the way a crawler would: fetch the
// index, discover article links from the graph itself, fetch a few articles,
// then induce and apply the schema.
func TestLiveArticleSchema(t *testing.T) {
	for _, s := range liveSites() {
		t.Run(s.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
			defer cancel()

			// Discover article URLs using wom itself: parse the index, then
			// read the hrefs straight out of the graph.
			index := wom.New()
			body, contentType := fetchLive(ctx, t, s.index)
			if err := index.AddBody(s.index, contentType, body); err != nil {
				t.Fatalf("add index: %v", err)
			}

			seen := make(map[string]bool)
			var urls []string
			index.Walk(func(n *wom.Node) bool {
				if n.Kind == wom.KindAttribute && n.Name == "href" &&
					s.articles.MatchString(n.Value) && !seen[n.Value] {
					seen[n.Value] = true
					urls = append(urls, n.Value)
				}
				return len(urls) < 3
			})
			if len(urls) < 2 {
				t.Skipf("found only %d article links on %s; markup may have changed", len(urls), s.index)
			}
			t.Logf("discovered %d articles", len(urls))

			w := wom.New()
			for _, u := range urls {
				body, contentType := fetchLive(ctx, t, u)
				if err := w.AddBody(u, contentType, body); err != nil {
					t.Errorf("add %s: %v", u, err)
					continue
				}
			}
			if w.Len() < 2 {
				t.Fatalf("graph holds %d documents, want at least 2", w.Len())
			}

			model, err := w.Model(articleSchema()...)
			if err != nil {
				t.Fatalf("Model: %v", err)
			}
			article, ok := findItem(model.Items, "article")
			if !ok {
				t.Fatalf("no \"article\" item; got %s", itemsString(model.Items))
			}
			t.Logf("\n%s", article.Tree())

			for _, field := range []string{"title", "authors", "published"} {
				child, ok := findItem(article.Items, field)
				if !ok {
					t.Errorf("field %q was not located on the live site", field)
					continue
				}
				if child.Path == "" {
					t.Errorf("field %q has no path", field)
				}
			}

			records := model.Extract(w)
			if len(records) == 0 {
				t.Fatal("Extract returned nothing from the live pages")
			}
			values := collectValues(records)
			for _, field := range []string{"title", "published"} {
				if len(values[field]) == 0 {
					t.Errorf("no %s extracted; got %v", field, values)
				}
			}
			for _, ts := range values["published"] {
				if _, err := parseLiveDate(ts); err != nil {
					t.Errorf("published value %q does not parse as a date: %v", ts, err)
				}
			}
			t.Logf("extracted %d records; titles=%d authors=%d published=%d",
				len(records), len(values["title"]), len(values["authors"]), len(values["published"]))
		})
	}
}

// parseLiveDate accepts the timestamp layouts news sites actually publish.
func parseLiveDate(s string) (time.Time, error) {
	layouts := []string{
		time.RFC3339,
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05",
		"2006-01-02",
	}
	s = strings.TrimSpace(s)
	var err error
	for _, layout := range layouts {
		var ts time.Time
		if ts, err = time.Parse(layout, s); err == nil {
			return ts, nil
		}
	}
	return time.Time{}, err
}

// TestLiveFixturesStillMatch checks the offline fixtures against the live
// site, so a markup change shows up as a clear signal to recapture them rather
// than as passing tests on stale data.
func TestLiveFixturesStillMatch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	for _, s := range newsSites() {
		t.Run(s.name, func(t *testing.T) {
			offline, err := s.load(t).Model(articleSchema()...)
			if err != nil {
				t.Fatalf("Model from fixtures: %v", err)
			}

			live := wom.New()
			var added int
			for _, u := range s.urls {
				body, contentType := fetchLive(ctx, t, u)
				if err := live.AddBody(u, contentType, body); err == nil {
					added++
				}
			}
			if added == 0 {
				t.Skip("none of the recorded article URLs are still reachable")
			}

			records := offline.Extract(live)
			if len(records) == 0 {
				t.Errorf("locators induced from fixtures no longer match the live pages; recapture testdata/%s-*", s.name)
			}
		})
	}
}
