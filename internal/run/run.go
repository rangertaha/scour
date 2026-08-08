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

// Shutdown is how long the tidying up after a crawl may take.
//
// It runs on a context that is deliberately not the crawl's, so an interrupted
// crawl can still write the records it is holding, close its exports and say
// what it did. It still needs a bound: a store that has stopped answering must
// not turn ctrl-c into a process that never exits, which is the thing pressing
// ctrl-c was meant to avoid.
const Shutdown = 30 * time.Second

// StallFor is how long a run waits with nothing progressing before it gives up,
// given the two things that legitimately make it wait.
//
// A host cooling, and one fetch taking as long as the job allows it to.
// Progress is only recorded after a fetch returns, so both have to be in the
// bound: the rate term was added and the fetch term was not, and a job with
// `timeout = "10m"` and one slow page had a worker declare the crawl stalled
// six minutes in while another was still fetching perfectly happily. The run
// then reported Stalled and blamed the store for zero refused writes.
//
// A function rather than an expression at the one call site, so a test can
// assert the bound this computes rather than recomputing it and agreeing with
// itself.
func StallFor(rate, fetch time.Duration) time.Duration {
	return Stall + rate + fetch
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
	// A pipeline is refused outright, because there is no seam to supply and
	// no node that serves one: [Options] has a Fetch and a Read and nothing
	// for a pipeline, and [node.Options.Serve] answers "nothing here serves a
	// pipeline stage". Saying so is the whole fix. Running it here silently
	// wrote the operator's records on the machine they were moving them off.
	if job.Pipeline != nil && job.Pipeline.IsExternal() {
		return fmt.Errorf(
			"run: job %q: its pipeline is external, and nothing serves an external pipeline. "+
				"Take `external = true` out of the pipeline block", job.Name)
	}

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
		stall = StallFor(rate, fetch)
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
							if ctx.Err() != nil {
								ending.Store(Stopped)
								return
							}
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
					// An interrupt that lands while the frontier is being
					// asked comes back as a context error from the query, and
					// that is the interrupt rather than a failure. Reported as
					// one, the run exited non-zero and printed no summary at
					// all: whether ctrl-c was a clean stop or a broken store
					// depended on which microsecond it arrived in.
					if ctx.Err() != nil {
						ending.Store(Stopped)
						return
					}
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

	// Tidying up runs on a context of its own, because the one the crawl ran
	// under may be the reason it stopped.
	//
	// Cancellation means "start nothing new", and everything after this point
	// is finishing what was already started: writing the records the pipeline
	// is holding, closing the exports, counting what is left. Doing that on the
	// cancelled context meant ctrl-c produced "frontier/sqlite: len: context
	// canceled" and a non-zero exit instead of the summary, and the Stopped
	// ending the loop had carefully computed never reached the person who
	// pressed the key. The records in flight were dropped with it.
	//
	// Bounded, because a shutdown that cannot finish still has to end.
	shutdown, done := context.WithTimeout(context.WithoutCancel(ctx), Shutdown)
	defer done()

	// The workers' problems are collected before anything can return, because
	// a flush failure used to return first and take them with it: an exporter
	// running out of disk hid the frontier failure that had stopped the crawl
	// early, which is the more diagnostic of the two and the reason there was
	// nothing left to export.
	var failures []error
	for err := range problems {
		failures = append(failures, err)
	}
	if err := r.flush(shutdown); err != nil {
		failures = append(failures, err)
	}
	if err := errors.Join(failures...); err != nil {
		return ending.Load().(Ending), err
	}

	// A crawl that could not queue what it discovered has not finished the
	// site, whatever the frontier's emptiness suggests. Reporting success
	// here would exit zero on a run that threw its work away.
	//
	// Unless it was interrupted, in which case the links it could not queue
	// were the ones in flight when the person pressed the key, and they are
	// rediscovered by the resume that being interrupted is supposed to leave
	// possible. Treating them as a failure made ctrl-c exit non-zero and print
	// no summary, depending on whether a page happened to be mid-read: whether
	// a clean stop looked clean came down to timing.
	if lost := r.stats.Lost.Load(); lost > 0 && ending.Load().(Ending) != Stopped {
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
	case err != nil && ctx.Err() != nil:
		// The crawl was interrupted while this page was in flight. That is not
		// the page failing, and reporting it as one spends a frontier attempt
		// on it: three interruptions while the same slow URL was being fetched
		// abandoned it altogether, so the resume that interruption exists to
		// preserve quietly lost a URL. The lease expires on its own and the
		// next run takes it.
		r.log.DebugContext(ctx, "interrupted mid-fetch", "url", req.URL)
		return
	case err != nil:
		r.stats.Failed.Add(1)
		r.log.WarnContext(ctx, "fetch failed", "url", req.URL, "error", err)
		r.report(ctx, "report a failure", req.URL, func(ctx context.Context) error {
			return r.sched.Fail(ctx, req.Hash, req.Attempt)
		})
		return
	}

	r.stats.Fetched.Add(1)
	if resp.Cached {
		r.stats.Cached.Add(1)
	}

	// What the site asked for, on its way to the only stage that can honour it.
	//
	// Before the page is read rather than after, because reading it is the slow
	// part and the next lease of this host may happen while it is going on. The
	// downloader sends each host once, so this is one write per host.
	r.pace(ctx, resp)

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

	added, err := r.sched.Submit(ctx, out.Links...)

	// What did land is counted whatever else happened, and only what did not
	// is counted lost.
	//
	// Submit returns both, because a partial failure is the ordinary one: most
	// of a page's links are usually dropped as out of scope, which is not a
	// failure at all. Counting the whole page as lost on any error meant a page
	// with a hundred links, ninety-nine of them off-site and one write that
	// failed, reported a hundred urls thrown away and failed the run, while the
	// summary said nothing had been queued from a page that queued one.
	r.stats.Queued.Add(int64(added))

	if err != nil {
		// Counted, not merely logged. A store that has stopped accepting work
		// is not a quiet crawl: every link found from here on is thrown away,
		// the frontier drains, and the run reports that it finished the site.
		// The count is what [Run.Do] turns into a failure at the end.
		if lost := len(out.Links) - added; lost > 0 {
			r.stats.Lost.Add(int64(lost))
		}
		r.log.WarnContext(ctx, "could not queue what a page found", "url", req.URL, "error", err)
	}

	if len(out.Items) > 0 {
		r.stats.Items.Add(int64(len(out.Items)))
		r.keep(record.From(out.URL, out.Spec, resp.Fetched, out.Items))
	}

	r.done(ctx, req)
}

