// SPDX-License-Identifier: GPL-3.0-or-later

package downloader_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/rangertaha/scour/internal/chain"
	"github.com/rangertaha/scour/internal/downloader"
	"github.com/rangertaha/scour/internal/plugin"
)

// policed is a site with a robots.txt, counting what was asked of it. The two
// counters are what separates "was not fetched" from "was fetched quickly", and
// the rules can change mid-test because a site that recovers is a case worth
// having.
type policed struct {
	*httptest.Server
	robots atomic.Int32
	pages  atomic.Int32
	rules  atomic.Value
}

func site(t *testing.T, rules http.HandlerFunc) *policed {
	t.Helper()

	s := &policed{}
	s.rules.Store(rules)
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			s.robots.Add(1)
			s.rules.Load().(http.HandlerFunc)(w, r)
			return
		}
		s.pages.Add(1)
		fmt.Fprintf(w, "page %s", r.URL.Path)
	}))
	t.Cleanup(s.Close)
	return s
}

// says replaces what robots.txt will answer from now on.
func (s *policed) says(rules http.HandlerFunc) { s.rules.Store(rules) }

// saying serves a robots.txt.
func saying(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, body)
	}
}

// missing is a site with no robots.txt, which is most sites.
func missing() http.HandlerFunc { return http.NotFound }

// broken is a site that cannot answer, which is not the same as one that
// answered yes.
func broken(status int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { http.Error(w, "sorry", status) }
}

func TestADisallowedURLIsNeverFetched(t *testing.T) {
	server := site(t, saying("User-agent: *\nDisallow: /private\n"))
	s := stage(t, job(t, ""))

	_, err := s.Handle(context.Background(), &downloader.Request{URL: server.URL + "/private/page"})
	if !errors.Is(err, downloader.ErrDisallowed) {
		t.Fatalf("err = %v, want ErrDisallowed", err)
	}
	// A drop, not a failure: a crawl that obeys robots.txt does this all day.
	if !chain.Dropped(err) {
		t.Error("a refusal was reported as a failure")
	}
	if server.pages.Load() != 0 {
		t.Error("the page was fetched anyway")
	}

	// And what the site did not refuse still gets through.
	if got := string(get(t, s, server.URL+"/public/page").Body); got != "page /public/page" {
		t.Errorf("body = %q", got)
	}
}

// TestRobotsIsAskedOncePerHost, not once per request. A crawler that refetched
// robots.txt for every URL would be the rudest thing on the site.
func TestRobotsIsAskedOncePerHost(t *testing.T) {
	server := site(t, saying("User-agent: *\nDisallow: /private\n"))
	s := stage(t, job(t, ""))

	for i := range 5 {
		get(t, s, fmt.Sprintf("%s/public/%d", server.URL, i))
	}
	if server.robots.Load() != 1 {
		t.Errorf("robots.txt was fetched %d times", server.robots.Load())
	}
	if server.pages.Load() != 5 {
		t.Errorf("%d pages were fetched", server.pages.Load())
	}
}

// TestOneRobotsFetchUnderLoad. The stage is one per job and shared by every
// worker on this node, so the first request for a host must not become one
// request per goroutine.
func TestOneRobotsFetchUnderLoad(t *testing.T) {
	server := site(t, saying("User-agent: *\nDisallow: /private\n"))
	s := stage(t, job(t, ""))

	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Handle(context.Background(), &downloader.Request{
				URL: fmt.Sprintf("%s/public/%d", server.URL, i),
			}); err != nil {
				t.Errorf("fetch: %v", err)
			}
		}()
	}
	wg.Wait()

	if server.robots.Load() != 1 {
		t.Errorf("robots.txt was fetched %d times", server.robots.Load())
	}
}

// TestEachHostHasItsOwnRules, or one site's rules would be enforced against
// another's pages.
func TestEachHostHasItsOwnRules(t *testing.T) {
	strict := site(t, saying("User-agent: *\nDisallow: /\n"))
	open := site(t, missing())

	s := stage(t, job(t, ""))

	if _, err := s.Handle(context.Background(), &downloader.Request{URL: strict.URL + "/page"}); !chain.Dropped(err) {
		t.Errorf("the strict site was crawled: %v", err)
	}
	if got := string(get(t, s, open.URL+"/page").Body); got != "page /page" {
		t.Errorf("the open site was refused: %q", got)
	}
}

// TestNoRobotsMeansNothingToObey. Most sites have no robots.txt at all, and a
// 404 has to read as permission or the crawler would never fetch anything.
func TestNoRobotsMeansNothingToObey(t *testing.T) {
	server := site(t, missing())
	s := stage(t, job(t, ""))

	get(t, s, server.URL+"/anything")
	if server.pages.Load() != 1 {
		t.Error("a site with no robots.txt was not crawled")
	}
}

