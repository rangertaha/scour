// SPDX-License-Identifier: GPL-3.0-or-later

package node_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rangertaha/scour/internal/bus"
	"github.com/rangertaha/scour/internal/cache"
	"github.com/rangertaha/scour/internal/cache/local"
	"github.com/rangertaha/scour/internal/downloader"
	"github.com/rangertaha/scour/internal/engine"
	"github.com/rangertaha/scour/internal/frontier"
	"github.com/rangertaha/scour/internal/frontier/sqlite"
	"github.com/rangertaha/scour/internal/node"
	"github.com/rangertaha/scour/internal/run"

	_ "github.com/rangertaha/scour/internal/pipeline/steps"
	_ "github.com/rangertaha/scour/internal/scheduler/dupefilter"
	_ "github.com/rangertaha/scour/internal/spider/httperror"
)

// site is a handful of pages, each of which records which node fetched it.
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

		if r.URL.Path == "/" {
			fmt.Fprint(w, `<html><head><title>Index</title></head><body>
			  <a href="/a">a</a><a href="/b">b</a><a href="/c">c</a>
			  <a href="/d">d</a><a href="/e">e</a><a href="/f">f</a>
			</body></html>`)
			return
		}
		fmt.Fprintf(w, `<html><head><meta property="og:title" content="Page %s"></head><body></body></html>`,
			strings.TrimPrefix(r.URL.Path, "/"))
	}))
	t.Cleanup(server.Close)
	return server, &hits
}

