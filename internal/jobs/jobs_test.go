// SPDX-License-Identifier: GPL-3.0-or-later

package jobs_test

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rangertaha/scour/internal/bus"
	"github.com/rangertaha/scour/internal/cache"
	"github.com/rangertaha/scour/internal/cache/local"
	"github.com/rangertaha/scour/internal/jobs"
	"github.com/rangertaha/scour/internal/node"

	_ "github.com/rangertaha/scour/internal/pipeline/steps"
	_ "github.com/rangertaha/scour/internal/scheduler/dupefilter"
	_ "github.com/rangertaha/scour/internal/spider/httperror"
)

// The manager is the half of the cluster that was missing: something that owns
// a job and drives its crawl. What these check is that it does both, and that
// the two agree. A control plane that reports a phase the crawl is not in is
// worse than none, because the phase is what everything else acts on.

// site is a small site whose pages link to each other.
func site(t *testing.T) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")

		if r.URL.Path == "/" {
			fmt.Fprint(w, `<html><head><title>Index</title></head><body>
			  <a href="/a">a</a><a href="/b">b</a><a href="/c">c</a>
			</body></html>`)
			return
		}
		fmt.Fprintf(w,
			`<html><head><meta property="og:title" content="Page %s"></head><body></body></html>`,
			strings.TrimPrefix(r.URL.Path, "/"))
	}))
	t.Cleanup(server.Close)
	return server
}

func document(server *httptest.Server, name string) []byte {
	return fmt.Appendf(nil, `
job %q {
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
`, name, strings.TrimPrefix(server.URL, "http://"), server.URL)
}

