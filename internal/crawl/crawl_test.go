// SPDX-License-Identifier: GPL-3.0-or-later

package crawl

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rangertaha/scour/internal/cache"
	"github.com/rangertaha/scour/internal/config"
	"github.com/rangertaha/scour/internal/content"
	"github.com/rangertaha/scour/internal/score"
	"github.com/rangertaha/scour/internal/store"
)

// site serves a small tree of pages, plus the awkward cases a crawler meets:
// a PDF, an image, a page that 404s and one that 500s.
func site(t *testing.T) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()
	page := func(pattern, body string) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, body)
		})
	}

	page("/", `<html><body>
		<a href="/cars/">cars</a>
		<a href="/careers/">careers</a>
		<a href="/brochure.pdf">brochure</a>
		<a href="/logo.png">logo</a>
	</body></html>`)
	page("/cars/", `<html><body>
		<a href="/cars/one/">one</a>
		<a href="/cars/two/">two</a>
		<a href="/missing">gone</a>
		<a href="/broken">broken</a>
	</body></html>`)
	page("/cars/one/", `<html><body><h1>Ford F-Series 2026</h1></body></html>`)
	page("/cars/two/", `<html><body><h1>Chevrolet Silverado 2025</h1></body></html>`)
	page("/careers/", `<html><body><h1>Jobs</h1></body></html>`)

	mux.HandleFunc("/brochure.pdf", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		fmt.Fprint(w, "%PDF-1.4 pretend")
	})
	// Deliberately mislabelled: the extension says image, and so does the
	// header, so this must be refused before the body is read.
	mux.HandleFunc("/logo.png", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		fmt.Fprint(w, strings.Repeat("x", 4096))
	})
	mux.HandleFunc("/missing", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "gone", http.StatusNotFound)
	})
	mux.HandleFunc("/broken", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// harness wires a crawler to a throwaway store and cache.
func harness(t *testing.T) (*Crawler, *store.Store, config.Config) {
	t.Helper()

	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "scour.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	cfg := config.Default()
	cfg.Paths.Data = dir
	cfg.Paths.Cache = filepath.Join(dir, "cache")
	cfg.Crawl.Rate = config.Duration(0) // tests should not sleep
	cfg.Crawl.Robots = false            // httptest serves no robots.txt
	cfg.Crawl.Concurrency = 4

	return New(cfg, s, cache.New(cfg.PagesDir())), s, cfg
}

// entity creates an entity with one URL target pointing at the test server.
func entity(t *testing.T, s *store.Store, base string) (*store.Entity, []store.Target) {
	t.Helper()
	ctx := context.Background()

	e, err := s.CreateEntity(ctx, "vehicle")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddTarget(ctx, e.ID, store.TargetURL, base+"/", false, 0); err != nil {
		t.Fatal(err)
	}
	full, err := s.EntityFull(ctx, "vehicle")
	if err != nil {
		t.Fatal(err)
	}
	return full, full.Targets
}

func types(t *testing.T, allow ...string) *content.Set {
	t.Helper()
	set, err := content.New(allow, nil)
	if err != nil {
		t.Fatal(err)
	}
	return set
}

func TestCrawlFollowsLinksAndCachesPages(t *testing.T) {
	srv := site(t)
	c, s, cfg := harness(t)
	e, targets := entity(t, s, srv.URL)
	ctx := context.Background()

	result, err := c.Run(ctx, Options{
		Entity: e, Targets: targets, Types: types(t, "html"), Depth: 5,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Fetched < 5 {
		t.Errorf("fetched %d pages, want at least the five html ones", result.Fetched)
	}

	rows, err := s.FetchedURLs(ctx, e.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]store.URL{}
	for _, r := range rows {
		found[strings.TrimPrefix(r.URL, srv.URL)] = r
	}
	for _, want := range []string{"/", "/cars/", "/cars/one/", "/cars/two/", "/careers/"} {
		if _, ok := found[want]; !ok {
			t.Errorf("never fetched %s", want)
		}
	}

	// Bodies are in the cache, and the row records how big each was.
	pages := cache.New(cfg.PagesDir())
	if !pages.Has(srv.URL + "/cars/one/") {
		t.Error("page body was not cached")
	}
	if got := found["/cars/one/"]; got.Size == 0 || got.ContentType != "html" {
		t.Errorf("row = %+v, want a size and the html format", got)
	}
}

func TestCrawlRecordsDepthAndParent(t *testing.T) {
	srv := site(t)
	c, s, _ := harness(t)
	e, targets := entity(t, s, srv.URL)
	ctx := context.Background()

	if _, err := c.Run(ctx, Options{
		Entity: e, Targets: targets, Types: types(t, "html"), Depth: 5,
	}); err != nil {
		t.Fatal(err)
	}

	rows, err := s.FetchedURLs(ctx, e.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		switch strings.TrimPrefix(r.URL, srv.URL) {
		case "/":
			if r.Depth != 1 {
				t.Errorf("seed depth = %d, want 1", r.Depth)
			}
		case "/cars/one/":
			if r.Depth != 3 {
				t.Errorf("/cars/one/ depth = %d, want 3", r.Depth)
			}
			if r.ParentID == nil {
				t.Error("/cars/one/ has no parent recorded")
			}
		}
	}
}

func TestContentTypeFilteringSkipsUnwantedBodies(t *testing.T) {
	srv := site(t)
	c, s, cfg := harness(t)
	e, targets := entity(t, s, srv.URL)
	ctx := context.Background()

	result, err := c.Run(ctx, Options{
		Entity: e, Targets: targets, Types: types(t, "html"), Depth: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Skipped == 0 {
		t.Error("the pdf and the image should have been skipped")
	}

	pages := cache.New(cfg.PagesDir())
	if pages.Has(srv.URL + "/logo.png") {
		t.Error("an image body was downloaded despite the html-only filter")
	}
	if pages.Has(srv.URL + "/brochure.pdf") {
		t.Error("a pdf body was downloaded despite the html-only filter")
	}

	rows, err := s.URLs(ctx, e.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	var skipped int
	for _, r := range rows {
		if r.Status == store.URLSkipped {
			skipped++
		}
	}
	if skipped == 0 {
		t.Error("skips were not recorded in the frontier")
	}
}

func TestPDFIsFetchedWhenAllowed(t *testing.T) {
	srv := site(t)
	c, s, cfg := harness(t)
	e, targets := entity(t, s, srv.URL)
	ctx := context.Background()

	if _, err := c.Run(ctx, Options{
		Entity: e, Targets: targets, Types: types(t, "html", "pdf"), Depth: 5,
	}); err != nil {
		t.Fatal(err)
	}

	if !cache.New(cfg.PagesDir()).Has(srv.URL + "/brochure.pdf") {
		t.Error("the pdf should have been fetched once pdf was allowed")
	}
	if cache.New(cfg.PagesDir()).Has(srv.URL + "/logo.png") {
		t.Error("the image should still have been skipped")
	}
}

func TestErrorsAreRecordedNotFatal(t *testing.T) {
	srv := site(t)
	c, s, _ := harness(t)
	e, targets := entity(t, s, srv.URL)
	ctx := context.Background()

	result, err := c.Run(ctx, Options{
		Entity: e, Targets: targets, Types: types(t, "html"), Depth: 5,
	})
	if err != nil {
		t.Fatalf("a 404 and a 500 must not fail the crawl: %v", err)
	}
	if result.Failed != 2 {
		t.Errorf("failed = %d, want the 404 and the 500", result.Failed)
	}
	if result.Statuses[404] != 1 || result.Statuses[500] != 1 {
		t.Errorf("statuses = %v, want one 404 and one 500", result.Statuses)
	}

	rows, err := s.URLs(ctx, e.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	var failures int
	for _, r := range rows {
		if r.Status == store.URLFailed {
			failures++
		}
	}
	if failures != 2 {
		t.Errorf("recorded %d failures, want 2", failures)
	}
}

func TestDepthLimitIsHonoured(t *testing.T) {
	srv := site(t)
	c, s, _ := harness(t)
	e, targets := entity(t, s, srv.URL)
	ctx := context.Background()

	// Depth 2 reaches the seed and its direct links, but not /cars/one/.
	if _, err := c.Run(ctx, Options{
		Entity: e, Targets: targets, Types: types(t, "html"), Depth: 2,
	}); err != nil {
		t.Fatal(err)
	}

	rows, err := s.FetchedURLs(ctx, e.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if strings.HasSuffix(r.URL, "/cars/one/") {
			t.Errorf("depth 2 should not have reached %s", r.URL)
		}
	}
}

func TestMaxPagesStopsTheCrawl(t *testing.T) {
	srv := site(t)
	c, s, _ := harness(t)
	e, targets := entity(t, s, srv.URL)

	result, err := c.Run(context.Background(), Options{
		Entity: e, Targets: targets, Types: types(t, "html"), Depth: 5, Limit: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	// colly's workers may have a request in flight when the budget runs out,
	// so allow a small overshoot but not a runaway.
	if result.Fetched > 4 {
		t.Errorf("fetched %d pages despite a limit of 2", result.Fetched)
	}
}

func TestCrawlWithoutTargetsIsAnError(t *testing.T) {
	c, s, _ := harness(t)
	ctx := context.Background()

	e, err := s.CreateEntity(ctx, "vehicle")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Run(ctx, Options{Entity: e, Types: types(t, "html")}); err == nil {
		t.Error("crawling an entity with no targets must fail with advice")
	}
}

func TestCancellationStopsTheCrawl(t *testing.T) {
	srv := site(t)
	c, s, _ := harness(t)
	e, targets := entity(t, s, srv.URL)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := c.Run(ctx, Options{
		Entity: e, Targets: targets, Types: types(t, "html"), Depth: 5,
	})
	if err == nil {
		t.Error("a cancelled crawl should report the cancellation")
	}
	if result != nil && result.Fetched > 1 {
		t.Errorf("fetched %d pages after cancellation", result.Fetched)
	}
}

func TestSubdomainScope(t *testing.T) {
	targets := []store.Target{{Kind: store.TargetDomain, Value: "example.com"}}
	filters, err := urlFilters(targets)
	if err != nil {
		t.Fatal(err)
	}
	if len(filters) != 1 {
		t.Fatalf("got %d filters, want 1", len(filters))
	}
	if !filters[0].MatchString("http://www.example.com/cars/") {
		t.Error("www should be in scope")
	}
	if filters[0].MatchString("http://shop.example.com/") {
		t.Error("a subdomain must be out of scope without --subdomains")
	}

	targets[0].Subdomains = true
	filters, err = urlFilters(targets)
	if err != nil {
		t.Fatal(err)
	}
	if !filters[0].MatchString("http://shop.example.com/") {
		t.Error("a subdomain must be in scope with --subdomains")
	}
	if filters[0].MatchString("http://example.com.evil.test/") {
		t.Error("a lookalike domain must not match")
	}
}

func TestScoreOrderDecidesWhatIsCrawledFirst(t *testing.T) {
	srv := site(t)
	c, s, _ := harness(t)
	e, targets := entity(t, s, srv.URL)

	// A scorer that likes /cars/ and dislikes everything else, which is what a
	// trained model looks like once a crawl has found where the records are.
	scorer := score.FuncScorer(func(f score.Features) float64 {
		if strings.Contains(f.URL, "/cars/") {
			return 0.9
		}
		return 0.2
	})

	// Budget for two pages beyond the seed, so what gets fetched is decided by
	// the queue order rather than by exhausting the site.
	if _, err := c.Run(context.Background(), Options{
		Entity: e, Targets: targets, Types: types(t, "html"),
		Depth: 5, Limit: 3, Scorer: scorer,
	}); err != nil {
		t.Fatal(err)
	}

	rows, err := s.FetchedURLs(context.Background(), e.ID)
	if err != nil {
		t.Fatal(err)
	}
	var cars, careers int
	for _, r := range rows {
		switch {
		case strings.Contains(r.URL, "/cars"):
			cars++
		case strings.Contains(r.URL, "/careers"):
			careers++
		}
	}
	if cars == 0 {
		t.Errorf("a budgeted crawl never reached the high-scoring section: %+v", urls(rows))
	}
	if careers > cars {
		t.Errorf("the low-scoring section was preferred: %d careers, %d cars", careers, cars)
	}
}

func urls(rows []store.URL) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.URL)
	}
	return out
}

func TestElapsedHandlesMissingStart(t *testing.T) {
	if got := elapsed(""); got != 0 {
		t.Errorf("elapsed = %v, want 0 when no start was recorded", got)
	}
	if got := elapsed(time.Now().Add(-time.Second).Format(time.RFC3339Nano)); got < time.Second {
		t.Errorf("elapsed = %v, want at least a second", got)
	}
}

func TestVisitedSetMakesASecondCrawlResume(t *testing.T) {
	srv := site(t)
	c, s, _ := harness(t)
	e, targets := entity(t, s, srv.URL)
	ctx := context.Background()

	opts := Options{Entity: e, Targets: targets, Types: types(t, "html"), Depth: 5}

	first, err := c.Run(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	if first.Fetched == 0 {
		t.Fatal("the first crawl fetched nothing")
	}

	// The visited set is in the database, so the second crawl has nothing left
	// to do rather than starting the site again.
	second, err := c.Run(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	if second.Fetched != 0 {
		t.Errorf("the second crawl fetched %d pages, want 0: the visited set did not persist", second.Fetched)
	}

	// Clearing it puts the work back.
	if err := s.ClearCrawlState(ctx, e.ID); err != nil {
		t.Fatal(err)
	}
	third, err := c.Run(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	if third.Fetched != first.Fetched {
		t.Errorf("after clearing, fetched %d pages, want the original %d", third.Fetched, first.Fetched)
	}
}

func TestQueueSurvivesAnInterruptedCrawl(t *testing.T) {
	srv := site(t)
	c, s, _ := harness(t)
	e, targets := entity(t, s, srv.URL)
	ctx := context.Background()

	// Stop after one page, leaving the links it found still queued.
	if _, err := c.Run(ctx, Options{
		Entity: e, Targets: targets, Types: types(t, "html"), Depth: 5, Limit: 1,
	}); err != nil {
		t.Fatal(err)
	}

	queued, err := s.QueueSize(ctx, e.ID)
	if err != nil {
		t.Fatal(err)
	}
	if queued == 0 {
		t.Fatal("an interrupted crawl left nothing queued, so there is nothing to resume")
	}

	// Resuming picks the queue up rather than starting from the seeds.
	resumed, err := c.Run(ctx, Options{
		Entity: e, Targets: targets, Types: types(t, "html"), Depth: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Fetched == 0 {
		t.Error("the resumed crawl fetched nothing")
	}
}
