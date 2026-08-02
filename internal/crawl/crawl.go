// SPDX-License-Identifier: GPL-3.0-or-later

// Package crawl drives colly.
//
// colly is the crawl engine, not a fetching library called from a hand-rolled
// loop: scheduling, robots, cookies, retries, redirects, depth tracking and
// link discovery are all left to it. This package supplies the parts colly
// leaves open, which for now is the callback set, and later the scored queue
// and the transport.
package crawl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gocolly/colly/v2"
	"github.com/gocolly/colly/v2/debug"
	collyqueue "github.com/gocolly/colly/v2/queue"

	"github.com/rangertaha/scour/internal/cache"
	"github.com/rangertaha/scour/internal/config"
	"github.com/rangertaha/scour/internal/content"
	crawlqueue "github.com/rangertaha/scour/internal/crawl/queue"
	crawlstorage "github.com/rangertaha/scour/internal/crawl/storage"
	"github.com/rangertaha/scour/internal/schedule"
	"github.com/rangertaha/scour/internal/score"
	"github.com/rangertaha/scour/internal/store"
	"github.com/rangertaha/scour/internal/transport"
)

// Options configures one crawl.
type Options struct {
	Item *store.Item
	// RunID is the occasion, recorded on every page this crawl fetches so the
	// run has a log. Zero for a crawl nobody is keeping a history of.
	RunID uint
	// Job is the crawl this run belongs to, and owns the frontier it drains.
	// Nil resolves the item's implicit job, which is what a bare "scour crawl
	// <item>" wants: one job, named after the item, made on first use.
	Job     *store.Job
	Targets []store.Target
	Types   *content.Set
	Depth   int
	Limit   int // stop after this many fetches; 0 for no limit
	// MaxTime stops the crawl after this long. It is a budget rather than a
	// timeout: reaching it is a normal end, and everything fetched so far is
	// kept. Zero means no limit.
	MaxTime time.Duration
	// Browser is the escalation policy: never, auto or always. Empty means
	// auto, which tries plain HTTP first and only pays for a browser when the
	// page turns out to need one.
	Browser string
	Scorer  score.Scorer
	Debug   bool
	// Frontier is where the crawl takes its work from. Nil builds the
	// database-backed one, which is the single-process default; a crawler
	// being handed work by another process supplies its own.
	Frontier Frontier
}

// Frontier is the queue a crawl takes its work from.
//
// Declared here because this package is the consumer. It exists so a crawler
// fed by a broker and one reading the database are the same crawler: colly's
// loop, robots, cookies, redirects and the browser escalation are shared, and
// only where the next request comes from differs.
type Frontier interface {
	Init() error
	AddRequest([]byte) error
	GetRequest() ([]byte, error)
	QueueSize() (int, error)
	IsEmpty() (bool, error)

	// Freeze makes the queue report itself empty without discarding anything,
	// which is how a crawl stops early and stays resumable.
	Freeze()
	Frozen() bool
	// SetScorer decides a request's priority as it is added.
	SetScorer(func(data []byte) float64)
	// SetRefill supplies the next batch of seeds when the queue runs dry.
	SetRefill(func() int)
	// SetBudget is asked, before each request is handed out, whether the crawl
	// can still pay for one, and claims the slot when it says yes.
	SetBudget(func() bool)
	// SetOrder is asked, before each lease, what order to drain in.
	SetOrder(func() schedule.Order)
}

// Result reports what a crawl did.
type Result struct {
	Fetched  int
	Skipped  int
	Failed   int
	Bytes    int64
	Elapsed  time.Duration
	Statuses map[int]int
	// BudgetSpent names what ended the crawl: "pages", "time", or "pause" when
	// someone stopped it. It is
	// is empty when the frontier ran out. The distinction matters: an empty
	// frontier means the site is exhausted, a spent budget means there is more
	// to fetch next run.
	BudgetSpent string
}

// Finished is this result as a run's history row.
//
// It lives here because only the crawl knows why it stopped, and the
// distinction is the one a page count hides: an exhausted frontier means the
// site is done for the scope it was given, a spent budget means there is more
// waiting for next time, and both leave the same number of pages behind.
//
// A nil result is a crawl that did not get far enough to have one, which is
// still a run that happened and still worth a row saying so.
func (r *Result) Finished(err error) store.Finished {
	f := store.Finished{Err: err, State: store.RunDone}
	if err != nil {
		f.State = store.RunFailed
	}
	if r == nil {
		return f
	}
	f.Fetched, f.Failed, f.Skipped = r.Fetched, r.Failed, r.Skipped
	f.Bytes, f.Budget, f.Statuses = r.Bytes, r.BudgetSpent, r.Statuses
	if err == nil {
		switch r.BudgetSpent {
		case "":
			f.State = store.RunDone
		case "pause":
			f.State = store.RunStopped
		default:
			f.State = store.RunBudget
		}
	}
	return f
}

