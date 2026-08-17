// SPDX-License-Identifier: GPL-3.0-or-later

// Package scheduler decides what gets fetched, and in what order.
//
// It owns the frontier, and it is the one stage a job may not hand to somebody
// else: two schedulers handing out the same host cannot honour a crawl delay
// between them, so politeness forces exactly one decision point per host.
//
// # The chain runs on the way in
//
// A request passes through the middleware on its way into the frontier, so low
// order is nearest the spider that discovered it and high order is nearest the
// queue. `dupefilter` at 100 decides what counts as already seen before
// anything else pays to think about it; `offsite` at 200 drops what is out of
// bounds; a scorer nearer the queue says how much the rest of it is worth.
//
// Every link may change the request, which is the difference from the other
// chains: what comes out is what gets queued. A link that returns [chain.ErrDrop]
// refuses the URL, which is the ordinary outcome for most of what a crawl finds.
//
// # What is not in the chain
//
// The budget. `max_depth` and `max_pages` are `scheduler` attributes, and an
// attribute's enforcement cannot be optional: a plugin that could be turned off
// is a budget that can be exceeded by deleting a line, which is not what
// anybody writing a budget means. So they are checked outside the chain, the
// same way robots.txt is in the downloader and for the same reason.
package scheduler

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/hashicorp/hcl/v2"

	"github.com/rangertaha/scour/internal/chain"
	"github.com/rangertaha/scour/internal/engine"
	"github.com/rangertaha/scour/internal/frontier"
	"github.com/rangertaha/scour/internal/plugin"
	"github.com/rangertaha/scour/internal/scope"
	"github.com/rangertaha/scour/internal/urls"
)

// Errors a scheduler produces for a URL it will not queue. All of them wrap
// [chain.ErrDrop], because refusing most of what a crawl discovers is what a
// focused crawl is, and none of it is a failure.
var (
	// ErrTooDeep reports a URL past the job's max_depth.
	ErrTooDeep = fmt.Errorf("deeper than the job crawls: %w", chain.ErrDrop)

	// ErrOutOfScope reports a URL outside the job's domains, included or
	// excluded.
	ErrOutOfScope = fmt.Errorf("outside the job's scope: %w", chain.ErrDrop)
)

// The chain this stage runs. A link is given a request on its way to the
// frontier and returns the request to queue, or a drop.
type (
	// Request is what is queued. The frontier's own type, because a scheduler
	// that had a type of its own would be converting on both sides of itself.
	Request = frontier.Request

	// Handler queues a request, or refuses it.
	Handler = chain.Handler[*Request, *Request]

	// Wrapper is what a middleware returns.
	Wrapper = chain.Wrapper[*Request, *Request]

	// Middleware builds one wrapper from its configuration.
	Middleware = plugin.Factory[*Request, *Request]

	// HandlerFunc adapts an ordinary function to [Handler].
	HandlerFunc = chain.Func[*Request, *Request]
)

// reg holds this stage's middleware.
var reg = plugin.NewRegistry[*Request, *Request](engine.StageScheduler)

// Register adds a middleware, from an init function in its own package.
func Register(name string, m Middleware) { reg.Register(name, m) }

// Unregister removes a middleware, and exists for tests. See
// [registry.Registry.Unregister].
func Unregister(name string) { reg.Unregister(name) }

// Registered lists what this build has, sorted.
func Registered() []string { return reg.Names() }

// Has reports whether a middleware is registered.
func Has(name string) bool { return reg.Has(name) }

// Stage is a job's scheduler.
type Stage struct {
	job   string
	queue frontier.Frontier
	own   bool // whether Close should close the frontier

	handler Handler
	chain   *plugin.Chain[*Request, *Request]

	scope    *scope.Scope
	maxDepth int
	canon    urls.Options

	// paced is the crawl-delay already recorded for each host, so that a number
	// arriving on every page is written once. See [Stage.Pace].
	mu    sync.Mutex
	paced map[string]time.Duration
}

