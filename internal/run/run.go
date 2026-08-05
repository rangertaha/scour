// SPDX-License-Identifier: GPL-3.0-or-later

// Package run is the crawl: the four stages wired to each other directly.
//
// It is what a single node does, and it is the thing the bus has to be
// equivalent to. When stages become NATS services, the claim is that the same
// job produces the same records either way, and that claim needs this side of
// it to exist and to be tested first.
//
// # The loop
//
//	lease a request     from the scheduler, which decides what and paces it
//	fetch it            through the downloader, robots and cache included
//	read it             through the spider, into items and links
//	submit the links    back to the scheduler, which decides what they mean
//	report it done      so the lease is not handed out again
//
// Records go to the pipeline and then to the exporters in batches, because a
// graph over one record at a time cannot dedupe or rank, and those are two of
// the four things a pipeline is for.
//
// # When it stops
//
// When the frontier has nothing due and nothing is in flight, or when the
// budget runs out. Those are different endings and the summary says which: a
// crawl that finished the site and a crawl that hit its page limit look
// identical in the output and mean opposite things.
package run

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"

	"github.com/rangertaha/scour/internal/chain"
	"github.com/rangertaha/scour/internal/downloader"
	"github.com/rangertaha/scour/internal/engine"
	"github.com/rangertaha/scour/internal/exporter"
	"github.com/rangertaha/scour/internal/frontier"
	"github.com/rangertaha/scour/internal/pipeline"
	"github.com/rangertaha/scour/internal/record"
	"github.com/rangertaha/scour/internal/scheduler"
	"github.com/rangertaha/scour/internal/spider"
	"github.com/rangertaha/scour/internal/urls"
)

// Lease is how long a worker holds a request before another may take it.
//
// Long enough that a slow page does not get fetched twice, short enough that a
// worker which died holding one does not take the URL with it for the rest of
// the run.
//
// A floor rather than the whole answer: a job may set a request timeout longer
// than this, and the hold a run actually takes is computed against it. See the
// hold in [Run.Do].
const Lease = 5 * time.Minute

// Hold is how long a worker holds a request, given how long one fetch may take.
//
// [Lease] is a floor, not the answer. A job may set a request timeout longer
// than it, and then the lease expired while the page was still downloading: a
// second worker leased the same URL and fetched it alongside the first, two
// requests to one host at once, defeating the rate the job asked for. The first
// worker's report was then correctly discarded by the lease fence, which is
// what a fence is for and why nothing counted it or logged it. The crawl looked
// healthy and was hitting the site twice.
//
// A minute of margin over the fetch, because a request that has just timed out
// still has to be reported before the hold is worth releasing.
func Hold(fetch time.Duration) time.Duration {
	return max(Lease, fetch+time.Minute)
}

// Idle is how long the loop waits when the frontier has nothing due but is not
// empty, which is what politeness looks like from here.
const Idle = 50 * time.Millisecond

// Stall is how long a run waits with nothing progressing before it gives up.
//
// A crawl legitimately idles: politeness holds every host, and the loop waits
// for one to cool. What it must not do is idle forever, and it could. A
// frontier that cannot record a page as finished leaves every URL leased, so
// nothing is ever due, nothing is in flight, and the loop waits for leases
// that will never be resolved. Writing a test for a store that refuses writes
// is how that was found: the test ran until it timed out.
//
// Measured from the last thing that happened rather than from the start, so a
// slow polite crawl is not mistaken for a stalled one, and longer than [Lease]
// so that waiting for a lease to expire is not mistaken for one either.
//
// The job's politeness rate is added to it, because that is the other thing
// that legitimately makes the loop wait and this constant alone cannot know how
// long for. Without it a job with `rate = "10m"` and two URLs on one host
// fetched the first page, waited politely as it was told to, and killed itself
// six minutes in with an error blaming the store for zero refused writes. Any
// rate above a minute did that.
const Stall = Lease + time.Minute

