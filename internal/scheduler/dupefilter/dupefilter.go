// SPDX-License-Identifier: GPL-3.0-or-later

// Package dupefilter is the `dupefilter` middleware: it decides what counts as
// already seen.
//
// Import it for its side effect to make "dupefilter" available to a job:
//
//	import _ "github.com/rangertaha/scour/internal/scheduler/dupefilter"
//
// # Why this is a plugin when scope is not
//
// Because there is no right answer, only a right answer per site. Treating
// `?utm_source=x` as noise is correct nearly everywhere and wrong on a site
// that reads it; treating `/a/` and `/a` as one page is correct nearly
// everywhere and wrong on the servers that do not. A job that knows its site
// can say so, and one that does not gets the conservative default: only the
// transformations that cannot change what a server returns.
//
// It sits at 100, outermost, because everything after it is work: a URL already
// seen should be recognised before anything pays to think about it.
package dupefilter

import (
	"context"
	"fmt"

	"github.com/rangertaha/scour/internal/plugin"
	"github.com/rangertaha/scour/internal/scheduler"
	"github.com/rangertaha/scour/internal/urls"
)

// Name is what this middleware registers as.
const Name = "dupefilter"

func init() {
	scheduler.Register(Name, New)
}

// Config is what a `plugin "dupefilter"` block may set.
//
// Every one of these is off by default, because every one of them can merge two
// pages a server considers different, and a crawler that loses half a site is
// harder to notice than one that fetches a page twice.
type Config struct {
	// Tracking strips the parameters that whoever linked to a page added
	// rather than whoever wrote it. See [urls.Tracking] for the list.
	Tracking bool `hcl:"strip_tracking,optional"`

	// Strip removes named parameters as well, for the session identifiers and
	// house-built trackers no shared list can know about.
	Strip []string `hcl:"strip,optional"`

	// SortQuery treats ?a=1&b=2 and ?b=2&a=1 as one page.
	SortQuery bool `hcl:"sort_query,optional"`

	// TrailingSlash treats /a/ and /a as one page.
	TrailingSlash bool `hcl:"strip_trailing_slash,optional"`

	// LowerPath treats /A and /a as one page. True on Windows origins and on
	// very little else.
	LowerPath bool `hcl:"lower_path,optional"`
}

// New builds the middleware. It is [scheduler.Middleware].
func New(_ context.Context, cfg plugin.Config) (scheduler.Wrapper, error) {
	var c Config
	if err := cfg.Decode(&c); err != nil {
		return nil, err
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

	return func(next scheduler.Handler) scheduler.Handler {
		return scheduler.HandlerFunc(func(ctx context.Context, req *scheduler.Request) (*scheduler.Request, error) {
			normalised, err := urls.Normalise(req.URL, opts)
			if err != nil {
				// Not a drop: a URL that will not parse is a bug wherever it
				// came from, and swallowing it would hide the bug.
				return nil, fmt.Errorf("dupefilter: %w", err)
			}

			// The hash is set here, and the scheduler leaves one that is
			// already set alone. That is the whole mechanism: what this decides
			// is the same URL is what the frontier will refuse to queue twice.
			req.URL = normalised
			req.Hash = urls.Hash(normalised)
			req.Host = urls.Host(normalised)

			return next.Handle(ctx, req)
		})
	}, nil
}
