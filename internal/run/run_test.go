// SPDX-License-Identifier: GPL-3.0-or-later

package run_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rangertaha/scour/internal/downloader"
	"github.com/rangertaha/scour/internal/engine"
	"github.com/rangertaha/scour/internal/frontier"
	"github.com/rangertaha/scour/internal/frontier/sqlite"
	"github.com/rangertaha/scour/internal/record"
	"github.com/rangertaha/scour/internal/run"

	_ "github.com/rangertaha/scour/internal/cache/local"
	_ "github.com/rangertaha/scour/internal/downloader/httpcache"
	_ "github.com/rangertaha/scour/internal/exporter/files"
	_ "github.com/rangertaha/scour/internal/pipeline/steps"
	_ "github.com/rangertaha/scour/internal/scheduler/dupefilter"
	_ "github.com/rangertaha/scour/internal/spider/httperror"
)

// site is a small linked site: an index pointing at three stories, each of
// which points back. Enough to exercise discovery, dedup and depth.
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

		switch r.URL.Path {
		case "/":
			fmt.Fprint(w, `<html><head><title>Index</title></head><body>
			  <a href="/story/1">one</a>
			  <a href="/story/2">two</a>
			  <a href="/story/3">three</a>
			  <a href="/story/1?utm_source=nav">one again</a>
			  <a href="https://elsewhere.example/x">away</a>
			</body></html>`)
		case "/story/1", "/story/2", "/story/3":
			fmt.Fprintf(w, `<html><head>
			  <meta property="og:title" content="Story %s">
			  <meta property="article:published_time" content="2026-08-0%s T09:15:00Z">
			</head><body>
			  <article>Words about it.</article>
			  <a href="/">home</a>
			</body></html>`, r.URL.Path[len("/story/"):], r.URL.Path[len("/story/"):])
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server, &hits
}

func job(t *testing.T, src string) *engine.Job {
	t.Helper()

	doc, err := engine.Parse([]byte(src), "job.hcl")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := doc.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	return doc.Jobs[0]
}

func document(t *testing.T, server *httptest.Server, extra string) *engine.Job {
	t.Helper()

	// A rate a test can wait for, folded into whatever scheduler block the
	// caller wrote. Politeness itself is proved in the frontier's own tests,
	// which pass the clock in rather than sleeping.
	switch {
	case strings.Contains(extra, "rate"):
	case strings.Contains(extra, "scheduler {"):
		extra = strings.Replace(extra, "scheduler {", "scheduler {\n    rate = \"1ms\"", 1)
	default:
		extra = "\n  scheduler {\n    rate = \"1ms\"\n  }\n" + extra
	}

	return job(t, fmt.Sprintf(`
job "news" {
  domains = ["%s"]
  start   = ["%s/"]

  item "article" {
    property "title" {
      type     = str
      required = true
    }

    property "published_time" {
      type       = date
      transforms = [datetime]
    }
  }
%s
}
`, hostOf(server.URL), server.URL, extra))
}

func hostOf(url string) string {
	return strings.TrimPrefix(strings.TrimPrefix(url, "http://"), "https://")
}