// Options are what a caller supplies that the job document cannot.
type Options struct {
	// Dir is where the frontier and, failing a cache plugin, the bodies go.
	Dir string

	// Frontier is the queue. Nil opens one under Dir.
	Frontier frontier.Frontier

	// Eval resolves `secret()` in plugin configuration.
	Eval *hcl.EvalContext

	// Open builds a frontier when one was not supplied.
	Open func(frontier.Config) (frontier.Frontier, error)

	// Log is where progress goes. Nil is silent, which is what a test wants.
	Log *slog.Logger

	// Now is the clock. Nil is time.Now; a test passes its own so that
	// politeness can be proved without waiting for it.
	Now func() time.Time

	// Stall overrides how long a run waits with nothing progressing before it
	// gives up. Zero means [Stall].
	//
	// A knob only a test turns, and it exists because the obvious way to test
	// the bound does not work: winding a fake clock forward far enough to
	// reach it also expires every lease, so the URLs come back, the loop makes
	// progress, and the thing under test never happens.
	Stall time.Duration

	// Fetch and Read replace the local stages with ones that are somewhere
	// else. That is the whole of what running on a bus changes here: the loop
	// is told nothing about where its stages are, which is what makes the two
	// arrangements comparable.
	Fetch downloader.Handler
	Read  spider.Handler
}

// external refuses a job whose stages are somewhere else when nothing was
// supplied to reach them.
//
// The seam is [Options.Fetch] and [Options.Read]: that is the whole of what
// running on a bus changes here, and a caller that has not used it is a caller
// with no way to reach the stage the job named. Checked here rather than in
// each command, so a new caller cannot forget.
func external(job *engine.Job) error {
	var stages []string
	if job.Downloader != nil && job.Downloader.IsExternal() {
		stages = append(stages, "downloader")
	}
	if job.Spider != nil && job.Spider.IsExternal() {
		stages = append(stages, "spider")
	}
	if len(stages) == 0 {
		return nil
	}
	return fmt.Errorf(
		"run: job %q: its %s is external, and this run has no way to reach one. "+
			"Run it on a node that serves the stage, or take `external = true` out of the document",
		job.Name, strings.Join(stages, " and "))
}

// Run is one job, ready to crawl.
type Run struct {
	job   *engine.Job
	opts  Options
	log   *slog.Logger
	now   func() time.Time
	canon urls.Options

	sched *scheduler.Stage
	fetch downloader.Handler
	read  spider.Handler
	graph *pipeline.Pipeline
	write *exporter.Set

	// Only a stage this built is a stage this closes. One that was handed in
	// belongs to whoever handed it in.
	closeFetch func() error
	closeRead  func() error

	mu       sync.Mutex
	records  []*record.Record
	exported []*record.Record

	stats Stats
}

// Stats is what a crawl did.
type Stats struct {
	// Fetched is pages that reached the network or the cache.
	Fetched atomic.Int64
	// Cached is how many of those came from the cache.
	Cached atomic.Int64
	// Dropped is requests refused on purpose: robots, scope, status.
	Dropped atomic.Int64
	// Failed is requests that could not be fetched at all.
	Failed atomic.Int64
	// Items is what extraction found.
	Items atomic.Int64
	// Queued is URLs added to the frontier.
	Queued atomic.Int64
	// Lost is URLs a page found that could not be queued, because the store
	// refused them. Not the same as dropped: a drop is a decision and this is
	// a failure.
	Lost atomic.Int64
	// Store is how many times the frontier refused a bookkeeping write, which
	// is recoverable and not free: a page whose completion could not be
	// recorded is fetched again when its lease expires.
	Store atomic.Int64
	// Exported is records written out.
	Exported atomic.Int64
}

// Ending says why a crawl stopped, because a crawl that finished the site and
// one that hit its page limit look identical in the output and mean opposite
// things.
type Ending string

