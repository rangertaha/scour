// SPDX-License-Identifier: GPL-3.0-or-later

package downloader_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/rangertaha/scour/internal/chain"
	"github.com/rangertaha/scour/internal/downloader"
	"github.com/rangertaha/scour/internal/plugin"
)

// hops is a site that redirects a path onward, recording every request, which
// is how a test tells a redirect that was followed from one that was not.
func hops(t *testing.T, routes map[string]string) (*httptest.Server, func() []string) {
	t.Helper()

	var (
		seen  []string
		guard = make(chan struct{}, 1)
	)
	guard <- struct{}{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			http.NotFound(w, r)
			return
		}

		<-guard
		seen = append(seen, r.URL.Path)
		guard <- struct{}{}

		if to, ok := routes[r.URL.Path]; ok {
			http.Redirect(w, r, to, http.StatusFound)
			return
		}
		fmt.Fprintf(w, "page %s", r.URL.Path)
	}))
	t.Cleanup(server.Close)

	return server, func() []string {
		<-guard
		defer func() { guard <- struct{}{} }()
		return append([]string(nil), seen...)
	}
}

func TestARedirectIsFollowed(t *testing.T) {
	server, seen := hops(t, map[string]string{
		"/old":    "/middle",
		"/middle": "/new",
	})

	s := stage(t, job(t, ""))
	resp := get(t, s, server.URL+"/old")

	if got := string(resp.Body); got != "page /new" {
		t.Errorf("body = %q", got)
	}
	if resp.URL != server.URL+"/new" {
		t.Errorf("url = %q, want where the body came from", resp.URL)
	}
	// The caller asked for /old and is owed a response that says so. The two
	// differing is what "this redirected" looks like.
	if resp.Request.URL != server.URL+"/old" {
		t.Errorf("request url = %q, want what was asked for", resp.Request.URL)
	}
	if got := strings.Join(seen(), " "); got != "/old /middle /new" {
		t.Errorf("the site was asked for %q", got)
	}
}

func TestARelativeLocationIsResolvedAgainstWhereWeAre(t *testing.T) {
	server, _ := hops(t, map[string]string{
		"/a/b/old":      "sideways",
		"/a/b/sideways": "/done",
	})

	resp := get(t, stage(t, job(t, "")), server.URL+"/a/b/old")
	if got := string(resp.Body); got != "page /done" {
		t.Errorf("body = %q", got)
	}
}

// TestARedirectLoopEndsSomewhere, and says where it went round.
func TestARedirectLoopEndsSomewhere(t *testing.T) {
	server, _ := hops(t, map[string]string{
		"/a": "/b",
		"/b": "/a",
	})

	s := stage(t, job(t, `
  downloader {
    max_redirects = 3
  }
`))

	_, err := s.Handle(context.Background(), &downloader.Request{URL: server.URL + "/a"})
	if !errors.Is(err, downloader.ErrTooManyRedirects) {
		t.Fatalf("err = %v, want ErrTooManyRedirects", err)
	}
	// Not a drop. A site that redirects in a circle is broken, and counting it
	// as politeness would hide that.
	if chain.Dropped(err) {
		t.Error("a redirect loop was reported as a polite refusal")
	}
	if !strings.Contains(err.Error(), "/a") || !strings.Contains(err.Error(), "->") {
		t.Errorf("the error does not show where it went: %v", err)
	}
}

// TestRedirectsCanBeTurnedOff, and then the 3xx is the answer.
func TestRedirectsCanBeTurnedOff(t *testing.T) {
	server, seen := hops(t, map[string]string{"/old": "/new"})

	s := stage(t, job(t, `
  downloader {
    max_redirects = 0
  }
`))

	resp := get(t, s, server.URL+"/old")
	if resp.Status != http.StatusFound {
		t.Errorf("status = %d, want the redirect itself", resp.Status)
	}
	if resp.Header.Get("Location") == "" {
		t.Error("the Location was not handed back")
	}
	if got := strings.Join(seen(), " "); got != "/old" {
		t.Errorf("the site was asked for %q", got)
	}
}

