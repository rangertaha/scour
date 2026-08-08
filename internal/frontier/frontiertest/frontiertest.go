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
	"slices"
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
	t.Run("AStaleReportDoesNotFreeALiveLease", func(t *testing.T) { testStaleReport(t, open) })
	t.Run("DoneIsFinal", func(t *testing.T) { testDone(t, open) })
	t.Run("FailRetriesThenAbandons", func(t *testing.T) { testFail(t, open) })
	t.Run("PolitenessPacesAHost", func(t *testing.T) { testPoliteness(t, open) })
	t.Run("PolitenessIsPerHost", func(t *testing.T) { testPolitenessPerHost(t, open) })
	t.Run("APacedHostIsCrawledAtItsOwnDelay", func(t *testing.T) { testPace(t, open) })
	t.Run("APaceNeverOutrunsTheJobsRate", func(t *testing.T) { testPaceFloor(t, open) })
	t.Run("APaceHoldsTheHostOffAtOnce", func(t *testing.T) { testPaceHolds(t, open) })
	t.Run("APaceIsSharedBetweenJobs", func(t *testing.T) { testPaceShared(t, open) })
	t.Run("ASiteThatAsksForNothingIsNotSlowedDown", func(t *testing.T) { testPaceCostsNothing(t, open) })
	t.Run("JobsAreSeparate", func(t *testing.T) { testJobs(t, open) })
	t.Run("Priority", func(t *testing.T) { testPriority(t, open) })
	t.Run("Breadth", func(t *testing.T) { testBreadth(t, open) })
	t.Run("Depth", func(t *testing.T) { testDepth(t, open) })
	t.Run("BestFirstAcrossHostsWithSeveralEach", func(t *testing.T) { testBestFirstSeveralEach(t, open) })
	t.Run("TheBestOfWhatIsFreeWinsWhileOthersCool", func(t *testing.T) { testBestOfWhatIsFree(t, open) })
	t.Run("AFailedRequestRestoresItsHostsPlace", func(t *testing.T) { testFailRestoresPlace(t, open) })
	t.Run("PolicyIsCheckedWhenBuilt", func(t *testing.T) { testBadPolicy(t, open) })
	t.Run("AFullTieIsBrokenTheSameWay", func(t *testing.T) { testAFullTieIsBrokenTheSameWay(t, open) })
	t.Run("AnUnorderedPolicyIsNotShapedByInsertion", func(t *testing.T) { testAnUnorderedPolicyIsNotShapedByInsertion(t, open) })
}

func lease(t testing.TB, f frontier.Frontier, at time.Duration) *frontier.Request {
	t.Helper()
	req, err := f.Lease(context.Background(), job, Origin.Add(at), time.Minute)
	if err != nil {
		return nil
	}
	return req
}

