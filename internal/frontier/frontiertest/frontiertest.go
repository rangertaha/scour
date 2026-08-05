// SPDX-License-Identifier: GPL-3.0-or-later

// Package frontiertest is the contract every frontier must satisfy, and the
// benchmarks every frontier is measured by.
//
// Two suites, and the second is as important as the first. A frontier that is
// correct and takes a millisecond to lease is not a frontier: a crawl leases
// once per page, so lease cost is the ceiling on how fast anything can go. Both
// implementations run the same workloads, so the durable one can be compared
// against memory rather than against a hope.
//
// Time is passed in rather than slept through. Proving a two second crawl delay
// by waiting two seconds is a test nobody runs twice.
package frontiertest

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/rangertaha/scour/internal/frontier"
)

// Open returns an empty frontier built with the given config, and registers
// its cleanup.
type Open func(t testing.TB, cfg frontier.Config) frontier.Frontier

// Origin is the instant every test counts from, so failures read as offsets
// rather than as timestamps.
var Origin = time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)

const job = "test"

// Req builds a request. The hash is the URL, which is enough for a test and
// makes a failure say which URL rather than which digest.
func Req(url, host string, depth int, score float64, at time.Duration) frontier.Request {
	return frontier.Request{
		URL:        url,
		Host:       host,
		Hash:       url,
		Depth:      depth,
		Score:      score,
		Discovered: Origin.Add(at),
	}
}

// Run exercises the whole contract.
func Run(t *testing.T, open Open) {
	t.Helper()

	t.Run("AddAndLease", func(t *testing.T) { testAddAndLease(t, open) })
	t.Run("DeduplicatesByHash", func(t *testing.T) { testDedup(t, open) })
	t.Run("EmptyIsNotAnError", func(t *testing.T) { testEmpty(t, open) })
	t.Run("LeaseIsExclusive", func(t *testing.T) { testExclusive(t, open) })
	t.Run("LeaseExpires", func(t *testing.T) { testLeaseExpires(t, open) })
	t.Run("DoneIsFinal", func(t *testing.T) { testDone(t, open) })
	t.Run("FailRetriesThenAbandons", func(t *testing.T) { testFail(t, open) })
	t.Run("PolitenessPacesAHost", func(t *testing.T) { testPoliteness(t, open) })
	t.Run("PolitenessIsPerHost", func(t *testing.T) { testPolitenessPerHost(t, open) })
	t.Run("JobsAreSeparate", func(t *testing.T) { testJobs(t, open) })
	t.Run("Priority", func(t *testing.T) { testPriority(t, open) })
	t.Run("Breadth", func(t *testing.T) { testBreadth(t, open) })
	t.Run("Depth", func(t *testing.T) { testDepth(t, open) })
	t.Run("PolicyIsCheckedWhenBuilt", func(t *testing.T) { testBadPolicy(t, open) })
}

func lease(t testing.TB, f frontier.Frontier, at time.Duration) *frontier.Request {
	t.Helper()
	req, err := f.Lease(context.Background(), job, Origin.Add(at), time.Minute)
	if err != nil {
		return nil
	}
	return req
}

func add(t testing.TB, f frontier.Frontier, reqs ...frontier.Request) int {
	t.Helper()
	n, err := f.Add(context.Background(), job, reqs...)
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	return n
}

// order leases everything available and reports the URLs in the order they
// came out, which is what every policy test asserts on.
func order(t testing.TB, f frontier.Frontier) []string {
	t.Helper()

	var out []string
	for i := 0; i < 100; i++ {
		req := lease(t, f, time.Duration(i)*time.Hour)
		if req == nil {
			break
		}
		out = append(out, req.URL)
		if err := f.Done(context.Background(), job, req.Hash); err != nil {
			t.Fatalf("done: %v", err)
		}
	}
	return out
}

func testAddAndLease(t *testing.T, open Open) {
	f := open(t, frontier.Config{})

	if n := add(t, f, Req("/a", "example.com", 0, 1, 0)); n != 1 {
		t.Fatalf("added %d, want 1", n)
	}

	req := lease(t, f, 0)
	if req == nil || req.URL != "/a" {
		t.Fatalf("leased %v", req)
	}
	if n, _ := f.Len(context.Background(), job); n != 1 {
		t.Errorf("a leased request is not waiting any more: len %d, want 1", n)
	}
}

