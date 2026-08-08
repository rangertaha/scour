// SPDX-License-Identifier: GPL-3.0-or-later

// Package memory is a frontier that keeps everything in memory.
//
// It is not the one a crawl runs on: nothing survives a restart, and a real
// crawl's frontier outgrows memory. It exists so the contract has a second
// implementation, which is what stops that contract from being a description of
// whichever store happened to be written first, and so the benchmarks have a
// floor to measure the durable one against.
package memory

import (
	"context"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/rangertaha/scour/internal/frontier"
)

// Frontier holds a job's waiting requests in a map.
type Frontier struct {
	policy frontier.Policy
	rate   time.Duration

	mu    sync.Mutex
	jobs  map[string]*queue
	hosts map[string]time.Time // when each host may next be touched

	// delays is what each host asked for in its own robots.txt, kept apart from
	// rate because they answer different questions and the wait is the longer
	// of the two. Shared across jobs, like hosts and for the same reason.
	delays map[string]time.Duration
}

type queue struct {
	// order preserves insertion, so a policy's tie-break on discovery time is
	// stable even when two requests share a timestamp.
	order []string
	byID  map[string]*entry
}

type entry struct {
	req    frontier.Request
	leased time.Time
	// attempts is both the retry budget and the identity of the current hold,
	// the same as the column of that name in the durable implementation.
	attempts int
	done     bool
}

// Open returns an in-memory frontier.
func Open(cfg frontier.Config) (*Frontier, error) {
	policy, err := frontier.Policies(cfg.Policy)
	if err != nil {
		return nil, err
	}
	return &Frontier{
		policy: policy,
		rate:   cfg.Rate,
		jobs:   map[string]*queue{},
		hosts:  map[string]time.Time{},
		delays: map[string]time.Duration{},
	}, nil
}

// wait is how long a host must be left alone after being touched: the longer of
// what this job configured and what the site asked for.
//
// Not the caller's choice at either end. A permissive robots.txt does not make
// a job faster than it configured itself to be, and a job's own rate does not
// make it faster than the site asked.
func (f *Frontier) wait(host string) time.Duration {
	return max(f.rate, f.delays[host])
}

// Policy is the ordering this was built with.
func (f *Frontier) Policy() string { return f.policy.Name() }

// Add implements [frontier.Frontier].
func (f *Frontier) Add(_ context.Context, job string, reqs ...frontier.Request) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	q := f.jobs[job]
	if q == nil {
		q = &queue{byID: map[string]*entry{}}
		f.jobs[job] = q
	}

	var added int
	for _, r := range reqs {
		if r.Hash == "" || q.byID[r.Hash] != nil {
			// Re-discovering a URL is not news. Keeping the first sighting
			// keeps the crawl path that found it, which is what a shallower
			// later one would throw away.
			continue
		}
		q.byID[r.Hash] = &entry{req: r}
		q.order = append(q.order, r.Hash)
		added++
	}
	return added, nil
}

// Lease implements [frontier.Frontier].
func (f *Frontier) Lease(_ context.Context, job string, now time.Time, hold time.Duration) (*frontier.Request, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	q := f.jobs[job]
	if q == nil {
		return nil, frontier.ErrEmpty
	}

	var best *entry
	seen := 0
	for _, hash := range q.order {
		e := q.byID[hash]
		switch {
		case e.done:
			continue
		case e.leased.After(now):
			continue // somebody else has it
		}
		// Politeness first, and it is not negotiable by the policy: a policy
		// that could override it could hammer one server by choosing badly.
		if next, seen := f.hosts[e.req.Host]; seen && next.After(now) {
			continue
		}
		// A policy with no ordering wants one of the candidates rather than the
		// best of them, and reservoir sampling is what makes that uniform:
		// taking each with probability one over the number seen so far leaves
		// every candidate equally likely. Using Less as a coin flip instead
		// made `random` overwhelmingly pick the most recently added, which is
		// insertion order wearing a disguise.
		seen++
		if _, unordered := f.policy.(frontier.Unordered); unordered {
			if rand.IntN(seen) == 0 {
				best = e
			}
			continue
		}
		if best == nil || f.policy.Less(e.req, best.req) {
			best = e
		}
	}

	if best == nil {
		return nil, frontier.ErrEmpty
	}

	best.leased = now.Add(hold)
	best.attempts++
	if wait := f.wait(best.req.Host); wait > 0 {
		f.hosts[best.req.Host] = now.Add(wait)
	}

	// The copy carries the attempt and the stored request does not, because the
	// attempt belongs to this hold rather than to the URL.
	req := best.req
	req.Attempt = best.attempts
	return &req, nil
}

// Pace implements [frontier.Frontier].
func (f *Frontier) Pace(_ context.Context, host string, now time.Time, delay time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if delay < 0 {
		delay = 0
	}
	f.delays[host] = delay

	// The site's number alone, and never pulled in. The job's rate was already
	// applied by the lease that led here, measured from when that lease was
	// taken; applying it again from now would measure it from after the fetch,
	// so every host that asked for nothing would be left alone for a rate plus
	// however long its page took. The floor lives in [Frontier.Lease].
	if until := now.Add(delay); until.After(f.hosts[host]) {
		f.hosts[host] = until
	}
	return nil
}

// Done implements [frontier.Frontier].
func (f *Frontier) Done(_ context.Context, job, hash string, attempt int) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if q := f.jobs[job]; q != nil {
		if e := q.byID[hash]; e != nil && e.attempts == attempt {
			e.done = true
		}
	}
	return nil
}

// Fail implements [frontier.Frontier].
func (f *Frontier) Fail(_ context.Context, job, hash string, attempt int) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	q := f.jobs[job]
	if q == nil {
		return nil
	}
	e := q.byID[hash]
	if e == nil || e.attempts != attempt {
		// A report from a hold that is over. The URL went out again when this
		// one expired, so freeing it here would hand it to a third worker while
		// the second is still fetching it. Silence is the answer: the holder
		// will report for itself.
		return nil
	}

	if e.attempts >= frontier.MaxAttempts {
		// Handed out as often as it is going to be. A request nothing will
		// ever report on must not cycle for the length of the crawl.
		e.done = true
		return nil
	}
	e.leased = time.Time{}
	return nil
}

// Len implements [frontier.Frontier].
func (f *Frontier) Len(_ context.Context, job string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	q := f.jobs[job]
	if q == nil {
		return 0, nil
	}

	var n int
	for _, e := range q.byID {
		if !e.done {
			n++
		}
	}
	return n, nil
}

// Close implements [frontier.Frontier].
func (f *Frontier) Close() error { return nil }