// TestEveryHopIsCheckedAgainstItsOwnHost is why this is not a plugin. A
// redirect to another host is a request to another host, and that host's
// robots.txt has to be read before it is made.
func TestEveryHopIsCheckedAgainstItsOwnHost(t *testing.T) {
	private := site(t, saying("User-agent: *\nDisallow: /\n"))

	var redirects atomic.Int32
	open := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			http.NotFound(w, r)
			return
		}
		redirects.Add(1)
		http.Redirect(w, r, private.URL+"/somewhere", http.StatusFound)
	}))
	defer open.Close()

	s := stage(t, job(t, ""))

	_, err := s.Handle(context.Background(), &downloader.Request{URL: open.URL + "/leaving"})
	if !errors.Is(err, downloader.ErrDisallowed) {
		t.Fatalf("err = %v, want the second host's robots.txt to have refused it", err)
	}
	if redirects.Load() != 1 {
		t.Errorf("the first host was asked %d times", redirects.Load())
	}
	if private.pages.Load() != 0 {
		t.Error("a redirect walked into a site that disallows everything")
	}
	if private.robots.Load() != 1 {
		t.Errorf("the second host's robots.txt was fetched %d times", private.robots.Load())
	}
}

// TestEveryHopGoesThroughTheChain: a hop is a request like any other, so the
// middleware that would have seen the first one sees this one too.
func TestEveryHopGoesThroughTheChain(t *testing.T) {
	var seen []string

	register(t, "test-hops", func(_ context.Context, cfg plugin.Config) (downloader.Wrapper, error) {
		return func(next downloader.Handler) downloader.Handler {
			return downloader.HandlerFunc(func(ctx context.Context, req *downloader.Request) (*downloader.Response, error) {
				seen = append(seen, req.URL)
				return next.Handle(ctx, req)
			})
		}, nil
	})

	server, _ := hops(t, map[string]string{"/old": "/new"})

	s := stage(t, job(t, `
  downloader {
    plugin "test-hops" {
      order = 500
    }
  }
`))

	get(t, s, server.URL+"/old")

	if len(seen) != 2 {
		t.Fatalf("the chain saw %v", seen)
	}
	if !strings.HasSuffix(seen[0], "/old") || !strings.HasSuffix(seen[1], "/new") {
		t.Errorf("the chain saw %v", seen)
	}
}

// TestCredentialsDoNotFollowARedirectOffTheHost. They were given to one site,
// and a redirect to another is exactly how they end up somewhere they were
// never meant to go.
func TestCredentialsDoNotFollowARedirectOffTheHost(t *testing.T) {
	type headers struct{ auth, cookie string }
	got := make(chan headers, 4)

	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			http.NotFound(w, r)
			return
		}
		got <- headers{auth: r.Header.Get("Authorization"), cookie: r.Header.Get("Cookie")}
		fmt.Fprint(w, "elsewhere")
	}))
	defer elsewhere.Close()

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			http.NotFound(w, r)
			return
		}
		switch r.URL.Path {
		case "/off":
			http.Redirect(w, r, elsewhere.URL+"/landed", http.StatusFound)
		case "/within":
			http.Redirect(w, r, "/still-here", http.StatusFound)
		default:
			got <- headers{auth: r.Header.Get("Authorization"), cookie: r.Header.Get("Cookie")}
			fmt.Fprint(w, "same host")
		}
	}))
	defer origin.Close()

	s := stage(t, job(t, ""))
	credentials := http.Header{
		"Authorization": {"Bearer secret"},
		"Cookie":        {"session=secret"},
	}

	if _, err := s.Handle(context.Background(), &downloader.Request{
		URL:    origin.URL + "/off",
		Header: credentials.Clone(),
	}); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if h := <-got; h.auth != "" || h.cookie != "" {
		t.Errorf("credentials reached another host: %+v", h)
	}

	// On the same host they are kept, or a login would not survive the
	// redirect that every login ends with.
	if _, err := s.Handle(context.Background(), &downloader.Request{
		URL:    origin.URL + "/within",
		Header: credentials.Clone(),
	}); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if h := <-got; h.auth != "Bearer secret" || h.cookie != "session=secret" {
		t.Errorf("credentials were dropped on the same host: %+v", h)
	}
}

