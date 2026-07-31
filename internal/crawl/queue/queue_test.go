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

	e, err := s.CreateEntity(context.Background(), "vehicle")
	if err != nil {
		t.Fatal(err)
	}
	q := New(context.Background(), s, e.ID)
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
	e, err := s.CreateEntity(ctx, "vehicle")
	if err != nil {
		t.Fatal(err)
	}
	q := New(ctx, s, e.ID)
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

	q2 := New(ctx, s2, e.ID)
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

func TestEntitiesHaveSeparateQueues(t *testing.T) {
	q, s, _ := harness(t)
	ctx := context.Background()

	other, err := s.CreateEntity(ctx, "article")
	if err != nil {
		t.Fatal(err)
	}
	otherQ := New(ctx, s, other.ID)

	if err := q.AddRequest([]byte("for vehicle")); err != nil {
		t.Fatal(err)
	}

	if n, _ := otherQ.QueueSize(); n != 0 {
		t.Errorf("the other entity sees %d requests, want 0", n)
	}
	if _, err := otherQ.GetRequest(); !errors.Is(err, store.ErrQueueEmpty) {
		t.Errorf("err = %v, want the other entity's queue to be empty", err)
	}
}
