// SPDX-License-Identifier: GPL-3.0-or-later

package downloader

import (
	"context"
	"errors"
	"fmt"
	"net/http"

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
}

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
	handler := built.Handler(core)
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

		handler = newGuard(&reader, core.Agent).wrap(handler)
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
		handler = (&follower{max: hops, bounds: bounds}).wrap(handler)
	}

	return &Stage{
		job:     job.Name,
		handler: handler,
		chain:   built,
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