const (
	// Finished means the frontier ran dry.
	Finished Ending = "finished"
	// BudgetSpent means max_pages was reached.
	BudgetSpent Ending = "budget spent"
	// TimeUp means max_time was reached.
	TimeUp Ending = "time up"
	// Stopped means the caller's context ended it.
	Stopped Ending = "stopped"
	// Stalled means nothing progressed for long enough that nothing was going
	// to. A frontier that cannot record a page as finished produces exactly
	// this: every URL stays leased, so nothing is ever due again.
	Stalled Ending = "stalled"
)

// New builds every stage a job needs.
func New(ctx context.Context, job *engine.Job, opts Options) (*Run, error) {
	if job == nil {
		return nil, errors.New("run: no job")
	}
	if opts.Log == nil {
		opts.Log = slog.New(slog.DiscardHandler)
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}

	r := &Run{job: job, opts: opts, log: opts.Log.With("job", job.Name), now: opts.Now}

	// The dupefilter decides what counts as the same page, and the spider has
	// to report links in the spelling the frontier will store them under, or
	// one URL arrives as two.
	r.canon = canonOf(job)

	var err error
	if r.sched, err = scheduler.New(ctx, job, scheduler.Options{
		Frontier: opts.Frontier,
		Dir:      opts.Dir,
		Eval:     opts.Eval,
		Canon:    r.canon,
	}, opts.Open); err != nil {
		return nil, err
	}

	// A job that says its downloader is external and a caller that supplied no
	// handler for it do not agree, and crawling locally is the wrong way to
	// resolve that. `scour show` reports the setting back to the operator as
	// "yes, waiting 5m0s", so a run that quietly ignored it left them believing
	// their pages were being fetched somewhere they were not. Refused by name,
	// which is what the whole engine does with a job it cannot honour.
	if err := external(job); err != nil && opts.Fetch == nil && opts.Read == nil {
		r.sched.Close()
		return nil, err
	}

	if opts.Fetch != nil {
		r.fetch = opts.Fetch
	} else {
		local, err := downloader.New(ctx, job, downloader.Options{Eval: opts.Eval})
		if err != nil {
			r.sched.Close()
			return nil, err
		}
		r.fetch, r.closeFetch = local, local.Close
	}

	if opts.Read != nil {
		r.read = opts.Read
	} else {
		local, err := spider.New(ctx, job, spider.Options{Eval: opts.Eval, Canon: r.canon})
		if err != nil {
			r.close()
			return nil, err
		}
		r.read, r.closeRead = local, local.Close
	}

	if r.graph, err = pipeline.New(ctx, job); err != nil {
		r.close()
		return nil, err
	}

	if r.write, err = exporter.New(ctx, job, nil); err != nil {
		r.close()
		return nil, err
	}
	return r, nil
}

// Seed queues the job's start URLs.
//
// Separate from [Run.Do] so that a resumed crawl can skip it: the frontier
// survives a restart, and re-seeding would be harmless but would say a crawl
// had found URLs it had merely remembered.
func (r *Run) Seed(ctx context.Context) (int, error) {
	reqs := make([]*scheduler.Request, 0, len(r.job.Start))
	for _, start := range r.job.Start {
		reqs = append(reqs, &scheduler.Request{URL: start, Depth: 0, Discovered: r.now().UTC()})
	}

	added, err := r.sched.Submit(ctx, reqs...)
	r.stats.Queued.Add(int64(added))
	return added, err
}