// TestWhatEachRedirectDoesToTheMethod. 303 means fetch the other thing with
// GET; 301 and 302 are treated the same way for anything that had a body,
// because that is what every client does and therefore what every server
// expects. 307 and 308 exist because they do not.
func TestWhatEachRedirectDoesToTheMethod(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		method string
		want   string
	}{
		"303 posts as get":   {http.StatusSeeOther, http.MethodPost, "GET "},
		"302 posts as get":   {http.StatusFound, http.MethodPost, "GET "},
		"301 posts as get":   {http.StatusMovedPermanently, http.MethodPost, "GET "},
		"307 keeps the post": {http.StatusTemporaryRedirect, http.MethodPost, "POST q=x"},
		"308 keeps the post": {http.StatusPermanentRedirect, http.MethodPost, "POST q=x"},
		"303 keeps a get":    {http.StatusSeeOther, http.MethodGet, "GET "},
	} {
		t.Run(name, func(t *testing.T) {
			landed := make(chan string, 1)

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/robots.txt":
					http.NotFound(w, r)
				case "/start":
					http.Redirect(w, r, "/landed", tc.status)
				default:
					body := make([]byte, 32)
					n, _ := r.Body.Read(body)
					landed <- r.Method + " " + string(body[:n])
					fmt.Fprint(w, "done")
				}
			}))
			defer server.Close()

			s := stage(t, job(t, ""))
			if _, err := s.Handle(context.Background(), &downloader.Request{
				URL:    server.URL + "/start",
				Method: tc.method,
				Body:   []byte("q=x"),
			}); err != nil {
				t.Fatalf("fetch: %v", err)
			}

			if got := <-landed; got != tc.want {
				t.Errorf("the target saw %q, want %q", got, tc.want)
			}
		})
	}
}

// TestA3xxWithNowhereToGoIsTheAnswer. There is nothing to follow, and inventing
// somewhere would be worse than handing back what the server said.
func TestA3xxWithNowhereToGoIsTheAnswer(t *testing.T) {
	server := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusFound)
		fmt.Fprint(w, "moved, but not saying where")
	})

	resp := get(t, stage(t, job(t, "")), server.URL+"/nowhere")
	if resp.Status != http.StatusFound {
		t.Errorf("status = %d", resp.Status)
	}
	if string(resp.Body) != "moved, but not saying where" {
		t.Errorf("body = %q", resp.Body)
	}
}

// TestALocationThatIsNotAURLNeverReachesUs. net/http parses the header before
// it decides whether to hand the response back, so a broken Location is a
// failed fetch rather than a 3xx to think about. Worth pinning: it is the
// reason this package has no branch for it.
func TestALocationThatIsNotAURLNeverReachesUs(t *testing.T) {
	server := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "://not a url")
		w.WriteHeader(http.StatusMovedPermanently)
	})

	_, err := stage(t, job(t, "")).Handle(context.Background(),
		&downloader.Request{URL: server.URL + "/broken"})
	if err == nil {
		t.Fatal("a Location that is not a URL produced a response")
	}
	if !strings.Contains(err.Error(), "Location") {
		t.Errorf("the error does not say what was wrong: %v", err)
	}
}

// TestANonRedirectStatusIsLeftAlone, so nothing in here can eat an ordinary
// response.
func TestANonRedirectStatusIsLeftAlone(t *testing.T) {
	server := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "/somewhere")
		w.WriteHeader(http.StatusNotModified)
	})

	resp := get(t, stage(t, job(t, "")), server.URL+"/unchanged")
	if resp.Status != http.StatusNotModified {
		t.Errorf("status = %d", resp.Status)
	}
}
