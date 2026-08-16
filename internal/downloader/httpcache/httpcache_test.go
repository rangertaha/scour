// SPDX-License-Identifier: GPL-3.0-or-later

package httpcache_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rangertaha/scour/internal/registry/registrytest"

	"github.com/rangertaha/scour/internal/cache"
	"github.com/rangertaha/scour/internal/downloader"
	"github.com/rangertaha/scour/internal/engine"
	"github.com/rangertaha/scour/internal/secret"

	_ "github.com/rangertaha/scour/internal/cache/local"
	_ "github.com/rangertaha/scour/internal/downloader/httpcache"
)

// TestMain quiets the middleware, which logs what it could not read or write.
// That is right in a crawl and noise in a test; the two tests that care about
// it install a logger of their own.
// It also fails the package if a test left a name in the cache's global table.
// See [registrytest].
func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	done := registrytest.Watch(cache.Backends)
	os.Exit(done(m.Run()))
}

// capture makes the default logger write somewhere this test can read, for as
// long as this test runs. A cache failure is swallowed on purpose, and the log
// line is the only place it is ever reported.
func capture(t *testing.T) func() string {
	t.Helper()

	buf := &syncBuffer{}
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, nil)))
	t.Cleanup(func() { slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil))) })

	return buf.String
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// site is a server that counts what was actually asked of it, which is the only
// way to tell a hit from a very fast fetch.
type site struct {
	*httptest.Server
	hits atomic.Int32
}

func serve(t *testing.T, h http.HandlerFunc) *site {
	t.Helper()

	s := &site{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// robots.txt is fetched by the downloader itself, outside the chain
		// and outside the cache, so it is neither this handler's business nor
		// something to count as a page.
		if r.URL.Path == "/robots.txt" {
			http.NotFound(w, r)
			return
		}
		s.hits.Add(1)
		h(w, r)
	}))
	t.Cleanup(s.Close)
	return s
}

// stage builds a downloader whose cache is a directory this test owns.
func stage(t *testing.T, dir string, settings string) *downloader.Stage {
	t.Helper()

	src := fmt.Sprintf(`
job "news" {
  start = ["https://example.com/"]

  item "article" {
    property "title" {
      type = str
    }
  }

  downloader {
    plugin "cache" {
      dir = %q
%s
    }
  }
}
`, dir, settings)

	doc, err := engine.Parse([]byte(src), "job.hcl")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := doc.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}

	s, err := downloader.New(context.Background(), doc.Jobs[0], downloader.Options{})
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

// TestASecondFetchDoesNotReachTheSite is the whole point: fetching is the
// expensive, rate-limited, impolite part and understanding a page is not.
func TestASecondFetchDoesNotReachTheSite(t *testing.T) {
	server := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, "<h1>hello</h1>")
	})

	s := stage(t, t.TempDir(), "")

	first := get(t, s, server.URL+"/article")
	if first.Cached {
		t.Error("the first fetch says it came from the cache")
	}

	second := get(t, s, server.URL+"/article")
	if !second.Cached {
		t.Error("the second fetch went to the network")
	}
	if server.hits.Load() != 1 {
		t.Errorf("the site was asked %d times", server.hits.Load())
	}
	if string(second.Body) != string(first.Body) {
		t.Errorf("the cache returned %q", second.Body)
	}
}

// TestAHitIsTheResponseNotJustTheBody. A body with its provenance filed off
// cannot be decoded, cannot be filtered by status, and cannot be aged.
func TestAHitIsTheResponseNotJustTheBody(t *testing.T) {
	server := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("X-Served-By", "test")
		fmt.Fprint(w, "<h1>hello</h1>")
	})

	s := stage(t, t.TempDir(), "")

	first := get(t, s, server.URL+"/article")
	second := get(t, s, server.URL+"/article")

	if second.Status != first.Status {
		t.Errorf("status = %d, want %d", second.Status, first.Status)
	}
	if second.URL != first.URL {
		t.Errorf("url = %q, want %q", second.URL, first.URL)
	}
	if second.Header.Get("X-Served-By") != "test" {
		t.Errorf("headers were lost: %v", second.Header)
	}
	// Not when it was read back: the age of the evidence is what a TTL, a
	// revalidation and a report all measure.
	if !second.Fetched.Equal(first.Fetched.Round(0)) && second.Fetched.Sub(first.Fetched).Abs() > time.Second {
		t.Errorf("fetched = %s, want the original %s", second.Fetched, first.Fetched)
	}
	if second.Request == nil || second.Request.URL != server.URL+"/article" {
		t.Error("a hit does not say what was asked for")
	}
}