// Do crawls until there is nothing left to do, or the budget runs out.
func (r *Run) Do(ctx context.Context) (Ending, error) {
	workers := r.job.Scheduler.Parallelism()
	if workers < 1 {
		workers = 1
	}

	deadline, err := r.job.Scheduler.MaxTimeDuration()
	if err != nil {
		return "", fmt.Errorf("run: job %q: %w", r.job.Name, err)
	}

	var (
		inFlight atomic.Int64
		ending   atomic.Value
		problems = make(chan error, workers)
		wg       sync.WaitGroup
	)
	ending.Store(Finished)

	maxPages := r.job.Scheduler.Pages()

	// A budget is checked rather than enforced by cancelling, and the
	// difference is a bug this had: cancelling the context stopped the page
	// that was in flight from queueing what it had just found, so a crawl that
	// hit its budget left nothing for a later run to resume from. Work already
	// started finishes; workers simply stop taking more.
	var until time.Time
	if deadline > 0 {
		until = r.now().Add(deadline)
	}
	spent := func() Ending {
		switch {
		case maxPages > 0 && r.stats.Fetched.Load() >= int64(maxPages):
			return BudgetSpent
		case !until.IsZero() && r.now().After(until):
			return TimeUp
		}
		return ""
	}

	// A store that has stopped accepting bookkeeping is a loop, not a slow
	// crawl: the same page comes back every time its lease expires. Stopping
	// is the only outcome that ends.
	// The last time anything happened, which is what tells a slow polite crawl
	// apart from a stalled one.
	var progress atomic.Int64
	progress.Store(r.now().UnixNano())

	rate, err := r.job.Scheduler.RateDuration()
	if err != nil {
		return "", fmt.Errorf("run: job %q: %w", r.job.Name, err)
	}

	// The hold has to outlast one fetch, and a job may set a request timeout
	// longer than [Lease]. With `timeout = "10m"` the lease expired while the
	// page was still downloading, so a second worker leased the same URL and
	// fetched it alongside the first, defeating the per-host rate the job asked
	// for. The first worker's report was then correctly discarded by the fence,
	// which is what a fence is for and why nothing counted it or logged it: the
	// crawl looked healthy and was hitting the site twice.
	//
	// Computed here beside the stall bound, because both are the same question:
	// how long a thing that is working can legitimately take.
	fetch, err := r.job.Downloader.RequestTimeout()
	if err != nil {
		return "", fmt.Errorf("run: job %q: %w", r.job.Name, err)
	}
	hold := Hold(fetch)

	stall := r.opts.Stall
	if stall <= 0 {
		stall = Stall + rate
	}

	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()

			for {
				if ctx.Err() != nil {
					ending.Store(Stopped)
					return
				}
				if why := spent(); why != "" {
					ending.Store(why)
					return
				}
				// Nothing has progressed for long enough that nothing is
				// going to. A frontier that cannot record a page as finished
				// leaves every URL leased, so nothing is ever due again and
				// the loop below waits forever.
				if r.now().Sub(time.Unix(0, progress.Load())) > stall {
					ending.Store(Stalled)
					return
				}

				req, err := r.sched.Next(ctx, r.now(), hold)
				switch {
				case errors.Is(err, frontier.ErrEmpty):
					// Nothing due. Either the crawl is over or a host is
					// cooling, and the only way to tell is whether anything is
					// still in flight that could queue more.
					if inFlight.Load() == 0 {
						waiting, err := r.sched.Waiting(ctx)
						if err != nil {
							problems <- err
							return
						}
						if waiting == 0 {
							return
						}
					}
					select {
					case <-ctx.Done():
						// Recorded here as well as at the top of the loop. A
						// worker parked on the politeness backoff returns from
						// this branch and never reaches that check, so an
						// interrupted crawl with URLs still queued reported
						// itself finished: the exact confusion Ending exists
						// to prevent.
						ending.Store(Stopped)
						return
					case <-time.After(Idle):
					}
					continue

				case err != nil:
					problems <- err
					return
				}

				inFlight.Add(1)
				r.one(ctx, req)
				inFlight.Add(-1)
				progress.Store(r.now().UnixNano())
			}
		}()
	}

	wg.Wait()
	close(problems)

	if err := r.flush(ctx); err != nil {
		return ending.Load().(Ending), err
	}

	var failures []error
	for err := range problems {
		failures = append(failures, err)
	}
	if err := errors.Join(failures...); err != nil {
		return ending.Load().(Ending), err
	}

	// A crawl that could not queue what it discovered has not finished the
	// site, whatever the frontier's emptiness suggests. Reporting success
	// here would exit zero on a run that threw its work away.
	if lost := r.stats.Lost.Load(); lost > 0 {
		return ending.Load().(Ending), fmt.Errorf(
			"run: job %q: %d discovered urls could not be queued", r.job.Name, lost)
	}
	if ending.Load().(Ending) == Stalled {
		return Stalled, fmt.Errorf(
			"run: job %q: nothing progressed for %s, with %d frontier writes refused",
			r.job.Name, stall, r.stats.Store.Load())
	}
	return ending.Load().(Ending), nil
}

