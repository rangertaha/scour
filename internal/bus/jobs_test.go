// SPDX-License-Identifier: GPL-3.0-or-later

package bus_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rangertaha/scour/internal/bus"
)

const newsJob = `
job "news" {
  start = ["https://example.com/"]

  item "article" {
    property "title" {
      type = str
    }
  }
}
`

func jobs(t *testing.T, conn *bus.Conn) *bus.Jobs {
	t.Helper()

	store, err := conn.OpenJobs(context.Background())
	if err != nil {
		t.Fatalf("open jobs: %v", err)
	}
	return store
}

// TestAJobSubmittedOnOneNodeIsVisibleOnEvery. The whole reason the control
// plane is KV: a node joining needs an address and nothing else.
func TestAJobSubmittedOnOneNodeIsVisibleOnEvery(t *testing.T) {
	first := connect(t)
	ctx := context.Background()

	if _, err := jobs(t, first).Put(ctx, "news", []byte(newsJob)); err != nil {
		t.Fatalf("put: %v", err)
	}

	// A second node, joining the first rather than starting its own server.
	second, err := bus.Connect(bus.Options{URL: first.Address(), Name: "second"})
	if err != nil {
		t.Fatalf("join: %v", err)
	}
	defer second.Close()

	job, revision, err := jobs(t, second).Job(ctx, "news")
	if err != nil {
		t.Fatalf("the second node cannot see the job: %v", err)
	}
	if job.Name != "news" || revision == 0 {
		t.Errorf("job = %+v at revision %d", job, revision)
	}
	if len(job.Items) != 1 {
		t.Errorf("the job arrived without its shape")
	}
}

// TestWhatWasSubmittedIsWhatComesBack. Reformatting somebody's document on the
// way through would make the diff between two submissions unreadable, and the
// diff is the whole of what a resubmission is reviewed by.
func TestWhatWasSubmittedIsWhatComesBack(t *testing.T) {
	store := jobs(t, connect(t))
	ctx := context.Background()

	if _, err := store.Put(ctx, "news", []byte(newsJob)); err != nil {
		t.Fatal(err)
	}

	back, _, err := store.Get(ctx, "news")
	if err != nil {
		t.Fatal(err)
	}
	if string(back) != newsJob {
		t.Errorf("the document changed on the way through:\n%s", back)
	}
}

// TestTwoClientsSubmittingAtOnce: one wins and the other is told, rather than
// one of them silently disappearing.
func TestTwoClientsSubmittingAtOnce(t *testing.T) {
	store := jobs(t, connect(t))
	ctx := context.Background()

	revision, err := store.Put(ctx, "news", []byte(newsJob))
	if err != nil {
		t.Fatal(err)
	}

	changed := strings.Replace(newsJob, "example.com", "changed.example", 1)
	if _, err := store.Update(ctx, "news", []byte(changed), revision); err != nil {
		t.Fatalf("the first update failed: %v", err)
	}
	if _, err := store.Update(ctx, "news", []byte(newsJob), revision); err == nil {
		t.Error("a second client wrote over the first without being told")
	}
}

func TestAJobNobodySubmittedSaysSo(t *testing.T) {
	store := jobs(t, connect(t))

	_, _, err := store.Get(context.Background(), "nothing")
	if !errors.Is(err, bus.ErrNoJob) {
		t.Errorf("err = %v, want ErrNoJob", err)
	}
	if _, _, err := store.Job(context.Background(), "nothing"); !errors.Is(err, bus.ErrNoJob) {
		t.Errorf("err = %v", err)
	}
}

// TestAJobThatDoesNotValidateIsRefusedWhenItIsRead, so a node picking up work
// cannot be handed something the engine will not run.
func TestAJobThatDoesNotValidateIsRefusedWhenItIsRead(t *testing.T) {
	store := jobs(t, connect(t))
	ctx := context.Background()

	if _, err := store.Put(ctx, "broken", []byte(`job "broken" {}`)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Job(ctx, "broken"); err == nil {
		t.Error("a job with nothing in it was accepted")
	}
}

// TestANameThatWouldNotBeAKeyIsRefusedAtTheDoor.
func TestANameThatWouldNotBeAKeyIsRefusedAtTheDoor(t *testing.T) {
	store := jobs(t, connect(t))

	for _, name := range []string{"", "news uk", "news.uk", "news/uk", "news*"} {
		if _, err := store.Put(context.Background(), name, []byte(newsJob)); err == nil {
			t.Errorf("accepted %q as a job name", name)
		}
	}
}

func TestNamesAndDelete(t *testing.T) {
	store := jobs(t, connect(t))
	ctx := context.Background()

	if names, err := store.Names(ctx); err != nil || len(names) != 0 {
		t.Errorf("a fresh cluster has %v jobs, %v", names, err)
	}

	if _, err := store.Put(ctx, "news", []byte(newsJob)); err != nil {
		t.Fatal(err)
	}
	names, err := store.Names(ctx)
	if err != nil || len(names) != 1 || names[0] != "news" {
		t.Errorf("names = %v, %v", names, err)
	}

	if err := store.Delete(ctx, "news"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Get(ctx, "news"); !errors.Is(err, bus.ErrNoJob) {
		t.Errorf("the job survived being deleted: %v", err)
	}
}

// TestANodePicksUpWorkWithoutBeingTold, which is what a watch is for: join,
// watch, and serve whatever appears.
func TestANodePicksUpWorkWithoutBeingTold(t *testing.T) {
	store := jobs(t, connect(t))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	changes, stop, err := store.Watch(ctx)
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	defer stop()

	// The end of the replay says the cluster has no jobs, which is different
	// from not having been told yet.
	select {
	case change := <-changes:
		if !change.Replayed {
			t.Errorf("the first thing on an empty cluster was %+v", change)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the watch never said it had caught up")
	}

	if _, err := store.Put(ctx, "news", []byte(newsJob)); err != nil {
		t.Fatal(err)
	}

	select {
	case change := <-changes:
		if change.Name != "news" || change.Deleted {
			t.Errorf("change = %+v", change)
		}
		if !strings.Contains(string(change.Document), "example.com") {
			t.Error("the change arrived without the document")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a submitted job was never reported")
	}

	if err := store.Delete(ctx, "news"); err != nil {
		t.Fatal(err)
	}
	select {
	case change := <-changes:
		if !change.Deleted {
			t.Errorf("a deleted job arrived as %+v", change)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a deleted job was never reported")
	}
}

// TestANodeThatStoppedStopsBeingListed. A row that outlives its process is a
// lie an operator will act on.
func TestANodeThatStoppedStopsBeingListed(t *testing.T) {
	conn := connect(t)
	nodes, err := conn.OpenNodes(context.Background())
	if err != nil {
		t.Fatalf("open nodes: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	if err := nodes.Announce(ctx, "worker-one", []byte(`{"stages":["download"]}`)); err != nil {
		t.Fatalf("announce: %v", err)
	}

	here, err := nodes.Here(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, listed := here["worker-one"]; !listed {
		t.Fatalf("the node did not appear: %v", here)
	}

	// Gone deliberately rather than left to expire, so that a listing is right
	// immediately rather than in half a minute.
	cancel()
	for range 100 {
		here, err = nodes.Here(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if _, listed := here["worker-one"]; !listed {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Error("the node was still listed after it stopped")
}