// Options are what a caller supplies that the job document cannot.
type Options struct {
	// Frontier is where requests are queued. Nil opens one under Dir, which is
	// what a server does; a test passes its own.
	Frontier frontier.Frontier

	// Dir is where a frontier of our own keeps its database.
	Dir string

	// Eval resolves `secret()` in plugin configuration.
	Eval *hcl.EvalContext

	// Canon is how URLs are made comparable. The `dupefilter` plugin normally
	// sets this per job; a caller with no plugins can set it directly.
	Canon urls.Options
}

// New builds the scheduler a job configured.
func New(ctx context.Context, job *engine.Job, opts Options, open func(frontier.Config) (frontier.Frontier, error)) (*Stage, error) {
	if job == nil {
		return nil, errors.New("scheduler: no job")
	}

	bounds, err := scope.New(job.Domains, job.Included, job.Excluded)
	if err != nil {
		return nil, fmt.Errorf("scheduler: job %q: %w", job.Name, err)
	}

	rate, err := job.Scheduler.RateDuration()
	if err != nil {
		return nil, fmt.Errorf("scheduler: job %q: %w", job.Name, err)
	}

	queue, own := opts.Frontier, false
	if queue == nil {
		if open == nil {
			return nil, errors.New("scheduler: no frontier, and no way to open one")
		}
		queue, err = open(frontier.Config{
			Policy: job.Scheduler.OrderPolicy(),
			Rate:   rate,
			Dir:    opts.Dir,
		})
		if err != nil {
			return nil, fmt.Errorf("scheduler: job %q: %w", job.Name, err)
		}
		own = true
	}

	built, err := plugin.Build(ctx, reg, job, engine.StageScheduler, opts.Eval)
	if err != nil {
		if own {
			_ = queue.Close()
		}
		return nil, err
	}

	s := &Stage{
		job:      job.Name,
		queue:    queue,
		own:      own,
		chain:    built,
		scope:    bounds,
		maxDepth: job.Scheduler.Depth(),
		canon:    opts.Canon,
		paced:    map[string]time.Duration{},
	}
	s.handler = built.Handler(HandlerFunc(s.enqueue))
	return s, nil
}

// Submit offers URLs to the frontier and reports how many were new.
//
// Each goes through the chain on its own, so one refusal does not take the rest
// of a page's links with it. A drop is not an error and is not counted.
func (s *Stage) Submit(ctx context.Context, reqs ...*Request) (int, error) {
	var (
		added    int
		problems []error
	)

	for _, req := range reqs {
		if req == nil {
			continue
		}
		queued, err := s.handler.Handle(ctx, req)
		switch {
		case chain.Dropped(err):
			continue
		case err != nil:
			problems = append(problems, err)
			continue
		case queued == nil:
			// Already known, which is the most ordinary thing a crawl finds.
			continue
		}
		added++
	}
	return added, errors.Join(problems...)
}

// enqueue is the core the chain wraps: what a request means once every link has
// had its say.
//
// Depth and scope are checked here rather than in a link, because both are
// attributes and an attribute's enforcement cannot be something a job turns off
// by deleting a plugin.
//
// max_pages is not checked here, and the difference is worth stating: depth is
// a property of a URL and is knowable the moment one arrives, while a page
// budget is a count of pages fetched and is only knowable where fetching
// happens. Checking it here against the length of the queue looked equivalent
// and was not: it stopped a crawl from queueing what it had discovered, so a
// crawl that hit its budget left nothing for a later run to resume from.
func (s *Stage) enqueue(ctx context.Context, req *Request) (*Request, error) {
	if err := s.prepare(req); err != nil {
		return nil, err
	}

	if s.maxDepth > 0 && req.Depth > s.maxDepth {
		return nil, fmt.Errorf("%s: %w (depth %d)", req.URL, ErrTooDeep, req.Depth)
	}
	if !s.scope.Allows(req.URL) {
		return nil, fmt.Errorf("%s: %w", req.URL, ErrOutOfScope)
	}
	added, err := s.queue.Add(ctx, s.job, *req)
	if err != nil {
		return nil, err
	}
	if added == 0 {
		// Already known. Not a drop and not an error: re-discovering a URL is
		// the most ordinary thing a crawl does.
		return nil, nil
	}
	return req, nil
}

