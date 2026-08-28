// SPDX-License-Identifier: GPL-3.0-or-later

package bus_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rangertaha/scour/internal/bus"
)

// What these check is the wire, not the manager.
//
// The manager has its own tests, which crawl a real site through a real node.
// What has to be true here is narrower and easier to get wrong: that every
// operation reaches the other side with its arguments intact, that a refusal
// comes back as an answer rather than as a timeout, and that a caller can tell
// a refusal from an unreachable cluster. The command line branches on that
// difference to pick an exit code.

// recorder is a Controller that remembers what it was asked and answers what it
// was told to.
type recorder struct {
	mu sync.Mutex

	calls    []string
	document []byte
	name     string
	fresh    bool

	status bus.JobStatus
	stats  bus.JobStats
	err    error
}

func (r *recorder) note(call string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, call)
}

func (r *recorder) called() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

func (r *recorder) List(context.Context) ([]bus.JobStatus, error) {
	r.note("list")
	return []bus.JobStatus{r.status}, r.err
}

func (r *recorder) Create(_ context.Context, document []byte) (bus.JobStatus, error) {
	r.note("create")
	r.document = document
	return r.status, r.err
}

func (r *recorder) Update(_ context.Context, document []byte) (bus.JobStatus, error) {
	r.note("update")
	r.document = document
	return r.status, r.err
}

func (r *recorder) Delete(_ context.Context, name string) error {
	r.note("delete")
	r.name = name
	return r.err
}

func (r *recorder) Status(_ context.Context, name string) (bus.JobStatus, error) {
	r.note("status")
	r.name = name
	return r.status, r.err
}

func (r *recorder) Stats(_ context.Context, name string) (bus.JobStats, error) {
	r.note("stats")
	r.name = name
	return r.stats, r.err
}

func (r *recorder) Start(_ context.Context, name string, fresh bool) (bus.JobStatus, error) {
	r.note("start")
	r.name, r.fresh = name, fresh
	return r.status, r.err
}

func (r *recorder) Stop(_ context.Context, name string) (bus.JobStatus, error) {
	r.note("stop")
	r.name = name
	return r.status, r.err
}

func (r *recorder) Pause(_ context.Context, name string) (bus.JobStatus, error) {
	r.note("pause")
	r.name = name
	return r.status, r.err
}

func (r *recorder) Resume(_ context.Context, name string) (bus.JobStatus, error) {
	r.note("resume")
	r.name = name
	return r.status, r.err
}

func (r *recorder) Document(_ context.Context, name string) ([]byte, error) {
	r.note("document")
	r.name = name
	return r.document, r.err
}