// one fetches and reads a single request.
//
// Every outcome is recorded and none of them stops the crawl. A page that was
// refused, one that failed and one that produced nothing are all ordinary, and
// a crawler that stopped at the first would never finish a site.
func (r *Run) one(ctx context.Context, req *scheduler.Request) {
	resp, err := r.fetch.Handle(ctx, &downloader.Request{
		URL:   req.URL,
		Job:   r.job.Name,
		Depth: req.Depth,
	})
	switch {
	case chain.Dropped(err):
		r.stats.Dropped.Add(1)
		r.log.DebugContext(ctx, "dropped", "url", req.URL, "why", err)
		r.done(ctx, req)
		return
	case err != nil:
		r.stats.Failed.Add(1)
		r.log.WarnContext(ctx, "fetch failed", "url", req.URL, "error", err)
		r.bookkeeping(ctx, "report a failure", req.URL, r.sched.Fail(ctx, req.Hash, req.Attempt))
		return
	}

	r.stats.Fetched.Add(1)
	if resp.Cached {
		r.stats.Cached.Add(1)
	}

	out, err := r.read.Handle(ctx, resp)
	switch {
	case chain.Dropped(err):
		r.stats.Dropped.Add(1)
		r.done(ctx, req)
		return
	case err != nil:
		r.stats.Failed.Add(1)
		r.log.WarnContext(ctx, "could not read the page", "url", req.URL, "error", err)
		r.done(ctx, req)
		return
	}

	if added, err := r.sched.Submit(ctx, out.Links...); err != nil {
		// Counted, not merely logged. A store that has stopped accepting work
		// is not a quiet crawl: every link found from here on is thrown away,
		// the frontier drains, and the run reports that it finished the site.
		// The count is what [Run.Do] turns into a failure at the end.
		r.stats.Lost.Add(int64(len(out.Links)))
		r.log.WarnContext(ctx, "could not queue what a page found", "url", req.URL, "error", err)
	} else {
		r.stats.Queued.Add(int64(added))
	}

	if len(out.Items) > 0 {
		r.stats.Items.Add(int64(len(out.Items)))
		r.keep(record.From(out.URL, out.Spec, resp.Fetched, out.Items))
	}

	r.done(ctx, req)
}

func (r *Run) done(ctx context.Context, req *scheduler.Request) {
	r.bookkeeping(ctx, "report a page finished", req.URL, r.sched.Done(ctx, req.Hash, req.Attempt))
}

// bookkeeping records a frontier write that failed.
//
// One place, because there were three and each of them logged and moved on. A
// crawl whose store has started refusing writes is not a quiet crawl: pages
// whose completion could not be recorded are fetched again when their leases
// expire, and the operator sees a run that took twice as long for no stated
// reason. These are recoverable, so they do not fail the run the way a lost
// URL does; they are counted, and the summary says so.
func (r *Run) bookkeeping(ctx context.Context, what, url string, err error) {
	if err == nil {
		return
	}
	r.stats.Store.Add(1)
	r.log.WarnContext(ctx, "could not "+what, "url", url, "error", err)
}