// crawl runs a job to completion in a directory of its own.
func crawl(t *testing.T, j *engine.Job, dir string) (*run.Run, run.Ending) {
	t.Helper()

	if dir == "" {
		dir = t.TempDir()
	}
	// The exporters write relative to the working directory, so a run that
	// exports gets one of its own.
	back, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(back) })

	r, err := run.New(context.Background(), j, run.Options{
		Dir:  dir,
		Open: func(cfg frontier.Config) (frontier.Frontier, error) { return sqlite.Open(cfg) },
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	t.Cleanup(func() { r.Close() })

	if _, err := r.Seed(context.Background()); err != nil {
		t.Fatalf("seed: %v", err)
	}

	ending, err := r.Do(context.Background())
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	return r, ending
}

// TestACrawlFindsTheSiteAndStops. The whole loop: seed, lease, fetch, read,
// queue what was found, and finish when there is nothing left.
func TestACrawlFindsTheSiteAndStops(t *testing.T) {
	server, hits := site(t)

	r, ending := crawl(t, document(t, server, `
  scheduler {
    concurrency = 2
  }

  exporter "jsonlines" "article" {}
`), "")

	if ending != run.Finished {
		t.Errorf("ending = %q, want a crawl that ran out of work", ending)
	}

	stats := r.Stats()
	// Five pages: the index, three stories, and the story linked a second
	// time with a tracking parameter, which is a different page until a job
	// says otherwise.
	if got := stats.Fetched.Load(); got != 5 {
		t.Errorf("fetched %d pages", got)
	}
	if got := hits.Load(); got != 5 {
		t.Errorf("the site was asked %d times", got)
	}
	// The index has a <title>, so it is an article too as far as this shape is
	// concerned. Deciding it is not is the pipeline's job, not extraction's.
	if got := stats.Items.Load(); got != 5 {
		t.Errorf("extracted %d items", got)
	}
	if got := stats.Exported.Load(); got != 5 {
		t.Errorf("exported %d records", got)
	}

	waiting, err := r.Waiting(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if waiting != 0 {
		t.Errorf("%d urls left queued after the crawl finished", waiting)
	}
}

// TestScopeKeepsACrawlOnItsOwnSite.
func TestScopeKeepsACrawlOnItsOwnSite(t *testing.T) {
	server, _ := site(t)

	r, _ := crawl(t, document(t, server, ""), "")
	if got := r.Stats().Fetched.Load(); got != 5 {
		t.Errorf("fetched %d pages; the link off-site was followed", got)
	}
}

// TestTheSamePageIsFetchedOnce, whatever spelling it was linked under.
func TestTheSamePageIsFetchedOnce(t *testing.T) {
	server, hits := site(t)

	crawl(t, document(t, server, `
  scheduler {
    plugin "dupefilter" {
      strip_tracking = true
    }
  }
`), "")

	if got := hits.Load(); got != 4 {
		t.Errorf("the site was asked %d times; a tracking parameter made one page look like two", got)
	}
}

// TestWithoutTheDupefilterTheTrackedURLIsAnotherPage, which is the point of
// making it a plugin: the default is conservative and a job says otherwise.
func TestWithoutTheDupefilterTheTrackedURLIsAnotherPage(t *testing.T) {
	server, hits := site(t)

	crawl(t, document(t, server, ""), "")
	if got := hits.Load(); got != 5 {
		t.Errorf("the site was asked %d times, want the tracked URL fetched separately", got)
	}
}

// TestTheExportIsWhatWasFound, in a file, readable.
func TestTheExportIsWhatWasFound(t *testing.T) {
	server, _ := site(t)
	dir := t.TempDir()

	crawl(t, document(t, server, `
  exporter "jsonlines" "article" {}
`), dir)

	body, err := os.ReadFile(filepath.Join(dir, "article.jsonl"))
	if err != nil {
		t.Fatalf("no export: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(body)), "\n")
	if len(lines) != 5 {
		t.Fatalf("exported %d records:\n%s", len(lines), body)
	}

	seen := map[string]bool{}
	for _, line := range lines {
		var r record.Record
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("not JSON: %v", err)
		}
		if r.Spec == "" {
			t.Error("a record does not say which shape it was read under")
		}
		seen[r.Get("title")] = true
	}
	for _, want := range []string{"Story 1", "Story 2", "Story 3"} {
		if !seen[want] {
			t.Errorf("%q was not exported: %v", want, seen)
		}
	}
}

// TestThePipelineRunsBeforeTheExport.
func TestThePipelineRunsBeforeTheExport(t *testing.T) {
	server, _ := site(t)
	dir := t.TempDir()

	// validate drops the index, which has a title and no published time, and
	// dedupe collapses the story that was linked twice. What is left is the
	// three stories, ranked.
	crawl(t, document(t, server, `
  pipeline {
    step "clean" "article" {}

    step "validate" "article" {
      require  = ["published_time"]
      requires = [clean.article]
    }

    step "dedupe" "article" {
      by       = ["title"]
      requires = [validate.article]
    }

    step "rank" "article" {
      by         = "title"
      descending = true
      limit      = 2
      requires   = [dedupe.article]
    }
  }

  exporter "jsonlines" "article" {}
`), dir)

	body, err := os.ReadFile(filepath.Join(dir, "article.jsonl"))
	if err != nil {
		t.Fatalf("no export: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(body)), "\n")
	if len(lines) != 2 {
		t.Fatalf("the limit was not applied: %d records", len(lines))
	}

	var first record.Record
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatal(err)
	}
	if first.Get("title") != "Story 3" {
		t.Errorf("first = %q, want the ranking to have been applied", first.Get("title"))
	}
}

// TestMaxDepthStopsACrawlGoingDeeper.
//
// A chain of pages rather than the usual fixture, because depth is only
// observable on a site that has some.
func TestMaxDepthStopsACrawlGoingDeeper(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		switch r.URL.Path {
		case "/":
			fmt.Fprint(w, `<html><head><title>Zero</title></head><body><a href="/one">on</a></body></html>`)
		case "/one":
			fmt.Fprint(w, `<html><head><title>One</title></head><body><a href="/two">on</a></body></html>`)
		case "/two":
			fmt.Fprint(w, `<html><head><title>Two</title></head><body><a href="/three">on</a></body></html>`)
		default:
			fmt.Fprint(w, `<html><head><title>Deep</title></head><body></body></html>`)
		}
	}))
	defer server.Close()

	r, ending := crawl(t, document(t, server, `
  scheduler {
    max_depth = 1
    rate      = "1ms"
  }
`), "")

	if ending != run.Finished {
		t.Errorf("ending = %q", ending)
	}
	if got := r.Stats().Fetched.Load(); got != 2 {
		t.Errorf("fetched %d pages, want the start url and one level below it", got)
	}
}

// TestABudgetEndsACrawlAndSaysWhy. A crawl that finished the site and one that
// hit its page limit look identical in the output and mean opposite things.
func TestABudgetEndsACrawlAndSaysWhy(t *testing.T) {
	server, _ := site(t)

	r, ending := crawl(t, document(t, server, `
  scheduler {
    max_pages = 2
  }
`), "")

	if ending != run.BudgetSpent {
		t.Errorf("ending = %q, want the budget to be named", ending)
	}
	if got := r.Stats().Fetched.Load(); got < 2 {
		t.Errorf("fetched %d before stopping", got)
	}
}

// TestARunThatIsStoppedSaysSo.
func TestARunThatIsStoppedSaysSo(t *testing.T) {
	server, _ := site(t)
	dir := t.TempDir()

	r, err := run.New(context.Background(), document(t, server, ""), run.Options{
		Dir:  dir,
		Open: func(cfg frontier.Config) (frontier.Frontier, error) { return sqlite.Open(cfg) },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	if _, err := r.Seed(context.Background()); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	ending, err := r.Do(ctx)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	if ending != run.Stopped {
		t.Errorf("ending = %q", ending)
	}
}

// TestACrawlResumes: the frontier survives, so a second run picks up what the
// first left and does not re-fetch what it finished.
func TestACrawlResumes(t *testing.T) {
	server, hits := site(t)
	dir := t.TempDir()

	first := document(t, server, `
  scheduler {
    max_pages = 1
  }
`)
	crawl(t, first, dir)

	after := hits.Load()
	if after != 1 {
		t.Fatalf("the first run fetched %d pages", after)
	}

	// The same directory, no budget: what the first run queued is still there.
	r, ending := crawl(t, document(t, server, ""), dir)
	if ending != run.Finished {
		t.Errorf("ending = %q", ending)
	}
	if got := r.Stats().Fetched.Load(); got == 0 {
		t.Error("the second run found nothing to do")
	}
	if got := hits.Load(); got > 5 {
		t.Errorf("the site was asked %d times in total; the first run's work was repeated", got)
	}
}

// TestPolitenessIsHonouredAcrossWorkers: two workers on one host still wait for
// each other, because the pacing is in the frontier and not in the loop.
func TestPolitenessIsHonouredAcrossWorkers(t *testing.T) {
	server, _ := site(t)

	r, ending := crawl(t, document(t, server, `
  scheduler {
    concurrency = 4
    rate        = "10ms"
  }
`), "")

	if ending != run.Finished {
		t.Errorf("ending = %q", ending)
	}
	if got := r.Stats().Fetched.Load(); got != 5 {
		t.Errorf("fetched %d pages", got)
	}
}

// TestAPageThatFailsIsRetriedAndThenAbandoned, without stopping the crawl.
func TestAPageThatFailsIsRetriedAndThenAbandoned(t *testing.T) {
	var asked atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/robots.txt":
			http.NotFound(w, r)
		case r.URL.Path == "/":
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, `<html><head><title>Index</title></head><body><a href="/broken">broken</a></body></html>`)
		default:
			asked.Add(1)
			// A connection that hangs up, which is a failure rather than a
			// status.
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				conn.Close()
			}
		}
	}))
	defer server.Close()

	r, ending := crawl(t, document(t, server, ""), "")

	if ending != run.Finished {
		t.Errorf("ending = %q; one broken page stopped the crawl", ending)
	}
	if got := r.Stats().Failed.Load(); got != int64(frontier.MaxAttempts) {
		t.Errorf("the broken page was tried %d times, want %d", got, frontier.MaxAttempts)
	}
	if got := r.Stats().Fetched.Load(); got != 1 {
		t.Errorf("fetched %d pages", got)
	}
}