// Crawler holds everything a crawl needs that outlives a single run.
type Crawler struct {
	cfg   config.Config
	store *store.Store
	cache cache.Store
	sink  Sink
	meter Meter
}

// New returns a crawler that writes results straight to the database.
func New(cfg config.Config, s *store.Store, c cache.Store) *Crawler {
	return &Crawler{cfg: cfg, store: s, cache: c, sink: DirectSink{Store: s}}
}

// WithSink returns a crawler that sends its results somewhere else, which is
// how the same crawl runs over the bus without knowing it.
func (c *Crawler) WithSink(sink Sink) *Crawler {
	clone := *c
	clone.sink = sink
	return &clone
}

// state is the per-run mutable data the callbacks share. colly runs callbacks
// from its worker pool, so every field here is guarded.
type state struct {
	mu      sync.Mutex
	fetched int
	skipped int
	failed  int
	// claimed counts requests that have gone out, whether or not they have
	// come back. The page budget has to be spent against this rather than
	// against fetched: by the time a response arrives, every other thread has
	// a request in flight already, and stopping then overshoots by a whole
	// batch. Requests that end up skipped or failed cost nothing, because the
	// budget counts pages fetched and not requests attempted.
	claimed  int
	bytes    int64
	statuses map[int]int

	// deadline is when a time budget runs out, zero when there is none.
	deadline time.Time
	// spentBudget names the budget that ended the crawl, empty when the
	// frontier simply ran out. Naming it matters: "stopped after 40 pages" and
	// "stopped after 15 minutes" tell an operator to change different numbers.
	spentBudget string

	// pausedAt is when the item was last seen paused, so the check is not
	// made on every response.
	checkedPause time.Time
	paused       bool
}

