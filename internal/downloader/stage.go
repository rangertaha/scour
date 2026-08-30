// SPDX-License-Identifier: GPL-3.0-or-later

package downloader

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/hashicorp/hcl/v2"

	"github.com/rangertaha/scour/internal/engine"
	"github.com/rangertaha/scour/internal/plugin"
	"github.com/rangertaha/scour/internal/robots"
	"github.com/rangertaha/scour/internal/scope"
)

// Stage is a job's downloader: the core, the middleware it asked for, and
// whatever that middleware holds open.
//
// One per job rather than one per node. Two jobs may want different caches,
// different agents and different limits, and a shared downloader would have to
// pick one of each.
type Stage struct {
	job     string
	handler Handler
	chain   *plugin.Chain[*Request, *Response]

	// worst is the longest one Handle can take. See [Stage.Worst].
	worst time.Duration
}

// Worst is the longest one call to [Stage.Handle] can take.
//
// # Why the chain answers this rather than the caller working it out
//
// Because the caller cannot, and kept trying. A lease in the crawl loop has to
// outlast the work it covers, and that length was computed there from the job
// document: the request timeout, then the timeout times the redirect
// allowance. Both were wrong, in that order, and each was found when a lease
// expired under a worker that was still fetching, so a second worker took the
// URL and both hit the same host at once. The report from the first was then
// discarded by the attempt fence, correctly and silently, so the crawl looked
// healthy while hitting the site twice.
//
// The second attempt was still wrong when it landed, because a redirect hop
// re-enters from the top of this chain and the robots guard has a redirect loop
// of its own: one hop can pay for a robots.txt fetch plus its own hops before
// the page is touched. Nothing in the job document says that, and nothing ever
// will - it is a property of how this chain is assembled.
//
// So it is accumulated here, as each wrapper is added, by the code that adds
// it. A wrapper introduced later either contributes its term or is missing from
// a number the loop depends on, and the answer is at least in the same file as
// the reason.
func (s *Stage) Worst() time.Duration { return s.worst }

// Options are what a caller supplies that the job document cannot.
type Options struct {
	// Client does the requests. Nil builds one from the job's timeout, which
	// is what everything outside a test wants.
	//
	// A client of your own that follows redirects hides them from the chain,
	// from robots and from the cache. The one built here does not follow.
	Client *http.Client

	// Eval resolves `secret()` in plugin configuration. Nil means a job whose
	// plugins reference a secret is refused here, which is the right answer on
	// a node with no way to read one.
	Eval *hcl.EvalContext
}

// New builds the downloader a job configured.
//
// The caller closes it. Everything a plugin opened, a bucket most of all,
// closes with it.
func New(ctx context.Context, job *engine.Job, opts Options) (*Stage, error) {
	if job == nil {
		return nil, errors.New("downloader: no job")
	}

	timeout, err := job.Downloader.RequestTimeout()
	if err != nil {
		return nil, fmt.Errorf("downloader: job %q: %w", job.Name, err)
	}

	// How many requests one Handle can make, accumulated by each wrapper below
	// as it is added. One for the page itself, before anything wraps it. See
	// [Stage.Worst].
	fetches := 1

	client := opts.Client
	if client == nil {
		client = &http.Client{
			Timeout: timeout,
			// Redirects are followed by [follower], outside everything, so that
			// each hop is checked against its own host's robots.txt and cached
			// under its own key. One the client followed would be invisible to
			// both.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}

	core := &Fetcher{
		Client:  client,
		Agent:   job.Downloader.Agent(),
		MaxBody: job.Downloader.BodyBytes(),
		Timeout: timeout,
	}

	built, err := plugin.Build(ctx, reg, job, engine.StageDownloader, opts.Eval)
	if err != nil {
		return nil, err
	}

	// Outside everything, so a URL the site refused is refused before the cache
	// is consulted and before anything else pays for it. See [guard] for why
	// this is not a plugin with a configurable position.
	var fetch Handler = core

	// The guard is built before the chain when robots is on, because it wraps
	// the chain from outside and the core from inside: see [guard.sending] for
	// why one check is not enough once a middleware can set the agent.
	var g *guard
	if job.Downloader.ObeysRobots() {
		// robots.txt is read to the cap RFC 9309 sets rather than the job's
		// max_body, which is a limit on pages: a job that will not download a
		// megabyte of HTML still has to be able to read a site's rules.
		reader := *core
		reader.MaxBody = robots.MaxSize
		// Truncated rather than refused. RFC 9309 says to parse the first 500
		// KiB and ignore the rest; refusing meant a site whose robots.txt was
		// larger had every URL on it dropped forever, and re-downloaded the
		// file to decide that each time.
		reader.Truncate = true

		g = newGuard(&reader, core.Agent)
		fetch = g.sending(core)
	}

	handler := built.Handler(fetch)
	if g != nil {
		handler = g.wrap(handler)

		// A robots.txt load is a fetch of its own, and it follows redirects
		// itself, so the guard costs the whole of that before the request it
		// guards is made.
		fetches += robotsRedirects + 1
	}

	// Outside even that: a redirect is a different URL on a host with its own
	// rules, so every hop re-enters from the top rather than skipping the
	// checks the first one passed.
	//
	// The job's scope goes with it. A redirect is the one URL a crawl fetches
	// that neither the job nor a page the job chose to read picked out: the
	// server on the other end did. The scheduler cannot vet it, because it has
	// already handed this request over, so the check has to happen here or
	// nowhere. It used to happen nowhere.
	if hops := job.Downloader.Redirects(); hops > 0 {
		bounds, err := scope.New(job.Domains, job.Included, job.Excluded)
		if err != nil {
			return nil, fmt.Errorf("downloader: job %q: %w", job.Name, err)
		}
		handler = (&follower{max: hops, bounds: bounds, canon: job.Canonical()}).wrap(handler)

		// Every hop re-enters from the top, so the whole of what is wrapped
		// happens again per hop, robots and all.
		fetches *= hops + 1
	}

	return &Stage{
		job:     job.Name,
		handler: handler,
		chain:   built,
		worst:   time.Duration(fetches) * timeout,
	}, nil
}

// Handle implements [Handler]: one request through the whole chain.
func (s *Stage) Handle(ctx context.Context, req *Request) (*Response, error) {
	if req.Job == "" {
		req = req.Clone()
		req.Job = s.job
	}
	return s.handler.Handle(ctx, req)
}

// Middleware lists the chain in the order it runs on the way out, which is what
// a log line at the start of a run should say.
func (s *Stage) Middleware() []string { return s.chain.Names() }

// Close releases what the middleware opened. Closing twice is not an error.
func (s *Stage) Close() error { return s.chain.Close() }