// testDedup: re-discovering a URL is not news. A crawl finds the same link on
// every page of a site, and a frontier that queued each one would fetch a page
// as many times as it was linked to.
func testDedup(t *testing.T, open Open) {
	f := open(t, frontier.Config{})

	add(t, f, Req("/a", "example.com", 0, 1, 0))
	if n := add(t, f, Req("/a", "example.com", 3, 9, time.Hour)); n != 0 {
		t.Errorf("re-adding a known URL added %d", n)
	}

	// And the first sighting wins, because it carries the path that found it.
	req := lease(t, f, 0)
	if req == nil || req.Depth != 0 {
		t.Errorf("the later, deeper sighting replaced the first: %v", req)
	}
}

func testEmpty(t *testing.T, open Open) {
	f := open(t, frontier.Config{})

	_, err := f.Lease(context.Background(), job, Origin, time.Minute)
	if err != frontier.ErrEmpty {
		t.Fatalf("err = %v, want ErrEmpty", err)
	}
	if n, err := f.Len(context.Background(), job); err != nil || n != 0 {
		t.Errorf("len = %d, %v", n, err)
	}
}

// testExclusive: two crawlers must not be handed the same page.
func testExclusive(t *testing.T, open Open) {
	f := open(t, frontier.Config{})
	add(t, f, Req("/a", "example.com", 0, 1, 0))

	if req := lease(t, f, 0); req == nil {
		t.Fatal("nothing leased")
	}
	if req := lease(t, f, 0); req != nil {
		t.Errorf("the same request was handed out twice: %v", req)
	}
}

// testLeaseExpires: a crawler that died holding work must not take it with it.
func testLeaseExpires(t *testing.T, open Open) {
	f := open(t, frontier.Config{})
	add(t, f, Req("/a", "example.com", 0, 1, 0))

	if req := lease(t, f, 0); req == nil {
		t.Fatal("nothing leased")
	}
	if req := lease(t, f, 30*time.Second); req != nil {
		t.Error("a live lease was handed out again")
	}
	if req := lease(t, f, 2*time.Minute); req == nil {
		t.Error("an expired lease was never handed out again")
	}
}

func testDone(t *testing.T, open Open) {
	f := open(t, frontier.Config{})
	add(t, f, Req("/a", "example.com", 0, 1, 0))

	req := lease(t, f, 0)
	if err := f.Done(context.Background(), job, req.Hash); err != nil {
		t.Fatalf("done: %v", err)
	}
	if req := lease(t, f, time.Hour); req != nil {
		t.Error("a finished request came back")
	}
	if n, _ := f.Len(context.Background(), job); n != 0 {
		t.Errorf("len = %d after finishing the only request", n)
	}
}

// testFail: a failure is retried, and then it is not. A request nothing will
// ever report on must not cycle for the length of the crawl.
func testFail(t *testing.T, open Open) {
	f := open(t, frontier.Config{})
	add(t, f, Req("/a", "example.com", 0, 1, 0))

	ctx := context.Background()
	var handed int
	for i := 0; i < frontier.MaxAttempts+3; i++ {
		req := lease(t, f, time.Duration(i)*time.Hour)
		if req == nil {
			break
		}
		handed++
		if err := f.Fail(ctx, job, req.Hash); err != nil {
			t.Fatalf("fail: %v", err)
		}
	}

	if handed != frontier.MaxAttempts {
		t.Errorf("handed out %d times, want %d", handed, frontier.MaxAttempts)
	}
}

// testPoliteness is the constraint the whole stage exists for.
func testPoliteness(t *testing.T, open Open) {
	f := open(t, frontier.Config{Rate: 2 * time.Second})

	add(t, f,
		Req("/a", "example.com", 0, 9, 0),
		Req("/b", "example.com", 0, 8, time.Second),
	)

	if req := lease(t, f, 0); req == nil || req.URL != "/a" {
		t.Fatalf("first lease = %v", req)
	}
	if req := lease(t, f, time.Second); req != nil {
		t.Errorf("the host was hit again after 1s of a 2s delay: %v", req)
	}
	if req := lease(t, f, 2*time.Second); req == nil || req.URL != "/b" {
		t.Errorf("the host was still cooling after its delay: %v", req)
	}
}