// spend reports whether either budget is used up, and records which.
//
// A budget is not a timeout. It ends the crawl the way an exhausted frontier
// does, leaving everything queued still queued and everything fetched written,
// so the next run resumes. Cancelling the context instead would take the
// database writes down with it and turn a normal stop into a tail of failures.
// claim reports whether the crawl can pay for one more request, and takes the
// slot when it can.
//
// The budget counts pages fetched, so a claim that ends up skipped or failed is
// refunded by the arithmetic rather than tracked separately: what is
// outstanding is whatever was claimed and has not yet come back one way or the
// other, and what is committed is that plus what has already been fetched.
func (s *state) claim(limit int) bool {
	if limit <= 0 {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.claimed-s.skipped-s.failed >= limit {
		return false
	}
	s.claimed++
	return true
}

func (s *state) spend(limit int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	switch {
	case limit > 0 && s.fetched >= limit:
		s.spentBudget = "pages"
	case !s.deadline.IsZero() && !time.Now().Before(s.deadline):
		s.spentBudget = "time"
	case s.paused:
		s.spentBudget = "pause"
	}
	return s.spentBudget != ""
}

// pauseInterval is how often a running crawl asks whether it has been paused.
//
// Short enough that pressing a key feels like it did something, long enough
// that a crawl fetching eight pages a second is not also running eight queries
// a second to ask permission.
const pauseInterval = time.Second

// refreshPause reads the item's paused flag, at most once per interval.
func (c *Crawler) refreshPause(ctx context.Context, st *state, itemID uint) {
	st.mu.Lock()
	if time.Since(st.checkedPause) < pauseInterval {
		st.mu.Unlock()
		return
	}
	st.checkedPause = time.Now()
	st.mu.Unlock()

	paused, err := c.store.IsPaused(ctx, itemID)
	if err != nil {
		// Not being able to ask is not a reason to stop crawling.
		slog.Debug("could not read paused state", "item", itemID, "err", err)
		return
	}
	st.mu.Lock()
	st.paused = paused
	st.mu.Unlock()
}

func (s *state) countStatus(code int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.statuses[code]++
}

// Run crawls one item and returns when the frontier is exhausted, the budget
// is spent, or ctx is cancelled.
func (c *Crawler) Run(ctx context.Context, opts Options) (*Result, error) {
	// A crawler handed its work by another process has no targets of its own:
	// what to fetch and what is in scope were both decided before it was told.
	if len(opts.Targets) == 0 && opts.Frontier == nil {
		return nil, fmt.Errorf("item %q has no targets: scour item add %s -d <domain>", opts.Item.Name, opts.Item.Name)
	}
	if opts.Scorer == nil {
		opts.Scorer = score.Fixed(1)
	}

	// The frontier belongs to a job, so a run without one named gets the
	// item's implicit job rather than an ambiguous queue. A crawler handed its
	// work by another process is drained by whoever handed it, and needs none.
	if opts.Job == nil && opts.Frontier == nil {
		job, err := c.store.JobForItem(ctx, opts.Item)
		if err != nil {
			return nil, fmt.Errorf("resolve job for %s: %w", opts.Item.Name, err)
		}
		opts.Job = job
	}

	st := &state{statuses: map[int]int{}}
	if opts.MaxTime > 0 {
		st.deadline = time.Now().Add(opts.MaxTime)
	}

	// Scope is enforced on the way into the queue rather than by colly's
	// URLFilters, which is a linear scan of one compiled expression per target.
	// See scope for what that cost on a real list.
	sc, err := NewScope(opts.Targets)
	if err != nil {
		return nil, err
	}

	collector, err := c.newCollector(opts)
	if err != nil {
		return nil, err
	}

	// The visited set and the cookie jar live in the database, so a restarted
	// crawl knows what it has already seen.
	visited := crawlstorage.New(ctx, c.store, opts.Item.ID)
	if err := collector.SetStorage(visited); err != nil {
		return nil, fmt.Errorf("attach storage: %w", err)
	}

	// So does the queue, which is also where scour's crawl order will live.
	pending := opts.Frontier
	if pending == nil {
		pending = crawlqueue.New(ctx, c.store, opts.Item.ID, opts.Job.ID)
	}
	threads := c.cfg.Crawl.Concurrency
	if threads < 1 {
		threads = 1
	}
	// This is what makes the crawl focused rather than breadth-first: the
	// queue pops in score order, and the score of a link is decided when it is
	// discovered, then carried on the request itself.
	pending.SetScorer(queuedScore)
	// The page budget is enforced where requests are handed out, so one that
	// cannot be paid for is left in the queue rather than dispatched and then
	// stopped on the way back, which overshot by whatever was in flight.
	pending.SetBudget(func() bool { return st.claim(opts.Limit) })

	// The scheduling policy is asked per lease, so it can change its mind as
	// the crawl goes on. A misspelled name fails in config validation, so by
	// here it is either registered or empty.
	policy, err := schedule.New(c.cfg.Crawl.Scheduler, schedule.Config{})
	if err != nil {
		return nil, err
	}
	pending.SetOrder(func() schedule.Order {
		st.mu.Lock()
		fetched := st.fetched
		st.mu.Unlock()
		return policy.Order(schedule.State{
			Item: opts.Item.ID, Fetched: fetched, Trained: trainedScorer(opts.Scorer),
		})
	})

	q, err := collyqueue.New(threads, pending)
	if err != nil {
		return nil, fmt.Errorf("create queue: %w", err)
	}

	c.register(ctx, collector, pending, sc, opts, st)

	// Seeds go in a batch at a time, topped up as the frontier drains, rather
	// than all at once. A list of a million targets wrote a 660MB write-ahead
	// log before fetching anything, and the crawl had to be killed having
	// fetched nothing at all: every seed was queued before the first request
	// went out. Nothing needs them queued in advance, since at most --max-pages
	// of them will ever be read.
	seeds := seedURLs(opts.Targets)
	pending.SetRefill(func() int {
		n := 0
		for len(seeds) > 0 && n < seedBatch {
			seed := seeds[0]
			seeds = seeds[1:]
			// Depth 1, matching what Collector.Visit would have used, so
			// --depth keeps meaning the same number of levels it did before
			// the queue.
			u, err := url.Parse(seed)
			if err != nil {
				slog.Warn("seed not queued", "url", seed, "err", err)
				continue
			}
			req := &colly.Request{
				URL:     u,
				Method:  "GET",
				Depth:   1,
				Headers: &http.Header{},
				Ctx:     colly.NewContext(),
			}
			if err := enqueue(pending, sc, req); err != nil {
				slog.Warn("seed not queued", "url", seed, "err", err)
				continue
			}
			n++
		}
		return n
	})

	start := time.Now()
	if err := q.Run(collector); err != nil && !errors.Is(err, store.ErrQueueEmpty) {
		return nil, fmt.Errorf("run queue: %w", err)
	}
	collector.Wait()

	// Anything taken and not fetched goes back now rather than waiting for its
	// lease to expire. A crawl stopped on its budget has usually taken a
	// threadful of requests it never got to, and leaving them in flight would
	// make the next run wait ten minutes for work it already has. Only for the
	// database frontier: a crawler handed its work does not own the queue.
	if opts.Frontier == nil {
		if err := c.store.ReturnLeases(ctx, opts.Job.ID); err != nil {
			slog.Warn("could not return leases", "item", opts.Item.Name, "err", err)
		}
	}

	st.mu.Lock()
	defer st.mu.Unlock()
	result := &Result{
		Fetched:  st.fetched,
		Skipped:  st.skipped,
		Failed:   st.failed,
		Bytes:    st.bytes,
		Elapsed:  time.Since(start),
		Statuses: st.statuses,
	}

	// Spending the budget is a normal end, not a failure, so only the caller's
	// own cancellation is reported as an error. Checking the caller's context
	// rather than ours is what tells the two apart.
	result.BudgetSpent = st.spentBudget
	return result, ctx.Err()
}

// newCollector builds the colly collector from the item's targets and the
// configuration. Depth, domain scope and politeness are colly's to enforce.
func (c *Crawler) newCollector(opts Options) (*colly.Collector, error) {
	depth := opts.Depth
	if depth <= 0 {
		depth = c.cfg.Crawl.Depth
	}

	// Not Async: the queue owns the threads now, and colly's own async mode
	// would put a second, unpersisted scheduler in front of it.
	settings := []colly.CollectorOption{
		colly.MaxDepth(depth),
		colly.UserAgent(c.cfg.Crawl.UserAgent),
	}
	if opts.Debug {
		settings = append(settings, colly.Debugger(&debug.LogDebugger{}))
	}

	collector := colly.NewCollector(settings...)
	// colly ignores robots.txt unless told otherwise, which is the opposite of
	// what a crawler should default to.
	collector.IgnoreRobotsTxt = !c.cfg.Crawl.Robots
	collector.SetRequestTimeout(c.cfg.Crawl.Timeout.Duration())

	rt, err := c.transport(opts)
	if err != nil {
		return nil, err
	}
	collector.WithTransport(rt)

	if err := c.applyLimits(collector); err != nil {
		return nil, err
	}
	return collector, nil
}

// transport builds the round tripper this crawl fetches through.
//
// Installing the browser here rather than as a second kind of fetcher is what
// keeps it invisible: a rendered page arrives through the same callbacks, is
// cached the same way, and is counted in the same metrics.
func (c *Crawler) transport(opts Options) (http.RoundTripper, error) {
	cfg := transport.Config{
		UserAgent: c.cfg.Crawl.UserAgent,
		Timeout:   c.cfg.Crawl.Timeout.Duration(),
		Browser: transport.BrowserConfig{
			Pool:     c.cfg.Browser.Pool,
			Timeout:  c.cfg.Browser.Timeout.Duration(),
			ExecPath: c.cfg.Browser.ExecPath,
		},
	}

	base, err := transport.New("http", cfg)
	if err != nil {
		return nil, err
	}

	// The flag decides when it was given, the config file otherwise. An
	// operator who turned the browser off in config means it, so that beats
	// both.
	policy := transport.ParsePolicy(c.cfg.Browser.Policy)
	if opts.Browser != "" {
		policy = transport.ParsePolicy(opts.Browser)
	}
	if !c.cfg.Browser.Enabled {
		policy = transport.Never
	}
	if policy == transport.Never {
		return base, nil
	}
	if !transport.Has("webdriver") {
		// Built without a browser. Say so once rather than failing a crawl
		// that will work perfectly well over plain HTTP.
		slog.Debug("no browser transport in this build, using plain http")
		return base, nil
	}

	browser, err := transport.New("webdriver", cfg)
	if err != nil {
		return nil, err
	}

	e := &transport.Escalating{
		Base:    base,
		Browser: browser,
		Policy:  policy,
		OnEscalate: func(host string) {
			slog.Info("host needs a browser", "host", host)
			if c.store == nil {
				return
			}
			if err := c.store.SetHostTransport(context.Background(), host, "webdriver"); err != nil {
				slog.Warn("could not record the escalation", "host", host, "err", err)
			}
		},
	}
	e.Prime(c.knownBrowserHosts()...)

	return e, nil
}

// knownBrowserHosts is every host already known to need a browser, from the
// operator's config and from what earlier crawls discovered. Starting with
// these turns a lesson learned once into one that stays learned.
func (c *Crawler) knownBrowserHosts() []string {
	var hosts []string
	for _, h := range c.cfg.Hosts {
		// Only exact hosts, since a pattern such as "*.example.com" is not
		// something the sticky set can match against a request's host.
		if h.Transport == "webdriver" && !strings.ContainsAny(h.Host, "*?[") {
			hosts = append(hosts, h.Host)
		}
	}

	if c.store != nil {
		recorded, err := c.store.HostsByTransport(context.Background(), "webdriver")
		if err != nil {
			// Worth a line, but not worth failing a crawl: without these the
			// crawl simply rediscovers them.
			slog.Warn("could not read which hosts need a browser", "err", err)
		}
		hosts = append(hosts, recorded...)
	}
	return hosts
}

// applyLimits maps the global crawl settings and every [[host]] block onto
// colly's own rate limiting, rather than reimplementing politeness.
func (c *Crawler) applyLimits(collector *colly.Collector) error {
	rules := []*colly.LimitRule{{
		DomainGlob:  "*",
		Delay:       c.cfg.Crawl.Rate.Duration(),
		Parallelism: c.cfg.Crawl.Concurrency,
	}}
	for _, h := range c.cfg.Hosts {
		if h.Host == "" {
			continue
		}
		rule := &colly.LimitRule{DomainGlob: h.Host, Delay: c.cfg.Crawl.Rate.Duration(), Parallelism: c.cfg.Crawl.Concurrency}
		if h.Rate > 0 {
			rule.Delay = h.Rate.Duration()
		}
		if h.Concurrency > 0 {
			rule.Parallelism = h.Concurrency
		}
		rules = append(rules, rule)
	}
	if err := collector.Limits(rules); err != nil {
		return fmt.Errorf("apply rate limits: %w", err)
	}
	return nil
}

// Context keys for values carried between callbacks on one request.
const (
	ctxStart  = "scour.start"
	ctxParent = "scour.parent"
	ctxScore  = "scour.score"
)

// register wires the callbacks. This is the whole integration with colly.
func (c *Crawler) register(ctx context.Context, collector *colly.Collector, pending Frontier, sc *Scope, opts Options, st *state) {
	itemID := opts.Item.ID
	// A crawler handed its work has no job of its own: whoever handed it the
	// work owns the frontier, and releases the entry when the result comes
	// back. Zero is that "not mine to release".
	var jobID uint
	if opts.Job != nil {
		jobID = opts.Job.ID
	}
	runID := opts.RunID

	// Attach the timing and lineage this request will be recorded with, and
	// drop links whose extension already disagrees with the allowed types.
	collector.OnRequest(func(r *colly.Request) {
		if ctx.Err() != nil {
			pending.Freeze()
			r.Abort()
			return
		}
		if !opts.Types.AllowsPath(r.URL.Path) {
			slog.Debug("skipped by extension", "url", r.URL.String())
			c.skip(ctx, st, itemID, jobID, runID, r.URL.String(), r.Depth)
			r.Abort()
			return
		}
		r.Ctx.Put(ctxStart, time.Now().Format(time.RFC3339Nano))
	})

	// The real Content-Type is only known now. Abandoning here means the body
	// is never downloaded.
	collector.OnResponseHeaders(func(r *colly.Response) {
		// An error response is a failure, not a skip. Filtering it by
		// Content-Type here would quietly reclassify every text/plain 404 as
		// content we chose not to read.
		if r.StatusCode >= 400 {
			return
		}
		ct := r.Headers.Get("Content-Type")
		if !opts.Types.AllowsMIME(ct) {
			slog.Debug("skipped by content type", "url", r.Request.URL.String(), "type", ct)
			c.skip(ctx, st, itemID, jobID, runID, r.Request.URL.String(), r.Request.Depth)
			r.Request.Abort()
			return
		}
		if max := int64(c.cfg.Crawl.MaxSize); max > 0 && r.Headers.Get("Content-Length") != "" {
			if size := parseSize(r.Headers.Get("Content-Length")); size > max {
				slog.Debug("skipped by size", "url", r.Request.URL.String(), "size", size)
				c.skip(ctx, st, itemID, jobID, runID, r.Request.URL.String(), r.Request.Depth)
				r.Request.Abort()
			}
		}
	})

	// A body arrived: cache it and record the fetch.
	collector.OnResponse(func(r *colly.Response) {
		rawURL := r.Request.URL.String()
		latency := elapsed(r.Ctx.Get(ctxStart))

		key, err := c.cache.Put(ctx, rawURL, r.Body)
		if err != nil {
			// A page whose body could not be stored has not been fetched in
			// any useful sense: the row would say it was, carry a key, and
			// have nothing behind it, and every later stage would skip it
			// without saying why. Recording the failure keeps it retryable and
			// makes a misconfigured page store loud instead of quiet, which
			// matters because the store is the one part of the pipeline that
			// can be pointed somewhere unreachable and still let a crawl look
			// like it worked.
			slog.Error("cache write failed", "url", rawURL, "err", err)

			st.mu.Lock()
			st.failed++
			st.mu.Unlock()

			failed := store.Fetched{
				ItemID:     itemID,
				JobID:      jobID,
				RunID:      runID,
				URL:        rawURL,
				ParentURL:  r.Ctx.Get(ctxParent),
				Depth:      r.Request.Depth,
				Score:      scoreFrom(r.Ctx.Get(ctxScore), opts.Scorer, rawURL, "", r.Request.Depth),
				Status:     store.URLFailed,
				StatusCode: r.StatusCode,
				Latency:    latency,
			}
			if err := c.sink.Fetched(ctx, failed); err != nil {
				slog.Error("record failure failed", "url", rawURL, "err", err)
			}
			return
		}

		st.mu.Lock()
		st.fetched++
		st.bytes += int64(len(r.Body))
		st.mu.Unlock()
		st.countStatus(r.StatusCode)

		c.refreshPause(ctx, st, itemID)
		if st.spend(opts.Limit) {
			// Freeze rather than abort: everything still queued stays queued,
			// so the next run resumes instead of starting over.
			//
			// Freezing is also what stops the crawl. colly's Queue.Stop writes
			// q.running under a mutex that its own loop reads without one, so
			// calling it races; an empty-looking queue ends the loop just as
			// well and touches nothing colly does not already guard.
			pending.Freeze()
		}

		f := store.Fetched{
			ItemID:      itemID,
			JobID:       jobID,
			RunID:       runID,
			URL:         rawURL,
			ParentURL:   r.Ctx.Get(ctxParent),
			Depth:       r.Request.Depth,
			Score:       scoreFrom(r.Ctx.Get(ctxScore), opts.Scorer, rawURL, "", r.Request.Depth),
			Status:      store.URLFetched,
			StatusCode:  r.StatusCode,
			ContentType: content.ShorthandOf(r.Headers.Get("Content-Type"), r.Body),
			Size:        int64(len(r.Body)),
			Latency:     latency,
			CacheKey:    key,
		}
		c.measureFetch(ctx, rawURL, r.StatusCode, latency, int64(len(r.Body)))

		if err := c.sink.Fetched(ctx, f); err != nil {
			slog.Error("record fetch failed", "url", rawURL, "err", err)
		}
	})

	// Link discovery. Scoring happens here, before the link is queued, so
	// colly's depth and domain rules still apply on top of our decision.
	//
	// The href and the anchor are passed in rather than read from an element,
	// because a link is not always an <a>. A feed names its articles in <link>
	// elements and carries their headlines in a sibling <title>, which is a
	// better anchor than most pages manage.
	discover := func(r *colly.Request, href, anchor string) {
		link := r.AbsoluteURL(href)
		if link == "" {
			return
		}
		link = stripFragment(link)

		predicted := opts.Scorer.Score(score.Features{
			URL:    link,
			Anchor: strings.TrimSpace(anchor),
			Depth:  r.Depth + 1,
			Parent: r.URL.String(),
		})
		if predicted < c.cfg.Model.MinScore {
			slog.Debug("link below cutoff", "url", link, "score", predicted)
			return
		}

		// Out of scope is not discovered: recording it would put links to
		// anywhere at all in the item's URL table, which is what the bus
		// path did until the store started applying the same test.
		if !sc.Allows(link) {
			return
		}

		if err := c.sink.Discovered(ctx, itemID, link, r.URL.String(), r.Depth+1, predicted); err != nil {
			slog.Error("record discovered failed", "url", link, "err", err)
		}

		// Queue rather than visit directly, so every URL goes through the one
		// scheduler and survives a restart. colly still applies its depth,
		// domain and revisit rules when the request comes back out.
		req, err := r.New("GET", link, nil)
		if err != nil {
			slog.Debug("not queued", "url", link, "err", err)
			return
		}
		// A fresh context: Request.New shares the parent's, and mutating that
		// would leak this link's score and lineage to its siblings.
		req.Depth = r.Depth + 1
		req.Ctx = colly.NewContext()
		req.Ctx.Put(ctxScore, formatScore(predicted))
		req.Ctx.Put(ctxParent, r.URL.String())

		if err := enqueue(pending, sc, req); err != nil {
			slog.Error("queue link failed", "url", link, "err", err)
		}
	}

	collector.OnHTML("a[href]", func(e *colly.HTMLElement) {
		discover(e.Request, e.Attr("href"), e.Text)
	})

	// A feed is a list of URLs, and it is the one document type where reading
	// only <a href> finds nothing at all. Pointing a crawl at a news site's
	// feed is the ordinary way to use one, and it fetched the feed and stopped.
	//
	// RSS and RDF put the article in a <link> element's text; Atom puts it in
	// that element's href. Both carry the headline in a sibling <title>, which
	// makes a better anchor for scoring than the link text on most pages.
	collector.OnXML("//item", func(e *colly.XMLElement) {
		discover(e.Request, strings.TrimSpace(e.ChildText("link")), e.ChildText("title"))
	})
	collector.OnXML("//entry", func(e *colly.XMLElement) {
		discover(e.Request, strings.TrimSpace(e.ChildAttr("link", "href")), e.ChildText("title"))
	})

	collector.OnError(func(r *colly.Response, err error) {
		rawURL := r.Request.URL.String()
		if errors.Is(err, colly.ErrAbortedAfterHeaders) || errors.Is(err, context.Canceled) {
			return // an abort we asked for, already recorded
		}

		// A redirect landing on a page already fetched is not a failure. colly
		// checks the destination against the visited set and reports the miss
		// against the source URL, with no status code, because no request ever
		// went out. Counted as a failure it makes a site that redirects read as
		// a site that is broken, and leaves a zero where the status belongs,
		// which nothing downstream can interpret. Canonical redirects are how
		// the web moves a page, so this is the ordinary case rather than an
		// awkward one. It is a skip, because skipping is what happened.
		var visited *colly.AlreadyVisitedError
		if errors.As(err, &visited) {
			slog.Debug("redirected to a page already fetched",
				"url", rawURL, "destination", visited.Destination)
			c.skip(ctx, st, itemID, jobID, runID, rawURL, r.Request.Depth)
			return
		}

		st.mu.Lock()
		st.failed++
		st.mu.Unlock()
		if r.StatusCode != 0 {
			st.countStatus(r.StatusCode)
		}

		slog.Debug("fetch failed", "url", rawURL, "status", r.StatusCode, "err", err)
		f := store.Fetched{
			ItemID:     itemID,
			JobID:      jobID,
			RunID:      runID,
			URL:        rawURL,
			ParentURL:  r.Ctx.Get(ctxParent),
			Depth:      r.Request.Depth,
			Score:      scoreFrom(r.Ctx.Get(ctxScore), opts.Scorer, rawURL, "", r.Request.Depth),
			Status:     store.URLFailed,
			StatusCode: r.StatusCode,
			Latency:    elapsed(r.Ctx.Get(ctxStart)),
		}
		c.measureFetch(ctx, rawURL, r.StatusCode, f.Latency, 0)

		if err := c.sink.Fetched(ctx, f); err != nil {
			slog.Error("record failure failed", "url", rawURL, "err", err)
		}
	})
}

// skip records a URL that was deliberately not downloaded.
func (c *Crawler) skip(ctx context.Context, st *state, itemID, jobID, runID uint, rawURL string, depth int) {
	st.mu.Lock()
	st.skipped++
	st.mu.Unlock()

	f := store.Fetched{
		ItemID: itemID,
		JobID:  jobID,
		RunID:  runID,
		URL:    rawURL,
		Depth:  depth,
		Status: store.URLSkipped,
	}
	if err := c.sink.Fetched(ctx, f); err != nil {
		slog.Error("record skip failed", "url", rawURL, "err", err)
	}
}

// seedURLs turns targets into the URLs a crawl starts from. A domain target
// starts at its root.
func seedURLs(targets []store.Target) []string {
	seen := make(map[string]bool, len(targets))
	out := make([]string, 0, len(targets))
	for _, t := range targets {
		var seed string
		switch t.Kind {
		case store.TargetURL:
			seed = t.Value
		case store.TargetDomain:
			seed = "http://" + t.Value + "/"
		}
		if seed != "" && !seen[seed] {
			seen[seed] = true
			out = append(out, seed)
		}
	}
	return out
}

// trainedScorer reports whether a scorer has been fitted to anything.
//
// A policy that waits for a model needs to know, and the scorer is the only
// thing that can say: before one is fitted every score is equal, so ordering by
// score is ordering by noise. A scorer with no opinion on the question is
// treated as untrained, which is the safe answer.
func trainedScorer(s score.Scorer) bool {
	t, ok := s.(score.Trained)
	return ok && t.Trained()
}

// queuedScore recovers the score a request was queued with, from the context
// the crawler attached to it. Seeds carry no score and sort first, which is
// right: they are the pages the user asked for by name.
func queuedScore(data []byte) float64 {
	var req struct {
		Ctx map[string]any `json:"Ctx"`
	}
	if err := json.Unmarshal(data, &req); err != nil {
		return 1
	}
	raw, ok := req.Ctx[ctxScore].(string)
	if !ok {
		return 1
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 1
	}
	return v
}

// seedBatch is how many targets are queued at once.
//
// Large enough that topping up is rare and the queue always has work for every
// thread, small enough that a list of a million targets does not become a
// million rows before the first fetch.
const seedBatch = 500

// enqueue writes a request straight to the queue storage.
//
// colly's own Queue.AddRequest signals an unbuffered wake channel that only
// its scheduling loop reads, so calling it from a callback deadlocks the
// moment that loop has stopped. Writing to the storage avoids the channel
// entirely, and is safe because every call happens inside an active request:
// the loop only terminates when the queue is empty and nothing is in flight,
// and this row lands before that request reports itself complete.
func enqueue(pending Frontier, sc *Scope, r *colly.Request) error {
	// Out-of-scope links are dropped here rather than filtered on the way out.
	// colly checks its URLFilters when a request is dequeued, so a link that
	// was never going to be fetched would still be stored, scored and read
	// back first. Refusing it at the door keeps the frontier to pages the
	// crawl might actually visit.
	if !sc.Allows(r.URL.String()) {
		return nil
	}
	data, err := r.Marshal()
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}
	return pending.AddRequest(data)
}