// done reports a leased request as finished, so a test reads as a timeline
// rather than as error handling.
func done(t testing.TB, f frontier.Frontier, req *frontier.Request) {
	t.Helper()
	if err := f.Done(context.Background(), job, req.Hash, req.Attempt); err != nil {
		t.Fatalf("done %s: %v", req.URL, err)
	}
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
		if err := f.Done(context.Background(), job, req.Hash, req.Attempt); err != nil {
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

// testStaleReport: a worker whose hold has already been taken off it must not
// be able to free the URL somebody else is fetching.
//
// This is the exclusivity invariant the whole stage exists for, and no lease
// here has expired unnoticed: the second hold is legitimate, and the first
// worker is merely late. If a report acts on the URL alone then a fetch that
// fails six minutes into a five minute hold clears the hold of a worker that is
// still working, and the next lease hands the same page to a third.
func testStaleReport(t *testing.T, open Open) {
	f := open(t, frontier.Config{})
	ctx := context.Background()
	add(t, f, Req("/a", "example.com", 0, 1, 0))

	// One worker takes it and then stalls past its hold.
	first := lease(t, f, 0)
	if first == nil {
		t.Fatal("nothing leased")
	}

	// A second worker takes it over, which is what an expiry is for.
	second := lease(t, f, 2*time.Minute)
	if second == nil {
		t.Fatal("an expired lease was never handed out again")
	}
	if second.Attempt == first.Attempt {
		t.Fatalf("two holds on one URL cannot be told apart: both are attempt %d", first.Attempt)
	}

	// The first worker's fetch finally fails, and it says so.
	if err := f.Fail(ctx, job, first.Hash, first.Attempt); err != nil {
		t.Fatalf("fail: %v", err)
	}
	if req := lease(t, f, 2*time.Minute+time.Second); req != nil {
		t.Errorf("a stale failure freed a live lease: %s went to a third worker while the second held it", req.URL)
	}

	// And a stale success is no better: it would finish a page still being
	// fetched, and lose whatever that fetch returns.
	if err := f.Done(ctx, job, first.Hash, first.Attempt); err != nil {
		t.Fatalf("done: %v", err)
	}
	if n, _ := f.Len(ctx, job); n != 1 {
		t.Errorf("a stale completion finished a request another worker holds: len %d, want 1", n)
	}

	// The holder's own report is still heard, or the fence would have replaced
	// one bug with a frontier that never drains.
	if err := f.Done(ctx, job, second.Hash, second.Attempt); err != nil {
		t.Fatalf("done: %v", err)
	}
	if n, _ := f.Len(ctx, job); n != 0 {
		t.Errorf("the holder's own report was ignored as well: len %d, want 0", n)
	}
}

func testDone(t *testing.T, open Open) {
	f := open(t, frontier.Config{})
	add(t, f, Req("/a", "example.com", 0, 1, 0))

	req := lease(t, f, 0)
	if err := f.Done(context.Background(), job, req.Hash, req.Attempt); err != nil {
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
		if err := f.Fail(ctx, job, req.Hash, req.Attempt); err != nil {
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

// pace is Pace with the suite's clock, so a test reads as a timeline.
func pace(t testing.TB, f frontier.Frontier, host string, at, delay time.Duration) {
	t.Helper()
	if err := f.Pace(context.Background(), host, Origin.Add(at), delay); err != nil {
		t.Fatalf("pace %s: %v", host, err)
	}
}

// testPace: a site that asks for longer than the job's rate gets it.
//
// This is the whole of what `Crawl-delay` is, and it was parsed and thrown
// away: robots.Rules.Delay had no caller anywhere, so a site asking for ten
// seconds was crawled every second and nothing anywhere said so.
func testPace(t *testing.T, open Open) {
	f := open(t, frontier.Config{Rate: time.Second})

	add(t, f,
		Req("/a", "example.com", 0, 9, 0),
		Req("/b", "example.com", 0, 8, 0),
	)

	if req := lease(t, f, 0); req == nil || req.URL != "/a" {
		t.Fatalf("first lease = %v", req)
	}
	pace(t, f, "example.com", 0, 10*time.Second)

	if req := lease(t, f, 2*time.Second); req != nil {
		t.Errorf("the site asked for 10s and was hit again after 2: %v", req)
	}
	if req := lease(t, f, 10*time.Second); req == nil || req.URL != "/b" {
		t.Errorf("the host was still cooling after the delay it asked for: %v", req)
	}
}

// testPaceFloor: a permissive robots.txt does not make a job faster than it
// configured itself to be.
//
// The two settings answer different questions and the answer is the longer of
// them. A site saying it does not mind is not a site asking to be hammered, and
// an operator who wrote `rate = "5s"` meant it.
func testPaceFloor(t *testing.T, open Open) {
	f := open(t, frontier.Config{Rate: 5 * time.Second})

	add(t, f,
		Req("/a", "example.com", 0, 9, 0),
		Req("/b", "example.com", 0, 8, 0),
	)

	if req := lease(t, f, 0); req == nil {
		t.Fatal("first lease came back empty")
	}
	pace(t, f, "example.com", 0, time.Second)

	if req := lease(t, f, 2*time.Second); req != nil {
		t.Errorf("a 1s crawl-delay overrode the job's own 5s rate: %v", req)
	}
	if req := lease(t, f, 5*time.Second); req == nil {
		t.Error("the job's own rate was not honoured either")
	}
}

// testPaceHolds: the delay applies from the request that discovered it.
//
// A crawl-delay is learnt by fetching robots.txt, which happens on the first
// request to a host, by which point that host has already been paced at the
// job's rate. Recording the number without acting on it would let the second
// request go out at the old rate, and the second request is exactly the one
// that shows whether the file was read.
func testPaceHolds(t *testing.T, open Open) {
	f := open(t, frontier.Config{Rate: time.Second})

	add(t, f,
		Req("/a", "example.com", 0, 9, 0),
		Req("/b", "example.com", 0, 8, 0),
	)

	if req := lease(t, f, 0); req == nil {
		t.Fatal("first lease came back empty")
	}
	pace(t, f, "example.com", 0, 30*time.Second)

	// The job's rate would have allowed this at 1s.
	if req := lease(t, f, time.Second); req != nil {
		t.Errorf("the second request went out at the job's rate, not the site's: %v", req)
	}

	// And a host already cooling for longer is not pulled in by a later,
	// shorter pace: politeness only ever moves one way.
	pace(t, f, "example.com", time.Second, time.Second)
	if req := lease(t, f, 3*time.Second); req != nil {
		t.Errorf("a shorter delay cut short a hold already taken: %v", req)
	}
}

// testPaceShared: robots.txt is the host's instruction to everybody.
//
// Host state is shared between jobs because a rate limit is per host, and where
// the number came from does not change that. Two jobs on one site honour one
// crawl-delay between them, not one each.
func testPaceShared(t *testing.T, open Open) {
	f := open(t, frontier.Config{Rate: time.Second})
	ctx := context.Background()

	if _, err := f.Add(ctx, "one", Req("/a", "example.com", 0, 9, 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Add(ctx, "two", Req("/b", "example.com", 0, 9, 0)); err != nil {
		t.Fatal(err)
	}

	if err := f.Pace(ctx, "example.com", Origin, 10*time.Second); err != nil {
		t.Fatalf("pace: %v", err)
	}

	if req, _ := f.Lease(ctx, "two", Origin.Add(2*time.Second), time.Minute); req != nil {
		t.Errorf("a second job crawled the site at its own pace: %v", req)
	}
	if req, _ := f.Lease(ctx, "two", Origin.Add(10*time.Second), time.Minute); req == nil {
		t.Error("the second job never got the host back")
	}
}

// testPaceCostsNothing: the overwhelmingly common case must not get slower.
//
// Most sites have no robots.txt at all, and most that do ask for no delay, so
// almost every host is paced with zero. That arrives after the page came back,
// which is the trap: a pace that re-applied the job's own rate from the moment
// it was told would push the host out by a rate measured from the end of the
// fetch rather than the start, and every crawl in the world would slow down by
// one page's fetch time per page to support a setting nearly nobody sets.
func testPaceCostsNothing(t *testing.T, open Open) {
	f := open(t, frontier.Config{Rate: 2 * time.Second})

	add(t, f,
		Req("/a", "example.com", 0, 9, 0),
		Req("/b", "example.com", 0, 8, 0),
	)

	// Leased at 0, so the next handout is due at 2s.
	if req := lease(t, f, 0); req == nil {
		t.Fatal("first lease came back empty")
	}

	// The page took a second to arrive, and the site asked for nothing.
	pace(t, f, "example.com", time.Second, 0)

	if req := lease(t, f, 2*time.Second); req == nil {
		t.Error("a site that asked for no delay was held past the job's own rate, " +
			"so being told about robots.txt made the crawl slower")
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
// The ordering tests above use [spread], which gives every host exactly one URL
// and no politeness. That is the shape in which "the best URL on each host" is
// trivially the same thing as "the best URL", so an implementation that kept a
// summary per host would be right there whatever it did with it.
//
// These three are the shape where the two come apart. They pass against an
// implementation that reads the queue directly and asks the question afresh
// every time, which is what both of them do today. They exist for the one that
// does not: a frontier fast enough to schedule by host has to keep something
// per host, and a kept thing is a thing that can go stale. Every way it can is
// below.

// testBestFirstSeveralEach: best-first is across the whole frontier, not within
// each host.
//
// A host is a queue of its own for politeness, and that is a fact about pacing
// rather than about ordering. A crawl that took the best from each host in turn
// would be round-robin wearing a priority label, and a focused crawl is exactly
// the thing that must not be: its whole claim is that it fetches the most
// promising page next, wherever it lives.
func testBestFirstSeveralEach(t *testing.T, open Open) {
	f := open(t, frontier.Config{Policy: "priority"})

	add(t, f,
		Req("/a-9", "a.example", 0, 0.9, 0),
		Req("/a-3", "a.example", 0, 0.3, time.Second),
		Req("/b-8", "b.example", 0, 0.8, 2*time.Second),
		Req("/b-7", "b.example", 0, 0.7, 3*time.Second),
		Req("/c-5", "c.example", 0, 0.5, 4*time.Second),
		Req("/c-1", "c.example", 0, 0.1, 5*time.Second),
	)

	want := "/a-9 /b-8 /b-7 /c-5 /a-3 /c-1"
	if got := strings.Join(order(t, f), " "); got != want {
		t.Errorf("priority order across hosts\n got %s\nwant %s", got, want)
	}
}

// testBestOfWhatIsFree: while a host is cooling it is out of the running, and
// when it comes back it comes back with what it has left.
//
// Two things at once, and the second is the one worth stating. A host that
// returns has had its best URL taken already, so whatever an implementation
// remembers about how good that host is has to have changed when the URL left.
// Remembering the score of a page that has already been fetched puts the host
// permanently ahead of where it belongs, and the crawl quietly stops being
// best-first without anything failing.
func testBestOfWhatIsFree(t *testing.T, open Open) {
	f := open(t, frontier.Config{Policy: "priority", Rate: time.Second})

	// a.example leads on its best URL and trails on everything else. Once that
	// one URL is gone the host is the worst in the frontier, and the timeline
	// below puts both hosts free at the same moment so that the comparison
	// actually has to happen.
	add(t, f,
		Req("/a-9", "a.example", 0, 0.9, 0),
		Req("/a-4", "a.example", 0, 0.4, time.Second),
		Req("/b-8", "b.example", 0, 0.8, 2*time.Second),
		Req("/b-5", "b.example", 0, 0.5, 3*time.Second),
	)

	// The best there is. a.example cools until 1s.
	first := lease(t, f, 0)
	if first == nil || first.URL != "/a-9" {
		t.Fatalf("first lease = %v, want the best URL there is", first)
	}
	done(t, f, first)

	// Both hosts free. a.example has 0.4 left, not the 0.9 already fetched, so
	// it loses to b.example. An implementation still valuing that host at 0.9
	// hands back /a-4 here.
	second := lease(t, f, 2*time.Second)
	if second == nil || second.URL != "/b-8" {
		t.Fatalf("lease = %v, want /b-8: a.example is worth what it has left, not what it had", second)
	}
	done(t, f, second)

	// And again, with the margin narrowed to 0.5 against 0.4.
	third := lease(t, f, 4*time.Second)
	if third == nil || third.URL != "/b-5" {
		t.Fatalf("lease = %v, want /b-5: a.example is still worth only 0.4", third)
	}
	done(t, f, third)

	if last := lease(t, f, 6*time.Second); last == nil || last.URL != "/a-4" {
		t.Fatalf("lease = %v, want the one URL left", last)
	}
}

// testFailRestoresPlace: a URL that failed is waiting again, so its host is
// worth what it was worth before.
//
// The other direction of the same problem. Anything kept per host has to go up
// when a URL comes back as well as down when one leaves, and a failure is the
// path that puts one back. An implementation that only ever lowered its summary
// would send a host to the back of the queue for the rest of the crawl on the
// strength of one timeout.
func testFailRestoresPlace(t *testing.T, open Open) {
	f := open(t, frontier.Config{Policy: "priority", Rate: time.Second})

	add(t, f,
		Req("/a-9", "a.example", 0, 0.9, 0),
		Req("/b-5", "b.example", 0, 0.5, time.Second),
		Req("/b-45", "b.example", 0, 0.45, 2*time.Second),
	)

	first := lease(t, f, 0)
	if first == nil || first.URL != "/a-9" {
		t.Fatalf("first lease = %v", first)
	}
	if err := f.Fail(context.Background(), job, first.Hash, first.Attempt); err != nil {
		t.Fatalf("fail: %v", err)
	}

	// The other host, because a.example is cooling whatever became of its URL.
	second := lease(t, f, 500*time.Millisecond)
	if second == nil || second.URL != "/b-5" {
		t.Fatalf("second lease = %v", second)
	}
	done(t, f, second)

	// Both free, and a.example is the better host again: the URL that failed is
	// waiting, not gone. An implementation that lowered its summary when the URL
	// went out and never raised it again hands back /b-45 here, and would go on
	// preferring b.example for the rest of the crawl on the strength of one
	// timeout.
	back := lease(t, f, 2*time.Second)
	if back == nil || back.URL != "/a-9" {
		t.Fatalf("lease = %v, want /a-9: a failure puts a URL back, so its host is worth what it was", back)
	}
}

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
		if err := f.Done(ctx, job, req.Hash, req.Attempt); err != nil {
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
		_ = f.Done(ctx, job, req.Hash, req.Attempt)
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
		_ = f.Done(ctx, job, req.Hash, req.Attempt)
	}
}

// testAFullTieIsBrokenTheSameWay: every implementation drains a page's links in
// one order.
//
// A full tie is the ordinary case rather than the rare one: every link found on
// one page carries that page's depth and one discovery time, so a page's whole
// link set ties on both ordering columns. With nothing after them the two
// frontiers disagreed — SQLite's rowid tie-break handed out the last link on
// the page and the memory frontier's scan kept the first, which for a
// depth-first policy is the opposite of what it means. The suite could not see
// it, because its own fixture gives every request a distinct time.
func testAFullTieIsBrokenTheSameWay(t *testing.T, open Open) {
	ctx := context.Background()
	f := open(t, frontier.Config{Policy: "depth"})

	// One page's links: one depth, one instant, different hashes.
	at := time.Date(2026, 8, 5, 9, 0, 0, 0, time.UTC)
	var reqs []frontier.Request
	for _, name := range []string{"a", "b", "c", "d"} {
		reqs = append(reqs, frontier.Request{
			Hash:       "hash-" + name,
			URL:        "https://example.com/" + name,
			Host:       "example.com",
			Depth:      2,
			Discovered: at,
		})
	}
	if _, err := f.Add(ctx, "news", reqs...); err != nil {
		t.Fatal(err)
	}

	// The order is whatever it is, but it has to be the same everywhere, so it
	// is pinned rather than merely observed.
	var got []string
	for range reqs {
		req, err := f.Lease(ctx, "news", at, time.Minute)
		if err != nil {
			t.Fatalf("lease: %v", err)
		}
		got = append(got, req.Hash)
		if err := f.Done(ctx, "news", req.Hash, req.Attempt); err != nil {
			t.Fatal(err)
		}
	}

	want := []string{"hash-d", "hash-c", "hash-b", "hash-a"}
	if !slices.Equal(got, want) {
		t.Errorf("a page's tied links drained %v, want %v: two frontiers must agree", got, want)
	}
}

// testAnUnorderedPolicyIsNotShapedByInsertion.
//
// `random` exists to sample a site without the sample being shaped by the
// scorer. Used as a comparator in a scan it replaced the best with probability
// one half at every candidate, so the last thing added won half the time and
// the k-th from the end one time in 2^k: ten thousand waiting URLs drew from
// the last fifteen. Insertion order wearing a disguise.
func testAnUnorderedPolicyIsNotShapedByInsertion(t *testing.T, open Open) {
	ctx := context.Background()
	at := time.Date(2026, 8, 5, 9, 0, 0, 0, time.UTC)

	const urls = 32
	const draws = 200

	// How often the first half of what was added comes out first. Uniform
	// sampling puts it near half; a scan biased to the end puts it near zero.
	early := 0
	for draw := range draws {
		f := open(t, frontier.Config{Policy: "random"})

		var reqs []frontier.Request
		for i := range urls {
			reqs = append(reqs, frontier.Request{
				Hash:       fmt.Sprintf("hash-%02d", i),
				URL:        fmt.Sprintf("https://example.com/%d", i),
				Host:       fmt.Sprintf("host-%d.example", i),
				Discovered: at,
			})
		}
		if _, err := f.Add(ctx, fmt.Sprintf("news-%d", draw), reqs...); err != nil {
			t.Fatal(err)
		}

		req, err := f.Lease(ctx, fmt.Sprintf("news-%d", draw), at, time.Minute)
		if err != nil {
			t.Fatalf("lease: %v", err)
		}
		var n int
		if _, err := fmt.Sscanf(req.Hash, "hash-%d", &n); err != nil {
			t.Fatal(err)
		}
		if n < urls/2 {
			early++
		}
	}

	// Generous bounds: this is a fairness check, not a statistics exam. A
	// scan biased toward the end scores essentially zero.
	if early < draws/5 {
		t.Errorf("the first half of what was added was drawn %d times in %d: the sample is shaped by insertion order",
			early, draws)
	}
}
