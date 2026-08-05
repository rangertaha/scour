// SPDX-License-Identifier: GPL-3.0-or-later

package bus_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rangertaha/scour/internal/bus"
	"github.com/rangertaha/scour/internal/cache"
	"github.com/rangertaha/scour/internal/cache/local"
	"github.com/rangertaha/scour/internal/chain"
	"github.com/rangertaha/scour/internal/downloader"
	"github.com/rangertaha/scour/internal/engine"
	"github.com/rangertaha/scour/internal/frontier"
	"github.com/rangertaha/scour/internal/frontier/sqlite"
	"github.com/rangertaha/scour/internal/record"
	"github.com/rangertaha/scour/internal/run"
	"github.com/rangertaha/scour/internal/spider"

	_ "github.com/rangertaha/scour/internal/pipeline/steps"
	_ "github.com/rangertaha/scour/internal/scheduler/dupefilter"
	_ "github.com/rangertaha/scour/internal/spider/httperror"
)

// site is a small linked site, the same shape the run loop is tested against.
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
			  <a href="/story/1">one</a><a href="/story/2">two</a>
			</body></html>`)
		default:
			fmt.Fprintf(w, `<html><head><meta property="og:title" content="Story %s"></head>
			  <body><a href="/">home</a></body></html>`, r.URL.Path[len("/story/"):])
		}
	}))
	t.Cleanup(server.Close)
	return server, &hits
}

func job(t *testing.T, server *httptest.Server) *engine.Job {
	t.Helper()

	src := fmt.Sprintf(`
