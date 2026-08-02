// SPDX-License-Identifier: GPL-3.0-or-later

package service

import (
	"context"

	"github.com/rangertaha/scour/internal/cluster"
)

// Reporter is a component that can say what it is carrying.
//
// Not every service is one. The numbers on a node's line are the frontier it
// holds and the pages it has fetched, and a component that does neither has
// nothing to add to them.
type Reporter interface {
	// Load is this component's share of its node's queue depth and page count.
	Load(ctx context.Context) cluster.Load
}

// Health sums what the given services are carrying, which is one node's line in
// `scour node ls`.
//
// Summed rather than reported per component because a node is a process and a
// process is one row: a machine running both roles has one queue depth, the
// frontier it is holding, and one throughput, the pages it fetched.
func Health(services ...Service) cluster.Health {
	reporters := make([]Reporter, 0, len(services))
	for _, svc := range services {
		if r, ok := svc.(Reporter); ok {
			reporters = append(reporters, r)
		}
	}
	return func(ctx context.Context) cluster.Load {
		var total cluster.Load
		for _, r := range reporters {
			load := r.Load(ctx)
			total.Queue += load.Queue
			total.Fetched += load.Fetched
		}
		return total
	}
}

// Load implements [Reporter] for the store.
//
// The store's contribution is the frontier: what the fleet has left to fetch is
// held here and can be counted nowhere else. It fetches nothing itself, so it
// reports no pages, and in a single process the two roles are summed into one
// line anyway.
//
// A number that cannot be read is reported as nothing rather than as a failure.
// A heartbeat that does not go out is worse than one carrying a zero, because
// the first makes a working node look dead.
func (s *StoreService) Load(ctx context.Context) cluster.Load {
	jobs, err := s.store.QueuedJobs(ctx)
	if err != nil {
		return cluster.Load{}
	}
	var queue int64
	for _, id := range jobs {
		n, err := s.store.QueueSize(ctx, id)
		if err != nil {
			continue
		}
		queue += int64(n)
	}
	return cluster.Load{Queue: queue}
}

// Load implements [Reporter] for a crawler.
//
// Its queue is what the store has handed it and it has not fetched yet, which
// is a different number from the frontier and the one that says whether this
// crawler is the fleet's bottleneck. Its page count is every fetch it has made.
func (c *CrawlService) Load(context.Context) cluster.Load {
	c.mu.Lock()
	var queue int64
	for _, feed := range c.feeds {
		queue += int64(feed.Held())
	}
	c.mu.Unlock()

	return cluster.Load{Queue: queue, Fetched: c.fetched.Load()}
}
