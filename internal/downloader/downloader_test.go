// SPDX-License-Identifier: GPL-3.0-or-later

package downloader_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rangertaha/scour/internal/chain"
	"github.com/rangertaha/scour/internal/downloader"
	"github.com/rangertaha/scour/internal/engine"
	"github.com/rangertaha/scour/internal/plugin"
)

// job parses and validates a document with the given blocks inside it, which is
// the only way a stage is ever configured.
func job(t *testing.T, blocks string) *engine.Job {
	t.Helper()

	j := parse(t, blocks)
	doc, _ := engine.Parse([]byte(document(blocks)), "job.hcl")
	if err := doc.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	return j
}

// parse reads a document without validating it, which is what a stage handed a
// job off the bus has to cope with.
func parse(t *testing.T, blocks string) *engine.Job {
	t.Helper()

	doc, err := engine.Parse([]byte(document(blocks)), "job.hcl")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return doc.Jobs[0]
}

func document(blocks string) string {
	return `
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
}

// stage builds a downloader and closes it when the test ends.
func stage(t *testing.T, j *engine.Job) *downloader.Stage {
	t.Helper()

	s, err := downloader.New(context.Background(), j, downloader.Options{})
	if err != nil {
		t.Fatalf("new downloader: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	return s
}

func get(t *testing.T, s *downloader.Stage, url string) *downloader.Response {
	t.Helper()

	resp, err := s.Handle(context.Background(), &downloader.Request{URL: url})
	if err != nil {
		t.Fatalf("fetch %s: %v", url, err)
	}
	return resp
}

// The core.

func TestFetchesAPage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("X-Served-By", "test")
		fmt.Fprint(w, "<h1>hello</h1>")
	}))
	defer server.Close()

	before := time.Now()
	resp := get(t, stage(t, job(t, "")), server.URL+"/article")

	if resp.Status != http.StatusOK {
		t.Errorf("status = %d", resp.Status)
	}
	if string(resp.Body) != "<h1>hello</h1>" {
		t.Errorf("body = %q", resp.Body)
	}
	if resp.URL != server.URL+"/article" {
		t.Errorf("url = %q", resp.URL)
	}
	if resp.Header.Get("X-Served-By") != "test" {
		t.Errorf("headers did not survive: %v", resp.Header)
	}
	if resp.Fetched.Before(before) {
		t.Errorf("fetched at %s, before the request was made", resp.Fetched)
	}
	if resp.Cached {
		t.Error("a fetch from the network says it was cached")
	}
	if !resp.OK() {
		t.Error("a 200 is not OK()")
	}
}

// TestTheJobSaysWhoWeAre, because a crawler that will not identify itself is
// the one a site blocks.
func TestTheJobSaysWhoWeAre(t *testing.T) {
	var agent atomic.Value

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		agent.Store(r.UserAgent())
	}))
	defer server.Close()

	get(t, stage(t, job(t, "")), server.URL)
	if got := agent.Load(); got != engine.DefaultUserAgent {
		t.Errorf("sent %q, want the default", got)
	}

	get(t, stage(t, job(t, `
  downloader {
    user_agent = "acme-crawler/1.0"
  }
`)), server.URL)
	if got := agent.Load(); got != "acme-crawler/1.0" {
		t.Errorf("sent %q, want the job's", got)
	}
}

// TestARequestMayOverrideTheAgent, which is how a middleware rotates one.
func TestARequestMayOverrideTheAgent(t *testing.T) {
	var agent atomic.Value

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		agent.Store(r.UserAgent())
	}))
	defer server.Close()

	s := stage(t, job(t, ""))
	req := &downloader.Request{URL: server.URL, Header: http.Header{"User-Agent": {"something-else"}}}
	if _, err := s.Handle(context.Background(), req); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if got := agent.Load(); got != "something-else" {
		t.Errorf("sent %q, want the request's own", got)
	}
}

// TestARequestMaySendABody. Not every crawl is a GET: a search that only
// answers a form post is still a page worth having.
func TestARequestMaySendABody(t *testing.T) {
	type sent struct {
		method string
		body   string
	}
	var got atomic.Value

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got.Store(sent{method: r.Method, body: string(body)})
		fmt.Fprint(w, "results")
	}))
	defer server.Close()

	s := stage(t, job(t, ""))
	resp, err := s.Handle(context.Background(), &downloader.Request{
		URL:    server.URL + "/search",
		Method: http.MethodPost,
		Body:   []byte("q=crawling"),
	})
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	if string(resp.Body) != "results" {
		t.Errorf("body = %q", resp.Body)
	}
	if want := (sent{method: http.MethodPost, body: "q=crawling"}); got.Load() != want {
		t.Errorf("the server saw %+v", got.Load())
	}
}

// TestAStatusNobodyWantedIsStillAResponse. Whether a 404 is a failure is the
// spider's decision, and returning an error here would take it away.
func TestAStatusNobodyWantedIsStillAResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gone", http.StatusNotFound)
	}))
	defer server.Close()

	resp := get(t, stage(t, job(t, "")), server.URL)
	if resp.Status != http.StatusNotFound {
		t.Errorf("status = %d", resp.Status)
	}
	if len(resp.Body) == 0 {
		t.Error("the error page's body was thrown away")
	}
	if resp.OK() {
		t.Error("a 404 reports OK()")
	}
}

// TestAHugeBodyIsRefusedOnItsDeclaredLength, so a link to a video costs the
// headers rather than the video.
func TestAHugeBodyIsRefusedOnItsDeclaredLength(t *testing.T) {
	body := strings.Repeat("x", 4096)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Declared, which is what a server sending a video does and what
		// makes refusing it cheap.
		w.Header().Set("Content-Length", fmt.Sprint(len(body)))
		fmt.Fprint(w, body)
	}))
	defer server.Close()

	s := stage(t, job(t, `
  downloader {
    max_body = 100
  }
`))

	_, err := s.Handle(context.Background(), &downloader.Request{URL: server.URL})
	if !errors.Is(err, downloader.ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
	// "declared" is only in the Content-Length branch, so this is what says the
	// body was refused before it was read rather than after.
	if !strings.Contains(err.Error(), "declared") {
		t.Errorf("refused after reading it: %v", err)
	}
}

// TestABodyThatDeclaresNothingIsRefusedWhileReading. A chunked response has no
// length to check, and a server may simply lie, so the read is bounded too.
func TestABodyThatDeclaresNothingIsRefusedWhileReading(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Flushing before the body is complete forces chunked encoding, so no
		// Content-Length is sent.
		fmt.Fprint(w, "start")
		w.(http.Flusher).Flush()
		fmt.Fprint(w, strings.Repeat("x", 4096))
	}))
	defer server.Close()

	s := stage(t, job(t, `
  downloader {
    max_body = 100
  }
`))

	_, err := s.Handle(context.Background(), &downloader.Request{URL: server.URL})
	if !errors.Is(err, downloader.ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
	if strings.Contains(err.Error(), "declared") {
		t.Errorf("claimed a declared length that was never sent: %v", err)
	}
}

// TestABodyExactlyAtTheLimitIsFine: off by one here silently drops the pages
// that happen to sit on the boundary.
func TestABodyExactlyAtTheLimitIsFine(t *testing.T) {
	body := strings.Repeat("x", 100)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "start")
		w.(http.Flusher).Flush()
		fmt.Fprint(w, body[5:])
	}))
	defer server.Close()

	resp := get(t, stage(t, job(t, `
  downloader {
    max_body = 100
  }
`)), server.URL)

	if len(resp.Body) != 100 {
		t.Errorf("read %d bytes of a body exactly at the limit", len(resp.Body))
	}
}

// TestNoLimitReadsWhatever, which is what a fetcher built by hand for a test
// gets rather than a surprise.
func TestNoLimitReadsWhatever(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, strings.Repeat("x", 5000))
	}))
	defer server.Close()

	f := &downloader.Fetcher{Client: server.Client()}
	resp, err := f.Handle(context.Background(), &downloader.Request{URL: server.URL})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(resp.Body) != 5000 {
		t.Errorf("read %d bytes", len(resp.Body))
	}
}

// TestTheTimeoutCoversTheBody, not just the headers. A server that answers
// instantly and then dribbles for an hour is the case a header-only timeout
// misses.
func TestTheTimeoutCoversTheBody(t *testing.T) {
	release := make(chan struct{})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "headers are here")
		w.(http.Flusher).Flush()
		<-release
	}))
	defer func() {
		close(release)
		server.Close()
	}()

	s := stage(t, job(t, `
  downloader {
    timeout = "50ms"
  }
`))

	started := time.Now()
	_, err := s.Handle(context.Background(), &downloader.Request{URL: server.URL})
	if err == nil {
		t.Fatal("waited out a server that never finished")
	}
	if time.Since(started) > 5*time.Second {
		t.Errorf("gave up after %s, not after the timeout", time.Since(started))
	}
}

func TestACancelledContextStopsTheFetch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "hello")
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := stage(t, job(t, "")).Handle(ctx, &downloader.Request{URL: server.URL})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestNothingToFetch(t *testing.T) {
	s := stage(t, job(t, ""))

	if _, err := s.Handle(context.Background(), &downloader.Request{}); err == nil {
		t.Error("fetched a request with no URL")
	}
	if _, err := s.Handle(context.Background(), &downloader.Request{URL: "://not a url"}); err == nil {
		t.Error("fetched something that is not a URL")
	}
	if _, err := s.Handle(context.Background(), &downloader.Request{URL: "http://127.0.0.1:1/"}); err == nil {
		t.Error("fetched from a port nothing is listening on")
	}
}

// TestTheBodyIsNotDecodedOnTheWayIn: what arrives is what the server sent, and
// [downloader.Response.Text] is where it becomes UTF-8. That order is what lets
// the cache hold original bytes.
func TestTheBodyIsNotDecodedOnTheWayIn(t *testing.T) {
	// "цена" in windows-1251, declared only in the Content-Type header.
	raw := []byte{0xf6, 0xe5, 0xed, 0xe0}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=windows-1251")
		w.Write(raw)
	}))
	defer server.Close()

	resp := get(t, stage(t, job(t, "")), server.URL)

	if string(resp.Body) != string(raw) {
		t.Errorf("body = % x, want the bytes the server sent", resp.Body)
	}

	text, err := resp.Text()
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if string(text) != "цена" {
		t.Errorf("text = %q", text)
	}
}

func TestATextlessResponseDecodesToNothing(t *testing.T) {
	var resp *downloader.Response

	if text, err := resp.Text(); err != nil || text != nil {
		t.Errorf("Text() = %q, %v", text, err)
	}
	if resp.ContentType() != "" {
		t.Error("a response that does not exist has a content type")
	}
	if resp.OK() {
		t.Error("a response that does not exist is OK()")
	}
	if (&downloader.Response{}).ContentType() != "" {
		t.Error("a response with no headers has a content type")
	}
}

// The chain.

func TestTheChainWrapsTheFetch(t *testing.T) {
	var log []string

	downloader.Register("test-outer", noting("test-outer", &log))
	downloader.Register("test-inner", noting("test-inner", &log))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log = append(log, "fetch")
		fmt.Fprint(w, "hello")
	}))
	defer server.Close()

	s := stage(t, job(t, `
  downloader {
    plugin "test-inner" {
      order = 800
    }

    plugin "test-outer" {
      order = 100
    }
  }
`))

	if got := strings.Join(s.Middleware(), " "); got != "test-outer test-inner" {
		t.Errorf("Middleware() = %q", got)
	}

	get(t, s, server.URL)
	if got := strings.Join(log, " "); got != "test-outer test-inner fetch" {
		t.Errorf("ran %q, want lowest order outermost", got)
	}
}

// TestAMiddlewareMayAnswerWithoutFetching is what a cache hit is, and the
// reason the chain wraps rather than hooks.
func TestAMiddlewareMayAnswerWithoutFetching(t *testing.T) {
	var fetched atomic.Int32

	downloader.Register("test-answer", func(_ context.Context, cfg plugin.Config) (downloader.Wrapper, error) {
		return func(next downloader.Handler) downloader.Handler {
			return downloader.HandlerFunc(func(ctx context.Context, req *downloader.Request) (*downloader.Response, error) {
				return &downloader.Response{Request: req, Status: 200, Body: []byte("from the plugin")}, nil
			})
		}, nil
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetched.Add(1)
	}))
	defer server.Close()

	resp := get(t, stage(t, job(t, `
  downloader {
    plugin "test-answer" {
      order = 900
    }
  }
`)), server.URL)

	if string(resp.Body) != "from the plugin" {
		t.Errorf("body = %q", resp.Body)
	}
	if fetched.Load() != 0 {
		t.Error("the network was used anyway")
	}
}

// TestAMiddlewareMayDrop, and a drop is not a failure: a crawl that obeys
// robots.txt does it all day.
func TestAMiddlewareMayDrop(t *testing.T) {
	downloader.Register("test-drop", func(_ context.Context, cfg plugin.Config) (downloader.Wrapper, error) {
		return func(next downloader.Handler) downloader.Handler {
			return downloader.HandlerFunc(func(ctx context.Context, req *downloader.Request) (*downloader.Response, error) {
				return nil, fmt.Errorf("test-drop: %s: %w", req.URL, chain.ErrDrop)
			})
		}, nil
	})

	s := stage(t, job(t, `
  downloader {
    plugin "test-drop" {
      order = 100
    }
  }
`))

	_, err := s.Handle(context.Background(), &downloader.Request{URL: "http://example.com/"})
	if !chain.Dropped(err) {
		t.Errorf("err = %v, want a drop", err)
	}
}

// TestTheStageSaysWhichJobARequestBelongsTo, so a middleware keeping anything
// per job does not have to be told twice.
func TestTheStageSaysWhichJobARequestBelongsTo(t *testing.T) {
	var seen atomic.Value

	downloader.Register("test-job", func(_ context.Context, cfg plugin.Config) (downloader.Wrapper, error) {
		return func(next downloader.Handler) downloader.Handler {
			return downloader.HandlerFunc(func(ctx context.Context, req *downloader.Request) (*downloader.Response, error) {
				seen.Store(req.Job)
				return &downloader.Response{Request: req, Status: 200}, nil
			})
		}, nil
	})

	s := stage(t, job(t, `
  downloader {
    plugin "test-job" {
      order = 100
    }
  }
`))

	if _, err := s.Handle(context.Background(), &downloader.Request{URL: "http://example.com/"}); err != nil {
		t.Fatal(err)
	}
	if got := seen.Load(); got != "news" {
		t.Errorf("the middleware saw job %q", got)
	}

	// A request that already says which job it is keeps its answer, because a
	// middleware may be replaying one from somewhere else.
	if _, err := s.Handle(context.Background(), &downloader.Request{URL: "http://example.com/", Job: "other"}); err != nil {
		t.Fatal(err)
	}
	if got := seen.Load(); got != "other" {
		t.Errorf("the middleware saw job %q, want the request's own", got)
	}
}

// TestTheStageClosesWhatItsPluginsOpened, which is the reason a chain has a
// lifetime at all.
func TestTheStageClosesWhatItsPluginsOpened(t *testing.T) {
	var closed atomic.Int32

	downloader.Register("test-closer", func(_ context.Context, cfg plugin.Config) (downloader.Wrapper, error) {
		cfg.Defer(func() error {
			closed.Add(1)
			return nil
		})
		return func(next downloader.Handler) downloader.Handler { return next }, nil
	})

	s, err := downloader.New(context.Background(), job(t, `
  downloader {
    plugin "test-closer" {
      order = 100
    }
  }
`), downloader.Options{})
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	if closed.Load() != 0 {
		t.Fatal("closed before anything asked")
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if closed.Load() != 1 {
		t.Errorf("closed %d times", closed.Load())
	}
}

// TestAJobNamingSomethingThisNodeLacksIsRefused, at build rather than on the
// first request. This build has no "cache" in it, because this package does not
// import the package that registers one.
func TestAJobNamingSomethingThisNodeLacksIsRefused(t *testing.T) {
	if downloader.Has("cache") {
		t.Fatal("this test only means something in a build without the cache plugin")
	}

	_, err := downloader.New(context.Background(), job(t, `
  downloader {
    plugin "cache" {}
  }
`), downloader.Options{})
	if err == nil {
		t.Fatal("built a downloader with middleware nothing implements")
	}
	if !strings.Contains(err.Error(), "cache") {
		t.Errorf("the error does not name it: %v", err)
	}
}

func TestADownloaderNeedsAJob(t *testing.T) {
	if _, err := downloader.New(context.Background(), nil, downloader.Options{}); err == nil {
		t.Error("built a downloader for no job at all")
	}
}

// TestAnUnvalidatedJobIsStillChecked. A job arriving over the bus was validated
// by whoever accepted it, but a stage that trusts that and divides by it is one
// bad message from a panic.
func TestAnUnvalidatedJobIsStillChecked(t *testing.T) {
	j := parse(t, `
  downloader {
    timeout = "whenever"
  }
`)

	_, err := downloader.New(context.Background(), j, downloader.Options{})
	if err == nil {
		t.Fatal("accepted a timeout that is not a duration")
	}
	if !strings.Contains(err.Error(), "news") {
		t.Errorf("the error does not say which job: %v", err)
	}
}

// TestACloneIsACopy, because a middleware that edits the request it was handed
// leaves a retry with nothing to retry.
func TestACloneIsACopy(t *testing.T) {
	req := &downloader.Request{
		URL:    "http://example.com/",
		Header: http.Header{"X-Test": {"one"}},
		Body:   []byte("payload"),
		Depth:  2,
	}

	clone := req.Clone()
	clone.URL = "http://elsewhere.example/"
	clone.Header.Set("X-Test", "two")
	clone.Body[0] = 'X'

	if req.URL != "http://example.com/" {
		t.Errorf("the original's URL changed to %q", req.URL)
	}
	if req.Header.Get("X-Test") != "one" {
		t.Errorf("the original's header changed to %q", req.Header.Get("X-Test"))
	}
	if string(req.Body) != "payload" {
		t.Errorf("the original's body changed to %q", req.Body)
	}
	if clone.Depth != 2 {
		t.Errorf("the copy lost its depth")
	}
	if (*downloader.Request)(nil).Clone() != nil {
		t.Error("cloned a request that does not exist")
	}
}

func TestRegisteredListsWhatThisBuildHas(t *testing.T) {
	names := strings.Join(downloader.Registered(), " ")
	if !strings.Contains(names, "test-drop") {
		t.Errorf("Registered() = %q", names)
	}
}

// noting is a middleware that records that it ran, which is how order is
// observed from outside.
func noting(name string, log *[]string) downloader.Middleware {
	return func(_ context.Context, cfg plugin.Config) (downloader.Wrapper, error) {
		return func(next downloader.Handler) downloader.Handler {
			return downloader.HandlerFunc(func(ctx context.Context, req *downloader.Request) (*downloader.Response, error) {
				*log = append(*log, name)
				return next.Handle(ctx, req)
			})
		}, nil
	}
}