job "news" {
  domains = ["%s"]
  start   = ["%s/"]

  item "article" {
    property "title" {
      type     = str
      required = true
    }
  }

  scheduler {
    rate = "1ms"
  }
}
`, strings.TrimPrefix(server.URL, "http://"), server.URL)

	doc, err := engine.Parse([]byte(src), "job.hcl")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := doc.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	return doc.Jobs[0]
}

func connect(t *testing.T) *bus.Conn {
	t.Helper()

	conn, err := bus.Connect(bus.Options{Name: "test"})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func bodies(t *testing.T) cache.Store {
	t.Helper()

	store, err := local.Open(t.TempDir())
	if err != nil {
		t.Fatalf("cache: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

// crawl runs a job, either wired directly or through the bus, and returns the
// records it produced in a stable order.
func crawl(t *testing.T, j *engine.Job, opts run.Options) []*record.Record {
	t.Helper()

	if opts.Open == nil {
		opts.Open = func(cfg frontier.Config) (frontier.Frontier, error) { return sqlite.Open(cfg) }
	}
	if opts.Dir == "" {
		opts.Dir = t.TempDir()
	}

	r, err := run.New(context.Background(), j, opts)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer r.Close()

	if _, err := r.Seed(context.Background()); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := r.Do(context.Background()); err != nil {
		t.Fatalf("do: %v", err)
	}
	return sorted(r.Records())
}

func sorted(records []*record.Record) []*record.Record {
	out := append([]*record.Record(nil), records...)
	sort.Slice(out, func(i, j int) bool { return out[i].URL < out[j].URL })
	return out
}

// TestTheSameJobProducesTheSameRecordsEitherWay.
//
// The whole claim of the bus. If it holds, a stage can be somewhere else and a
// spider somebody wrote in another language is a spider like any other. If it
// does not, the bus is a second implementation of the crawler with its own
// bugs, and every one of them would show up as a difference in the output
// rather than as a failure anybody could see.
func TestTheSameJobProducesTheSameRecordsEitherWay(t *testing.T) {
	server, hits := site(t)
	j := job(t, server)

	direct := crawl(t, j, run.Options{})
	if len(direct) == 0 {
		t.Fatal("the direct run found nothing, so there is nothing to compare")
	}
	askedDirectly := hits.Load()

	// The same stages, served over the bus, with the run loop told nothing
	// about where they are.
	conn := connect(t)
	store := bodies(t)
	ctx := context.Background()

	fetcher, err := downloader.New(ctx, j, downloader.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer fetcher.Close()

	reader, err := spider.New(ctx, j, spider.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()

	if _, err := conn.ServeDownloader(ctx, j.Name, fetcher, store); err != nil {
		t.Fatalf("serve downloader: %v", err)
	}
	if _, err := conn.ServeSpider(ctx, j.Name, reader, store); err != nil {
		t.Fatalf("serve spider: %v", err)
	}

	overBus := crawl(t, j, run.Options{
		Fetch: conn.NewDownloader(j.Name, store, 0),
		Read:  conn.NewSpider(j.Name, store, 0),
	})

	if len(overBus) != len(direct) {
		t.Fatalf("direct produced %d records and the bus produced %d", len(direct), len(overBus))
	}
	for i := range direct {
		if direct[i].URL != overBus[i].URL {
			t.Errorf("record %d: %s directly, %s over the bus", i, direct[i].URL, overBus[i].URL)
			continue
		}
		if direct[i].Spec != overBus[i].Spec {
			t.Errorf("%s: read under %s directly and %s over the bus",
				direct[i].URL, direct[i].Spec, overBus[i].Spec)
		}
		for name, want := range direct[i].Values {
			if got := overBus[i].Get(name); got != want {
				t.Errorf("%s: %s = %q directly and %q over the bus", direct[i].URL, name, want, got)
			}
		}
		if len(direct[i].Values) != len(overBus[i].Values) {
			t.Errorf("%s: %d values directly and %d over the bus",
				direct[i].URL, len(direct[i].Values), len(overBus[i].Values))
		}
	}

	if got := hits.Load() - askedDirectly; got != askedDirectly {
		t.Errorf("the site was asked %d times over the bus and %d directly", got, askedDirectly)
	}
}

// TestABodyNeverCrossesTheBus. The message carries a key and the reader fetches
// it, which is what keeps a megabyte of HTML off the wire at any real rate.
func TestABodyNeverCrossesTheBus(t *testing.T) {
	server, _ := site(t)
	j := job(t, server)
	ctx := context.Background()

	conn := connect(t)
	store := bodies(t)

	fetcher, err := downloader.New(ctx, j, downloader.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer fetcher.Close()

	if _, err := conn.ServeDownloader(ctx, j.Name, fetcher, store); err != nil {
		t.Fatal(err)
	}

	client := conn.NewDownloader(j.Name, store, 0)
	resp, err := client.Handle(ctx, &downloader.Request{URL: server.URL + "/story/1", Job: j.Name})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if !strings.Contains(string(resp.Body), "Story 1") {
		t.Errorf("the body did not arrive: %q", resp.Body)
	}

	// The body is in the cache, which is where it came from.
	stored, err := cache.GetBytes(ctx, store, cache.Key(server.URL+"/story/1"))
	if err != nil {
		t.Fatalf("the body is not in the cache: %v", err)
	}
	if string(stored) != string(resp.Body) {
		t.Error("what the client returned is not what the cache holds")
	}
}

// TestADropTravelsAsADrop. A refusal is an ordinary outcome and a caller has to
// be able to tell it from a stage that broke, on the other side of a wire that
// only carries bytes.
func TestADropTravelsAsADrop(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			fmt.Fprint(w, "User-agent: *\nDisallow: /\n")
			return
		}
		fmt.Fprint(w, "<html><head><title>Secret</title></head><body></body></html>")
	}))
	defer server.Close()

	j := job(t, server)
	ctx := context.Background()

	conn := connect(t)
	store := bodies(t)

	fetcher, err := downloader.New(ctx, j, downloader.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer fetcher.Close()

	if _, err := conn.ServeDownloader(ctx, j.Name, fetcher, store); err != nil {
		t.Fatal(err)
	}

	_, err = conn.NewDownloader(j.Name, store, 0).Handle(ctx, &downloader.Request{URL: server.URL + "/a"})
	if !chain.Dropped(err) {
		t.Fatalf("err = %v, want a drop", err)
	}
	if !strings.Contains(err.Error(), "robots") {
		t.Errorf("the reason was lost: %v", err)
	}
}

// TestNothingServingIsNotATimeout. A stage that is not there is a
// misconfiguration and an operator should find out immediately.
func TestNothingServingIsNotATimeout(t *testing.T) {
	conn := connect(t)
	store := bodies(t)

	started := time.Now()
	_, err := conn.NewDownloader("nobody-serves-this", store, 0).Handle(
		context.Background(), &downloader.Request{URL: "https://example.com/a"})

	if !errors.Is(err, bus.ErrNoStage) {
		t.Errorf("err = %v, want ErrNoStage", err)
	}
	if elapsed := time.Since(started); elapsed > 10*time.Second {
		t.Errorf("waited %s to find out nothing was listening", elapsed)
	}
}

// TestTwoWorkersShareTheWork, which is the whole of how a cluster distributes
// it: one queue group, and NATS hands each request to one member.
func TestTwoWorkersShareTheWork(t *testing.T) {
	server, _ := site(t)
	j := job(t, server)
	ctx := context.Background()

	conn := connect(t)
	store := bodies(t)

	var first, second atomic.Int32
	for _, counter := range []*atomic.Int32{&first, &second} {
		fetcher, err := downloader.New(ctx, j, downloader.Options{})
		if err != nil {
			t.Fatal(err)
		}
		defer fetcher.Close()

		if _, err := conn.ServeDownloader(ctx, j.Name, countingStage{fetcher, counter}, store); err != nil {
			t.Fatal(err)
		}
	}

	client := conn.NewDownloader(j.Name, store, 0)
	for i := range 20 {
		if _, err := client.Handle(ctx, &downloader.Request{
			URL: fmt.Sprintf("%s/story/%d", server.URL, i%2+1),
		}); err != nil {
			t.Fatalf("fetch: %v", err)
		}
	}

	if first.Load() == 0 || second.Load() == 0 {
		t.Errorf("one worker did all of it: %d and %d", first.Load(), second.Load())
	}
	if first.Load()+second.Load() != 20 {
		t.Errorf("%d requests were served for 20 made", first.Load()+second.Load())
	}
}

func TestAnEmbeddedServerNeedsNothingInstalled(t *testing.T) {
	conn := connect(t)

	if !conn.Embedded() {
		t.Error("a connection with no address did not start a server")
	}
	if !strings.HasPrefix(conn.Address(), "nats://") {
		t.Errorf("address = %q", conn.Address())
	}
}

func TestConnectingToNothingFails(t *testing.T) {
	if _, err := bus.Connect(bus.Options{URL: "nats://127.0.0.1:1"}); err == nil {
		t.Error("connected to a port nothing is listening on")
	}
}

// countingStage wraps a downloader and counts what it was asked, which is how
// a test tells one worker from another when NATS is the thing choosing.
type countingStage struct {
	inner downloader.Handler
	count *atomic.Int32
}

func (c countingStage) Handle(ctx context.Context, req *downloader.Request) (*downloader.Response, error) {
	c.count.Add(1)
	return c.inner.Handle(ctx, req)
}

func TestSubjectsAndQueues(t *testing.T) {
	if got := bus.Subject("news", bus.DownloadSubject); got != "scour.news.download" {
		t.Errorf("subject = %q", got)
	}
	if got := bus.Queue("news", bus.ReadSubject); got != "scour-news-read" {
		t.Errorf("queue = %q", got)
	}
}

// TestAnAddressToJoinIsOneThatWorks. A server listening on every interface
// reports 0.0.0.0, which is a thing to listen on and not a thing to connect to.
// Printing it as the address to join prints an address that does not work.
func TestAnAddressToJoinIsOneThatWorks(t *testing.T) {
	conn := connect(t)

	address := conn.Address()
	if strings.Contains(address, "0.0.0.0") || strings.Contains(address, "[::]") {
		t.Fatalf("address = %q, which nothing can connect to", address)
	}

	// And it does connect.
	joined, err := bus.Connect(bus.Options{URL: address, Name: "joiner"})
	if err != nil {
		t.Fatalf("the printed address does not work: %v", err)
	}
	joined.Close()
}
