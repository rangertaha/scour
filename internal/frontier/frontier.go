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

	// Attempt is which handout this is: 1 the first time the URL goes out, 2
	// the next. It identifies the lease, and [Frontier.Done] and [Frontier.Fail]
	// act only on a report whose attempt still matches the stored one.
	//
	// The handout count is the identity rather than a token minted per lease
	// because a frontier already has to keep it: a URL is abandoned after
	// [MaxAttempts], so the number is stored and incremented on every lease
	// whatever else happens. An epoch column of its own would be a second thing
	// to write on the one query that caps how fast a crawl can go, to say what
	// the first already says.
	//
	// Only [Frontier.Lease] sets it. [Frontier.Add] ignores it, because how
	// often a URL has been handed out is not something the spider that found it
	// can know.
	Attempt int
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

	// Lease hands out the next request and holds it for the given duration,
	// with [Request.Attempt] set to the identity of that hold.
	//
	// The best request whose host is not cooling, by whichever policy this
	// frontier was built with. It returns [ErrEmpty] when nothing is due,
	// which covers both an exhausted crawl and one waiting on politeness.
	Lease(ctx context.Context, job string, now time.Time, hold time.Duration) (*Request, error)

	// Done reports a leased request as finished, so it is never handed out
	// again. The attempt is the one [Frontier.Lease] returned.
	//
	// A report whose attempt no longer matches does nothing, and that is the
	// right answer rather than a tolerated one. A worker that stalled past its
	// hold has already had the URL taken off it and given to somebody else, so
	// what it is describing is a fetch nobody is waiting on, and the worker
	// holding the URL now is still working. Being late is not an error, so
	// neither is reporting late.
	Done(ctx context.Context, job, hash string, attempt int) error

	// Fail reports a leased request as failed. It becomes available again
	// until it has been tried too often, because a URL nothing will ever
	// report on must not cycle for the length of the crawl.
	//
	// The attempt fences it the same way [Frontier.Done] is fenced, and here it
	// is the exclusivity invariant itself at stake: an unfenced failure clears
	// the hold, so a report from a worker that lost the URL an hour ago hands
	// that URL to a third worker while the second is still fetching it.
	Fail(ctx context.Context, job, hash string, attempt int) error

	// Pace records the least time a host asked for between requests, and holds
	// it off for that long from now.
	//
	// It is how a site's own `Crawl-delay` reaches the one place that can
	// honour it. Politeness is decided here, and robots.txt is read in the
	// downloader, so without a way back the file was parsed and obeyed in every
	// respect but this one: `robots.Rules.Delay` had no caller at all, and a
	// site asking for thirty seconds was crawled at whatever `scheduler.rate`
	// happened to say.
	//
	// # The longer of the two wins
	//
	// [Config.Rate] is how fast this job is willing to go and the delay is how
	// fast the site is willing to be crawled, so [Frontier.Lease] waits for
	// whichever is longer. A job cannot use a permissive robots.txt to go
	// faster than it configured, and it cannot use its own rate to go faster
	// than the site asked.
	//
	// # Not job-scoped
	//
	// For the same reason host state is shared: robots.txt is the host's
	// instruction to everybody, so two jobs on one site honour one delay
	// between them rather than one each. That is the same argument politeness
	// is built on, applied to where the number came from.
	//
	// # It holds the host off, rather than only recording the number
	//
	// A crawl-delay is learnt on the first fetch of a host, by which point that
	// host has already been paced at the job's rate. Recording the number alone
	// would let the second request, the one that proves the file was read at
	// all, go out a second after the first. So this also pushes the host out to
	// now+delay, and never pulls it in: a host already cooling for longer stays
	// that way.
	//
	// A delay of zero records that the site asked for nothing, which is not the
	// same as never having asked and is worth storing so that a site dropping
	// its `Crawl-delay` takes effect.
	Pace(ctx context.Context, host string, now time.Time, delay time.Duration) error

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