// TestASiteThatCannotAnswerIsNotASiteThatSaidYes. RFC 9309 §2.3.1.3: a 5xx is
// unreachable, and unreachable is not permission.
func TestASiteThatCannotAnswerIsNotASiteThatSaidYes(t *testing.T) {
	server := site(t, broken(http.StatusInternalServerError))
	s := stage(t, job(t, ""))

	_, err := s.Handle(context.Background(), &downloader.Request{URL: server.URL + "/page"})
	if !errors.Is(err, downloader.ErrNoRobots) {
		t.Fatalf("err = %v, want ErrNoRobots", err)
	}
	if !chain.Dropped(err) {
		t.Error("an unreadable robots.txt was reported as a failure")
	}
	if server.pages.Load() != 0 {
		t.Error("the page was fetched anyway")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("the error does not say what happened: %v", err)
	}
}

// TestAFailureIsNotRemembered. A robots.txt that could not be fetched is a
// question that has not been answered rather than an answer, so a blip costs
// one request rather than a host for the rest of the run.
func TestAFailureIsNotRemembered(t *testing.T) {
	server := site(t, broken(http.StatusBadGateway))
	s := stage(t, job(t, ""))

	if _, err := s.Handle(context.Background(), &downloader.Request{URL: server.URL + "/page"}); !chain.Dropped(err) {
		t.Fatalf("err = %v", err)
	}

	server.says(saying("User-agent: *\nDisallow: /private\n"))

	if got := string(get(t, s, server.URL+"/page").Body); got != "page /page" {
		t.Errorf("a site that recovered was still refused: %q", got)
	}
	if server.robots.Load() != 2 {
		t.Errorf("robots.txt was fetched %d times, want the failure to have been asked again", server.robots.Load())
	}

	// And now that it has answered, the answer is kept.
	get(t, s, server.URL+"/other")
	if server.robots.Load() != 2 {
		t.Errorf("a successful answer was asked for again: %d fetches", server.robots.Load())
	}
}

// TestAnUnreachableHostIsNotCrawled: no answer at all is the same as an answer
// nobody could read.
func TestAnUnreachableHostIsNotCrawled(t *testing.T) {
	s := stage(t, job(t, ""))

	_, err := s.Handle(context.Background(), &downloader.Request{URL: "http://127.0.0.1:1/page"})
	if !errors.Is(err, downloader.ErrNoRobots) {
		t.Errorf("err = %v, want ErrNoRobots", err)
	}
}

// TestRobotsCanBeTurnedOff, which is legitimate against a site you own or a
// staging server, and is the only thing the attribute controls.
func TestRobotsCanBeTurnedOff(t *testing.T) {
	server := site(t, saying("User-agent: *\nDisallow: /\n"))

	s := stage(t, job(t, `
  downloader {
    robots = false
  }
`))

	if got := string(get(t, s, server.URL+"/private").Body); got != "page /private" {
		t.Errorf("body = %q", got)
	}
	if server.robots.Load() != 0 {
		t.Error("robots.txt was fetched by a job that had turned it off")
	}
}

// TestTheJobsAgentPicksTheGroup. A site addressing us by name has to be obeyed
// over the catch-all, and obeying the wrong group means following rules written
// for somebody else.
func TestTheJobsAgentPicksTheGroup(t *testing.T) {
	rules := saying(`
User-agent: *
Disallow: /

User-agent: acmebot
Disallow: /private
`)

	// Under our own name we get the catch-all, which refuses everything.
	strict := site(t, rules)
	if _, err := stage(t, job(t, "")).Handle(context.Background(),
		&downloader.Request{URL: strict.URL + "/public"}); !chain.Dropped(err) {
		t.Errorf("the catch-all group was not applied: %v", err)
	}

	// Under the name the site addressed, we get its group.
	named := site(t, rules)
	s := stage(t, job(t, `
  downloader {
    user_agent = "acmebot/1.0 (+https://acme.example/bot)"
  }
`))
	if got := string(get(t, s, named.URL+"/public").Body); got != "page /public" {
		t.Errorf("the group addressed to us was not applied: %q", got)
	}
	if _, err := s.Handle(context.Background(), &downloader.Request{URL: named.URL + "/private"}); !chain.Dropped(err) {
		t.Errorf("our own group's disallow was ignored: %v", err)
	}
}

// TestRobotsIsCheckedOutsideEverything. It is not a plugin, and the reason is
// that there is one correct position: outside the cache, outside a retry, and
// outside anything else that would otherwise pay for a request the site had
// already refused.
func TestRobotsIsCheckedOutsideEverything(t *testing.T) {
	var reached atomic.Int32

	downloader.Register("test-inside-robots", func(_ context.Context, cfg plugin.Config) (downloader.Wrapper, error) {
		return func(next downloader.Handler) downloader.Handler {
			return downloader.HandlerFunc(func(ctx context.Context, req *downloader.Request) (*downloader.Response, error) {
				reached.Add(1)
				return &downloader.Response{Request: req, Status: 200, Body: []byte("answered from the chain")}, nil
			})
		}, nil
	})

	server := site(t, saying("User-agent: *\nDisallow: /private\n"))

	// Order 1, so it is as far outside as a plugin can get and still be one.
	s := stage(t, job(t, `
  downloader {
    plugin "test-inside-robots" {
      order = 1
    }
  }
`))

	if _, err := s.Handle(context.Background(), &downloader.Request{URL: server.URL + "/private"}); !chain.Dropped(err) {
		t.Fatalf("err = %v, want a drop", err)
	}
	if reached.Load() != 0 {
		t.Error("the outermost plugin ran for a URL the site had refused")
	}

	// The same plugin does run for a URL the site allows, so this is about
	// where robots sits and not about the plugin never running.
	if _, err := s.Handle(context.Background(), &downloader.Request{URL: server.URL + "/public"}); err != nil {
		t.Fatal(err)
	}
	if reached.Load() != 1 {
		t.Error("the plugin did not run for an allowed URL")
	}
}

