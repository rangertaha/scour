// SPDX-License-Identifier: GPL-3.0-or-later

package jobs_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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

	joined, err := node.Join(ctx, conn, node.Options{Name: "worker", Bodies: shared})
	if err != nil {
		t.Fatalf("join: %v", err)
	}
	t.Cleanup(func() { _ = joined.Close() })

	go func() { _ = joined.Watch(ctx) }()

	manager, err := jobs.New(ctx, conn, jobs.Options{
		Dir:    t.TempDir(),
		Bodies: shared,
		Name:   "test",
	})
	if err != nil {
		t.Fatalf("manager: %v", err)
	}
	t.Cleanup(func() { _ = manager.Close() })

	return manager, conn
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
