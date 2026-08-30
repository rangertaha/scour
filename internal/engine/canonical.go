// SPDX-License-Identifier: GPL-3.0-or-later

package engine

import (
	"github.com/hashicorp/hcl/v2/gohcl"

	"github.com/rangertaha/scour/internal/urls"
)

// Canonical is how this job decides two URLs are the same page.
//
// Read off the `dupefilter` plugin's settings, which is where an operator
// writes them, and read from the document rather than from the built plugin
// because the plugin is a function by then.
//
// # Why it lives on the job
//
// Because more than one stage needs the answer and they must not differ. The
// scheduler normalises with it before asking whether a URL is in scope; the
// downloader's redirect follower asks the same scope about the target of a
// 3xx, and normalised with the zero options instead. So a job with
// `lower_path = true` and `excluded = ["*/private/*"]` refused
// /PRIVATE/secret at the scheduler and fetched it when a server redirected
// there - the one URL in a crawl that a third party chooses.
//
// scope's own package doc names this: "two subtly different scope checks is a
// crawl that leaves the site through whichever of them is looser". Deriving it
// twice from one document is what stops them drifting; deriving it once and
// letting the other caller default is what happened instead. A new stage that
// needs it asks here rather than reading the plugin again.
func (j *Job) Canonical() urls.Options {
	if j == nil {
		return urls.Options{}
	}

	for _, p := range j.Chain(StageScheduler) {
		if p.Name != "dupefilter" || p.Config == nil {
			continue
		}

		var c struct {
			Tracking      bool     `hcl:"strip_tracking,optional"`
			Strip         []string `hcl:"strip,optional"`
			SortQuery     bool     `hcl:"sort_query,optional"`
			TrailingSlash bool     `hcl:"strip_trailing_slash,optional"`
			LowerPath     bool     `hcl:"lower_path,optional"`
		}
		if diags := gohcl.DecodeBody(p.Config, nil, &c); diags.HasErrors() {
			// Unreadable settings are the plugin's own error to report when it
			// is built. Answering with the defaults here keeps this from being
			// a second place that refuses a job.
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