// TestTheCorpusHoldsWhatTheServerSent, in the encoding it was sent in, and
// decodes to the same text on the way back out.
//
// This is the highest-risk item in the plan. A page in windows-1251 that
// declares its encoding in the Content-Type header and nowhere else decodes
// into mojibake if the header is lost, and nothing about the resulting text
// says it went wrong.
func TestTheCorpusHoldsWhatTheServerSent(t *testing.T) {
	// "цена" in windows-1251.
	raw := []byte{0xf6, 0xe5, 0xed, 0xe0}

	server := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=windows-1251")
		w.Write(raw)
	})

	dir := t.TempDir()
	s := stage(t, dir, "")

	fetched := get(t, s, server.URL+"/price")
	text, err := fetched.Text()
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if string(text) != "цена" {
		t.Fatalf("the fetch decoded to %q", text)
	}

	// What is on disk is the original bytes, not the decoding. Detection
	// improves; a corpus decoded on the way in has its mistakes baked in until
	// somebody re-crawls.
	stored := body(t, dir, server.URL+"/price")
	if string(stored) != string(raw) {
		t.Errorf("the cache holds % x, want the bytes the server sent", stored)
	}

	hit := get(t, s, server.URL+"/price")
	if !hit.Cached {
		t.Fatal("the second read was not a hit")
	}
	text, err = hit.Text()
	if err != nil {
		t.Fatalf("decode from cache: %v", err)
	}
	if string(text) != "цена" {
		t.Errorf("the cache decoded to %q, want what the fetch decoded to", text)
	}
}

// TestOnlyWhatWasAskedForIsCached: two URLs are two entries.
func TestOnlyWhatWasAskedForIsCached(t *testing.T) {
	server := serve(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "page %s", r.URL.Path)
	})

	s := stage(t, t.TempDir(), "")

	if got := string(get(t, s, server.URL+"/one").Body); got != "page /one" {
		t.Errorf("first = %q", got)
	}
	if got := string(get(t, s, server.URL+"/two").Body); got != "page /two" {
		t.Errorf("second = %q", got)
	}
	if got := string(get(t, s, server.URL+"/one").Body); got != "page /one" {
		t.Errorf("the first URL came back as %q", got)
	}
	if server.hits.Load() != 2 {
		t.Errorf("the site was asked %d times for two URLs", server.hits.Load())
	}
}

// TestOnlyTheStatusesWorthKeeping. A 404 says a URL is dead today, and caching
// it would keep saying so after the page came back.
func TestOnlyTheStatusesWorthKeeping(t *testing.T) {
	server := serve(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gone", http.StatusNotFound)
	})

	s := stage(t, t.TempDir(), "")

	get(t, s, server.URL+"/missing")
	get(t, s, server.URL+"/missing")
	if server.hits.Load() != 2 {
		t.Errorf("a 404 was cached: the site was asked %d times", server.hits.Load())
	}
}

// TestAJobMayWidenWhatIsWorthKeeping, because a job archiving a site wants the
// 404s too.
func TestAJobMayWidenWhatIsWorthKeeping(t *testing.T) {
	server := serve(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gone", http.StatusNotFound)
	})

	s := stage(t, t.TempDir(), "      statuses = [200, 404]")

	get(t, s, server.URL+"/missing")
	hit := get(t, s, server.URL+"/missing")
	if !hit.Cached {
		t.Error("a status the job asked to keep was not kept")
	}
	if hit.Status != http.StatusNotFound {
		t.Errorf("status = %d", hit.Status)
	}
}

