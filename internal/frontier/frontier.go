// SPDX-License-Identifier: GPL-3.0-or-later

// Package frontier is what a crawl has left to fetch, and the order it comes
// out in.
//
// A crawl always has more URLs waiting than it will ever fetch, so the order is
// most of what a crawl is. That makes this the one stage a job cannot hand to
// somebody else: two schedulers handing out the same host cannot honour a crawl
// delay between them, so politeness forces exactly one decision point per host.
//
// # Time is passed in
//
// [Frontier.Lease] takes the current time rather than reading a clock. Pacing a
// host is the whole point of this package, and a test that has to sleep two
// seconds to prove a two-second delay is a test nobody runs. Real callers pass
// time.Now.
package frontier

import (
	"context"
	"errors"
	"time"
)

// ErrEmpty is returned by [Frontier.Lease] when nothing is due.
//
// Not an error in the ordinary sense: an empty frontier means the crawl has
// finished, and a frontier whose hosts are all cooling means it has not. The
// caller tells them apart with [Frontier.Len].
var ErrEmpty = errors.New("frontier: nothing due")

// Request is one URL waiting to be fetched.
type Request struct {
	// URL is the address, normalised.
	URL string
	// Host is what politeness is paced against.
	Host string
	// Hash identifies the URL for deduplication. Re-discovering one is an
	// upsert rather than a second row.
	Hash string
	// Depth is how far from a start URL this was found.
	Depth int
	// Score is how likely this is to hold what the job wants. What the
	// ordering policy sorts by.
	Score float64
	// Parent is the page it was found on, which is what makes a crawl path
	// reconstructable.
	Parent string
	// Discovered is when it was first seen. Ties in every policy break on it,
	// so the same document always produces the same crawl.
	Discovered time.Time
}

// Frontier holds a job's waiting URLs.
//
// Implementations must be safe for concurrent use: a crawl leases from several
// goroutines at once.
type Frontier interface {
	// Add queues requests, ignoring any already known. It reports how many
	// were new, which is what tells a crawl whether it is still finding
	// anything.
	Add(ctx context.Context, job string, reqs ...Request) (int, error)

	// Lease hands out the next request and holds it for the given duration.
	//
	// The best request whose host is not cooling, by whichever policy this
	// frontier was built with. It returns [ErrEmpty] when nothing is due,
	// which covers both an exhausted crawl and one waiting on politeness.
	Lease(ctx context.Context, job string, now time.Time, hold time.Duration) (*Request, error)

	// Done reports a leased request as finished, so it is never handed out
	// again.
	Done(ctx context.Context, job, hash string) error

	// Fail reports a leased request as failed. It becomes available again
	// until it has been tried too often, because a URL nothing will ever
	// report on must not cycle for the length of the crawl.
	Fail(ctx context.Context, job, hash string) error

	// Len is how many requests are still waiting, leased or not.
	Len(ctx context.Context, job string) (int, error)

	// Close releases whatever the implementation holds open.
	Close() error
}

// MaxAttempts is how many times a request is handed out before it is abandoned.
const MaxAttempts = 3

// Config is what a frontier is built from.
type Config struct {
	// Policy names the ordering. Empty means [DefaultPolicy].
	Policy string
	// Rate is the least time between two requests to one host.
	Rate time.Duration
	// Dir is where a durable implementation keeps its file.
	Dir string
}