func TestARunNeedsAJob(t *testing.T) {
	if _, err := run.New(context.Background(), nil, run.Options{}); err == nil {
		t.Error("built a run for no job")
	}
}

// TestAStoreThatRefusesBookkeepingStopsRatherThanSpinning.
//
// Three call sites each logged a failed frontier write and moved on. Writing
// this test found something worse than the silence: a frontier that cannot
// record a page as finished hands the same page out every time its lease
// expires, so the crawl neither progresses nor ends. It ran until the test
// timed out.
//
// One refused write is recoverable and is only counted. Refused consistently,
// the run stops and says so, because stopping is the only outcome that ends.
func TestAStoreThatRefusesBookkeepingIsCountedRatherThanIgnored(t *testing.T) {
	server, _ := site(t)

	r, ending := crawl(t, document(t, server, ""), "")
	if ending != run.Finished {
		t.Fatalf("ending = %q", ending)
	}
	if got := r.Stats().Store.Load(); got != 0 {
		t.Errorf("a healthy crawl counted %d refused writes", got)
	}

	// And a store that refuses them is counted rather than lost.
	// The bound is shortened rather than the clock wound forward. Winding it
	// forward far enough to reach the bound also expires every lease, so the
	// URLs come back, the loop makes progress, and the stall under test never
	// happens: the first version of this test hung for that reason.
	refusing := &refusingFrontier{Frontier: mustOpen(t)}
	broken, err := run.New(context.Background(), document(t, server, ""),
		run.Options{Frontier: refusing, Stall: 200 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer broken.Close()

	if _, err := broken.Seed(context.Background()); err != nil {
		t.Fatal(err)
	}
	refusing.refuse.Store(true)

	ending, err = broken.Do(context.Background())
	if err == nil {
		t.Fatal("a frontier refusing every write produced a successful run")
	}
	if ending != run.Stalled {
		t.Errorf("ending = %q, want the run to say it stalled", ending)
	}
	if got := broken.Stats().Store.Load(); got == 0 {
		t.Error("a frontier refusing every bookkeeping write was counted zero times")
	}
	if !strings.Contains(err.Error(), "refused") {
		t.Errorf("the error does not say what happened: %v", err)
	}
}

// refusingFrontier accepts everything until told to refuse the bookkeeping.
type refusingFrontier struct {
	frontier.Frontier
	refuse atomic.Bool
}

func (f *refusingFrontier) Done(ctx context.Context, job, hash string, attempt int) error {
	if f.refuse.Load() {
		return errors.New("database is locked")
	}
	return f.Frontier.Done(ctx, job, hash, attempt)
}

func mustOpen(t *testing.T) frontier.Frontier {
	t.Helper()

	f, err := sqlite.Open(frontier.Config{Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

// TestAPoliteCrawlIsNotMistakenForAStalledOne.
//
// The stall bound was a constant six minutes measured from the last completed
// fetch, and politeness waiting is invisible to that: the loop takes the
// nothing-due branch and sleeps without touching the clock it is judged by. So
// a job that was told `rate = "8m"` fetched its first page, waited exactly as
// instructed, and killed itself two minutes later with an ending of "stalled"
// and an error blaming the store for zero refused writes. Every rate above a
// minute did it, and the larger the rate the more certain.
//
// The clock steps forward on every reading rather than being wound by the test,
// because winding it forward expires the leases too, and then the URLs come
// back, the loop progresses, and the stall under test cannot happen.
func TestAPoliteCrawlIsNotMistakenForAStalledOne(t *testing.T) {
	server, _ := site(t)

	var ticks atomic.Int64
	base := time.Date(2026, 8, 5, 9, 0, 0, 0, time.UTC)
	clock := func() time.Time {
		return base.Add(time.Duration(ticks.Add(1)) * 20 * time.Second)
	}

	r, err := run.New(context.Background(), document(t, server, `
  scheduler {
    rate      = "8m"
    max_pages = 2
  }
`), run.Options{
		Dir:  t.TempDir(),
		Open: func(cfg frontier.Config) (frontier.Frontier, error) { return sqlite.Open(cfg) },
		Now:  clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	if _, err := r.Seed(context.Background()); err != nil {
		t.Fatal(err)
	}

	ending, err := r.Do(context.Background())
	if err != nil {
		t.Fatalf("a crawl waiting out its own politeness rate failed: %v", err)
	}
	if ending == run.Stalled {
		t.Error("ending = stalled: the crawl was killed for obeying its rate")
	}
	if got := r.Stats().Store.Load(); got != 0 {
		t.Errorf("a healthy crawl counted %d refused writes", got)
	}
}

// TestAJobWhoseStageIsElsewhereIsRefusedRatherThanRunLocally.
//
// `external = true` was accepted by the parser, validated, carried into the
// resolved job, and reported back to the operator by `scour show` as "yes,
// waiting 5m0s". Nothing in a running crawl read it. So a job that said its
// downloader was somewhere else was crawled locally, in full, while the tool
// told the operator otherwise: the pages were fetched from this machine, with
// this machine's address and this machine's rate limit, which is the whole
// reason somebody moves a downloader elsewhere.
//
// The check is at the seam rather than in each command, because Options.Fetch
// and Options.Read are the only way to reach a stage that is not here, and a
// caller that has not used one has no way to reach it.
func TestAJobWhoseStageIsElsewhereIsRefusedRatherThanRunLocally(t *testing.T) {
	server, hits := site(t)

	_, err := run.New(context.Background(), document(t, server, `
  downloader {
    external = true
  }
`), run.Options{
		Dir:  t.TempDir(),
		Open: func(cfg frontier.Config) (frontier.Frontier, error) { return sqlite.Open(cfg) },
	})
	if err == nil {
		t.Fatal("a job whose downloader is elsewhere was accepted by a run that cannot reach one")
	}
	if !strings.Contains(err.Error(), "external") {
		t.Errorf("the error does not say what is wrong: %v", err)
	}
	if hits.Load() != 0 {
		t.Errorf("it fetched %d pages locally before refusing", hits.Load())
	}

	// And a run that was given a way to reach it is not refused.
	r, err := run.New(context.Background(), document(t, server, `
  downloader {
    external = true
  }
`), run.Options{
		Dir:   t.TempDir(),
		Open:  func(cfg frontier.Config) (frontier.Frontier, error) { return sqlite.Open(cfg) },
		Fetch: downloader.HandlerFunc(func(context.Context, *downloader.Request) (*downloader.Response, error) { return nil, nil }),
	})
	if err != nil {
		t.Fatalf("a run given a handler for the external stage was refused anyway: %v", err)
	}
	r.Close()
}

// TestAnExternalPipelineIsRefusedBecauseNothingServesOne.
//
// The same class as the test above, and the case it missed. `external = true`
// in a `pipeline` block was accepted, validated and reported, and then the
// items were written here: no node serves a pipeline stage, [Options] has no
// seam to reach one, and the run said nothing. An operator who moved the
// pipeline elsewhere got their records written on this machine instead, which
// is where they were trying not to put them.
//
// So this one is refused whatever the caller supplied, unlike the downloader
// and the spider, and the message says why rather than suggesting a node that
// does not exist.
func TestAnExternalPipelineIsRefusedBecauseNothingServesOne(t *testing.T) {
	server, hits := site(t)

	_, err := run.New(context.Background(), document(t, server, `
  pipeline {
    external = true
  }
`), run.Options{
		Dir:  t.TempDir(),
		Open: func(cfg frontier.Config) (frontier.Frontier, error) { return sqlite.Open(cfg) },
	})
	if err == nil {
		t.Fatal("a job whose pipeline is elsewhere was accepted, and its items would be written here")
	}
	if !strings.Contains(err.Error(), "pipeline") || !strings.Contains(err.Error(), "external") {
		t.Errorf("the error does not say what is wrong: %v", err)
	}
	if hits.Load() != 0 {
		t.Errorf("it fetched %d pages locally before refusing", hits.Load())
	}
}

// TestTheHoldOutlastsOneFetch.
//
// The hold was the constant run.Lease, and a job may set a request timeout
// longer than it. With `timeout = "10m"` the lease expired while the page was
// still downloading, so a second worker leased the same URL and fetched it
// alongside the first: two requests to one host at once, defeating the rate the
// job asked for. The first worker's report was then correctly discarded by the
// lease fence, which is what a fence is for and why nothing counted it or
// logged it. The crawl looked healthy and was hitting the site twice.
//
// The arithmetic is asserted directly, because what a hold has to be is a
// statement about two durations and observing it through a crawl would mean
// waiting one out.
func TestTheHoldOutlastsOneFetch(t *testing.T) {
	for _, fetch := range []time.Duration{
		30 * time.Second,
		5 * time.Minute,
		10 * time.Minute,
		time.Hour,
	} {
		hold := run.Hold(fetch)
		if hold <= fetch {
			t.Errorf("Hold(%s) = %s, and the lease expires while the fetch is still running", fetch, hold)
		}
		if hold < run.Lease {
			t.Errorf("Hold(%s) = %s, below the floor of %s", fetch, hold, run.Lease)
		}
	}

	// And the job's own timeout is what it is computed from, so a document
	// asking for a long fetch gets a hold that covers it.
	server, _ := site(t)
	j := document(t, server, `
  downloader {
    timeout = "10m"
  }
`)
	fetch, err := j.Downloader.RequestTimeout()
	if err != nil {
		t.Fatal(err)
	}
	if fetch != 10*time.Minute {
		t.Fatalf("the job's timeout read as %s", fetch)
	}
	if got := run.Hold(fetch); got <= 10*time.Minute {
		t.Errorf("Hold = %s for a ten minute fetch", got)
	}
}

// TestASlowFetchIsNotAStall.
//
// Hold lets one fetch outlast the lease, and the stall bound was not updated to
// match: `timeout = "10m"` and one slow page had a worker declare the crawl
// stalled six minutes in while another was still fetching happily. The run then
// reported Stalled and blamed the store for zero refused writes.
//
// Asserted on the arithmetic, because observing it through a crawl would mean
// waiting a fetch timeout out.
func TestASlowFetchIsNotAStall(t *testing.T) {
	server, _ := site(t)

	j := document(t, server, `
  downloader {
    timeout = "10m"
  }

  scheduler {
    rate = "1s"
  }
`)

	fetch, err := j.Downloader.RequestTimeout()
	if err != nil {
		t.Fatal(err)
	}
	rate, err := j.Scheduler.RateDuration()
	if err != nil {
		t.Fatal(err)
	}

	stall := run.StallFor(rate, fetch)
	hold := run.Hold(fetch)

	if stall <= hold {
		t.Errorf("stall %s does not outlast a hold of %s, so a worker waiting on one fetch declares a stall",
			stall, hold)
	}
	if stall <= fetch {
		t.Errorf("stall %s does not outlast a fetch of %s", stall, fetch)
	}
}

// TestAPartialFailureLosesOnlyWhatWasLost.
//
// Most of a page's links are usually dropped as out of scope, which is not a
// failure at all, so counting the whole page as lost on any error reported a
// hundred urls thrown away when one write had failed — and discarded the count
// of what had landed, so the summary said nothing was queued from a page that
// had queued the rest.
//
// The frontier here takes some and then refuses, which is the shape a real
// partial failure has and the shape no other test produced.
func TestAPartialFailureLosesOnlyWhatWasLost(t *testing.T) {
	server, _ := site(t)

	partial := &partialFrontier{Frontier: mustOpen(t), accept: 2}
	r, err := run.New(context.Background(), document(t, server, ""), run.Options{
		Dir:   t.TempDir(),
		Open:  func(cfg frontier.Config) (frontier.Frontier, error) { return partial, nil },
		Stall: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	if _, err := r.Seed(context.Background()); err != nil {
		t.Fatal(err)
	}
	partial.on.Store(true)

	// The run fails, because links really were thrown away.
	if _, err := r.Do(context.Background()); err == nil {
		t.Fatal("a run that lost links reported success")
	}

	stats := r.Stats()
	lost, queued := stats.Lost.Load(), stats.Queued.Load()

	if lost == 0 {
		t.Fatal("nothing was counted lost by a frontier that refused writes")
	}
	// The page that failed had more links than the two the frontier took, and
	// what it took is not lost.
	if queued == 0 {
		t.Error("the urls the frontier did take were not counted queued")
	}
	if int(lost) >= len(indexLinks)+int(queued) {
		t.Errorf("lost %d of a page whose links the frontier partly took (queued %d)", lost, queued)
	}
}

// indexLinks is what the test site's index page offers, which is what a partial
// failure is measured against.
var indexLinks = []string{"/story/1", "/story/2", "/story/3", "/story/1?utm_source=nav"}

// partialFrontier takes a few requests and then refuses, which is what a store
// under pressure does and what no other double here produced.
type partialFrontier struct {
	frontier.Frontier
	on     atomic.Bool
	accept int
	taken  atomic.Int64
}

func (f *partialFrontier) Add(ctx context.Context, job string, reqs ...frontier.Request) (int, error) {
	if !f.on.Load() {
		return f.Frontier.Add(ctx, job, reqs...)
	}

	var added int
	for _, req := range reqs {
		if int(f.taken.Load()) >= f.accept {
			return added, errors.New("database is locked")
		}
		n, err := f.Frontier.Add(ctx, job, req)
		if err != nil {
			return added, err
		}
		f.taken.Add(1)
		added += n
	}
	return added, nil
}
