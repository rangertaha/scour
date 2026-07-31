// SPDX-License-Identifier: GPL-3.0-or-later

package crawl

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rangertaha/scour/internal/cache"
	"github.com/rangertaha/scour/internal/store"
)

// wideSite serves a hub linking to n pages, each slow enough that several are
// in flight at once. The delay is the point: a budget checked when a response
// arrives looks correct against a fast local server and overshoots by a whole
// batch against a real one.
func wideSite(t *testing.T, n int, delay time.Duration) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(delay)
		w.Header().Set("Content-Type", "text/html")
		if r.URL.Path != "/" {
			fmt.Fprint(w, "<html><body><h1>leaf</h1></body></html>")
			return
		}
		var b strings.Builder
		b.WriteString("<html><body>")
		for i := 0; i < n; i++ {
			fmt.Fprintf(&b, `<a href="/p%d/">p%d</a>`, i, i)
		}
		b.WriteString("</body></html>")
		fmt.Fprint(w, b.String())
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// --max-pages is a budget, so it is a ceiling and not an approximation.
func TestMaxPagesIsNotExceeded(t *testing.T) {
	for _, limit := range []int{1, 2, 3, 5, 8, 13} {
		srv := wideSite(t, 60, 30*time.Millisecond)
		c, s, _ := harness(t)
		e, targets := entity(t, s, srv.URL)

		result, err := c.Run(context.Background(), Options{
			Entity: e, Targets: targets, Types: types(t, "html"), Depth: 5, Limit: limit,
		})
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if result.Fetched != limit {
			t.Errorf("--max-pages %d fetched %d", limit, result.Fetched)
		}
		if result.BudgetSpent != "pages" {
			t.Errorf("--max-pages %d ended with budget %q, want \"pages\"", limit, result.BudgetSpent)
		}
	}
}

// The budget must hold when many threads are asking at once, which is the case
// the old check got wrong: two threads could each find the last slot free.
func TestMaxPagesHoldsUnderConcurrency(t *testing.T) {
	srv := wideSite(t, 200, 20*time.Millisecond)
	_, s, cfg := harness(t)
	cfg.Crawl.Concurrency = 16
	c := New(cfg, s, cache.Local(cfg.PagesDir()))
	e, targets := entity(t, s, srv.URL)

	result, err := c.Run(context.Background(), Options{
		Entity: e, Targets: targets, Types: types(t, "html"), Depth: 5, Limit: 10,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Fetched != 10 {
		t.Errorf("--max-pages 10 at concurrency 16 fetched %d", result.Fetched)
	}
}

// Stopping on a budget has to leave the frontier usable, which is why the limit
// is enforced by declining to hand a request out rather than by aborting one:
// an abort marks the URL visited in the same storage that is the frontier.
func TestBudgetedCrawlResumes(t *testing.T) {
	srv := wideSite(t, 60, 5*time.Millisecond)
	c, s, _ := harness(t)
	e, targets := entity(t, s, srv.URL)
	ctx := context.Background()

	first, err := c.Run(ctx, Options{
		Entity: e, Targets: targets, Types: types(t, "html"), Depth: 5, Limit: 5,
	})
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if first.Fetched != 5 {
		t.Fatalf("first run fetched %d, want 5", first.Fetched)
	}

	// Everything discovered and not fetched is still queued, not burned.
	queued, err := s.Status(ctx, e.ID)
	if err != nil {
		t.Fatal(err)
	}
	if queued.Queued == 0 {
		t.Fatal("the frontier was emptied by a budgeted stop")
	}

	second, err := c.Run(ctx, Options{
		Entity: e, Targets: targets, Types: types(t, "html"), Depth: 5, Limit: 5,
	})
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if second.Fetched != 5 {
		t.Errorf("second run fetched %d, want 5: the crawl did not resume", second.Fetched)
	}

	// Ten distinct pages across the two runs, so the second did not refetch
	// what the first already had.
	rows, err := s.FetchedURLs(ctx, e.ID)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, r := range rows {
		if r.Status == store.URLFetched {
			seen[r.URL] = true
		}
	}
	if len(seen) != 10 {
		t.Errorf("fetched %d distinct pages across two runs of 5, want 10", len(seen))
	}
}