func (r *Run) keep(records []*record.Record) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records = append(r.records, records...)
}

// flush puts everything through the graph and out to the exporters.
//
// At the end rather than as it goes, because dedupe and rank are two of the
// four things a pipeline is for and neither can work on one record at a time.
// The cost is that a crawl holds its records in memory; the records database
// will be where that stops being true.
func (r *Run) flush(ctx context.Context) error {
	r.mu.Lock()
	records := r.records
	r.records = nil
	r.mu.Unlock()

	if len(records) == 0 {
		return nil
	}

	out, err := r.graph.Run(ctx, records)
	if err != nil {
		return err
	}
	if err := r.write.Write(ctx, out...); err != nil {
		return err
	}
	r.stats.Exported.Add(int64(len(out)))

	// Kept so a caller can compare two runs. It is what makes the claim about
	// the bus checkable: the same job, wired two ways, and the records held
	// against each other.
	r.mu.Lock()
	r.exported = append(r.exported, out...)
	r.mu.Unlock()
	return nil
}

// Records is what the crawl exported, for a caller that wants to look at them
// rather than read a file back.
func (r *Run) Records() []*record.Record {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*record.Record(nil), r.exported...)
}

// Stats is what the crawl did, for a caller that wants to report it.
func (r *Run) Stats() *Stats { return &r.stats }

// Waiting is how many URLs are still queued.
func (r *Run) Waiting(ctx context.Context) (int, error) { return r.sched.Waiting(ctx) }

// Close releases every stage.
func (r *Run) Close() error { return r.close() }

func (r *Run) close() error {
	var problems []error
	for _, closer := range []func() error{
		func() error {
			if r.write != nil {
				return r.write.Close()
			}
			return nil
		},
		func() error {
			if r.graph != nil {
				return r.graph.Close()
			}
			return nil
		},
		func() error {
			if r.closeRead != nil {
				return r.closeRead()
			}
			return nil
		},
		func() error {
			if r.closeFetch != nil {
				return r.closeFetch()
			}
			return nil
		},
		func() error {
			if r.sched != nil {
				return r.sched.Close()
			}
			return nil
		},
	} {
		if err := closer(); err != nil {
			problems = append(problems, err)
		}
	}
	return errors.Join(problems...)
}

// canonOf reads the dupefilter's settings, so the spider and the frontier agree
// about what the same page is.
//
// Read from the document rather than from the built plugin because the plugin
// is a function by then, and two stages needing the same answer is exactly the
// case where reading it twice from one source is right.
func canonOf(job *engine.Job) urls.Options {
	for _, p := range job.Chain(engine.StageScheduler) {
		if p.Name != "dupefilter" {
			continue
		}
		var c struct {
			Tracking      bool     `hcl:"strip_tracking,optional"`
			Strip         []string `hcl:"strip,optional"`
			SortQuery     bool     `hcl:"sort_query,optional"`
			TrailingSlash bool     `hcl:"strip_trailing_slash,optional"`
			LowerPath     bool     `hcl:"lower_path,optional"`
		}
		if p.Config == nil {
			return urls.Options{}
		}
		if diags := decodeInto(p.Config, &c); diags != nil {
			return urls.Options{}
		}

		opts := urls.Options{
			StripQuery:         c.Strip,
			SortQuery:          c.SortQuery,
			StripTrailingSlash: c.TrailingSlash,
			LowerPath:          c.LowerPath,
		}
		if c.Tracking {
			opts.StripQuery = append(append([]string(nil), urls.Tracking...), c.Strip...)
		}
		return opts
	}
	return urls.Options{}
}

// decodeInto is the one place this package touches HCL, which it does only to
// read a plugin's settings a second time.
func decodeInto(body hcl.Body, into any) error {
	if diags := gohcl.DecodeBody(body, nil, into); diags.HasErrors() {
		return diags
	}
	return nil
}
