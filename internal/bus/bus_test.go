// SPDX-License-Identifier: GPL-3.0-or-later

package bus

import (
	"context"
	"sync"
	"testing"
	"time"
)

func open(t *testing.T) *Bus {
	t.Helper()
	b, err := Open(context.Background(), Options{Name: "test", Timeout: 15 * time.Second})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { b.Close() })
	return b
}

func TestEmbeddedBrokerNeedsNothingInstalled(t *testing.T) {
	b := open(t)
	if b.Conn() == nil || !b.Conn().IsConnected() {
		t.Fatal("not connected to the embedded broker")
	}
	if b.JetStream() == nil {
		t.Fatal("no jetstream")
	}
}

func TestPublishThenConsume(t *testing.T) {
	b := open(t)
	ctx := context.Background()

	var (
		mu   sync.Mutex
		seen []string
	)
	stop, err := b.Consume(ctx, StreamCrawl, "test-fetched", AllEntities(SubjectFetched),
		func(_ context.Context, data []byte) error {
			mu.Lock()
			defer mu.Unlock()
			seen = append(seen, string(data))
			return nil
		})
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}
	defer stop()

	if err := b.Publish(ctx, Subject("vehicle", SubjectFetched), "one", Fetched{URL: "http://example.com/"}); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(seen) == 1
	}, "the message to arrive")
}

// At-least-once delivery is only safe to build on if a repeat is cheap, and
// the cheapest repeat is one the broker collapses before anyone sees it.
func TestDuplicateIDsCollapse(t *testing.T) {
	b := open(t)
	ctx := context.Background()

	var (
		mu sync.Mutex
		n  int
	)
	stop, err := b.Consume(ctx, StreamCrawl, "test-dupes", AllEntities(SubjectDiscovered),
		func(context.Context, []byte) error {
			mu.Lock()
			defer mu.Unlock()
			n++
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()

	for range 3 {
		err := b.Publish(ctx, Subject("vehicle", SubjectDiscovered), "same-url",
			Discovered{URL: "http://example.com/cars/"})
		if err != nil {
			t.Fatal(err)
		}
	}

	waitFor(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return n >= 1
	}, "the first message")

	time.Sleep(200 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if n != 1 {
		t.Errorf("delivered %d times, want the duplicates suppressed", n)
	}
}

// A command that prints what another component wrote has to wait for it. This
// is the barrier that makes that safe.
func TestDrainWaitsForConsumers(t *testing.T) {
	b := open(t)
	ctx := context.Background()

	release := make(chan struct{})
	stop, err := b.Consume(ctx, StreamCrawl, "test-slow", AllEntities(SubjectFetched),
		func(context.Context, []byte) error {
			<-release
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()

	if err := b.Publish(ctx, Subject("vehicle", SubjectFetched), "slow", Fetched{URL: "http://x/"}); err != nil {
		t.Fatal(err)
	}

	quick, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	if err := b.Drain(quick, StreamCrawl); err == nil {
		t.Error("Drain returned while a message was still unacknowledged")
	}

	close(release)
	slow, cancel2 := context.WithTimeout(ctx, 10*time.Second)
	defer cancel2()
	if err := b.Drain(slow, StreamCrawl); err != nil {
		t.Errorf("Drain: %v", err)
	}
}

func TestSubjectsAreScopedPerEntity(t *testing.T) {
	if got := Subject("vehicle", SubjectFetched); got != "scour.vehicle.fetched" {
		t.Errorf("Subject = %q", got)
	}
	if got := AllEntities(SubjectFetched); got != "scour.*.fetched" {
		t.Errorf("AllEntities = %q", got)
	}

	// An entity named with NATS syntax must not be able to widen a
	// subscription or split a subject.
	for _, name := range []string{"a.b", "a*b", "a>b", "a b"} {
		got := Subject(name, SubjectFetched)
		if got != "scour.a_b.fetched" {
			t.Errorf("Subject(%q) = %q, want the special characters neutralised", name, got)
		}
	}
}

func TestPendingCountsWaitingMessages(t *testing.T) {
	b := open(t)
	ctx := context.Background()

	if n, err := b.Pending(ctx, StreamRecords); err != nil || n != 0 {
		t.Fatalf("Pending on an empty stream = %d, %v", n, err)
	}

	if err := b.Publish(ctx, Subject("vehicle", SubjectRecord), "r1", map[string]string{"a": "b"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		n, err := b.Pending(ctx, StreamRecords)
		return err == nil && n == 1
	}, "the message to be counted")
}

func TestCloseIsSafeTwice(t *testing.T) {
	b, err := Open(context.Background(), Options{Timeout: 15 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if err := b.Close(); err != nil {
		t.Errorf("closing twice: %v", err)
	}
}

func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