// TestATTLMakesAnOldHitAMiss, which is the difference between an archive and a
// monitor.
func TestATTLMakesAnOldHitAMiss(t *testing.T) {
	server := serve(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "today's price")
	})

	dir := t.TempDir()
	s := stage(t, dir, `      ttl = "1h"`)

	get(t, s, server.URL+"/price")
	if hit := get(t, s, server.URL+"/price"); !hit.Cached {
		t.Fatal("a fresh entry was not a hit")
	}

	// Age the entry rather than the test: what a TTL reads is the fetch time
	// in the sidecar.
	age(t, dir, server.URL+"/price", -2*time.Hour)

	if hit := get(t, s, server.URL+"/price"); hit.Cached {
		t.Error("an entry older than the ttl was still a hit")
	}
	if server.hits.Load() != 2 {
		t.Errorf("the site was asked %d times", server.hits.Load())
	}
}

// TestNoTTLNeverExpires, which is what an archive wants.
func TestNoTTLNeverExpires(t *testing.T) {
	server := serve(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "hello")
	})

	dir := t.TempDir()
	s := stage(t, dir, "")

	get(t, s, server.URL+"/article")
	age(t, dir, server.URL+"/article", -5*365*24*time.Hour)

	if hit := get(t, s, server.URL+"/article"); !hit.Cached {
		t.Error("an entry with no ttl expired anyway")
	}
}

// TestABodyWithNoSidecarIsAMiss. An interrupted write leaves one of the two
// keys, and half an entry has to be refetched rather than half believed.
func TestABodyWithNoSidecarIsAMiss(t *testing.T) {
	server := serve(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "hello")
	})

	dir := t.TempDir()
	s := stage(t, dir, "")

	get(t, s, server.URL+"/article")
	remove(t, dir, cache.Key(server.URL+"/article")+".meta")

	if hit := get(t, s, server.URL+"/article"); hit.Cached {
		t.Error("a body with no sidecar was served as a hit")
	}
	if server.hits.Load() != 2 {
		t.Errorf("the site was asked %d times", server.hits.Load())
	}
}

// TestASidecarWithNoBodyIsAMiss, the other half of an interrupted write.
func TestASidecarWithNoBodyIsAMiss(t *testing.T) {
	server := serve(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "hello")
	})

	dir := t.TempDir()
	s := stage(t, dir, "")

	get(t, s, server.URL+"/article")
	remove(t, dir, cache.Key(server.URL+"/article"))

	hit := get(t, s, server.URL+"/article")
	if hit.Cached {
		t.Error("a sidecar with no body was served as a hit")
	}
	if string(hit.Body) != "hello" {
		t.Errorf("the refetch returned %q", hit.Body)
	}
}

// TestAnUnreadableSidecarIsAMiss, because a cache written by an older version
// is a thing that happens and refetching is always available.
func TestAnUnreadableSidecarIsAMiss(t *testing.T) {
	server := serve(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "hello")
	})

	dir := t.TempDir()
	s := stage(t, dir, "")

	get(t, s, server.URL+"/article")
	write(t, dir, cache.Key(server.URL+"/article")+".meta", []byte("not json"))

	if hit := get(t, s, server.URL+"/article"); hit.Cached {
		t.Error("an unreadable entry was served as a hit")
	}
}

// TestAFetchedPageIsNotLostToACacheThatWillNotWrite. The disk was only ever an
// optimisation, and the fetch has already been paid for.
func TestAFetchedPageIsNotLostToACacheThatWillNotWrite(t *testing.T) {
	server := serve(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "hello")
	})

	logged := capture(t)

	dir := t.TempDir()
	s := stage(t, dir, "")

	// A read-only cache directory is what a full disk or a revoked credential
	// looks like from here.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o750) })

	resp, err := s.Handle(context.Background(), &downloader.Request{URL: server.URL + "/article"})
	if err != nil {
		t.Fatalf("a cache that would not write failed the fetch: %v", err)
	}
	if string(resp.Body) != "hello" {
		t.Errorf("body = %q", resp.Body)
	}
	// Swallowed, but not silently: nothing else will ever mention it.
	if !strings.Contains(logged(), "cache write failed") {
		t.Errorf("a cache that would not write said nothing: %q", logged())
	}
}