// served starts a control service over a stub and returns a client for it.
func served(t *testing.T, stub *recorder) *bus.ControlClient {
	t.Helper()

	conn, err := bus.Connect(bus.Options{Name: "control-test", StoreDir: t.TempDir()})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	service, err := conn.ServeControl(stub, 5*time.Second)
	if err != nil {
		t.Fatalf("serve: %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })

	return conn.NewControl(5 * time.Second)
}

// TestEveryOperationCrossesTheBus.
//
// One test over the whole surface, because the failure it catches is an
// operation that was added to the interface and never registered: the client
// has a method, nothing answers the subject, and the call fails as a timeout
// that reads like a busy cluster.
func TestEveryOperationCrossesTheBus(t *testing.T) {
	stub := &recorder{
		status: bus.JobStatus{
			Name:     "news",
			Revision: 7,
			State:    bus.JobState{Phase: bus.PhaseRunning, Revision: 7, Driver: "node-a"},
		},
		stats:    bus.JobStats{Fetched: 12, Items: 9, Waiting: 3},
		document: []byte("job \"news\" {}\n"),
	}
	client := served(t, stub)
	ctx := context.Background()

	for _, call := range []struct {
		name string
		do   func() error
	}{
		{"list", func() error { _, err := client.List(ctx); return err }},
		{"create", func() error { _, err := client.Create(ctx, []byte("a")); return err }},
		{"update", func() error { _, err := client.Update(ctx, []byte("b")); return err }},
		{"delete", func() error { return client.Delete(ctx, "news") }},
		{"status", func() error { _, err := client.Status(ctx, "news"); return err }},
		{"stats", func() error { _, err := client.Stats(ctx, "news"); return err }},
		{"start", func() error { _, err := client.Start(ctx, "news", true); return err }},
		{"stop", func() error { _, err := client.Stop(ctx, "news"); return err }},
		{"pause", func() error { _, err := client.Pause(ctx, "news"); return err }},
		{"resume", func() error { _, err := client.Resume(ctx, "news"); return err }},
		{"document", func() error { _, err := client.Document(ctx, "news"); return err }},
	} {
		if err := call.do(); err != nil {
			t.Errorf("%s: %v", call.name, err)
		}
	}

	want := []string{"list", "create", "update", "delete", "status", "stats",
		"start", "stop", "pause", "resume", "document"}
	got := stub.called()
	if len(got) != len(want) {
		t.Fatalf("the service saw %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("call %d was %q, want %q", i, got[i], want[i])
		}
	}
}

// TestTheArgumentsSurviveTheTrip.
//
// A document that arrives empty and a `--fresh` that arrives false are both
// silent: the service does something reasonable with what it got, and what it
// got is not what was sent. The one that would hurt is fresh, which decides
// whether a frontier is emptied.
func TestTheArgumentsSurviveTheTrip(t *testing.T) {
	stub := &recorder{}
	client := served(t, stub)
	ctx := context.Background()

	document := []byte("job \"news\" {\n  domains = [\"example.com\"]\n}\n")
	if _, err := client.Create(ctx, document); err != nil {
		t.Fatalf("create: %v", err)
	}
	if string(stub.document) != string(document) {
		t.Errorf("the document arrived as %q", stub.document)
	}

	if _, err := client.Start(ctx, "news", true); err != nil {
		t.Fatalf("start: %v", err)
	}
	if stub.name != "news" {
		t.Errorf("the name arrived as %q", stub.name)
	}
	if !stub.fresh {
		t.Error("--fresh did not survive, so a frontier that should have been emptied would not be")
	}
}

// TestWhatTheServiceAnswersComesBack.
func TestWhatTheServiceAnswersComesBack(t *testing.T) {
	stub := &recorder{
		status: bus.JobStatus{
			Name:     "news",
			Revision: 9,
			State: bus.JobState{
				Phase:    bus.PhaseRunning,
				Revision: 7,
				Driver:   "node-a",
				Since:    time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC),
			},
		},
		stats: bus.JobStats{Fetched: 12, Cached: 4, Items: 9, Waiting: 3, Elapsed: 90 * time.Second},
	}
	client := served(t, stub)
	ctx := context.Background()

	status, err := client.Status(ctx, "news")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.Name != "news" || status.State.Phase != bus.PhaseRunning || status.State.Driver != "node-a" {
		t.Errorf("the status came back as %+v", status)
	}
	if !status.State.Since.Equal(stub.status.State.Since) {
		t.Errorf("the time came back as %s", status.State.Since)
	}

	// The two revisions are the point of having both: a job resubmitted while
	// it crawls is running one and holding another, and Stale is how an
	// operator is told without having to compare two numbers themselves.
	if !status.Stale() {
		t.Error("a job running revision 7 with 9 submitted is not reported as stale")
	}

	stats, err := client.Stats(ctx, "news")
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats.Fetched != 12 || stats.Items != 9 || stats.Waiting != 3 {
		t.Errorf("the stats came back as %+v", stats)
	}
	if stats.Elapsed != 90*time.Second {
		t.Errorf("elapsed came back as %s, so a duration does not survive JSON", stats.Elapsed)
	}
}

// TestARefusalIsAnAnswerAndNotATimeout.
//
// The distinction the whole reply envelope exists for, and the one the command
// line branches on to choose an exit code. A refusal that arrived as a
// transport failure would make a script retry a job the cluster will never
// accept.
func TestARefusalIsAnAnswerAndNotATimeout(t *testing.T) {
	stub := &recorder{err: errors.New(`jobs: "news" already exists. Change it with update`)}
	client := served(t, stub)

	_, err := client.Create(context.Background(), []byte("a"))
	if err == nil {
		t.Fatal("a refusal came back as success")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("the message did not survive: %v", err)
	}
	if !bus.Answered(err) {
		t.Error("a refusal is not reported as answered, so the command line would exit as though the cluster were unreachable")
	}
}

// TestNothingServingIsNotAnAnswer is the other side of it.
//
// A cluster with no job service answers immediately with "no responders", and
// that has to be distinguishable from the service saying no.
func TestNothingServingIsNotAnAnswer(t *testing.T) {
	conn, err := bus.Connect(bus.Options{Name: "empty", StoreDir: t.TempDir()})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = conn.Close() }()

	_, err = conn.NewControl(2 * time.Second).List(context.Background())
	if err == nil {
		t.Fatal("listing jobs on a cluster with no job service succeeded")
	}
	if bus.Answered(err) {
		t.Errorf("an unreachable service is reported as having answered: %v", err)
	}
}