func bodies(t *testing.T) cache.Store {
	t.Helper()

	store, err := local.Open(t.TempDir())
	if err != nil {
		t.Fatalf("cache: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// cluster is a broker, a node serving both stages, and a manager driving.
//
// The whole arrangement, because the manager alone cannot crawl anything: it
// asks for every fetch and every read over the bus, which is the point of it.
// A test that stubbed the stages would be testing the bookkeeping and not the
// claim.
func cluster(t *testing.T) (*jobs.Manager, *bus.Conn) {
	t.Helper()

	manager, conn, _ := clusterIn(t)
	return manager, conn
}

// clusterIn is [cluster] and also says where the manager keeps its frontiers,
// for the tests that care what it leaves on disk.
func clusterIn(t *testing.T) (*jobs.Manager, *bus.Conn, string) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	conn, err := bus.Connect(bus.Options{Name: "test", StoreDir: t.TempDir()})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	// One cache for both, because a body never crosses the bus: the stage that
	// fetched it puts it there and the driver takes it out.
	shared := bodies(t)

	// One name for the node and the manager, which is what `scour server`
	// does: it passes a single --name to both. The manager records that name
	// as the job's driver, and another manager asking whether that driver is
	// still in the cluster looks it up in the node registry. Two names here
	// would make every such lookup answer "gone", and the check would pass
	// while proving nothing.
	joined, err := node.Join(ctx, conn, node.Options{Name: "test", Bodies: shared})
	if err != nil {
		t.Fatalf("join: %v", err)
	}
	t.Cleanup(func() { _ = joined.Close() })

	go func() { _ = joined.Watch(ctx) }()

	dir := t.TempDir()
	manager, err := jobs.New(ctx, conn, jobs.Options{
		Dir:    dir,
		Bodies: shared,
		Name:   "test",
	})
	if err != nil {
		t.Fatalf("manager: %v", err)
	}
	t.Cleanup(func() { _ = manager.Close() })

	return manager, conn, dir
}

// waitFor polls until a job reaches one of the phases, or gives up.
//
// Polled rather than slept on, because how long a crawl of four pages takes is
// a property of the machine and a sleep long enough to be safe is a test that
// is slow on every machine.
func waitFor(t *testing.T, m *jobs.Manager, name string, want ...bus.Phase) bus.JobStatus {
	t.Helper()

	ctx := context.Background()
	deadline := time.Now().Add(30 * time.Second)
	var last bus.JobStatus

	for time.Now().Before(deadline) {
		status, err := m.Status(ctx, name)
		if err != nil {
			t.Fatalf("status of %q: %v", name, err)
		}
		last = status
		for _, phase := range want {
			if status.State.Phase == phase {
				return status
			}
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("%q never reached %v, and is %s (%s)", name, want, last.State.Phase, last.State.Error)
	return last
}

// TestAJobIsCreatedStartedAndFinishes is the whole claim in one test.
//
// A document goes in, a crawl happens on the cluster, and the phase at the end
// says it finished rather than that it was stopped or that it failed. Every
// page is fetched by the node and read by the node; the manager owns only the
// order.
func TestAJobIsCreatedStartedAndFinishes(t *testing.T) {
	manager, _ := cluster(t)
	ctx := context.Background()
	server := site(t)

	status, err := manager.Create(ctx, document(server, "news"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if status.State.Phase != bus.PhaseStopped {
		t.Errorf("a created job is %s, want stopped: creating is not starting", status.State.Phase)
	}
	if status.Revision == 0 {
		t.Error("a created job has no revision")
	}

	if _, err := manager.Start(ctx, "news", false); err != nil {
		t.Fatalf("start: %v", err)
	}

	done := waitFor(t, manager, "news", bus.PhaseDone, bus.PhaseFailed)
	if done.State.Phase != bus.PhaseDone {
		t.Fatalf("the crawl %s: %s", done.State.Phase, done.State.Error)
	}
	if done.State.Ending != "finished" {
		t.Errorf("it ended %q, want finished: the frontier should have run dry", done.State.Ending)
	}

	stats, err := manager.Stats(ctx, "news")
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats.Fetched < 4 {
		t.Errorf("fetched %d, want the index and its three links", stats.Fetched)
	}
	if stats.Items < 3 {
		t.Errorf("extracted %d items, want one per page below the index", stats.Items)
	}
}

// TestCreateRefusesANameAlreadyTaken.
//
// Overwriting somebody else's job by picking their name is not something to do
// silently, and the message has to say what to do instead: a create that
// replaced would be discovered by the person whose crawl changed shape.
func TestCreateRefusesANameAlreadyTaken(t *testing.T) {
	manager, _ := cluster(t)
	ctx := context.Background()
	server := site(t)

	if _, err := manager.Create(ctx, document(server, "news")); err != nil {
		t.Fatalf("create: %v", err)
	}

	_, err := manager.Create(ctx, document(server, "news"))
	if err == nil {
		t.Fatal("creating a job twice was allowed")
	}
	if !strings.Contains(err.Error(), "update") {
		t.Errorf("the refusal does not say to use update: %v", err)
	}
}

// TestOnlyOneCreateWinsWhenTheyRace.
//
// Create read the store to see whether the name was free and wrote if it was,
// which is two operations with a gap between them. Eight simultaneous creates
// of one name all found nothing, all wrote, and the last silently replaced the
// rest.
//
// The read is also what sends an operator to Update, and Update is what reviews
// a change against a job that is already running, so a create landing in that
// gap replaced a running job's document with no review at all. Update has
// always used compare-and-swap for this reason; Create now lets the store
// decide too.
func TestOnlyOneCreateWinsWhenTheyRace(t *testing.T) {
	manager, _ := cluster(t)
	ctx := context.Background()
	server := site(t)

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		won     int
		refused int
	)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := manager.Create(ctx, document(server, "news"))

			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				won++
			// Either message: a loser that reached the store is told the name
			// is taken, and one refused at the claim is told another request
			// has it. Both are true and both leave exactly one job.
			case strings.Contains(err.Error(), "already exists"),
				strings.Contains(err.Error(), "is busy"):
				refused++
			default:
				t.Errorf("create failed for another reason: %v", err)
			}
		}()
	}
	wg.Wait()

	if won != 1 {
		t.Errorf("%d of 8 racing creates won, want exactly 1: the rest overwrote a job that existed", won)
	}
	if refused != 7 {
		t.Errorf("%d were refused, want 7: the rest overwrote a job that existed", refused)
	}
}

// TestUpdateRefusesAJobThatIsNotThere is the other half.
//
// The two commands exist to be different: one refuses a name that is taken and
// the other refuses one that is free, so a script cannot create where it meant
// to change or change where it meant to create.
func TestUpdateRefusesAJobThatIsNotThere(t *testing.T) {
	manager, _ := cluster(t)
	server := site(t)

	_, err := manager.Update(context.Background(), document(server, "news"))
	if err == nil {
		t.Fatal("updating a job nobody submitted was allowed")
	}
	if !strings.Contains(err.Error(), "create") {
		t.Errorf("the refusal does not say to use create: %v", err)
	}
}

// TestASubmissionIsOneJob.
//
// The bucket's key is the job's name, so a document holding two jobs stored
// under one of them is a job the cluster serves and a job it silently does not.
// Refused by name, with the names listed.
func TestASubmissionIsOneJob(t *testing.T) {
	manager, _ := cluster(t)
	server := site(t)

	two := append(document(server, "news"), document(server, "sport")...)
	_, err := manager.Create(context.Background(), two)
	if err == nil {
		t.Fatal("a document holding two jobs was accepted")
	}
	if !strings.Contains(err.Error(), "news") || !strings.Contains(err.Error(), "sport") {
		t.Errorf("the refusal does not name the jobs, so there is nothing to act on: %v", err)
	}
}

// TestPauseKeepsTheFrontierAndResumeCarriesOn.
//
// The difference between pause and stop is the recorded intention, and this is
// what that intention is for: resume carries on, and a job that was stopped is
// started instead. A pause that lost the queue would make the whole phase
// meaningless.
func TestPauseKeepsTheFrontierAndResumeCarriesOn(t *testing.T) {
	manager, _ := cluster(t)
	ctx := context.Background()
	server := site(t)

	if _, err := manager.Create(ctx, document(server, "news")); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := manager.Start(ctx, "news", false); err != nil {
		t.Fatalf("start: %v", err)
	}

	paused, err := manager.Pause(ctx, "news")
	if err != nil {
		t.Fatalf("pause: %v", err)
	}
	if paused.State.Phase != bus.PhasePaused {
		t.Fatalf("pausing left it %s", paused.State.Phase)
	}

	// Resuming is allowed from paused and start is what a stopped job takes.
	// The phases are not decoration: they decide which of the two is right.
	if _, err := manager.Resume(ctx, "news"); err != nil {
		t.Fatalf("resume: %v", err)
	}
	waitFor(t, manager, "news", bus.PhaseDone, bus.PhaseFailed)
}

// TestPausingMidCrawlLeavesNothingLeased.
//
// The regression that the small-site test above only caught by luck. Pausing
// used to cancel the crawl's context, which aborts the fetches in flight, and
// an aborted fetch tells the frontier nothing on purpose so that an interrupted
// URL is not charged an attempt. Its lease then had to expire before anybody
// could have that URL again: five minutes, during which a resumed job reported
// itself running and did nothing at all.
//
// So the site here is slow enough that a pause is guaranteed to land while
// pages are in flight, and the assertion is that resuming finishes long inside
// [run.Lease]. Held to well under it, because a test that allowed five minutes
// would pass on the broken behaviour.
func TestPausingMidCrawlLeavesNothingLeased(t *testing.T) {
	manager, _ := cluster(t)
	ctx := context.Background()

	// Every page below the index takes a moment, so workers are still holding
	// leases when the pause arrives.
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")

		if r.URL.Path == "/" {
			for i := range 12 {
				fmt.Fprintf(w, `<a href="/p%d">p%d</a>`, i, i)
			}
			return
		}
		time.Sleep(150 * time.Millisecond)
		fmt.Fprintf(w,
			`<html><head><meta property="og:title" content="Page %s"></head><body></body></html>`,
			strings.TrimPrefix(r.URL.Path, "/"))
	}))
	t.Cleanup(slow.Close)

	if _, err := manager.Create(ctx, document(slow, "news")); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := manager.Start(ctx, "news", false); err != nil {
		t.Fatalf("start: %v", err)
	}

	// Long enough that the index has been read and its links queued and leased,
	// short enough that the crawl is nowhere near done.
	time.Sleep(200 * time.Millisecond)

	paused, err := manager.Pause(ctx, "news")
	if err != nil {
		t.Fatalf("pause: %v", err)
	}
	if paused.State.Phase != bus.PhasePaused {
		t.Fatalf("pausing left it %s", paused.State.Phase)
	}

	// What the fix is actually about: the frontier is holding nothing, so the
	// work that was in flight is due again now rather than in five minutes.
	stats, err := manager.Stats(ctx, "news")
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats.Waiting == 0 {
		t.Skip("the crawl finished before the pause landed, so there is nothing to resume")
	}

	if _, err := manager.Resume(ctx, "news"); err != nil {
		t.Fatalf("resume: %v", err)
	}

	started := time.Now()
	waitFor(t, manager, "news", bus.PhaseDone, bus.PhaseFailed)
	if took := time.Since(started); took > 30*time.Second {
		t.Errorf("resuming took %s, which means it waited out leases the pause should have released", took)
	}

	after, err := manager.Stats(ctx, "news")
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if after.Waiting != 0 {
		t.Errorf("%d URLs are still waiting after the crawl finished", after.Waiting)
	}
}