func (r *Run) done(ctx context.Context, req *scheduler.Request) {
	r.report(ctx, "report a page finished", req.URL, func(ctx context.Context) error {
		return r.sched.Done(ctx, req.Hash, req.Attempt)
	})
}

// pace tells the scheduler what the hosts on this response asked for between
// requests.
//
// Called once per page, because every response carries the delay of the host
// that served it. It is not a write per page: [scheduler.Stage.Pace] keeps what
// it has already recorded and drops a repeat, which is the only place that
// knows.
//
// Routed through report because it is the same kind of write as the others
// here: it describes something that has already happened, a failure is
// recoverable, and the crawl should be counted as having had a store problem
// rather than stopped. The cost of losing one is that the host stays at the
// job's own rate, which is the behaviour this whole path exists to replace, so
// it must be visible rather than silent.
func (r *Run) pace(ctx context.Context, resp *downloader.Response) {
	for _, asked := range resp.Delays {
		r.report(ctx, "record a host's crawl-delay", asked.Host, func(ctx context.Context) error {
			// r.now rather than time.Now, because the frontier is paced and
			// leased against one clock and a hold taken on a different one is a
			// hold in the wrong place. See [Options.Now].
			return r.sched.Pace(ctx, asked.Host, r.now(), asked.Delay)
		})
	}
}

// report performs a frontier write that describes work already done, and counts
// it when it fails.
//
// One place, because there were three and each of them logged and moved on. A
// crawl whose store has started refusing writes is not a quiet crawl: pages
// whose completion could not be recorded are fetched again when their leases
// expire, and the operator sees a run that took twice as long for no stated
// reason. These are recoverable, so they do not fail the run the way a lost
// URL does; they are counted, and the summary says so.
func (r *Run) report(ctx context.Context, what, url string, write func(context.Context) error) {
	// A report is not cancelled with the crawl, for the same reason the tidying
	// up is not: it describes work that has already happened. On ctrl-c the
	// fetch in flight failed with "context canceled" and the Fail that should
	// have released its lease failed the same way, so the URL stayed held for
	// the length of the lease and the resume that ctrl-c is supposed to make
	// safe had nothing due. Bounded, so a store that has stopped answering
	// cannot hold the shutdown open.
	ctx, done := context.WithTimeout(context.WithoutCancel(ctx), Shutdown)
	defer done()

	if err := write(ctx); err != nil {
		r.stats.Store.Add(1)
		r.log.WarnContext(ctx, "could not "+what, "url", url, "error", err)
	}
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
