// SPDX-License-Identifier: GPL-3.0-or-later

package queue

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/rangertaha/scour/internal/store"
)

func harness(t *testing.T) (*Storage, *store.Store, uint) {
	t.Helper()

	s, err := store.Open(filepath.Join(t.TempDir(), "scour.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	e, err := s.CreateItem(context.Background(), "vehicle")
	if err != nil {
		t.Fatal(err)
	}
	job, err := s.JobForItem(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	q := New(context.Background(), s, e.ID, job.ID)
	if err := q.Init(); err != nil {
		t.Fatal(err)
	}
	return q, s, e.ID
}

func TestAddThenGet(t *testing.T) {
	q, _, _ := harness(t)

	if err := q.AddRequest([]byte("first")); err != nil {
		t.Fatalf("AddRequest: %v", err)
	}
	n, err := q.QueueSize()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("size = %d, want 1", n)
	}

	got, err := q.GetRequest()
	if err != nil {
		t.Fatalf("GetRequest: %v", err)
	}
	if string(got) != "first" {
		t.Errorf("got %q, want the request that was added", got)
	}

	if n, _ := q.QueueSize(); n != 0 {
		t.Errorf("size = %d, want the queue drained", n)
	}
}

func TestEmptyQueueReportsItself(t *testing.T) {
	q, _, _ := harness(t)

	_, err := q.GetRequest()
	if !errors.Is(err, store.ErrQueueEmpty) {
		t.Fatalf("err = %v, want ErrQueueEmpty so an exhausted queue is not mistaken for a broken one", err)
	}

	empty, err := q.IsEmpty()
	if err != nil {
		t.Fatal(err)
	}
	if !empty {
		t.Error("IsEmpty should report true")
	}
}

func TestFIFOWithoutAScorer(t *testing.T) {
	q, _, _ := harness(t)

	for _, s := range []string{"a", "b", "c"} {
		if err := q.AddRequest([]byte(s)); err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range []string{"a", "b", "c"} {
		got, err := q.GetRequest()
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Errorf("got %q, want %q: with no scorer the order must be the one colly would have used", got, want)
		}
	}
}

func TestScorerSetsThePriority(t *testing.T) {
	q, _, _ := harness(t)

	// The scorer stands in for M4's: here, longer requests are better.
	q.SetScorer(func(data []byte) float64 { return float64(len(data)) })

	for _, s := range []string{"a", "bbb", "cc"} {
		if err := q.AddRequest([]byte(s)); err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range []string{"bbb", "cc", "a"} {
		got, err := q.GetRequest()
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Errorf("got %q, want %q: the queue must pop in score order", got, want)
		}
	}
}

func TestQueueSurvivesReopening(t *testing.T) {
	dir := t.TempDir()
	dsn := filepath.Join(dir, "scour.db")
	ctx := context.Background()

	s, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	e, err := s.CreateItem(ctx, "vehicle")
	if err != nil {
		t.Fatal(err)
	}
	job, err := s.JobForItem(ctx, e)
	if err != nil {
		t.Fatal(err)
	}
	q := New(ctx, s, e.ID, job.ID)
	if err := q.AddRequest([]byte("pending")); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	// A new process opens the same database and finds the work waiting, which
	// is the whole point of taking the queue out of memory.
	s2, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()

	job2, err := s2.JobForItem(ctx, e)
	if err != nil {
		t.Fatal(err)
	}
	q2 := New(ctx, s2, e.ID, job2.ID)
	n, err := q2.QueueSize()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("size after reopening = %d, want the queued request to have survived", n)
	}
	got, err := q2.GetRequest()
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "pending" {
		t.Errorf("got %q, want the request stored before the restart", got)
	}
}

func TestItemsHaveSeparateQueues(t *testing.T) {
	q, s, _ := harness(t)
	ctx := context.Background()

	other, err := s.CreateItem(ctx, "article")
	if err != nil {
		t.Fatal(err)
	}
	otherJob, err := s.JobForItem(ctx, other)
	if err != nil {
		t.Fatal(err)
	}
	otherQ := New(ctx, s, other.ID, otherJob.ID)

	if err := q.AddRequest([]byte("for vehicle")); err != nil {
		t.Fatal(err)
	}

	if n, _ := otherQ.QueueSize(); n != 0 {
		t.Errorf("the other item sees %d requests, want 0", n)
	}
	if _, err := otherQ.GetRequest(); !errors.Is(err, store.ErrQueueEmpty) {
		t.Errorf("err = %v, want the other item's queue to be empty", err)
	}
}

// Seeds are queued a batch at a time, so an empty queue does not mean an
// exhausted crawl: it may only mean the next batch has not been asked for.
// A list of a million targets wrote a 660MB write-ahead log and fetched nothing
// before this, because every seed was queued before the first request went out.
func TestAnEmptyQueueAsksForMoreSeeds(t *testing.T) {
	s, _, _ := harness(t)

	var calls, left int
	left = 3
	s.SetRefill(func() int {
		calls++
		if left == 0 {
			return 0
		}
		left--
		if err := s.AddRequest([]byte("seed")); err != nil {
			t.Errorf("AddRequest: %v", err)
		}
		return 1
	})

	// Each read drains the queue and asks for the next seed.
	for i := range 3 {
		data, err := s.GetRequest()
		if err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
		if string(data) != "seed" {
			t.Fatalf("read %d = %q", i, data)
		}
	}

	// Once the seeds run out the queue is genuinely empty and says so, rather
	// than looping forever asking for more.
	if _, err := s.GetRequest(); !errors.Is(err, store.ErrQueueEmpty) {
		t.Errorf("err = %v, want ErrQueueEmpty once the seeds are exhausted", err)
	}
	if calls == 0 {
		t.Error("the refill was never consulted")
	}
}

// colly stops its loop on an empty queue, so the size has to top up too, not
// only the read.
func TestQueueSizeAsksForMoreSeeds(t *testing.T) {
	s, _, _ := harness(t)

	asked := false
	s.SetRefill(func() int {
		if asked {
			return 0
		}
		asked = true
		if err := s.AddRequest([]byte("seed")); err != nil {
			t.Errorf("AddRequest: %v", err)
		}
		return 1
	})

	n, err := s.QueueSize()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("QueueSize = %d, want 1 after topping up", n)
	}
}