// TestResumeRefusesAJobThatIsNotPaused.
func TestResumeRefusesAJobThatIsNotPaused(t *testing.T) {
	manager, _ := cluster(t)
	ctx := context.Background()
	server := site(t)

	if _, err := manager.Create(ctx, document(server, "news")); err != nil {
		t.Fatalf("create: %v", err)
	}

	_, err := manager.Resume(ctx, "news")
	if err == nil {
		t.Fatal("resuming a job that was never started was allowed")
	}
	if !strings.Contains(err.Error(), "start") {
		t.Errorf("the refusal does not say what to use instead: %v", err)
	}
}

// TestStartRefusesAJobAlreadyRunning.
//
// Two drivers for one job is two schedulers handing out the same host, which is
// the one thing the single-driver rule exists to prevent. It is refused at the
// door rather than discovered as a site being hit twice.
func TestStartRefusesAJobAlreadyRunning(t *testing.T) {
	manager, _ := cluster(t)
	ctx := context.Background()
	server := site(t)

	if _, err := manager.Create(ctx, document(server, "news")); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := manager.Start(ctx, "news", false); err != nil {
		t.Fatalf("start: %v", err)
	}

	if _, err := manager.Start(ctx, "news", false); err == nil {
		t.Error("a second driver was allowed to start for one job")
	}
}