// TestACacheThatWillNotReadRefetches, for the same reason.
func TestACacheThatWillNotReadRefetches(t *testing.T) {
	server := serve(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "hello")
	})

	logged := capture(t)

	dir := t.TempDir()
	s := stage(t, dir, "")

	get(t, s, server.URL+"/article")

	// Unreadable rather than absent, which is a permission problem and not a
	// miss.
	path := find(t, dir, cache.Key(server.URL+"/article")+".meta")
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { os.Chmod(path, 0o640) })

	resp, err := s.Handle(context.Background(), &downloader.Request{URL: server.URL + "/article"})
	if err != nil {
		t.Fatalf("an unreadable cache failed the fetch: %v", err)
	}
	if resp.Cached {
		t.Error("served a hit from an entry it could not read")
	}
	if string(resp.Body) != "hello" {
		t.Errorf("body = %q", resp.Body)
	}
	if !strings.Contains(logged(), "cache read failed") {
		t.Errorf("a cache that would not read said nothing: %q", logged())
	}
}

// Configuration.

// TestABadTTLIsRefusedWhenTheChainIsBuilt, rather than on the first request an
// hour into a run.
func TestABadTTLIsRefusedWhenTheChainIsBuilt(t *testing.T) {
	_, err := build(t, `      ttl = "whenever"`)
	if err == nil {
		t.Fatal("accepted a ttl that is not a duration")
	}
	if !strings.Contains(err.Error(), "ttl") {
		t.Errorf("the error does not say which setting: %v", err)
	}
}

// TestANegativeTTLIsRefused. It parses, and it would make every entry stale the
// moment it was written, which looks exactly like a cache that is not working.
func TestANegativeTTLIsRefused(t *testing.T) {
	if _, err := build(t, `      ttl = "-1h"`); err == nil {
		t.Fatal("accepted a negative ttl")
	}
}

func TestABackendNothingImplementsIsRefused(t *testing.T) {
	_, err := build(t, `      backend = "punchcards"`)
	if err == nil {
		t.Fatal("built a cache on a backend nothing implements")
	}
	if !strings.Contains(err.Error(), "punchcards") {
		t.Errorf("the error does not name it: %v", err)
	}
}

func TestAFieldTheCacheDoesNotKnowIsRefused(t *testing.T) {
	_, err := build(t, `      buckt = "typo"`)
	if err == nil {
		t.Fatal("a typo was silently ignored")
	}
	if !strings.Contains(err.Error(), "job.hcl") {
		t.Errorf("the error has no position: %v", err)
	}
}

// build returns what New says about a cache block, without the test helper's
// fatal on error.
func build(t *testing.T, settings string) (*downloader.Stage, error) {
	t.Helper()

	src := fmt.Sprintf(`
job "news" {
  start = ["https://example.com/"]

  item "article" {
    property "title" {
      type = str
    }
  }

  downloader {
    plugin "cache" {
      dir = %q
%s
    }
  }
}
`, t.TempDir(), settings)

	doc, err := engine.Parse([]byte(src), "job.hcl")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return downloader.New(context.Background(), doc.Jobs[0], downloader.Options{})
}

// Reading the cache from outside, which is how a test checks what is really
// there rather than what the middleware says is there.

func body(t *testing.T, dir, url string) []byte {
	t.Helper()

	raw, err := os.ReadFile(find(t, dir, cache.Key(url)))
	if err != nil {
		t.Fatalf("read cached body: %v", err)
	}
	return raw
}

func find(t *testing.T, dir, key string) string {
	t.Helper()

	var found string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && d.Name() == key {
			found = path
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	if found == "" {
		t.Fatalf("no cache entry named %s under %s", key, dir)
	}
	return found
}

func remove(t *testing.T, dir, key string) {
	t.Helper()

	if err := os.Remove(find(t, dir, key)); err != nil {
		t.Fatalf("remove: %v", err)
	}
}

func write(t *testing.T, dir, key string, content []byte) {
	t.Helper()

	if err := os.WriteFile(find(t, dir, key), content, 0o640); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// age rewrites an entry's fetch time, so a TTL can be tested without waiting
// for one.
func age(t *testing.T, dir, url string, by time.Duration) {
	t.Helper()

	path := find(t, dir, cache.Key(url)+".meta")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}

	var meta map[string]any
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatalf("the sidecar is not JSON: %v", err)
	}
	fetched, err := time.Parse(time.RFC3339Nano, meta["fetched"].(string))
	if err != nil {
		t.Fatalf("the sidecar's time is not a time: %v", err)
	}
	meta["fetched"] = fetched.Add(by).Format(time.RFC3339Nano)

	raw, err = json.Marshal(meta)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o640); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}
}

