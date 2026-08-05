// SPDX-License-Identifier: GPL-3.0-or-later

package memory_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rangertaha/scour/internal/frontier"
	"github.com/rangertaha/scour/internal/frontier/frontiertest"
	"github.com/rangertaha/scour/internal/frontier/memory"
)

func open(t testing.TB, cfg frontier.Config) frontier.Frontier {
	t.Helper()
	f, err := memory.Open(cfg)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

func TestContract(t *testing.T)      { frontiertest.Run(t, open) }
func BenchmarkFrontier(b *testing.B) { frontiertest.Benchmark(b, open) }

func TestBadPolicyIsRefusedAtOpen(t *testing.T) {
	if _, err := memory.Open(frontier.Config{Policy: "carrier-pigeon"}); err == nil {
		t.Fatal("opened a frontier with a policy that does not exist")
	}
}

func TestDefaultPolicyIsPriority(t *testing.T) {
	f, err := memory.Open(frontier.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	if f.Policy() != frontier.DefaultPolicy {
		t.Errorf("policy = %q, want %q", f.Policy(), frontier.DefaultPolicy)
	}
}

// TestRandomSamplesRatherThanOrders is the one policy whose value is that it
// does not agree with itself: every other ordering answers a question the
// scorer already had an opinion about.
func TestRandomSamplesRatherThanOrders(t *testing.T) {
	seen := map[string]bool{}

	for run := 0; run < 40; run++ {
		f, err := memory.Open(frontier.Config{Policy: "random"})
		if err != nil {
			t.Fatal(err)
		}

		var reqs []frontier.Request
		for i := range 8 {
			reqs = append(reqs, frontiertest.Req(
				string(rune('a'+i)), "example.com", 0, float64(i), 0))
		}
		if _, err := f.Add(context.Background(), "test", reqs...); err != nil {
			t.Fatal(err)
		}

		first, err := f.Lease(context.Background(), "test", frontiertest.Origin, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		seen[first.URL] = true
		f.Close()
	}

	// Not a distribution test, just that it is not secretly sorted: forty
	// draws from eight should touch more than one.
	if len(seen) < 3 {
		t.Errorf("forty draws produced %d distinct first URLs, which is an ordering", len(seen))
	}
}

// TestConcurrentLeasesAreExclusive: a crawl leases from several goroutines, and
// two crawlers handed the same page is the bug this whole interface exists to
// prevent.
func TestConcurrentLeasesAreExclusive(t *testing.T) {
	f := open(t, frontier.Config{})
	ctx := context.Background()

	const n = 500
	var reqs []frontier.Request
	for i := range n {
		reqs = append(reqs, frontiertest.Req(
			strings.Repeat("x", i%5)+string(rune('a'+i%26))+string(rune('0'+i%10))+itoa(i),
			"example.com", 0, float64(i), 0))
	}
	if _, err := f.Add(ctx, "test", reqs...); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	handed := map[string]int{}

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				req, err := f.Lease(ctx, "test", frontiertest.Origin, time.Minute)
				if err != nil {
					return
				}
				mu.Lock()
				handed[req.Hash]++
				mu.Unlock()
				if err := f.Done(ctx, "test", req.Hash); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()

	for hash, times := range handed {
		if times != 1 {
			t.Errorf("%s was handed out %d times", hash, times)
		}
	}
	if len(handed) != n {
		t.Errorf("handed out %d of %d requests", len(handed), n)
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}
