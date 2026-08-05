// SPDX-License-Identifier: GPL-3.0-or-later

package sqlite_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rangertaha/scour/internal/frontier"
	"github.com/rangertaha/scour/internal/frontier/frontiertest"
	"github.com/rangertaha/scour/internal/frontier/sqlite"
)

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
	if _, err := second.Lease(ctx, "news", now.Add(3*time.Second), time.Minute); err != frontier.ErrEmpty {
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
	if _, err := f.Lease(ctx, "two", now.Add(time.Second), time.Minute); err != frontier.ErrEmpty {
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
	if _, err := f.Lease(ctx, "two", now.Add(time.Second), time.Minute); err != frontier.ErrEmpty {
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
			if strings.Contains(joined, "USE TEMP B-TREE FOR ORDER BY") {
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