// TestOnlyOneStartWinsWhenTheyRace.
//
// One driver per job is the politeness rule, not a simplification: two
// schedulers handing out the same host cannot honour a crawl delay between
// them. It was enforced by a check that released the lock before building the
// stages and seeding, which takes long enough for every concurrent caller to
// walk straight through it. Eight simultaneous starts produced eight drivers on
// one job, seven of them unreachable, because only the last reached the map.
//
// The control service answers each request on its own goroutine, so two `scour
// job start` at the same moment was all it took.
//
// The site blocks until the test lets it go, because the point is that the
// starts race while a crawl is definitely still running. Against a site that
// answers immediately the first crawl finishes mid-burst and a later start
// succeeds legitimately, which is not the bug and would make this test lie.
func TestOnlyOneStartWinsWhenTheyRace(t *testing.T) {
	manager, _ := cluster(t)
	ctx := context.Background()

	release := make(chan struct{})
	var once sync.Once

	held := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			http.NotFound(w, r)
			return
		}
		<-release
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<html><head><title>Held</title></head><body></body></html>`)
	}))
	t.Cleanup(held.Close)

	// Registered after the server, so it runs before it: cleanups are LIFO,
	// and closing a server whose handler is still blocked waits forever. A
	// t.Fatal anywhere below would otherwise hang the package rather than
	// report the failure.
	t.Cleanup(func() { once.Do(func() { close(release) }) })

	if _, err := manager.Create(ctx, document(held, "news")); err != nil {
		t.Fatalf("create: %v", err)
	}

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		won     int
		refused int
	)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := manager.Start(ctx, "news", false)

			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				won++
				return
			}
			// Either message: a loser that got as far as the running map is
			// told the job is already running, and one refused at the claim is
			// told another request has it. Both are true and both name the
			// job; which one a caller gets depends on how far the winner had
			// got, which is not something to assert.
			switch {
			case strings.Contains(err.Error(), "already running"),
				strings.Contains(err.Error(), "is busy"):
				if !strings.Contains(err.Error(), `"news"`) {
					t.Errorf("the refusal does not name the job: %v", err)
				}
				refused++
			default:
				t.Errorf("a losing start failed for another reason: %v", err)
			}
		}()
	}
	wg.Wait()

	if won != 1 {
		t.Errorf("%d of 8 racing starts won, want exactly 1: any more is two crawls on one frontier", won)
	}
	if refused != 7 {
		t.Errorf("%d starts were refused, want 7: the rest built a crawl nobody can reach", refused)
	}

	// And the one that won is the one the manager can still reach, which is the
	// half that made the losers dangerous: a driver nothing has a handle on
	// cannot be stopped, reported, or made to leave the site alone.
	status, err := manager.Status(ctx, "news")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.State.Phase != bus.PhaseRunning {
		t.Errorf("after a start that won, the job is %s", status.State.Phase)
	}

	once.Do(func() { close(release) })
	waitFor(t, manager, "news", bus.PhaseDone, bus.PhaseFailed)
}

// TestDeleteStopsARunningJob.
//
// Deleting a job whose crawl is still going would leave a driver working on
// something the cluster no longer knows about, which is the shape of a process
// nobody can find to stop.
func TestDeleteStopsARunningJob(t *testing.T) {
	manager, _ := cluster(t)
	ctx := context.Background()
	server := site(t)

	if _, err := manager.Create(ctx, document(server, "news")); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := manager.Start(ctx, "news", false); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := manager.Delete(ctx, "news"); err != nil {
		t.Fatalf("delete: %v", err)
	}

	if _, err := manager.Status(ctx, "news"); err == nil {
		t.Error("a deleted job still has a status")
	}

	listed, err := manager.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(listed) != 0 {
		t.Errorf("list has %d jobs after deleting the only one", len(listed))
	}
}

// TestARecreatedJobStartsFromNothing.
//
// Delete used to leave the frontier, on the reasoning that a job recreated
// under the same name should carry on. That reasoning is what stop is for.
// What it produced was a job somebody deleted, rewrote and started whose every
// start URL was already recorded as finished: Seed added nothing, the workers
// leased nothing, and the run ended "finished" with fetched 0 - which is what a
// site that has gone dark looks like, and was fixable only by knowing to pass
// --fresh to a job that had never been run.
func TestARecreatedJobStartsFromNothing(t *testing.T) {
	manager, _ := cluster(t)
	ctx := context.Background()
	server := site(t)

	crawl := func() int64 {
		t.Helper()
		if _, err := manager.Create(ctx, document(server, "news")); err != nil {
			t.Fatalf("create: %v", err)
		}
		if _, err := manager.Start(ctx, "news", false); err != nil {
			t.Fatalf("start: %v", err)
		}
		waitFor(t, manager, "news", bus.PhaseDone, bus.PhaseFailed)

		stats, err := manager.Stats(ctx, "news")
		if err != nil {
			t.Fatalf("stats: %v", err)
		}
		return stats.Fetched
	}

	first := crawl()
	if first == 0 {
		t.Fatal("the first crawl fetched nothing")
	}

	if err := manager.Delete(ctx, "news"); err != nil {
		t.Fatalf("delete: %v", err)
	}

	if second := crawl(); second == 0 {
		t.Errorf("a job created again after a delete fetched nothing, because it "+
			"inherited the finished queue of the job that was deleted (the first fetched %d)", first)
	}
}

// TestStartingAndDeletingAtOnceNeverDisagree.
//
// A driver only reaches the running map once its stages are built and its
// frontier is open, and begin reserves the name before that. Everything else
// that acts on a job has to ask the same question, and asked the running map
// alone: the whole build window was a hole.
//
// A delete landing in it returned success and removed the document while begin
// carried on, registered a driver and wrote a running phase for a job that no
// longer existed - a crawl nothing could find, and a state row nothing could
// ever clear, because every path that clears one starts by reading the
// document.
//
// The two must always agree afterwards, whichever won: a job that is gone is
// not being crawled, and a job being crawled has not been deleted.
func TestStartingAndDeletingAtOnceNeverDisagree(t *testing.T) {
	manager, _ := cluster(t)
	ctx := context.Background()

	// Slow rather than blocking, so a crawl that does start can still be
	// stopped: stopping drains, and draining waits for the page in flight.
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			http.NotFound(w, r)
			return
		}
		time.Sleep(50 * time.Millisecond)
		fmt.Fprint(w, `<html><head><title>Slow</title></head></html>`)
	}))
	t.Cleanup(slow.Close)

	for range 10 {
		if _, err := manager.Create(ctx, document(slow, "news")); err != nil {
			t.Fatalf("create: %v", err)
		}

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, _ = manager.Start(ctx, "news", false)
		}()
		go func() {
			defer wg.Done()
			_ = manager.Delete(ctx, "news")
		}()
		wg.Wait()

		listed, err := manager.List(ctx)
		if err != nil {
			t.Fatalf("list: %v", err)
		}

		var found bool
		for _, job := range listed {
			if job.Name == "news" {
				found = true
			}
		}

		if !found {
			// Deleted, so nothing may still be driving it: a running phase for
			// a job with no document is the row nothing can clear.
			if _, err := manager.Status(ctx, "news"); err == nil {
				t.Fatal("news was deleted and still has a status")
			}
			continue
		}

		// Still there, so tidy up for the next round.
		if status, err := manager.Status(ctx, "news"); err == nil && status.State.Phase.Live() {
			if _, err := manager.Stop(ctx, "news"); err != nil {
				t.Fatalf("stop: %v", err)
			}
		}
		if err := manager.Delete(ctx, "news"); err != nil {
			t.Fatalf("cleanup delete: %v", err)
		}
	}
}

// TestListReportsEveryJobAndItsPhase.
func TestListReportsEveryJobAndItsPhase(t *testing.T) {
	manager, _ := cluster(t)
	ctx := context.Background()
	server := site(t)

	for _, name := range []string{"news", "sport"} {
		if _, err := manager.Create(ctx, document(server, name)); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
	}

	listed, err := manager.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(listed) != 2 {
		t.Fatalf("list has %d jobs, want 2", len(listed))
	}
	for _, job := range listed {
		if job.State.Phase != bus.PhaseStopped {
			t.Errorf("%s is %s, want stopped", job.Name, job.State.Phase)
		}
		if job.Revision == 0 {
			t.Errorf("%s has no revision", job.Name)
		}
	}
}

// TestAJobStartedFromTheServiceIsWatchable.
//
// `scour job watch` is a subscriber and nothing else, so what it sees is
// whatever the driver published. The transition to a terminal phase is the one
// event a watcher must not miss: it is what ends the watch.
func TestAJobStartedFromTheServiceIsWatchable(t *testing.T) {
	manager, conn := cluster(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := site(t)

	events, stop, err := conn.WatchJob(ctx, "news")
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	defer func() { _ = stop() }()

	if _, err := manager.Create(ctx, document(server, "news")); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := manager.Start(ctx, "news", false); err != nil {
		t.Fatalf("start: %v", err)
	}

	deadline := time.After(30 * time.Second)
	var phases []bus.Phase
	for {
		select {
		case <-deadline:
			t.Fatalf("no terminal event arrived, saw %v", phases)
		case event := <-events:
			phases = append(phases, event.Phase)
			if event.Phase.Live() {
				continue
			}
			if event.Phase != bus.PhaseDone {
				t.Fatalf("the crawl ended %s: %s", event.Phase, event.Message)
			}
			if event.Stats.Fetched == 0 {
				t.Error("the closing event carries no counters, so a watcher learns nothing from it")
			}
			return
		}
	}
}

// TestTheClosingReportSaysWhatIsStillQueued.
//
// A crawl that finished a site and one that hit its page budget look identical
// unless the numbers say otherwise, which is what Ending exists to prevent and
// what this used to undo by another route. The closing snapshot was taken after
// the crawl was closed, and closing shuts the frontier, so asking how much was
// left failed and the report said zero. A crawl stopped at three pages with
// forty-one URLs queued announced "queued 0" to every watcher.
func TestTheClosingReportSaysWhatIsStillQueued(t *testing.T) {
	manager, conn := cluster(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// More links than the budget allows, so the crawl has to stop with work
	// left rather than running the site dry.
	wide := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")

		if r.URL.Path == "/" {
			for i := range 40 {
				fmt.Fprintf(w, `<a href="/p%d">p%d</a>`, i, i)
			}
			return
		}
		fmt.Fprintf(w,
			`<html><head><meta property="og:title" content="Page %s"></head><body></body></html>`,
			strings.TrimPrefix(r.URL.Path, "/"))
	}))
	t.Cleanup(wide.Close)

	budgeted := fmt.Appendf(nil, `
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
    concurrency = 1
    max_pages   = 3
  }
}
`, strings.TrimPrefix(wide.URL, "http://"), wide.URL)

	events, stop, err := conn.WatchJob(ctx, "news")
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	defer func() { _ = stop() }()

	if _, err := manager.Create(ctx, budgeted); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := manager.Start(ctx, "news", false); err != nil {
		t.Fatalf("start: %v", err)
	}

	deadline := time.After(30 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("no closing event arrived")
		case event := <-events:
			if event.Phase.Live() {
				continue
			}
			if event.Stats.Waiting == 0 {
				t.Errorf("the closing report says nothing is queued after a budget stop, "+
					"so a watcher is told the site was finished: %+v", event.Stats)
			}
			if event.Stats.Exported == 0 {
				t.Errorf("the closing report says nothing was exported, so reading the "+
					"counters before the close lost the flush: %+v", event.Stats)
			}
			return
		}
	}
}

// TestStatsForAJobThatIsNotRunningReportWhatIsLeft.
//
// The counters belong to a run, and a job that is not running has none. What it
// does have is a frontier, and how much is left in it is the question somebody
// asks about a job they paused.
func TestStatsForAJobThatIsNotRunningReportWhatIsLeft(t *testing.T) {
	manager, _ := cluster(t)
	ctx := context.Background()
	server := site(t)

	if _, err := manager.Create(ctx, document(server, "news")); err != nil {
		t.Fatalf("create: %v", err)
	}

	stats, err := manager.Stats(ctx, "news")
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats.Fetched != 0 || stats.Items != 0 {
		t.Errorf("a job that has never run reports counters: %+v", stats)
	}
}

// TestAJobNobodySubmittedIsRefusedByEveryOperation.
//
// One check, across the surface, because the alternative is one operation that
// forgot: an unknown name reaching the driver is a crawl of nothing, started
// successfully.
func TestAJobNobodySubmittedIsRefusedByEveryOperation(t *testing.T) {
	manager, _ := cluster(t)
	ctx := context.Background()

	for name, call := range map[string]func() error{
		"status": func() error { _, err := manager.Status(ctx, "absent"); return err },
		"stats":  func() error { _, err := manager.Stats(ctx, "absent"); return err },
		"start":  func() error { _, err := manager.Start(ctx, "absent", false); return err },
		"stop":   func() error { _, err := manager.Stop(ctx, "absent"); return err },
		"pause":  func() error { _, err := manager.Pause(ctx, "absent"); return err },
		"delete": func() error { return manager.Delete(ctx, "absent") },
		"document": func() error {
			_, err := manager.Document(ctx, "absent")
			return err
		},
	} {
		if err := call(); err == nil {
			t.Errorf("%s accepted a job nobody submitted", name)
		}
	}
}

// TestADeleteOwnsTheJobWhileItDrains.
//
// Delete stops a live crawl and then empties its frontier, and stopping means
// waiting out the pages in flight. It used to check that nothing else held the
// job and then release the lock for the whole of that wait: a start answered in
// the window seeded the frontier afresh, and the delete resumed and emptied it.
// The new crawl ended "finished, fetched 0" - the exact failure that emptying
// the frontier on delete exists to prevent, reached from the other side.
//
// The site blocks so the delete is definitely still draining while the starts
// are attempted.
func TestADeleteOwnsTheJobWhileItDrains(t *testing.T) {
	manager, _ := cluster(t)
	ctx := context.Background()

	release := make(chan struct{})
	var once sync.Once

	held := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			http.NotFound(w, r)
			return
		}
		<-release
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<html><head><title>Held</title></head><body></body></html>`)
	}))
	t.Cleanup(held.Close)

	// Registered after the server, so it runs before it: cleanups are LIFO,
	// and closing a server whose handler is still blocked waits forever. A
	// t.Fatal anywhere below would otherwise hang the package rather than
	// report the failure.
	t.Cleanup(func() { once.Do(func() { close(release) }) })

	if _, err := manager.Create(ctx, document(held, "news")); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := manager.Start(ctx, "news", true); err != nil {
		t.Fatalf("start: %v", err)
	}
	waitFor(t, manager, "news", bus.PhaseRunning)

	// Retried, because claim refuses rather than waits and the probe below
	// takes the claim itself for as long as it takes to be told no. A caller
	// that means to win has to ask again; that is the trade the claim makes.
	deleted := make(chan error, 1)
	go func() {
		for {
			err := manager.Delete(ctx, "news")
			if err == nil || !strings.Contains(err.Error(), "is busy") {
				deleted <- err
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()

	var owned bool
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		_, err := manager.Start(ctx, "news", true)
		if err == nil {
			t.Fatal("a start won a job a delete was in the middle of removing")
		}
		if strings.Contains(err.Error(), "is busy") {
			owned = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !owned {
		t.Error("the delete did not hold the job while it drained, so a start could seed a frontier it was about to empty")
	}

	once.Do(func() { close(release) })
	if err := <-deleted; err != nil {
		t.Fatalf("delete: %v", err)
	}
}

// TestAJobThatNeverRanLeavesNoFrontier.
//
// sqlite.Open creates the directory and the database, so every caller that
// merely asked about a frontier made one: `job stats` on a job that had never
// run left an empty frontier behind, and `job delete` created one on its way
// out and left the directory there after the job itself was gone. Nothing
// failed, which is why it went unnoticed - an empty frontier answers every
// question the same way a missing one should.
func TestAJobThatNeverRanLeavesNoFrontier(t *testing.T) {
	manager, _, dir := clusterIn(t)
	ctx := context.Background()

	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	t.Cleanup(site.Close)

	if _, err := manager.Create(ctx, document(site, "news")); err != nil {
		t.Fatalf("create: %v", err)
	}

	if _, err := manager.Stats(ctx, "news"); err != nil {
		t.Fatalf("stats: %v", err)
	}
	frontierDir := filepath.Join(dir, "jobs", "news")
	if _, err := os.Stat(frontierDir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("asking a job that never ran for its stats built it a frontier: %v", err)
	}

	if err := manager.Delete(ctx, "news"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := os.Stat(frontierDir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the deleted job left a frontier behind: %v", err)
	}
}

// TestAStoppedJobIsUpdatedWithoutReview.
//
// The mutation block is the operator's statement about which changes may be
// applied to a crawl in progress. A job that is not running has no work in
// progress for a costly change to cost anything, so it is changed without
// review - and it was, until Update began claiming the job for its whole span
// and then asked whether anything was working on it. The claim it was holding
// itself made the answer yes, so every stopped job was reviewed as if running,
// the default `costly = "refuse"` refused every scope change to one, and the
// message said the job was running when it was not.
func TestAStoppedJobIsUpdatedWithoutReview(t *testing.T) {
	manager, _ := cluster(t)
	ctx := context.Background()

	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	t.Cleanup(site.Close)

	if _, err := manager.Create(ctx, document(site, "news")); err != nil {
		t.Fatalf("create: %v", err)
	}

	// A scope change, which is what `costly` is about.
	widened := strings.Replace(string(document(site, "news")),
		`start   = ["`+site.URL+`/"]`,
		`start   = ["`+site.URL+`/", "`+site.URL+`/other"]`, 1)
	if widened == string(document(site, "news")) {
		t.Fatal("the fixture changed shape and this test no longer widens the scope")
	}

	if _, err := manager.Update(ctx, []byte(widened)); err != nil {
		t.Fatalf("a stopped job was refused a change nothing was running to be costly to: %v", err)
	}
}

// TestEveryMutatingOperationWaitsItsTurn walks them all, so an operation added
// later without a claim fails the build rather than shipping.
//
// The claim exists because every mutating operation here is "read this job's
// state, decide, act on it" over shared state, and four separate windows of
// that shape were fixed one at a time before the reservation was made the
// operation rather than a flag. Create was still outside it after that change:
// it writes the document and the state row as two writes, and a delete
// answered between them left a state row with no document, which is the row
// nothing can ever clear.
//
// A delete draining a held crawl is what holds the claim here, because it is a
// real operation that takes a real amount of time.
func TestEveryMutatingOperationWaitsItsTurn(t *testing.T) {
	manager, _ := cluster(t)
	ctx := context.Background()

	release := make(chan struct{})
	var once sync.Once

	held := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			http.NotFound(w, r)
			return
		}
		<-release
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<html><head><title>Held</title></head><body></body></html>`)
	}))
	t.Cleanup(held.Close)

	// Registered after the server, so it runs before it: cleanups are LIFO,
	// and closing a server whose handler is still blocked waits forever. A
	// t.Fatal anywhere below would otherwise hang the package rather than
	// report the failure.
	t.Cleanup(func() { once.Do(func() { close(release) }) })

	doc := document(held, "news")
	if _, err := manager.Create(ctx, doc); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := manager.Start(ctx, "news", true); err != nil {
		t.Fatalf("start: %v", err)
	}
	waitFor(t, manager, "news", bus.PhaseRunning)

	// Retried, because claim refuses rather than waits and the probes below
	// take the claim themselves for as long as it takes to be told no. A
	// caller that means to win has to ask again; that is the trade the claim
	// makes, and this is what it looks like.
	deleted := make(chan error, 1)
	go func() {
		for {
			err := manager.Delete(ctx, "news")
			if err == nil || !strings.Contains(err.Error(), "is busy") {
				deleted <- err
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()

	operations := map[string]func() error{
		"create": func() error { _, err := manager.Create(ctx, doc); return err },
		"update": func() error { _, err := manager.Update(ctx, doc); return err },
		"delete": func() error { return manager.Delete(ctx, "news") },
		"start":  func() error { _, err := manager.Start(ctx, "news", false); return err },
		"resume": func() error { _, err := manager.Resume(ctx, "news"); return err },
		"stop":   func() error { _, err := manager.Stop(ctx, "news"); return err },
		"pause":  func() error { _, err := manager.Pause(ctx, "news"); return err },
	}

	// Wait until the delete has the claim, using one of the operations to ask.
	// Before that it may legitimately still be reading the document.
	held2 := false
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		if err := operations["start"](); err != nil && strings.Contains(err.Error(), "is busy") {
			held2 = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !held2 {
		t.Fatal("the delete never took the claim, so this test proves nothing")
	}

	for _, name := range []string{"create", "update", "delete", "start", "resume", "stop", "pause"} {
		err := operations[name]()
		if err == nil {
			t.Errorf("%s went ahead on a job another request was in the middle of deleting", name)
			continue
		}
		if !strings.Contains(err.Error(), "is busy") {
			t.Errorf("%s was refused for another reason, so it is not waiting its turn: %v", name, err)
		}
	}

	once.Do(func() { close(release) })
	if err := <-deleted; err != nil {
		t.Fatalf("delete: %v", err)
	}
}

// TestAStandbyReviewsAnUpdateAgainstTheRunningJob.
//
// Control requests are answered in a queue group, so a second `scour server`
// is a standby that shares the load: a job running on one node can have its
// update answered by another. The running map is per-manager, so a standby saw
// no driver, decided the job was not running and skipped the mutation review
// entirely - the policy bypassed on exactly the crawl it exists to protect,
// decided by which node NATS happened to pick.
//
// Both managers share one bus here, which is what two servers in a cluster
// are.
func TestAStandbyReviewsAnUpdateAgainstTheRunningJob(t *testing.T) {
	driver, conn, _ := clusterIn(t)
	ctx := context.Background()

	standby, err := jobs.New(ctx, conn, jobs.Options{
		Dir:    t.TempDir(),
		Bodies: bodies(t),
		Name:   "standby",
	})
	if err != nil {
		t.Fatalf("standby: %v", err)
	}
	t.Cleanup(func() { _ = standby.Close() })

	release := make(chan struct{})
	var once sync.Once

	held := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			http.NotFound(w, r)
			return
		}
		<-release
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<html><head><title>Held</title></head><body></body></html>`)
	}))
	t.Cleanup(held.Close)
	t.Cleanup(func() { once.Do(func() { close(release) }) })

	doc := document(held, "news")
	if _, err := driver.Create(ctx, doc); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := driver.Start(ctx, "news", true); err != nil {
		t.Fatalf("start: %v", err)
	}
	waitFor(t, driver, "news", bus.PhaseRunning)

	// A scope change, which the default `costly = "refuse"` refuses on a
	// running job.
	widened := strings.Replace(string(doc),
		`start   = ["`+held.URL+`/"]`,
		`start   = ["`+held.URL+`/", "`+held.URL+`/other"]`, 1)
	if widened == string(doc) {
		t.Fatal("the fixture changed shape and this test no longer widens the scope")
	}

	if _, err := standby.Update(ctx, []byte(widened)); err == nil {
		t.Error("a standby applied a change the mutation policy refuses, " +
			"because the crawl was running on the other node")
	} else if !strings.Contains(err.Error(), "mutation policy") {
		t.Errorf("refused for another reason: %v", err)
	}

	// The node holding the crawl refuses it too, which is the answer both
	// should give.
	if _, err := driver.Update(ctx, []byte(widened)); err == nil {
		t.Error("the node running the crawl applied a change its policy refuses")
	}

	once.Do(func() { close(release) })
}

