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
		client = &http.Client{Timeout: timeout}
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

		handler = newGuard(&reader, core.Agent).wrap(handler)
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
