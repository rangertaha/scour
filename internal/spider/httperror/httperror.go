// SPDX-License-Identifier: GPL-3.0-or-later

// Package httperror is the `httperror` middleware: it refuses a response whose
// status says there is nothing to read.
//
// Import it for its side effect to make "httperror" available to a job:
//
//	import _ "github.com/rangertaha/scour/internal/spider/httperror"
//
// # Why the downloader does not do this
//
// Because whether a 404 is a failure is not the downloader's decision. A crawl
// archiving a site wants the error pages; one extracting articles does not; and
// a crawl that is measuring how much of a site has rotted wants nothing else.
// The downloader hands back what the server said and this is where a job says
// what it thinks of it.
//
// It sits at 50, before anything parses the page, because parsing an error page
// into an empty article is the failure this exists to prevent.
package httperror

import (
	"context"
	"fmt"
	"slices"

	"github.com/rangertaha/scour/internal/chain"
	"github.com/rangertaha/scour/internal/downloader"
	"github.com/rangertaha/scour/internal/plugin"
	"github.com/rangertaha/scour/internal/spider"
)

// Name is what this middleware registers as.
const Name = "httperror"

func init() {
	spider.Register(Name, New)
}

// Config is what a `plugin "httperror"` block may set.
type Config struct {
	// Allow are the extra status codes worth reading anyway, for the sites
	// that serve a real page under a wrong status. There are more of them than
	// anybody expects.
	Allow []int `hcl:"allow,optional"`

	// AllowAll reads every page whatever its status, which is what an archival
	// crawl wants and what makes this plugin a no-op.
	AllowAll bool `hcl:"allow_all,optional"`
}

// New builds the middleware. It is [spider.Middleware].
func New(_ context.Context, cfg plugin.Config) (spider.Wrapper, error) {
	var c Config
	if err := cfg.Decode(&c); err != nil {
		return nil, err
	}

	return func(next spider.Handler) spider.Handler {
		return spider.HandlerFunc(func(ctx context.Context, resp *downloader.Response) (*spider.Output, error) {
			switch {
			case c.AllowAll, resp.OK(), slices.Contains(c.Allow, resp.Status):
				return next.Handle(ctx, resp)
			}
			// A drop rather than a failure: a crawl of the open web finds dead
			// links all day, and counting them as errors would make a working
			// crawl look broken.
			return nil, fmt.Errorf("httperror: %s: status %d: %w", resp.URL, resp.Status, chain.ErrDrop)
		})
	}, nil
}