// standby is a second manager on one bus, which is what a second
// `scour server --drive` is: it joins the same control queue group and any
// request can land on it.
func standby(t *testing.T, conn *bus.Conn) *jobs.Manager {
	t.Helper()

	m, err := jobs.New(context.Background(), conn, jobs.Options{
		Dir:    t.TempDir(),
		Bodies: bodies(t),
		Name:   "standby",
	})
	if err != nil {
		t.Fatalf("standby: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m
}

// heldSite blocks every request until the returned func is called, so a crawl
// is definitely still running while something else is tried against it.
func heldSite(t *testing.T) (*httptest.Server, func()) {
	t.Helper()

	release := make(chan struct{})
	var once sync.Once
	let := func() { once.Do(func() { close(release) }) }

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			http.NotFound(w, r)
			return
		}
		<-release
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<html><head><title>Held</title></head><body></body></html>`)
	}))
	t.Cleanup(server.Close)
	t.Cleanup(let)
	return server, let
}

// TestAStandbyDoesNotStartASecondCrawl.
//
// One driver per job is the politeness rule, not a simplification: two
// schedulers handing out the same host cannot honour a crawl delay between
// them. It was enforced by a map that belongs to one process, and control
// requests are answered in a queue group - so a start landing on a standby
// found no local driver, built its own frontier, seeded it, and crawled the
// same site again. The site would have been the first to notice.
func TestAStandbyDoesNotStartASecondCrawl(t *testing.T) {
	driver, conn, _ := clusterIn(t)
	other := standby(t, conn)
	ctx := context.Background()

	held, let := heldSite(t)

	if _, err := driver.Create(ctx, document(held, "news")); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := driver.Start(ctx, "news", true); err != nil {
		t.Fatalf("start: %v", err)
	}
	waitFor(t, driver, "news", bus.PhaseRunning)

	_, err := other.Start(ctx, "news", true)
	if err == nil {
		t.Fatal("a standby started a second crawl on a job already being driven")
	}
	if !strings.Contains(err.Error(), "already running") {
		t.Errorf("refused for another reason: %v", err)
	}
	// And it says where, which is the thing an operator needs next.
	if !strings.Contains(err.Error(), "test") {
		t.Errorf("the refusal does not name the node driving it: %v", err)
	}

	let()
}

// TestAStandbyDoesNotReportALiveCrawlAsStopped.
//
// stop and pause answered from the local driver map, so a standby said "is not
// running" while the pages kept arriving and `scour job status` said running:
// two contradictory answers and no command that worked. delete swallowed the
// same answer, removed the document and the state row, and left the crawl
// going - and when it ended it wrote a state row for a document that no longer
// existed, which is the row nothing can ever clear.
func TestAStandbyDoesNotReportALiveCrawlAsStopped(t *testing.T) {
	driver, conn, _ := clusterIn(t)
	other := standby(t, conn)
	ctx := context.Background()

	held, let := heldSite(t)

	if _, err := driver.Create(ctx, document(held, "news")); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := driver.Start(ctx, "news", true); err != nil {
		t.Fatalf("start: %v", err)
	}
	waitFor(t, driver, "news", bus.PhaseRunning)

	for name, call := range map[string]func() error{
		"stop":   func() error { _, err := other.Stop(ctx, "news"); return err },
		"pause":  func() error { _, err := other.Pause(ctx, "news"); return err },
		"delete": func() error { return other.Delete(ctx, "news") },
	} {
		err := call()
		if err == nil {
			t.Errorf("%s on a standby reported success for a crawl running on another node", name)
			continue
		}
		if strings.Contains(err.Error(), "is not running") {
			t.Errorf("%s said the job is not running while it is: %v", name, err)
		}
		if !strings.Contains(err.Error(), "test") {
			t.Errorf("%s does not name the node driving it: %v", name, err)
		}
	}

	// The document is still there, which is the half that mattered for delete.
	if _, err := driver.Status(ctx, "news"); err != nil {
		t.Errorf("the job was deleted out from under the crawl: %v", err)
	}

	let()
}

// TestTheDocumentsExternalTimeoutBoundsTheStage.
//
// `external_timeout` says how long a stage somewhere else has to answer. It was
// parsed, defaulted, validated and printed by `scour job show`, and the request
// was bounded by bus.Timeout regardless, because the manager built its stage
// clients with a manager-wide wait of zero. A job asking for a long timeout had
// its pages failed at two minutes and a job asking for a short one waited two.
//
// This is the third time the field has been found unwired, and twice the fix
// wired it into something that displays it. So it is pinned by what actually
// happens rather than by what is reported: a site slower than the timeout fails
// the page, and the same site under a generous timeout does not.
func TestTheDocumentsExternalTimeoutBoundsTheStage(t *testing.T) {
	manager, _, _ := clusterIn(t)
	ctx := context.Background()

	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			http.NotFound(w, r)
			return
		}
		time.Sleep(300 * time.Millisecond)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<html><head><title>Slow</title></head><body></body></html>`)
	}))
	t.Cleanup(slow.Close)

	withTimeout := func(name, timeout string) []byte {
		doc := string(document(slow, name))
		block := "  downloader {\n    external         = true\n" +
			"    external_timeout = \"" + timeout + "\"\n  }\n\n  scheduler {"
		out := strings.Replace(doc, "  scheduler {", block, 1)
		if out == doc {
			t.Fatal("the fixture changed shape and this test no longer sets a timeout")
		}
		return []byte(out)
	}

	// Short enough that the site cannot answer in time.
	if _, err := manager.Create(ctx, withTimeout("tight", "20ms")); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := manager.Start(ctx, "tight", true); err != nil {
		t.Fatalf("start: %v", err)
	}
	waitFor(t, manager, "tight", bus.PhaseDone, bus.PhaseFailed, bus.PhaseStopped)

	tight, err := manager.Stats(ctx, "tight")
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if tight.Fetched != 0 {
		t.Errorf("a 20ms external timeout fetched %d pages from a site that takes 300ms, "+
			"so the document's timeout is not what bounds the request", tight.Fetched)
	}

	// Generous enough that it can.
	if _, err := manager.Create(ctx, withTimeout("roomy", "10s")); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := manager.Start(ctx, "roomy", true); err != nil {
		t.Fatalf("start: %v", err)
	}
	waitFor(t, manager, "roomy", bus.PhaseDone, bus.PhaseFailed, bus.PhaseStopped)

	roomy, err := manager.Stats(ctx, "roomy")
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if roomy.Fetched == 0 {
		t.Error("a 10s external timeout fetched nothing from a site that takes 300ms")
	}
}
