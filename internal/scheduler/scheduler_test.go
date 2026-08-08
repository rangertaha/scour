// SPDX-License-Identifier: GPL-3.0-or-later

package scheduler_test

import (
	"context"
	"errors"
	"github.com/rangertaha/scour/internal/registry/registrytest"
	"strings"
	"testing"
	"time"

	"github.com/rangertaha/scour/internal/chain"
	"github.com/rangertaha/scour/internal/engine"
	"github.com/rangertaha/scour/internal/frontier"
	"github.com/rangertaha/scour/internal/frontier/sqlite"
	"github.com/rangertaha/scour/internal/plugin"
	"github.com/rangertaha/scour/internal/scheduler"

	_ "github.com/rangertaha/scour/internal/scheduler/dupefilter"
)

var origin = time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)

func job(t *testing.T, blocks string) *engine.Job {
	t.Helper()

	src := `
job "news" {
  start = ["https://example.com/"]

  item "article" {
    property "title" {
      type = str
    }
  }
` + blocks + `
}
`
	doc, err := engine.Parse([]byte(src), "job.hcl")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := doc.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	return doc.Jobs[0]
}

// stage builds a scheduler on a real SQLite frontier, because the two are only
// worth testing together: the chain decides what is worth queueing and the
// frontier decides what comes back out.
func stage(t *testing.T, j *engine.Job) *scheduler.Stage {
	t.Helper()

	s, err := scheduler.New(context.Background(), j,
		scheduler.Options{Dir: t.TempDir()},
		func(cfg frontier.Config) (frontier.Frontier, error) { return sqlite.Open(cfg) })
	if err != nil {
		t.Fatalf("new scheduler: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	return s
}

func submit(t *testing.T, s *scheduler.Stage, urls ...string) int {
	t.Helper()

	reqs := make([]*scheduler.Request, 0, len(urls))
	for _, u := range urls {
		reqs = append(reqs, &scheduler.Request{URL: u, Discovered: origin})
	}
	added, err := s.Submit(context.Background(), reqs...)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	return added
}

func TestSubmitAndTakeBack(t *testing.T) {
	s := stage(t, job(t, ""))
	ctx := context.Background()

	if n := submit(t, s, "https://example.com/a", "https://example.com/b"); n != 2 {
		t.Fatalf("queued %d", n)
	}
	if n, _ := s.Waiting(ctx); n != 2 {
		t.Errorf("waiting = %d", n)
	}

	req, err := s.Next(ctx, origin, time.Minute)
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	if req.Hash == "" || req.Host != "example.com" {
		t.Errorf("the scheduler handed back %+v", req)
	}

	if err := s.Done(ctx, req.Hash, req.Attempt); err != nil {
		t.Fatalf("done: %v", err)
	}
	if n, _ := s.Waiting(ctx); n != 1 {
		t.Errorf("waiting = %d after finishing one", n)
	}
}

// TestTheSameURLTwiceIsOnePage. Re-discovering a URL is the most ordinary thing
// a crawl does, so it is neither news nor an error.
func TestTheSameURLTwiceIsOnePage(t *testing.T) {
	s := stage(t, job(t, ""))

	if n := submit(t, s, "https://example.com/a"); n != 1 {
		t.Fatalf("first = %d", n)
	}
	if n := submit(t, s, "https://example.com/a"); n != 0 {
		t.Errorf("the same URL was queued %d more times", n)
	}
	// And the forms that are the same page by any reading.
	if n := submit(t, s, "https://EXAMPLE.com/a#section", "https://example.com:443/a"); n != 0 {
		t.Errorf("%d spellings of one URL were queued as new pages", n)
	}
}

// TestScopeIsEnforcedWithoutAPlugin, because domains and excluded are
// attributes and an attribute's enforcement cannot be deleted.
func TestScopeIsEnforcedWithoutAPlugin(t *testing.T) {
	s := stage(t, job(t, `
  domains  = ["example.com"]
  excluded = ["*/print/*"]
`))

	if n := submit(t, s, "https://example.com/news"); n != 1 {
		t.Error("an in-scope URL was refused")
	}
	if n := submit(t, s, "https://elsewhere.example/news"); n != 0 {
		t.Error("a URL on another site was queued")
	}
	if n := submit(t, s, "https://example.com/print/news"); n != 0 {
		t.Error("an excluded URL was queued")
	}
}

// TestTheBudgetIsEnforcedWithoutAPluginEither.
func TestTheBudgetIsEnforcedWithoutAPluginEither(t *testing.T) {
	t.Run("depth", func(t *testing.T) {
		s := stage(t, job(t, `
  scheduler {
    max_depth = 2
  }
`))
		ctx := context.Background()

		shallow := &scheduler.Request{URL: "https://example.com/a", Depth: 2, Discovered: origin}
		if n, err := s.Submit(ctx, shallow); n != 1 || err != nil {
			t.Errorf("a URL at the limit was refused: %d, %v", n, err)
		}

		deep := &scheduler.Request{URL: "https://example.com/b", Depth: 3, Discovered: origin}
		if n, err := s.Submit(ctx, deep); n != 0 || err != nil {
			t.Errorf("a URL past the limit was queued: %d, %v", n, err)
		}
	})

	// max_pages is deliberately not enforced here. Depth is a property of a
	// URL and is knowable the moment one arrives; a page budget is a count of
	// pages fetched, and the scheduler does not fetch. Checking it here against
	// the length of the queue looked equivalent and was not.
	t.Run("pages are not the queue's business", func(t *testing.T) {
		s := stage(t, job(t, `
  scheduler {
    max_pages = 2
  }
`))
		if n := submit(t, s, "https://example.com/a", "https://example.com/b", "https://example.com/c"); n != 3 {
			t.Errorf("queued %d of three; a page budget stopped urls being discovered", n)
		}
	})
}

// TestARefusalIsNotAFailure. A crawl that obeys its own scope drops most of
// what it finds, and a caller has to be able to tell that from a broken store.
func TestARefusalIsNotAFailure(t *testing.T) {
	s := stage(t, job(t, `
  domains = ["example.com"]
`))

	added, err := s.Submit(context.Background(),
		&scheduler.Request{URL: "https://elsewhere.example/a", Discovered: origin},
		&scheduler.Request{URL: "https://example.com/a", Discovered: origin})
	if err != nil {
		t.Fatalf("a dropped URL was reported as an error: %v", err)
	}
	if added != 1 {
		t.Errorf("added = %d, want the one that was in scope", added)
	}
}

// TestOneBadURLDoesNotTakeAPagesLinksWithIt.
func TestOneBadURLDoesNotTakeAPagesLinksWithIt(t *testing.T) {
	s := stage(t, job(t, ""))

	added, err := s.Submit(context.Background(),
		&scheduler.Request{URL: "mailto:somebody@example.com", Discovered: origin},
		&scheduler.Request{URL: "https://example.com/real", Discovered: origin},
		nil)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if added != 1 {
		t.Errorf("added = %d, want the one that was fetchable", added)
	}
}

// TestPolitenessComesFromTheJob, and it is the frontier that enforces it.
func TestPolitenessComesFromTheJob(t *testing.T) {
	s := stage(t, job(t, `
  scheduler {
    rate = "10s"
  }
`))
	ctx := context.Background()

	submit(t, s, "https://example.com/a", "https://example.com/b")

	if _, err := s.Next(ctx, origin, time.Minute); err != nil {
		t.Fatalf("next: %v", err)
	}
	if _, err := s.Next(ctx, origin.Add(time.Second), time.Minute); !errors.Is(err, frontier.ErrEmpty) {
		t.Errorf("a second page came off the same host a second later: %v", err)
	}
	if _, err := s.Next(ctx, origin.Add(11*time.Second), time.Minute); err != nil {
		t.Errorf("the host never came back: %v", err)
	}
}

// TestThePolicyComesFromTheJob: best first is what makes a focused crawl
// focused, and a job that wants otherwise says so.
func TestThePolicyComesFromTheJob(t *testing.T) {
	for policy, want := range map[string]string{
		"priority": "https://example.com/best",
		"breadth":  "https://example.com/shallow",
	} {
		t.Run(policy, func(t *testing.T) {
			s := stage(t, job(t, "\n  scheduler {\n    policy = \""+policy+"\"\n  }\n"))
			ctx := context.Background()

			if _, err := s.Submit(ctx,
				&scheduler.Request{URL: "https://example.com/shallow", Depth: 1, Score: 0.1, Discovered: origin},
				&scheduler.Request{URL: "https://example.com/best", Depth: 4, Score: 0.9, Discovered: origin.Add(time.Second)},
			); err != nil {
				t.Fatal(err)
			}

			got, err := s.Next(ctx, origin, time.Minute)
			if err != nil {
				t.Fatalf("next: %v", err)
			}
			if got.URL != want {
				t.Errorf("leased %s, want %s", got.URL, want)
			}
		})
	}
}

// TestTheDupefilterDecidesWhatIsTheSamePage, which is the whole reason it is a
// plugin: there is no right answer, only a right answer per site.
func TestTheDupefilterDecidesWhatIsTheSamePage(t *testing.T) {
	plain := stage(t, job(t, ""))
	if n := submit(t, plain, "https://example.com/a", "https://example.com/a?utm_source=news"); n != 2 {
		t.Errorf("the default merged two URLs a server may distinguish: %d queued", n)
	}

	tidy := stage(t, job(t, `
  scheduler {
    plugin "dupefilter" {
      strip_tracking       = true
      sort_query           = true
      strip_trailing_slash = true
    }
  }
`))
	if n := submit(t, tidy, "https://example.com/a", "https://example.com/a?utm_source=news"); n != 1 {
		t.Errorf("a tracking parameter made one page look like %d", n)
	}
	if n := submit(t, tidy, "https://example.com/a/"); n != 0 {
		t.Error("a trailing slash made one page look like two")
	}
	if n := submit(t, tidy, "https://example.com/b?x=1&y=2"); n != 1 {
		t.Error("an ordinary URL was refused")
	}
	if n := submit(t, tidy, "https://example.com/b?y=2&x=1"); n != 0 {
		t.Error("a reordered query made one page look like two")
	}
}

func TestTheDupefilterIsInTheChain(t *testing.T) {
	s := stage(t, job(t, `
  scheduler {
    plugin "dupefilter" {}
  }
`))
	if got := strings.Join(s.Middleware(), " "); got != "dupefilter" {
		t.Errorf("Middleware() = %q", got)
	}
}

// TestAPluginMayScoreOnTheWayIn, which is how a focused crawl becomes focused:
// the ordering is only as good as what put the numbers there.
func TestAPluginMayScoreOnTheWayIn(t *testing.T) {
	register(t, "test-scorer", func(_ context.Context, cfg plugin.Config) (scheduler.Wrapper, error) {
		return func(next scheduler.Handler) scheduler.Handler {
			return scheduler.HandlerFunc(func(ctx context.Context, req *scheduler.Request) (*scheduler.Request, error) {
				if strings.Contains(req.URL, "/news/") {
					req.Score = 0.99
				}
				return next.Handle(ctx, req)
			})
		}, nil
	})

	s := stage(t, job(t, `
  scheduler {
    plugin "test-scorer" {
      order = 450
    }
  }
`))
	ctx := context.Background()

	submit(t, s, "https://example.com/about", "https://example.com/news/story")

	got, err := s.Next(ctx, origin, time.Minute)
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	if got.URL != "https://example.com/news/story" {
		t.Errorf("leased %s; the score a plugin set was not what the frontier ordered by", got.URL)
	}
}

// TestAPluginMayDrop, and the drop is not counted as queued.
func TestAPluginMayDrop(t *testing.T) {
	register(t, "test-refuse", func(_ context.Context, cfg plugin.Config) (scheduler.Wrapper, error) {
		return func(next scheduler.Handler) scheduler.Handler {
			return scheduler.HandlerFunc(func(ctx context.Context, req *scheduler.Request) (*scheduler.Request, error) {
				if strings.HasSuffix(req.URL, ".pdf") {
					return nil, chain.ErrDrop
				}
				return next.Handle(ctx, req)
			})
		}, nil
	})

	s := stage(t, job(t, `
  scheduler {
    plugin "test-refuse" {
      order = 200
    }
  }
`))
	if n := submit(t, s, "https://example.com/a.pdf", "https://example.com/a.html"); n != 1 {
		t.Errorf("queued %d, want only the one the plugin allowed", n)
	}
}

// TestFailPutsItBackUntilItHasBeenTriedEnough.
func TestFailPutsItBackUntilItHasBeenTriedEnough(t *testing.T) {
	s := stage(t, job(t, ""))
	ctx := context.Background()

	submit(t, s, "https://example.com/flaky")

	var handed int
	for i := range frontier.MaxAttempts + 2 {
		req, err := s.Next(ctx, origin.Add(time.Duration(i)*time.Hour), time.Minute)
		if errors.Is(err, frontier.ErrEmpty) {
			break
		}
		if err != nil {
			t.Fatalf("next: %v", err)
		}
		handed++
		if err := s.Fail(ctx, req.Hash, req.Attempt); err != nil {
			t.Fatalf("fail: %v", err)
		}
	}

	if handed != frontier.MaxAttempts {
		t.Errorf("handed out %d times, want %d", handed, frontier.MaxAttempts)
	}
	if n, _ := s.Waiting(ctx); n != 0 {
		t.Errorf("%d still waiting after it was abandoned", n)
	}
}

func TestASchedulerNeedsAJobAndAFrontier(t *testing.T) {
	ctx := context.Background()

	if _, err := scheduler.New(ctx, nil, scheduler.Options{}, nil); err == nil {
		t.Error("built a scheduler for no job")
	}
	if _, err := scheduler.New(ctx, job(t, ""), scheduler.Options{}, nil); err == nil {
		t.Error("built a scheduler with no way to open a frontier")
	}
}

// TestABadScopeIsRefusedWhenBuilt, rather than matching nothing all run.
func TestABadScopeIsRefusedWhenBuilt(t *testing.T) {
	j := job(t, `
  excluded = ["[unclosed"]
`)
	_, err := scheduler.New(context.Background(), j, scheduler.Options{Dir: t.TempDir()},
		func(cfg frontier.Config) (frontier.Frontier, error) { return sqlite.Open(cfg) })
	if err == nil {
		t.Fatal("accepted a pattern that is not one")
	}
	if !strings.Contains(err.Error(), "news") {
		t.Errorf("the error does not say which job: %v", err)
	}
}

// TestACallerMayBringItsOwnFrontier, and then closing the scheduler leaves it
// open, because it was never the scheduler's to close.
func TestACallerMayBringItsOwnFrontier(t *testing.T) {
	own, err := sqlite.Open(frontier.Config{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer own.Close()

	s, err := scheduler.New(context.Background(), job(t, ""),
		scheduler.Options{Frontier: own}, nil)
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	submit(t, s, "https://example.com/a")
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Still usable, because the scheduler did not open it.
	if n, err := own.Len(context.Background(), "news"); err != nil || n != 1 {
		t.Errorf("the caller's frontier was closed with the scheduler: %d, %v", n, err)
	}
}

// The remaining paths: what a node advertises, and the failures a store can
// produce that are not the caller's fault.

func TestRegisteredListsWhatThisBuildHas(t *testing.T) {
	names := strings.Join(scheduler.Registered(), " ")
	if !strings.Contains(names, "dupefilter") {
		t.Errorf("Registered() = %q", names)
	}
	if !scheduler.Has("dupefilter") || scheduler.Has("somebody-elses") {
		t.Error("Has disagrees with Registered")
	}
}

// TestAJobNamingSomethingThisNodeLacksIsRefused, at build rather than on the
// first URL.
func TestAJobNamingSomethingThisNodeLacksIsRefused(t *testing.T) {
	_, err := scheduler.New(context.Background(), job(t, `
  scheduler {
    plugin "cron" {}
  }
`), scheduler.Options{Dir: t.TempDir()},
		func(cfg frontier.Config) (frontier.Frontier, error) { return sqlite.Open(cfg) })
	if err == nil {
		t.Fatal("built a scheduler with middleware nothing implements")
	}
	if !strings.Contains(err.Error(), "cron") {
		t.Errorf("the error does not name it: %v", err)
	}
}

// TestAFrontierThatWillNotOpenIsReportedWithItsJob.
func TestAFrontierThatWillNotOpenIsReportedWithItsJob(t *testing.T) {
	_, err := scheduler.New(context.Background(), job(t, ""), scheduler.Options{},
		func(frontier.Config) (frontier.Frontier, error) {
			return nil, errors.New("the disk is full")
		})
	if err == nil {
		t.Fatal("built a scheduler on a frontier that would not open")
	}
	if !strings.Contains(err.Error(), "news") || !strings.Contains(err.Error(), "disk") {
		t.Errorf("the error lost either the job or the reason: %v", err)
	}
}

// TestAStoreThatFailsIsAFailure, unlike a URL that is merely refused. A caller
// that could not tell those apart would treat a broken database as a quiet
// crawl.
func TestAStoreThatFailsIsAFailure(t *testing.T) {
	broken := &brokenFrontier{err: errors.New("the database has gone away")}

	s, err := scheduler.New(context.Background(), job(t, ""),
		scheduler.Options{Frontier: broken}, nil)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer s.Close()

	added, err := s.Submit(context.Background(),
		&scheduler.Request{URL: "https://example.com/a", Discovered: origin})
	if err == nil {
		t.Fatal("a store that would not write reported success")
	}
	if added != 0 {
		t.Errorf("added = %d", added)
	}
	if chain.Dropped(err) {
		t.Error("a broken store was reported as a polite refusal")
	}
}

// TestClosingReportsWhatWouldNotClose, because a frontier this opened is one
// this has to be able to say it could not release.
func TestClosingReportsWhatWouldNotClose(t *testing.T) {
	stuck := errors.New("the file is still open")

	s, err := scheduler.New(context.Background(), job(t, ""), scheduler.Options{},
		func(frontier.Config) (frontier.Frontier, error) {
			return &brokenFrontier{closeErr: stuck}, nil
		})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := s.Close(); !errors.Is(err, stuck) {
		t.Errorf("close reported %v", err)
	}
}

// brokenFrontier is a store that fails at whatever it is asked.
type brokenFrontier struct {
	err      error
	closeErr error
}

func (b *brokenFrontier) Add(context.Context, string, ...frontier.Request) (int, error) {
	return 0, b.err
}

func (b *brokenFrontier) Lease(context.Context, string, time.Time, time.Duration) (*frontier.Request, error) {
	return nil, b.err
}

func (b *brokenFrontier) Pace(context.Context, string, time.Time, time.Duration) error {
	return b.err
}
func (b *brokenFrontier) Done(context.Context, string, string, int) error { return b.err }
func (b *brokenFrontier) Fail(context.Context, string, string, int) error { return b.err }
func (b *brokenFrontier) Len(context.Context, string) (int, error)        { return 0, b.err }
func (b *brokenFrontier) Close() error                                    { return b.closeErr }

// register puts a middleware in the global table for the length of one test.
//
// Every test that needs a middleware of its own goes through this rather than
// calling [scheduler.Register] directly, because the table is global and
// registering the same name twice panics: a test that registered without
// removing made this whole package impossible to run under `go test -count=2`
// or, once shuffling reordered it, under `-shuffle=on` either. Running the
// suite repeatedly is how a flaky test is found, so a package that cannot be is
// a package whose flakiness nobody will see. The gate runs -count=2 for that
// reason, which is what makes the next test that forgets fail the build rather
// than ship.
func register(t *testing.T, name string, m scheduler.Middleware) {
	t.Helper()
	scheduler.Register(name, m)
	t.Cleanup(func() { scheduler.Unregister(name) })
}

// TestMain fails the package if a test left a name in the global table. See
// [registrytest].
func TestMain(m *testing.M) { registrytest.Main(m, scheduler.Registered) }