func document(server *httptest.Server) string {
	return fmt.Sprintf(`
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
    rate        = "1ms"
    concurrency = 4
  }
}
`, strings.TrimPrefix(server.URL, "http://"), server.URL)
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

// counting wraps a downloader and records that this node did the work.
type counting struct {
	inner downloader.Handler
	count *atomic.Int32
}

func (c counting) Handle(ctx context.Context, req *downloader.Request) (*downloader.Response, error) {
	c.count.Add(1)
	return c.inner.Handle(ctx, req)
}

// TestTwoNodesOneJobWorkOnBoth.
//
// What a cluster has to do, and the only claim Phase 7 makes. Nothing here
// elects anything or assigns anything: both nodes serve the same job's
// downloader, they join one queue group, and NATS hands each request to one of
// them.
func TestTwoNodesOneJobWorkOnBoth(t *testing.T) {
	server, hits := site(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// The first node also runs the server, which is what a laptop does.
	first, err := bus.Connect(bus.Options{Name: "first", StoreDir: t.TempDir()})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer first.Close()

	// The second joins it with an address and nothing else.
	second, err := bus.Connect(bus.Options{Name: "second", URL: first.Address()})
	if err != nil {
		t.Fatalf("join: %v", err)
	}
	defer second.Close()

	// One cache, because a body never crosses the bus: the node that fetched
	// it puts it there and the node that reads it takes it out.
	shared := bodies(t)

	store, err := first.OpenJobs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put(ctx, "news", []byte(document(server))); err != nil {
		t.Fatal(err)
	}

	var workedFirst, workedSecond atomic.Int32
	nodes := []struct {
		conn  *bus.Conn
		name  string
		count *atomic.Int32
	}{
		{first, "first", &workedFirst},
		{second, "second", &workedSecond},
	}

	for _, each := range nodes {
		// The node serves the spider from its watch; the downloader is
		// registered here so the test can count which node served what. Two
		// subscriptions for one stage would both be in the queue group and the
		// counts would mean nothing.
		joined, err := node.Join(ctx, each.conn, node.Options{
			Name:   each.name,
			Serve:  []string{node.StageRead},
			Bodies: shared,
		})
		if err != nil {
			t.Fatalf("join %s: %v", each.name, err)
		}
		defer joined.Close()

		go joined.Watch(ctx)

		// The node's own stages are what the run loop will reach, and each is
		// wrapped so the test can tell which node served a request.
		fetcher, err := downloader.New(ctx, mustJob(t, store), downloader.Options{})
		if err != nil {
			t.Fatal(err)
		}
		defer fetcher.Close()

		if _, err := each.conn.ServeDownloader(ctx, "news", counting{fetcher, each.count}, shared, nil); err != nil {
			t.Fatal(err)
		}
	}

	// Give the nodes a moment to pick the job up, which is what a watch costs.
	waitFor(t, func() bool {
		return len(namesOf(store, ctx)) > 0
	})

	// One node drives the crawl, because the frontier cannot be shared: two
	// schedulers handing out one host cannot honour a crawl delay between them.
	job := mustJob(t, store)
	crawl, err := run.New(ctx, job, run.Options{
		Dir:   t.TempDir(),
		Open:  func(cfg frontier.Config) (frontier.Frontier, error) { return sqlite.Open(cfg) },
		Fetch: first.NewDownloader("news", shared, 0),
		Read:  first.NewSpider("news", shared, 0),
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	defer crawl.Close()

	if _, err := crawl.Seed(ctx); err != nil {
		t.Fatal(err)
	}
	ending, err := crawl.Do(ctx)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	if ending != run.Finished {
		t.Errorf("ending = %q", ending)
	}

	if got := crawl.Stats().Fetched.Load(); got != 7 {
		t.Errorf("fetched %d pages, want the index and six below it", got)
	}
	if hits.Load() != 7 {
		t.Errorf("the site was asked %d times", hits.Load())
	}
	if got := crawl.Stats().Items.Load(); got != 7 {
		t.Errorf("extracted %d items", got)
	}

	// The point of the whole arrangement.
	if workedFirst.Load() == 0 || workedSecond.Load() == 0 {
		t.Errorf("one node did all of it: first %d, second %d",
			workedFirst.Load(), workedSecond.Load())
	}
	t.Logf("first served %d, second served %d", workedFirst.Load(), workedSecond.Load())
}

// TestANodePicksUpAJobItWasNeverToldAbout, which is what makes adding a machine
// to a cluster a matter of starting it.
func TestANodePicksUpAJobItWasNeverToldAbout(t *testing.T) {
	server, _ := site(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn, err := bus.Connect(bus.Options{Name: "one", StoreDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	joined, err := node.Join(ctx, conn, node.Options{Name: "worker", Bodies: bodies(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer joined.Close()
	go joined.Watch(ctx)

	// The job arrives after the node did.
	store, err := conn.OpenJobs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put(ctx, "news", []byte(document(server))); err != nil {
		t.Fatal(err)
	}

	waitFor(t, func() bool { return len(joined.Serving()) == 1 })

	if got := joined.Serving(); len(got) != 1 || got[0] != "news" {
		t.Errorf("serving %v", got)
	}
	revision, ok := joined.Revision("news")
	if !ok || revision == 0 {
		t.Errorf("revision = %d, %v", revision, ok)
	}

	// And it lets go when the job does.
	if err := store.Delete(ctx, "news"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(joined.Serving()) == 0 })
}

// TestAResubmissionIsPickedUp, and the node says which revision it is on.
func TestAResubmissionIsPickedUp(t *testing.T) {
	server, _ := site(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn, err := bus.Connect(bus.Options{Name: "one", StoreDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	store, err := conn.OpenJobs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put(ctx, "news", []byte(document(server))); err != nil {
		t.Fatal(err)
	}

	joined, err := node.Join(ctx, conn, node.Options{Name: "worker", Bodies: bodies(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer joined.Close()
	go joined.Watch(ctx)

	waitFor(t, func() bool { return len(joined.Serving()) == 1 })
	first, _ := joined.Revision("news")

	changed := strings.Replace(document(server), `rate        = "1ms"`, `rate        = "2ms"`, 1)
	if _, err := store.Put(ctx, "news", []byte(changed)); err != nil {
		t.Fatal(err)
	}

	waitFor(t, func() bool {
		revision, ok := joined.Revision("news")
		return ok && revision > first
	})
}

// TestAJobThatWillNotBuildDoesNotStopTheNode. The rest of the cluster may be
// able to serve it, and the next submission may fix it.
func TestAJobThatWillNotBuildDoesNotStopTheNode(t *testing.T) {
	server, _ := site(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn, err := bus.Connect(bus.Options{Name: "one", StoreDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	store, err := conn.OpenJobs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put(ctx, "broken", []byte(`job "broken" {}`)); err != nil {
		t.Fatal(err)
	}

	joined, err := node.Join(ctx, conn, node.Options{Name: "worker", Bodies: bodies(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer joined.Close()
	go joined.Watch(ctx)

	// A good job after the bad one, and the node is still there to take it.
	if _, err := store.Put(ctx, "news", []byte(document(server))); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(joined.Serving()) == 1 })

	if got := joined.Serving(); len(got) != 1 || got[0] != "news" {
		t.Errorf("serving %v; a job that would not build took the node with it", got)
	}
}

func TestANodeNeedsANameAndACache(t *testing.T) {
	ctx := context.Background()

	conn, err := bus.Connect(bus.Options{Name: "one", StoreDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	if _, err := node.Join(ctx, nil, node.Options{Name: "x", Bodies: bodies(t)}); err == nil {
		t.Error("joined nothing")
	}
	if _, err := node.Join(ctx, conn, node.Options{Bodies: bodies(t)}); err == nil {
		t.Error("joined without a name")
	}
	if _, err := node.Join(ctx, conn, node.Options{Name: "x"}); err == nil {
		t.Error("joined without a cache, and a body never crosses the bus")
	}
}

func mustJob(t *testing.T, store *bus.Jobs) *engine.Job {
	t.Helper()

	job, _, err := store.Job(context.Background(), "news")
	if err != nil {
		t.Fatalf("job: %v", err)
	}
	return job
}

func namesOf(store *bus.Jobs, ctx context.Context) []string {
	names, _ := store.Names(ctx)
	return names
}

// waitFor polls until something is true, because a watch is asynchronous and a
// fixed sleep is either slow or flaky.
func waitFor(t *testing.T, done func() bool) {
	t.Helper()

	for range 200 {
		if done() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("timed out waiting")
}

// TestAClosedNodeStopsSayingItIsHere.
//
// The presence renewal ended only with the context the caller happened to pass
// in, which for a node is the one its whole run uses. A node that had been
// closed, with its stages torn down and answering nothing, went on rewriting
// its row every ten seconds for as long as that context lived — so the registry
// listed a node with its stages that nobody could get an answer from.
func TestAClosedNodeStopsSayingItIsHere(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn, err := bus.Connect(bus.Options{Name: "one", StoreDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	joined, err := node.Join(ctx, conn, node.Options{Name: "worker", Bodies: bodies(t)})
	if err != nil {
		t.Fatal(err)
	}

	nodes, err := conn.OpenNodes(ctx)
	if err != nil {
		t.Fatal(err)
	}

	here, err := nodes.Here(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, listed := here["worker"]; !listed {
		t.Fatal("a node that joined is not listed")
	}

	if err := joined.Close(); err != nil {
		t.Fatal(err)
	}

	// Gone at once rather than left to expire, which is what the announcement
	// already promised for a cancelled context and did not do for Close.
	here, err = nodes.Here(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, listed := here["worker"]; listed {
		t.Error("a closed node is still listed as here, with stages it no longer serves")
	}
}

// TestAClosedNodeTakesOnNothingFurther.
//
// serve releases the lock to build its stages and subscribe, which takes as
// long as opening a cache and a database. A Close in that window swapped in an
// empty map and returned having stopped nothing for the job, and serve then put
// its subscriptions into the fresh map where nothing would ever close them: the
// node answered for a job after Close returned, and a second Close could not
// help, having no handle on them.
func TestAClosedNodeTakesOnNothingFurther(t *testing.T) {
	server, _ := site(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	conn, err := bus.Connect(bus.Options{Name: "one", StoreDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	joined, err := node.Join(ctx, conn, node.Options{Name: "worker", Bodies: bodies(t)})
	if err != nil {
		t.Fatal(err)
	}
	go joined.Watch(ctx)

	if err := joined.Close(); err != nil {
		t.Fatal(err)
	}

	// A job arriving after Close is not taken on, however the watch happens to
	// be scheduled.
	store, err := conn.OpenJobs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put(ctx, "news", []byte(document(server))); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if serving := joined.Serving(); len(serving) != 0 {
			t.Fatalf("a closed node took on %v", serving)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