// testPolitenessPerHost: a slow host must not stall a fast one.
func testPolitenessPerHost(t *testing.T, open Open) {
	f := open(t, frontier.Config{Rate: time.Minute})

	add(t, f,
		Req("/a", "slow.example", 0, 9, 0),
		Req("/b", "fast.example", 0, 1, 0),
	)

	if req := lease(t, f, 0); req == nil || req.Host != "slow.example" {
		t.Fatalf("first lease = %v", req)
	}
	req := lease(t, f, time.Second)
	if req == nil {
		t.Fatal("one cooling host stalled the whole frontier")
	}
	if req.Host != "fast.example" {
		t.Errorf("leased %s while it was cooling", req.Host)
	}
}

func testJobs(t *testing.T, open Open) {
	f := open(t, frontier.Config{})
	ctx := context.Background()

	if _, err := f.Add(ctx, "one", Req("/a", "example.com", 0, 1, 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Add(ctx, "two", Req("/b", "example.com", 0, 1, 0)); err != nil {
		t.Fatal(err)
	}

	req, err := f.Lease(ctx, "one", Origin, time.Minute)
	if err != nil || req.URL != "/a" {
		t.Fatalf("job one leased %v, %v", req, err)
	}
	if n, _ := f.Len(ctx, "two"); n != 1 {
		t.Errorf("job two has %d waiting, want its own 1", n)
	}
}

// The orderings. Each asserts what its name claims, against a frontier holding
// the same five requests.

func spread(t testing.TB, f frontier.Frontier) {
	t.Helper()
	add(t, f,
		Req("/d0-lo", "a.example", 0, 0.1, 0),
		Req("/d0-hi", "b.example", 0, 0.9, time.Second),
		Req("/d1-mid", "c.example", 1, 0.5, 2*time.Second),
		Req("/d2-hi", "d.example", 2, 0.8, 3*time.Second),
		Req("/d1-lo", "e.example", 1, 0.2, 4*time.Second),
	)
}

func testPriority(t *testing.T, open Open) {
	f := open(t, frontier.Config{Policy: "priority"})
	spread(t, f)

	want := "/d0-hi /d2-hi /d1-mid /d1-lo /d0-lo"
	if got := strings.Join(order(t, f), " "); got != want {
		t.Errorf("priority order\n got %s\nwant %s", got, want)
	}
}

func testBreadth(t *testing.T, open Open) {
	f := open(t, frontier.Config{Policy: "breadth"})
	spread(t, f)

	// Every URL at one depth before any at the next, discovery order within.
	want := "/d0-lo /d0-hi /d1-mid /d1-lo /d2-hi"
	if got := strings.Join(order(t, f), " "); got != want {
		t.Errorf("breadth order\n got %s\nwant %s", got, want)
	}
}

func testDepth(t *testing.T, open Open) {
	f := open(t, frontier.Config{Policy: "depth"})
	spread(t, f)

	want := "/d2-hi /d1-lo /d1-mid /d0-hi /d0-lo"
	if got := strings.Join(order(t, f), " "); got != want {
		t.Errorf("depth order\n got %s\nwant %s", got, want)
	}
}

// testBadPolicy: a policy that does not exist is refused when the frontier is
// built, not discovered when the first URL comes out in the wrong order.
func testBadPolicy(t *testing.T, open Open) {
	if _, err := frontier.Policies("carrier-pigeon"); err == nil {
		t.Fatal("accepted a policy that does not exist")
	} else if !strings.Contains(err.Error(), "priority") {
		t.Errorf("the error does not list what there is: %v", err)
	}
}

// Benchmarks. Shared, so two implementations are compared on identical
// workloads rather than on whatever each one's author found convenient.

// Benchmark runs every measurement against one implementation.
func Benchmark(b *testing.B, open Open) {
	b.Helper()

	for _, size := range []int{1_000, 100_000} {
		b.Run(fmt.Sprintf("Lease/%d", size), func(b *testing.B) { benchLease(b, open, size) })
	}
	b.Run("Add/batch", func(b *testing.B) { benchAdd(b, open) })
	b.Run("Add/duplicates", func(b *testing.B) { benchDedup(b, open) })

	for _, policy := range frontier.PolicyNames() {
		b.Run("Policy/"+policy, func(b *testing.B) { benchPolicy(b, open, policy) })
	}
	b.Run("Politeness/manyHosts", func(b *testing.B) { benchHosts(b, open) })
}

// fill queues n requests spread over hosts, which is the shape a real frontier
// has: a few hundred hosts and a great many URLs.
func fill(b *testing.B, f frontier.Frontier, n, hosts int) {
	b.Helper()

	reqs := make([]frontier.Request, 0, n)
	for i := 0; i < n; i++ {
		reqs = append(reqs, Req(
			fmt.Sprintf("/page/%d", i),
			fmt.Sprintf("host%d.example", i%hosts),
			i%6,
			float64(i%1000)/1000,
			time.Duration(i)*time.Millisecond,
		))
	}
	if _, err := f.Add(context.Background(), job, reqs...); err != nil {
		b.Fatalf("fill: %v", err)
	}
}

// benchLease is the measurement that matters most: a crawl leases once per
// page, so this is the ceiling on how fast anything downstream can go.
func benchLease(b *testing.B, open Open, size int) {
	f := open(b, frontier.Config{})
	fill(b, f, size, 200)

	ctx := context.Background()
	now := Origin

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		now = now.Add(time.Millisecond)
		req, err := f.Lease(ctx, job, now, time.Minute)
		if err == frontier.ErrEmpty {
			b.StopTimer()
			fill(b, f, size, 200)
			now = now.Add(time.Hour)
			b.StartTimer()
			continue
		}
		if err != nil {
			b.Fatalf("lease: %v", err)
		}
		if err := f.Done(ctx, job, req.Hash); err != nil {
			b.Fatalf("done: %v", err)
		}
	}
}

func benchAdd(b *testing.B, open Open) {
	f := open(b, frontier.Config{})
	ctx := context.Background()

	batch := make([]frontier.Request, 100)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for j := range batch {
			batch[j] = Req(fmt.Sprintf("/p/%d/%d", i, j), "example.com", 1, 0.5, 0)
		}
		if _, err := f.Add(ctx, job, batch...); err != nil {
			b.Fatalf("add: %v", err)
		}
	}
}

