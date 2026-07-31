// SPDX-License-Identifier: GPL-3.0-or-later

package server

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// wait polls until a job reaches a terminal state, so the tests do not depend
// on how long the work takes.
func wait(t *testing.T, jobs *Jobs, id string) Job {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		job, ok := jobs.Get(id)
		if !ok {
			t.Fatalf("job %s vanished", id)
		}
		if job.State != Running {
			return job
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("job %s never finished", id)
	return Job{}
}

func TestAJobCarriesItsResult(t *testing.T) {
	jobs := NewJobs()

	started, err := jobs.Start("crawl", "vehicle", func(context.Context) (any, error) {
		return "42 pages", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if started.State != Running {
		t.Errorf("a job starts %q, want running", started.State)
	}

	done := wait(t, jobs, started.ID)
	if done.State != Done {
		t.Errorf("state = %q: %s", done.State, done.Error)
	}
	if done.Result != "42 pages" {
		t.Errorf("result = %v", done.Result)
	}
	if done.Finished.IsZero() {
		t.Error("a finished job has no finish time")
	}
}

func TestAFailedJobKeepsItsError(t *testing.T) {
	jobs := NewJobs()

	started, err := jobs.Start("train", "vehicle", func(context.Context) (any, error) {
		return nil, errors.New("no cached pages")
	})
	if err != nil {
		t.Fatal(err)
	}

	done := wait(t, jobs, started.ID)
	if done.State != Failed {
		t.Errorf("state = %q", done.State)
	}
	if done.Error != "no cached pages" {
		t.Errorf("error = %q", done.Error)
	}
}

// Two crawls of one entity would race on the frontier and double the load on
// somebody else's server.
func TestTheSameWorkCannotRunTwice(t *testing.T) {
	jobs := NewJobs()
	release := make(chan struct{})

	first, err := jobs.Start("crawl", "vehicle", func(context.Context) (any, error) {
		<-release
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = jobs.Start("crawl", "vehicle", func(context.Context) (any, error) { return nil, nil })

	var busy ErrBusy
	if !errors.As(err, &busy) {
		t.Fatalf("second start returned %v, want ErrBusy", err)
	}
	// The caller needs the id of the run that is already happening, or they
	// cannot watch it.
	if busy.ID != first.ID {
		t.Errorf("busy names job %q, the running one is %q", busy.ID, first.ID)
	}

	// A different entity, and a different kind for the same entity, are both
	// fine: the lock is per unit of work, not global.
	if _, err := jobs.Start("crawl", "other", func(context.Context) (any, error) { return nil, nil }); err != nil {
		t.Errorf("a different entity was blocked: %v", err)
	}
	if _, err := jobs.Start("train", "vehicle", func(context.Context) (any, error) { return nil, nil }); err != nil {
		t.Errorf("a different kind was blocked: %v", err)
	}

	close(release)
	wait(t, jobs, first.ID)

	// Once it finishes the slot frees.
	if _, err := jobs.Start("crawl", "vehicle", func(context.Context) (any, error) { return nil, nil }); err != nil {
		t.Errorf("the slot was not released: %v", err)
	}
}

// A caller who starts a crawl and hangs up has still started a crawl.
func TestWorkOutlivesTheRequest(t *testing.T) {
	jobs := NewJobs()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var sawCancelled bool
	started, err := jobs.Start("crawl", "vehicle", func(work context.Context) (any, error) {
		sawCancelled = work.Err() != nil
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = ctx

	wait(t, jobs, started.ID)
	if sawCancelled {
		t.Error("the job was handed a cancelled context and would have stopped immediately")
	}
}

func TestWaitBlocksUntilWorkFinishes(t *testing.T) {
	jobs := NewJobs()
	release := make(chan struct{})
	finished := make(chan struct{})

	if _, err := jobs.Start("crawl", "vehicle", func(context.Context) (any, error) {
		<-release
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}

	go func() {
		jobs.Wait()
		close(finished)
	}()

	select {
	case <-finished:
		t.Fatal("Wait returned while a job was still running")
	case <-time.After(30 * time.Millisecond):
	}

	close(release)
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("Wait never returned")
	}
}

func TestListIsNewestFirst(t *testing.T) {
	jobs := NewJobs()

	for i := range 3 {
		started, err := jobs.Start("crawl", fmt.Sprintf("entity-%d", i), func(context.Context) (any, error) {
			return nil, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		wait(t, jobs, started.ID)
		time.Sleep(2 * time.Millisecond)
	}

	list := jobs.List()
	if len(list) != 3 {
		t.Fatalf("listed %d jobs", len(list))
	}
	for i := 1; i < len(list); i++ {
		if list[i-1].Started.Before(list[i].Started) {
			t.Errorf("job %d is older than job %d", i-1, i)
		}
	}
}

// A long-lived service must not accumulate one record per crawl forever.
func TestFinishedJobsArePruned(t *testing.T) {
	jobs := NewJobs()

	for i := range maxFinished + 20 {
		started, err := jobs.Start("crawl", fmt.Sprintf("entity-%d", i), func(context.Context) (any, error) {
			return nil, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		wait(t, jobs, started.ID)
	}

	if got := len(jobs.List()); got > maxFinished+1 {
		t.Errorf("kept %d jobs, want no more than %d", got, maxFinished+1)
	}
}

func TestConcurrentStarts(t *testing.T) {
	jobs := NewJobs()

	var wg sync.WaitGroup
	var mu sync.Mutex
	var started, busy int

	for i := range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Half contend for one entity, half are distinct, so the test
			// exercises both the lock and the counter.
			entity := "shared"
			if i%2 == 0 {
				entity = fmt.Sprintf("entity-%d", i)
			}

			_, err := jobs.Start("crawl", entity, func(context.Context) (any, error) {
				time.Sleep(time.Millisecond)
				return nil, nil
			})

			mu.Lock()
			defer mu.Unlock()
			var e ErrBusy
			switch {
			case errors.As(err, &e):
				busy++
			case err == nil:
				started++
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	wg.Wait()
	jobs.Wait()

	// Every distinct entity started, and the contended one started at most once
	// per moment rather than sixteen times at once.
	if started == 0 {
		t.Error("nothing started")
	}
	if started+busy != 32 {
		t.Errorf("%d started plus %d busy is not 32", started, busy)
	}
}

func TestIDsAreUnique(t *testing.T) {
	jobs := NewJobs()
	seen := map[string]bool{}

	for i := range 20 {
		job, err := jobs.Start("crawl", fmt.Sprintf("entity-%d", i), func(context.Context) (any, error) {
			return nil, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if seen[job.ID] {
			t.Fatalf("duplicate job id %q", job.ID)
		}
		seen[job.ID] = true
	}
}