// TestRobotsIsReadToItsOwnLimit, not the job's. max_body is a limit on pages: a
// job that will not download a megabyte of HTML still has to be able to read
// what a site permits.
func TestRobotsIsReadToItsOwnLimit(t *testing.T) {
	rules := "User-agent: *\nDisallow: /private\n" +
		"# " + strings.Repeat("padding ", 40) + "\n"
	if len(rules) < 200 {
		t.Fatalf("the fixture is only %d bytes, which does not exceed the limit under test", len(rules))
	}

	server := site(t, saying(rules))
	s := stage(t, job(t, `
  downloader {
    max_body = 100
  }
`))

	if _, err := s.Handle(context.Background(), &downloader.Request{URL: server.URL + "/private"}); !chain.Dropped(err) {
		t.Errorf("a robots.txt larger than max_body was not read: %v", err)
	}
	// The page limit itself still applies to pages.
	if got := string(get(t, s, server.URL+"/public").Body); got != "page /public" {
		t.Errorf("body = %q", got)
	}
}

// TestSomethingThatIsNotHTTPIsNotOurRuleToEnforce. robots.txt is an HTTP
// protocol, and a scheme it says nothing about must not be silently dropped as
// though a site had refused it.
func TestSomethingThatIsNotHTTPIsNotOurRuleToEnforce(t *testing.T) {
	s := stage(t, job(t, ""))

	_, err := s.Handle(context.Background(), &downloader.Request{URL: "ftp://example.com/file"})
	if err == nil {
		t.Fatal("fetched over a scheme the client does not speak")
	}
	if chain.Dropped(err) {
		t.Errorf("a scheme robots.txt says nothing about was dropped as refused: %v", err)
	}
}

// TestAURLThatWillNotParseIsTheFetchersProblem, which reports it with a better
// message than this guard could.
func TestAURLThatWillNotParseIsTheFetchersProblem(t *testing.T) {
	s := stage(t, job(t, ""))

	_, err := s.Handle(context.Background(), &downloader.Request{URL: "://not a url"})
	if err == nil {
		t.Fatal("fetched something that is not a URL")
	}
	if chain.Dropped(err) {
		t.Errorf("a malformed URL was reported as a site refusing it: %v", err)
	}
}

// TestRobotsFollowsARedirect. A site that moved, or that serves http and
// redirects to https, or that keeps one robots.txt for several hostnames, is
// entirely ordinary. Treating the redirect as an unreadable file refuses the
// whole host, which is what this crawler did to blog.golang.org the first time
// it was pointed at a real site.
func TestRobotsFollowsARedirect(t *testing.T) {
	rules := site(t, saying("User-agent: *\nDisallow: /private\n"))

	moved := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			http.Redirect(w, r, rules.URL+"/robots.txt", http.StatusMovedPermanently)
			return
		}
		fmt.Fprintf(w, "page %s", r.URL.Path)
	}))
	defer moved.Close()

	s := stage(t, job(t, ""))

	if got := string(get(t, s, moved.URL+"/public").Body); got != "page /public" {
		t.Errorf("body = %q", got)
	}
	if _, err := s.Handle(context.Background(), &downloader.Request{URL: moved.URL + "/private"}); !chain.Dropped(err) {
		t.Errorf("the rules at the other end of the redirect were not applied: %v", err)
	}
	if rules.robots.Load() != 1 {
		t.Errorf("the redirect target was fetched %d times", rules.robots.Load())
	}
}

// TestRobotsStopsFollowingEventually, or a site that redirects its robots.txt
// in a circle would be a way to hang a crawler.
func TestRobotsStopsFollowingEventually(t *testing.T) {
	var asked atomic.Int32

	loop := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "robots") {
			asked.Add(1)
			http.Redirect(w, r, "/robots.txt", http.StatusFound)
			return
		}
		fmt.Fprint(w, "page")
	}))
	defer loop.Close()

	s := stage(t, job(t, ""))

	_, err := s.Handle(context.Background(), &downloader.Request{URL: loop.URL + "/page"})
	if !errors.Is(err, downloader.ErrNoRobots) {
		t.Errorf("err = %v, want the host refused", err)
	}
	if got := asked.Load(); got > 10 {
		t.Errorf("robots.txt was fetched %d times before giving up", got)
	}
}