// TestAFetchThatFailedIsNotCached, because a connection refused is not evidence
// about a page and serving it back would make an outage permanent.
func TestAFetchThatFailedIsNotCached(t *testing.T) {
	server := serve(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "hello")
	})

	// A site whose robots.txt is fine and whose pages hang up, so the failure
	// happens at the fetch rather than before the chain is ever entered.
	rude := serve(t, func(w http.ResponseWriter, r *http.Request) {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		conn.Close()
	})

	dir := t.TempDir()
	s := stage(t, dir, "")

	if _, err := s.Handle(context.Background(), &downloader.Request{URL: rude.URL + "/article"}); err == nil {
		t.Fatal("a server that hung up produced a response")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read cache dir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("a failed fetch left %d entries in the cache", len(entries))
	}

	// And the site that does answer still works through the same chain.
	if got := string(get(t, s, server.URL+"/article").Body); got != "hello" {
		t.Errorf("body = %q", got)
	}
}

// TestABodyThatCannotBeReadIsAMiss, the other half of TestACacheThatWillNotRead:
// the sidecar is fine and the body is not.
func TestABodyThatCannotBeReadIsAMiss(t *testing.T) {
	logged := capture(t)

	server := serve(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "hello")
	})

	dir := t.TempDir()
	s := stage(t, dir, "")

	get(t, s, server.URL+"/article")

	path := find(t, dir, cache.Key(server.URL+"/article"))
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { os.Chmod(path, 0o640) })

	resp, err := s.Handle(context.Background(), &downloader.Request{URL: server.URL + "/article"})
	if err != nil {
		t.Fatalf("an unreadable body failed the fetch: %v", err)
	}
	if resp.Cached {
		t.Error("served a hit from a body it could not read")
	}
	if !strings.Contains(logged(), "cache read failed") {
		t.Errorf("an unreadable body said nothing: %q", logged())
	}
}

// TestTheJobsBackendChoiceReachesTheCache. The middleware settles nothing about
// where bodies live: it passes what the job wrote to [cache.New] and lets the
// backend read the fields that apply to it. A field dropped here would silently
// send a fleet's pages to the wrong bucket.
func TestTheJobsBackendChoiceReachesTheCache(t *testing.T) {
	var given cache.Config

	register(t, "test-recorder", func(ctx context.Context, cfg cache.Config) (cache.Store, error) {
		given = cfg
		return memory{}, nil
	})

	s := stage(t, t.TempDir(), `
      backend  = "test-recorder"
      bucket   = "pages"
      prefix   = "news/"
      region   = "eu-west-2"
      endpoint = "http://minio.example:9000"
      profile  = "archive"`)

	server := serve(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "hello")
	})

	get(t, s, server.URL+"/article")
	if hit := get(t, s, server.URL+"/article"); !hit.Cached {
		t.Error("a backend that is not the local one did not serve a hit")
	}

	if given.Backend != "test-recorder" || given.Bucket != "pages" || given.Prefix != "news/" {
		t.Errorf("the backend was given %+v", given)
	}
	if given.Region != "eu-west-2" || given.Endpoint != "http://minio.example:9000" || given.Profile != "archive" {
		t.Errorf("the cloud settings were dropped: %+v", given)
	}
}

// memory is a cache backend that keeps nothing but what this test put in it.
type memory map[string][]byte

func (m memory) Put(_ context.Context, key string, r io.Reader) error {
	body, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	m[key] = body
	return nil
}

func (m memory) Get(_ context.Context, key string) (io.ReadCloser, error) {
	body, ok := m[key]
	if !ok {
		return nil, cache.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(body)), nil
}

func (m memory) Has(_ context.Context, key string) (bool, error) {
	_, ok := m[key]
	return ok, nil
}

func (m memory) Delete(_ context.Context, key string) error {
	delete(m, key)
	return nil
}

func (m memory) Keys(context.Context) iter.Seq2[string, error] {
	return func(yield func(string, error) bool) {
		for key := range m {
			if !yield(key, nil) {
				return
			}
		}
	}
}

func (m memory) Close() error { return nil }