// prepare fills in what a caller left out, so that a request built by hand and
// one discovered by a spider reach the frontier in the same shape.
func (s *Stage) prepare(req *Request) error {
	normalised, err := urls.Normalise(req.URL, s.canon)
	if err != nil {
		return fmt.Errorf("%w: %w", err, chain.ErrDrop)
	}
	req.URL = normalised

	if req.Hash == "" {
		req.Hash = urls.Hash(normalised)
	}
	if req.Host == "" {
		req.Host = urls.Host(normalised)
	}
	if req.Discovered.IsZero() {
		req.Discovered = time.Now().UTC()
	}
	return nil
}

// Next hands out the best request whose host is not cooling.
//
// It returns [frontier.ErrEmpty] when nothing is due, which covers both an
// exhausted crawl and one waiting on politeness. [Stage.Waiting] tells them
// apart.
func (s *Stage) Next(ctx context.Context, now time.Time, hold time.Duration) (*Request, error) {
	return s.queue.Lease(ctx, s.job, now, hold)
}

// Done reports a request as fetched. The attempt is the one [Stage.Next]
// returned, and a report that no longer holds the lease is ignored.
func (s *Stage) Done(ctx context.Context, hash string, attempt int) error {
	return s.queue.Done(ctx, s.job, hash, attempt)
}

// Fail reports a request as failed, so it is tried again until it has been
// tried too often. The attempt is the one [Stage.Next] returned, and a report
// that no longer holds the lease is ignored.
func (s *Stage) Fail(ctx context.Context, hash string, attempt int) error {
	return s.queue.Fail(ctx, s.job, hash, attempt)
}

// Pace records what a host's robots.txt asked for between requests, so that the
// stage which decides politeness knows what the site actually wants.
//
// The number is learnt in the downloader, where robots.txt is read, and acted
// on here, where the frontier is. That split is why `Crawl-delay` was parsed
// and thrown away: there was no way back, so every host was crawled at
// `scheduler.rate` whatever its file said.
//
// # The repetition is filtered here, and only here
//
// Every response carries the delay of the host that served it, so this is
// called once per page and has to write once per host. The downloader cannot do
// that filtering: it does not know whether the response carrying its report
// survived the trip, and when it tried, the redirect follower was quietly
// eating them.
//
// This stage can, because it is the one holding the frontier: what it has
// already written is a thing only it knows. A repeat with the same number is
// dropped and a changed one is written, so a site that alters its file mid-crawl
// still takes effect.
func (s *Stage) Pace(ctx context.Context, host string, now time.Time, delay time.Duration) error {
	s.mu.Lock()
	known, seen := s.paced[host]
	if seen && known == delay {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()

	if err := s.queue.Pace(ctx, host, now, delay); err != nil {
		// Not remembered, so the next page for this host tries again. A write
		// that failed is not a delay that was recorded.
		return err
	}

	s.mu.Lock()
	s.paced[host] = delay
	s.mu.Unlock()
	return nil
}

// Waiting is how many requests are still to be fetched.
func (s *Stage) Waiting(ctx context.Context) (int, error) {
	return s.queue.Len(ctx, s.job)
}

// Middleware lists the chain in the order it runs, which is what a log line at
// the start of a run should say.
func (s *Stage) Middleware() []string { return s.chain.Names() }

// Close releases the chain, and the frontier if this opened it.
func (s *Stage) Close() error {
	err := s.chain.Close()
	if s.own {
		if closeErr := s.queue.Close(); closeErr != nil {
			return errors.Join(err, closeErr)
		}
	}
	return err
}
