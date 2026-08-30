// SPDX-License-Identifier: GPL-3.0-or-later

package sqlite_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/rangertaha/scour/internal/frontier"
	"github.com/rangertaha/scour/internal/frontier/frontiertest"
	"github.com/rangertaha/scour/internal/frontier/sqlite"
)

// TestAnOlderDatabaseIsBroughtForward.
//
// A frontier is the state a crawl resumes from, so there is no acceptable
// answer to a schema change that involves deleting it. `CREATE TABLE IF NOT
// EXISTS` does nothing to a table that already exists, so a column added to the
// DDL reaches new databases and no existing one, and the failure is not at
// startup: it is the first lease of the resumed crawl, reporting a column that
// the file it just opened does not have.
//
// This builds the hosts table as an older build left it and then opens it.
func TestAnOlderDatabaseIsBroughtForward(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	old, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "frontier.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := old.Exec(`
CREATE TABLE hosts (host TEXT PRIMARY KEY, next_at INTEGER NOT NULL);
INSERT INTO hosts (host, next_at) VALUES ('example.com', 0);`); err != nil {
		t.Fatalf("build the old schema: %v", err)
	}
	if err := old.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	f, err := sqlite.Open(frontier.Config{Dir: dir, Rate: time.Second})
	if err != nil {
		t.Fatalf("open a database an older build made: %v", err)
	}
	defer f.Close()

	// The whole point of the column: the crawl that resumes has to be able to
	// pace a host and then honour it.
	if _, err := f.Add(ctx, "news", frontiertest.Req("/a", "example.com", 0, 1, 0)); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := f.Pace(ctx, "example.com", frontiertest.Origin, 10*time.Second); err != nil {
		t.Fatalf("pace: %v", err)
	}
	if req, _ := f.Lease(ctx, "news", frontiertest.Origin.Add(2*time.Second), time.Minute); req != nil {
		t.Errorf("the resumed crawl ignored the delay the site asked for: %v", req)
	}
	if req, _ := f.Lease(ctx, "news", frontiertest.Origin.Add(10*time.Second), time.Minute); req == nil {
		t.Error("the resumed crawl never got the host back")
	}
}

// TestAnOlderDepthIndexIsRebuilt.
//
// CREATE INDEX IF NOT EXISTS matches on the name alone, so an index whose
// columns change in the DDL reaches new databases and no existing one. The
// database opens cleanly and nothing fails; the depth lease just sorts the
// frontier on every call for the rest of that crawl's life.
func TestAnOlderDepthIndexIsRebuilt(t *testing.T) {
	dir := t.TempDir()

	// A database as the previous version left it: the index without hash, and
	// the version that says it is up to date.
	f, err := sqlite.Open(frontier.Config{Dir: dir, Policy: "depth"})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	old, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "frontier.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := old.Exec(`
DROP INDEX urls_depth;
CREATE INDEX urls_depth ON urls (job, status, depth DESC, discovered DESC);
PRAGMA user_version = 1;`); err != nil {
		t.Fatalf("build the old index: %v", err)
	}
	if err := old.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	again, err := sqlite.Open(frontier.Config{Dir: dir, Policy: "depth"})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer again.Close()

	plan, err := again.Plan(context.Background(), "news")
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if joined := strings.Join(plan, "\n"); strings.Contains(joined, "TEMP B-TREE") {
		t.Errorf("a resumed crawl sorts the frontier on every lease:\n%s", joined)
	}
}

// The contract, run against the store a crawl actually uses. It is the same
// suite the memory implementation runs, which is what stops the contract from
// being a description of whichever store happened to be written first.
func TestContract(t *testing.T) {
	frontiertest.Run(t, open)
}

func BenchmarkFrontier(b *testing.B) {
	frontiertest.Benchmark(b, open)
}

// open gives every test a database of its own, on disk, because a frontier that
// only works in memory is the one thing this package exists not to be.
func open(t testing.TB, cfg frontier.Config) frontier.Frontier {
	t.Helper()

	if cfg.Dir == "" {
		cfg.Dir = t.TempDir()
	}
	f, err := sqlite.Open(cfg)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() {
		if err := f.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	return f
}

// TestItSurvivesARestart is the whole reason this exists rather than the memory
// one, and the only property the shared contract cannot check.
func TestItSurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	now := frontiertest.Origin

	first, err := sqlite.Open(frontier.Config{Dir: dir, Rate: time.Second})
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	reqs := []frontier.Request{
		frontiertest.Req("https://a.example/1", "a.example", 1, 0.9, 0),
		frontiertest.Req("https://a.example/2", "a.example", 1, 0.5, time.Second),
		frontiertest.Req("https://b.example/1", "b.example", 1, 0.7, 2*time.Second),
	}
	if n, err := first.Add(ctx, "news", reqs...); err != nil || n != 3 {
		t.Fatalf("add = %d, %v", n, err)
	}

	// One finished, one in flight when the process stops.
	best, err := first.Lease(ctx, "news", now, time.Minute)
	if err != nil {
		t.Fatalf("lease: %v", err)
	}
	if err := first.Done(ctx, "news", best.Hash, best.Attempt); err != nil {
		t.Fatalf("done: %v", err)
	}
	if _, err := first.Lease(ctx, "news", now, time.Minute); err != nil {
		t.Fatalf("lease: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	second, err := sqlite.Open(frontier.Config{Dir: dir, Rate: time.Second})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer second.Close()

	// The finished one is gone and the other two are not.
	if n, err := second.Len(ctx, "news"); err != nil || n != 2 {
		t.Errorf("len = %d, %v; want the two that were not finished", n, err)
	}

	// Once the host has cooled there is exactly one request left to hand out:
	// not the finished one, and not the one still leased. A restart carries
	// both the completion and the lease across, which is the whole claim.
	got, err := second.Lease(ctx, "news", now.Add(2*time.Second), time.Minute)
	if err != nil {
		t.Fatalf("lease after restart: %v", err)
	}
	if got.Hash == best.Hash {
		t.Error("a finished request was handed out again")
	}
	if got.URL != "https://a.example/2" {
		t.Errorf("leased %s, want the only request that was neither done nor held", got.URL)
	}
	if _, err := second.Lease(ctx, "news", now.Add(3*time.Second), time.Minute); !errors.Is(err, frontier.ErrEmpty) {
		t.Error("something was handed out that was already done or already leased")
	}

	// And once the leases expire the held one comes back, because a worker
	// that died holding a URL must not take it with it.
	back, err := second.Lease(ctx, "news", now.Add(2*time.Hour), time.Minute)
	if err != nil {
		t.Fatalf("an expired lease was not reclaimed: %v", err)
	}
	if back.Hash == best.Hash {
		t.Error("the reclaimed request was the finished one")
	}
}

// TestTheFileIsWhereItSaysItIs, because the first thing anybody does when a
// crawl misbehaves is open the database in a shell.
func TestTheFileIsWhereItSaysItIs(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "made", "on", "demand")

	f, err := sqlite.Open(frontier.Config{Dir: dir})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()

	if _, err := os.Stat(sqlite.Path(dir)); err != nil {
		t.Errorf("no database at %s: %v", sqlite.Path(dir), err)
	}
	if sqlite.Path("") != "" {
		t.Error("an unconfigured directory has a path")
	}
}

// TestHostsAreSharedAcrossJobs is the reason this is one database rather than
// one per job: two jobs on one site get one allowance between them.
func TestHostsAreSharedAcrossJobs(t *testing.T) {
	ctx := context.Background()
	now := frontiertest.Origin

	f := open(t, frontier.Config{Rate: 10 * time.Second})

	if _, err := f.Add(ctx, "one", frontiertest.Req("https://a.example/1", "a.example", 1, 0.9, 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Add(ctx, "two", frontiertest.Req("https://a.example/2", "a.example", 1, 0.9, 0)); err != nil {
		t.Fatal(err)
	}

	if _, err := f.Lease(ctx, "one", now, time.Minute); err != nil {
		t.Fatalf("first job: %v", err)
	}

	// The second job wants the same host a moment later. It has to wait, or
	// two jobs on one site would each get their own allowance.
	if _, err := f.Lease(ctx, "two", now.Add(time.Second), time.Minute); !errors.Is(err, frontier.ErrEmpty) {
		t.Errorf("the second job was handed the host anyway: %v", err)
	}
	if _, err := f.Lease(ctx, "two", now.Add(11*time.Second), time.Minute); err != nil {
		t.Errorf("the second job never got its turn: %v", err)
	}
}

// TestRemoveDropsAJobAndLeavesTheHosts. Dropping a job must not forget that a
// site is being paced, because another job may be on it right now.
func TestRemoveDropsAJobAndLeavesTheHosts(t *testing.T) {
	ctx := context.Background()
	now := frontiertest.Origin

	dir := t.TempDir()
	f, err := sqlite.Open(frontier.Config{Dir: dir, Rate: 10 * time.Second})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()

	if _, err := f.Add(ctx, "one", frontiertest.Req("https://a.example/1", "a.example", 1, 0.9, 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Add(ctx, "two", frontiertest.Req("https://a.example/2", "a.example", 1, 0.9, 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Lease(ctx, "one", now, time.Minute); err != nil {
		t.Fatal(err)
	}

	if err := f.Remove(ctx, "one"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if n, err := f.Len(ctx, "one"); err != nil || n != 0 {
		t.Errorf("len = %d, %v after removing the job", n, err)
	}
	if n, err := f.Len(ctx, "two"); err != nil || n != 1 {
		t.Errorf("the other job lost %d rows", 1-n)
	}

	hosts, err := f.Hosts(ctx)
	if err != nil {
		t.Fatalf("hosts: %v", err)
	}
	if hosts != 1 {
		t.Errorf("hosts = %d; dropping a job forgot that a site was being paced", hosts)
	}
	if _, err := f.Lease(ctx, "two", now.Add(time.Second), time.Minute); !errors.Is(err, frontier.ErrEmpty) {
		t.Error("the surviving job was handed a host that is still cooling")
	}
}

// TestAPolicyThatDoesNotExistIsRefusedWhenOpened, not when the first URL is
// leased an hour into a run.
func TestAPolicyThatDoesNotExistIsRefusedWhenOpened(t *testing.T) {
	if _, err := sqlite.Open(frontier.Config{Dir: t.TempDir(), Policy: "alphabetical"}); err == nil {
		t.Fatal("opened a frontier with an ordering nothing implements")
	}
}

// TestTheLeaseIsServedByAnIndex.
//
// A crawl leases once per page, so this query is the ceiling on how fast
// anything can go, and the difference between walking an index and sorting the
// whole frontier is the difference between a crawl that scales and one that
// stops. Measured before this test existed: a lease that sorted took 47
// milliseconds at a hundred thousand URLs, and one that walks takes 0.37, flat
// from a thousand rows to a hundred thousand.
//
// It is a test rather than a benchmark because a benchmark only fails if
// somebody reads it.
func TestTheLeaseIsServedByAnIndex(t *testing.T) {
	for _, policy := range []string{"priority", "breadth", "depth"} {
		t.Run(policy, func(t *testing.T) {
			f, err := sqlite.Open(frontier.Config{Dir: t.TempDir(), Policy: policy})
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			defer f.Close()

			plan, err := f.Plan(context.Background(), "news")
			if err != nil {
				t.Fatalf("plan: %v", err)
			}

			joined := strings.Join(plan, "\n")
			// "TEMP B-TREE" alone, because SQLite has several spellings for
			// the same cost and this asserted the one it happens not to use
			// when only part of the ordering is indexed. A lease whose last
			// term fell out of its index reported "USE TEMP B-TREE FOR RIGHT
			// PART OF ORDER BY", which is not a substring of "USE TEMP B-TREE
			// FOR ORDER BY", and the test that exists to catch exactly that
			// passed.
			if strings.Contains(joined, "TEMP B-TREE") {
				t.Errorf("the lease sorts the frontier instead of walking it:\n%s", joined)
			}
			if !strings.Contains(joined, "USING INDEX urls_") {
				t.Errorf("the lease does not use one of the ordering indexes:\n%s", joined)
			}
			if strings.Contains(joined, "SCAN urls") {
				t.Errorf("the lease scans the table:\n%s", joined)
			}
		})
	}
}

// TestNoUrlIsInvisibleToTheGuard.
//
// [TestAnEmptyLeaseDoesNotReadTheFrontier] buys its speed by letting the hosts
// table answer for the urls table, and that trade is only safe while every host
// in the frontier has a row there. A host with no row is not read as a host that
// is free; it is not seen at all, so the guard says nothing is due and the URLs
// behind it are never handed out for as long as the crawl runs. A performance
// change that loses URLs is worse than the cost it saved.
//
// The empty host is the case worth pinning, because it is the one an argument
// about callers gets wrong: the scheduler normalises before queueing, so it
// should not arrive, and [frontier.Frontier] is an interface anybody can call.
func TestNoUrlIsInvisibleToTheGuard(t *testing.T) {
	ctx := context.Background()
	f := open(t, frontier.Config{Rate: time.Second})

	// Straight at the interface, the way a caller that is not the scheduler
	// reaches it.
	odd := frontier.Request{URL: "urn:example:1", Hash: "h1", Host: ""}
	if _, err := f.Add(ctx, "news", odd); err != nil {
		t.Fatalf("add: %v", err)
	}

	got, err := f.Lease(ctx, "news", frontiertest.Origin, time.Minute)
	if err != nil {
		t.Fatalf("a queued URL was never handed out, so the guard cannot see it: %v", err)
	}
	if got.Hash != odd.Hash {
		t.Errorf("leased %q, want the one URL that was queued", got.Hash)
	}
}

// TestAnEmptyLeaseDoesNotReadTheFrontier.
//
// A cost, asserted as a cost. [TestTheLeaseIsServedByAnIndex] pins the query
// plan, and a plan can stay exactly right while what it costs goes through the
// floor: this whole class of defect hid behind that test for as long as it
// existed.
//
// The case is the commonest crawl there is, one site. While its host is cooling
// every lease returns nothing, and finding that out by walking the urls table
// means reading all of it, because every row fails the politeness check and
// SQLite cannot know that until it has looked. Measured before the guard that
// fixed it: 0.55ms at a thousand URLs, 5.3ms at ten thousand, 69ms at a hundred
// thousand. Each worker asks again every [run.Idle] for the whole delay and
// holds the write lock to do it, which also blocks the Add queueing whatever
// the crawl is finding, so at a hundred thousand URLs a crawl spends more than
// a core proving it has nothing to do.
//
// A ratio rather than a number of milliseconds, so it means the same thing on a
// slow machine as on a fast one. The bound is loose on purpose: what it has to
// separate is flat from linear, and the defect it exists to catch was a
// hundredfold.
func TestAnEmptyLeaseDoesNotReadTheFrontier(t *testing.T) {
	ctx := context.Background()

	cost := func(size int) time.Duration {
		t.Helper()

		f, err := sqlite.Open(frontier.Config{Dir: t.TempDir(), Rate: time.Hour})
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		defer f.Close()

		reqs := make([]frontier.Request, 0, size)
		for i := range size {
			reqs = append(reqs, frontiertest.Req(
				fmt.Sprintf("/page/%d", i), "one.example",
				i%6, float64(i%1000)/1000, time.Duration(i)*time.Millisecond))
		}
		if _, err := f.Add(ctx, "news", reqs...); err != nil {
			t.Fatalf("add: %v", err)
		}

		// One lease, so the only host is cooling. Everything still waiting is
		// now behind it, which is the state a crawl of one site spends most of
		// its life in.
		now := frontiertest.Origin
		if _, err := f.Lease(ctx, "news", now, time.Minute); err != nil {
			t.Fatalf("first lease: %v", err)
		}

		const runs = 200
		start := time.Now()
		for range runs {
			if _, err := f.Lease(ctx, "news", now, time.Minute); !errors.Is(err, frontier.ErrEmpty) {
				t.Fatalf("size %d: leased something while the only host was cooling: %v", size, err)
			}
		}
		return time.Since(start) / runs
	}

	small, large := cost(1_000), cost(100_000)

	// Ten, against a defect worth a hundred. Anything inside this is noise on a
	// busy machine; anything outside it is the frontier being read again.
	if large > 10*small {
		t.Errorf("an empty lease costs %s at 100,000 URLs and %s at 1,000, so it "+
			"grows with the frontier: it is reading the queue to find out that "+
			"nothing is due, rather than asking which hosts are free.", large, small)
	}
}

// TestRandomIsAllowedToSort, because it has to: sampling without regard to
// score is a shuffle of everything waiting, and no index can express one. It is
// pinned here so that the cost is a known property of that policy rather than a
// surprise somebody hits in production.
func TestRandomIsAllowedToSort(t *testing.T) {
	f, err := sqlite.Open(frontier.Config{Dir: t.TempDir(), Policy: "random"})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()

	plan, err := f.Plan(context.Background(), "news")
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if !strings.Contains(strings.Join(plan, "\n"), "B-TREE FOR ORDER BY") {
		t.Log("random no longer sorts, which would be an improvement worth understanding")
	}
}