// TestACredentialTravelsAsACallAndArrivesAsAValue.
//
// The end of the chain the secret store starts: a job document holds
// secret("name"), the call is unevaluated everywhere it travels, and the
// backend is handed a credential. This is where those two halves meet, so it is
// where the claim is worth checking.
//
// The values here are obvious placeholders. A test carrying a real-looking key
// is a test somebody eventually copies into a document.
func TestACredentialTravelsAsACallAndArrivesAsAValue(t *testing.T) {
	var given cache.Config

	register(t, "test-credentials", func(ctx context.Context, cfg cache.Config) (cache.Store, error) {
		given = cfg
		return memory{}, nil
	})

	src := `
job "news" {
  start = ["https://example.com/"]

  item "article" {
    property "title" {
      type = str
    }
  }

  downloader {
    plugin "cache" {
      backend    = "test-credentials"
      bucket     = "pages"
      access_key = secret("acme-key")
      secret_key = secret("acme-secret")
    }
  }
}
`
	doc, err := engine.Parse([]byte(src), "job.hcl")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := doc.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}

	// The document parses and validates with the calls unevaluated: nothing
	// before the plugin is built has any business resolving them.
	stored := src
	for _, leaked := range []string{"PLACEHOLDER-ACCESS-KEY", "PLACEHOLDER-SECRET-KEY"} {
		if strings.Contains(stored, leaked) {
			t.Fatal("the fixture itself carries a value, which is not the case under test")
		}
	}

	eval := secret.Eval(context.Background(), fakeSecrets{
		"acme-key":    "PLACEHOLDER-ACCESS-KEY",
		"acme-secret": "PLACEHOLDER-SECRET-KEY",
	})

	stage, err := downloader.New(context.Background(), doc.Jobs[0], downloader.Options{Eval: eval})
	if err != nil {
		t.Fatalf("new downloader: %v", err)
	}
	defer stage.Close()

	if given.AccessKey != "PLACEHOLDER-ACCESS-KEY" || given.SecretKey != "PLACEHOLDER-SECRET-KEY" {
		t.Errorf("the backend was given %v", given)
	}
	if !given.Secret() {
		t.Error("the config does not report that it carries a credential")
	}
}

// TestWithoutAWayToResolveOneTheChainIsRefused, rather than the backend being
// handed an empty credential and failing later with a message about
// authentication.
func TestWithoutAWayToResolveOneTheChainIsRefused(t *testing.T) {
	src := `
job "news" {
  start = ["https://example.com/"]

  item "article" {
    property "title" {
      type = str
    }
  }

  downloader {
    plugin "cache" {
      bucket     = "pages"
      access_key = secret("acme-key")
    }
  }
}
`
	doc, err := engine.Parse([]byte(src), "job.hcl")
	if err != nil {
		t.Fatal(err)
	}

	_, err = downloader.New(context.Background(), doc.Jobs[0],
		downloader.Options{Eval: secret.Missing(context.Background())})
	if err == nil {
		t.Fatal("a node with no secrets built a cache that wanted one")
	}
	if !strings.Contains(err.Error(), "acme-key") {
		t.Errorf("the error does not say which secret: %v", err)
	}
}

// fakeSecrets answers without a cluster, which is what [secret.Resolver] is an
// interface for.
type fakeSecrets map[string]string

func (f fakeSecrets) Resolve(_ context.Context, name string) ([]byte, error) {
	value, ok := f[name]
	if !ok {
		return nil, fmt.Errorf("no such secret %q", name)
	}
	return []byte(value), nil
}

// register puts a cache backend in the global table for the length of one test.
//
// Every test that needs one of its own goes through this rather than calling
// [cache.Register] directly, because the table is global and registering the
// same name twice panics: a test that registered without removing made this
// whole package impossible to run under `go test -count=2` or, once shuffling
// reordered it, under `-shuffle=on` either. Running the suite repeatedly is how
// a flaky test is found, so a package that cannot be is a package whose
// flakiness nobody will see. The gate runs -count=2 for that reason, which is
// what makes the next test that forgets fail the build rather than ship.
func register(t *testing.T, name string, f cache.Factory) {
	t.Helper()
	cache.Register(name, f)
	t.Cleanup(func() { cache.Unregister(name) })
}