// TestAWatcherSeesWhatTheDriverPublishes.
func TestAWatcherSeesWhatTheDriverPublishes(t *testing.T) {
	conn, err := bus.Connect(bus.Options{Name: "watch-test", StoreDir: t.TempDir()})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = conn.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	events, stop, err := conn.WatchJob(ctx, "news")
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	defer func() { _ = stop() }()

	sent := bus.JobEvent{
		Name:    "news",
		At:      time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC),
		Phase:   bus.PhaseRunning,
		Message: "started",
		Stats:   bus.JobStats{Fetched: 3},
	}
	if err := conn.Announce(sent); err != nil {
		t.Fatalf("announce: %v", err)
	}

	select {
	case got := <-events:
		if got.Name != "news" || got.Phase != bus.PhaseRunning || got.Message != "started" {
			t.Errorf("the event arrived as %+v", got)
		}
		if got.Stats.Fetched != 3 {
			t.Errorf("the counters did not travel: %+v", got.Stats)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the event never arrived")
	}
}

// TestAWatcherOfOneJobIsNotToldAboutAnother.
//
// The subject is per job precisely so that watching a busy cluster's quiet job
// is quiet. A watcher subscribed to everything would make `scour job watch`
// unreadable on any cluster with more than one job running.
func TestAWatcherOfOneJobIsNotToldAboutAnother(t *testing.T) {
	conn, err := bus.Connect(bus.Options{Name: "watch-one", StoreDir: t.TempDir()})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = conn.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	events, stop, err := conn.WatchJob(ctx, "news")
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	defer func() { _ = stop() }()

	if err := conn.Announce(bus.JobEvent{Name: "sport", Phase: bus.PhaseRunning}); err != nil {
		t.Fatalf("announce: %v", err)
	}
	if err := conn.Announce(bus.JobEvent{Name: "news", Phase: bus.PhaseDone}); err != nil {
		t.Fatalf("announce: %v", err)
	}

	select {
	case got := <-events:
		if got.Name != "news" {
			t.Errorf("a watcher of news was told about %q", got.Name)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the event never arrived")
	}
}

// TestClosingAWatchDoesNotPanic.
//
// The subscription's callback runs on a NATS goroutine that can still be
// delivering when the watch is closed, so the channel a watcher ranges over
// cannot be both written by the callback and closed by the shutdown. It was,
// and a send on a closed channel takes the process down: a `scour job watch`
// interrupted at the wrong moment would have crashed rather than exited.
func TestClosingAWatchDoesNotPanic(t *testing.T) {
	conn, err := bus.Connect(bus.Options{Name: "watch-close", StoreDir: t.TempDir()})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = conn.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	events, _, err := conn.WatchJob(ctx, "news")
	if err != nil {
		t.Fatalf("watch: %v", err)
	}

	// Publishing while cancelling, which is the window the race lives in.
	go func() {
		for i := 0; i < 50; i++ {
			_ = conn.Announce(bus.JobEvent{Name: "news", Phase: bus.PhaseRunning})
		}
	}()
	cancel()

	// Drained until closed. Reaching the close without a panic is the whole
	// assertion; what arrives before it does not matter.
	for range events {
	}
}