// benchDedup is the common case rather than the exceptional one: a crawl
// re-discovers the same navigation links on every page it fetches.
func benchDedup(b *testing.B, open Open) {
	f := open(b, frontier.Config{})
	ctx := context.Background()
	fill(b, f, 10_000, 50)

	batch := make([]frontier.Request, 100)
	for j := range batch {
		batch[j] = Req(fmt.Sprintf("/page/%d", j), "host0.example", 1, 0.5, 0)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := f.Add(ctx, job, batch...); err != nil {
			b.Fatalf("add: %v", err)
		}
	}
}

// benchPolicy compares the orderings on identical data, so the cost of choosing
// one is visible rather than assumed.
func benchPolicy(b *testing.B, open Open, policy string) {
	f := open(b, frontier.Config{Policy: policy})
	fill(b, f, 10_000, 100)

	ctx := context.Background()
	now := Origin

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		now = now.Add(time.Millisecond)
		req, err := f.Lease(ctx, job, now, time.Minute)
		if err == frontier.ErrEmpty {
			b.StopTimer()
			fill(b, f, 10_000, 100)
			b.StartTimer()
			continue
		}
		if err != nil {
			b.Fatalf("lease: %v", err)
		}
		_ = f.Done(ctx, job, req.Hash)
	}
}

// benchHosts is the politeness path under the load that makes it expensive:
// many hosts, most of them cooling, so a lease has to look past them.
func benchHosts(b *testing.B, open Open) {
	f := open(b, frontier.Config{Rate: 30 * time.Second})
	fill(b, f, 50_000, 5_000)

	ctx := context.Background()
	now := Origin

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		now = now.Add(10 * time.Millisecond)
		req, err := f.Lease(ctx, job, now, time.Minute)
		if err == frontier.ErrEmpty {
			now = now.Add(time.Minute)
			continue
		}
		if err != nil {
			b.Fatalf("lease: %v", err)
		}
		_ = f.Done(ctx, job, req.Hash)
	}
}