func stripFragment(rawURL string) string {
	if i := strings.IndexByte(rawURL, '#'); i >= 0 {
		return rawURL[:i]
	}
	return rawURL
}

func elapsed(started string) time.Duration {
	if started == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339Nano, started)
	if err != nil {
		return 0
	}
	return time.Since(t)
}

func formatScore(score float64) string {
	return strconv.FormatFloat(score, 'g', -1, 64)
}

// scoreFrom recovers the score a link was queued with, falling back to scoring
// it again when the request did not come from a link, as a seed does not.
func scoreFrom(stored string, scorer score.Scorer, rawURL, anchor string, depth int) float64 {
	if stored != "" {
		if v, err := strconv.ParseFloat(stored, 64); err == nil {
			return v
		}
	}
	return scorer.Score(score.Features{URL: rawURL, Anchor: anchor, Depth: depth})
}

func parseSize(header string) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(header), 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// MarshalRequest builds the serialised request a frontier stores for a
// discovered link.
//
// Exported because in a distributed crawl the frontier is filled by the store
// rather than by the crawler that found the link: the crawler reports what it
// saw and the store decides what is worth queueing. Both paths have to produce
// the same bytes, or a request queued by one would lose the score and the
// lineage when read by the other.
func MarshalRequest(rawURL, parentURL string, depth int, score float64) ([]byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("parse %q: %w", rawURL, err)
	}
	ctx := colly.NewContext()
	ctx.Put(ctxScore, formatScore(score))
	ctx.Put(ctxParent, parentURL)

	req := &colly.Request{
		URL:     u,
		Method:  "GET",
		Depth:   depth,
		Headers: &http.Header{},
		Ctx:     ctx,
	}
	data, err := req.Marshal()
	if err != nil {
		return nil, fmt.Errorf("marshal request for %q: %w", rawURL, err)
	}
	return data, nil
}

// WithCache returns a crawler that keeps bodies somewhere else.
func (c *Crawler) WithCache(pages cache.Store) *Crawler {
	clone := *c
	clone.cache = pages
	return &clone
}
