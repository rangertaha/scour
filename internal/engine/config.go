// SPDX-License-Identifier: GPL-3.0-or-later

// Package engine holds the configuration a job runs under, and builds the
// components that configuration names.
//
// # Configuration belongs to the job, not to the process
//
// A client submits a job and the job carries its own engine configuration.
// Two jobs running in the same process may cache to different buckets, crawl
// at different rates and allow different content types, because what a job
// does is a property of the job rather than of whichever machine picked it up.
//
// That is what makes a run reproducible. A process-wide configuration means
// the same job submitted twice can behave differently, and the difference is
// invisible: it lives in a file on a server nobody was looking at.
package engine

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/rangertaha/scour/internal/cache"
)

// Config is everything a job needs to run, and nothing about what it is
// looking for.
//
// Every field is optional. [Config.WithDefaults] fills what was left out, and
// is applied when a job is accepted rather than when it runs, so a stored job
// records what it will actually do. A job that inherited its defaults at run
// time would quietly change behaviour when the server's defaults changed, and
// nothing in the job would show it.
type Config struct {
	// Cache is where fetched bodies are kept.
	Cache Cache `json:"cache"`
	// Limits is what stops a crawl.
	Limits Limits `json:"limits"`
	// Politeness is what keeps it welcome.
	Politeness Politeness `json:"politeness"`
	// Components says which stages this job runs itself, and which it expects
	// somebody else to run over the bus.
	Components Components `json:"components,omitempty"`
}

// Cache configures the page cache for one job.
//
// Two jobs naming the same backend and location share a cache, which is
// deliberate: a page fetched by one is a page the other does not have to ask a
// site for again. Prefix, or a separate directory, is how a job that must not
// share says so.
type Cache struct {
	// Backend names the implementation: local, s3, gcs, or anything else
	// registered. Empty means the local directory.
	Backend string `json:"backend,omitempty"`
	// Dir is the directory the local backend writes under.
	Dir string `json:"dir,omitempty"`
	// Bucket is the bucket for a cloud backend.
	Bucket string `json:"bucket,omitempty"`
	// Prefix confines this job's bodies to a path within the bucket.
	Prefix string `json:"prefix,omitempty"`
	// Region is the S3 region.
	Region string `json:"region,omitempty"`
	// Endpoint points the S3 backend at an S3-compatible service.
	Endpoint string `json:"endpoint,omitempty"`
	// Profile names an AWS credentials profile.
	Profile string `json:"profile,omitempty"`
}

// Store returns the cache configuration this section describes.
func (c Cache) Store() cache.Config {
	return cache.Config{
		Backend:  c.Backend,
		Dir:      c.Dir,
		Bucket:   c.Bucket,
		Prefix:   c.Prefix,
		Region:   c.Region,
		Endpoint: c.Endpoint,
		Profile:  c.Profile,
	}
}

// Limits is what stops a crawl. Zero means no limit, except where noted.
type Limits struct {
	// MaxPages stops the crawl after this many fetches.
	MaxPages int `json:"max_pages,omitempty"`
	// MaxDepth is how far from a seed a URL may be. Zero means the default,
	// because an unbounded depth is not a crawl, it is the whole web.
	MaxDepth int `json:"max_depth,omitempty"`
	// MaxTime stops the crawl after this long. Reaching it is a normal end:
	// everything fetched is kept and the frontier stays resumable.
	MaxTime Duration `json:"max_time,omitempty"`
	// MaxBodyBytes refuses a body larger than this, before it is downloaded.
	// Zero means the default rather than unlimited, because the size of the
	// largest page on the web is not a number anyone should discover by
	// filling a disk.
	MaxBodyBytes int64 `json:"max_body_bytes,omitempty"`
}

// Politeness is how hard a job may lean on one host.
type Politeness struct {
	// Rate is the least time between two requests to one host.
	Rate Duration `json:"rate,omitempty"`
	// Concurrency is how many requests may be in flight to one host.
	Concurrency int `json:"concurrency,omitempty"`
	// Robots obeys robots.txt. It is a pointer so that a job can turn it off
	// explicitly, which a bare bool could not express: false and unset would
	// be the same value, and unset has to mean on.
	Robots *bool `json:"robots,omitempty"`
	// UserAgent identifies the crawler. A job that does not set one gets the
	// default, which names scour and is what a site administrator will look up.
	UserAgent string `json:"user_agent,omitempty"`
}

// The defaults a job gets when it says nothing.
//
// They are conservative on purpose. A client that submits an empty
// configuration is telling us they have not thought about it, and the answer
// to that is a crawl that is slow, shallow and polite rather than one that
// gets someone's address blocked.
const (
	DefaultMaxDepth     = 5
	DefaultMaxBodyBytes = 32 << 20 // 32 MiB
	DefaultRate         = 1 * time.Second
	DefaultConcurrency  = 2
	DefaultUserAgent    = "scour (+https://github.com/rangertaha/scour)"
	DefaultCacheDir     = ".scour/cache"
)

// WithDefaults returns the configuration with everything unset filled in.
//
// It does not modify the receiver, so a caller holding what a client actually
// submitted keeps it. What the client sent and what the job will do are two
// different things, and both are worth being able to show.
func (c Config) WithDefaults() Config {
	out := c

	if out.Cache.Backend == "" {
		out.Cache.Backend = cache.DefaultBackend
	}
	if out.Cache.Backend == cache.DefaultBackend && out.Cache.Dir == "" {
		out.Cache.Dir = DefaultCacheDir
	}

	if out.Limits.MaxDepth == 0 {
		out.Limits.MaxDepth = DefaultMaxDepth
	}
	if out.Limits.MaxBodyBytes == 0 {
		out.Limits.MaxBodyBytes = DefaultMaxBodyBytes
	}

	if out.Politeness.Rate == 0 {
		out.Politeness.Rate = Duration(DefaultRate)
	}
	if out.Politeness.Concurrency == 0 {
		out.Politeness.Concurrency = DefaultConcurrency
	}
	if out.Politeness.Robots == nil {
		on := true
		out.Politeness.Robots = &on
	}
	if out.Politeness.UserAgent == "" {
		out.Politeness.UserAgent = DefaultUserAgent
	}

	if len(out.Components.External) > 0 && out.Components.Timeout == 0 {
		out.Components.Timeout = Duration(DefaultExternalTimeout)
	}

	return out
}

// ObeysRobots reports whether this job honours robots.txt.
func (p Politeness) ObeysRobots() bool { return p.Robots == nil || *p.Robots }

// Validate reports every problem with a configuration at once.
//
// At once, rather than the first: a client fixing a submission one error per
// round trip is a client we have made an enemy of. The errors are joined, so
// the message names everything wrong with what they sent.
//
// This runs when a job is submitted, not when it starts. A job naming a
// backend that does not exist should be refused by whoever accepted it, while
// there is still someone to tell.
func (c Config) Validate() error {
	var problems []error

	problems = append(problems, c.Cache.validate()...)
	problems = append(problems, c.Limits.validate()...)
	problems = append(problems, c.Politeness.validate()...)
	problems = append(problems, c.Components.validate()...)

	return errors.Join(problems...)
}

func (c Cache) validate() []error {
	var problems []error

	backend := c.Backend
	if backend == "" {
		backend = cache.DefaultBackend
	}
	if have := cache.Backends(); !slices.Contains(have, backend) {
		if len(have) == 0 {
			problems = append(problems, fmt.Errorf("cache.backend %q: no cache backends are registered", backend))
		} else {
			problems = append(problems, fmt.Errorf("cache.backend %q: not one of %s", backend, strings.Join(have, ", ")))
		}
	}

	// The location a backend needs is only knowable per backend, so this asks
	// about the ones that ship rather than inventing a general rule. An
	// unknown backend has already been reported above.
	switch backend {
	case "s3", "gcs":
		if c.Bucket == "" {
			problems = append(problems, fmt.Errorf("cache.bucket: required by the %s backend", backend))
		}
	}
	if c.Bucket != "" && backend == cache.DefaultBackend {
		problems = append(problems, errors.New("cache.bucket: the local backend writes to a directory, not a bucket"))
	}

	return problems
}

func (l Limits) validate() []error {
	var problems []error

	if l.MaxPages < 0 {
		problems = append(problems, fmt.Errorf("limits.max_pages: %d is negative", l.MaxPages))
	}
	if l.MaxDepth < 0 {
		problems = append(problems, fmt.Errorf("limits.max_depth: %d is negative", l.MaxDepth))
	}
	if l.MaxTime < 0 {
		problems = append(problems, fmt.Errorf("limits.max_time: %s is negative", time.Duration(l.MaxTime)))
	}
	if l.MaxBodyBytes < 0 {
		problems = append(problems, fmt.Errorf("limits.max_body_bytes: %d is negative", l.MaxBodyBytes))
	}

	return problems
}

func (p Politeness) validate() []error {
	var problems []error

	if p.Rate < 0 {
		problems = append(problems, fmt.Errorf("politeness.rate: %s is negative", time.Duration(p.Rate)))
	}
	if p.Concurrency < 0 {
		problems = append(problems, fmt.Errorf("politeness.concurrency: %d is negative", p.Concurrency))
	}
	// An unbounded concurrency against one host is a denial of service with
	// our name in the logs, so it is capped rather than trusted.
	const maxConcurrency = 64
	if p.Concurrency > maxConcurrency {
		problems = append(problems, fmt.Errorf("politeness.concurrency: %d is more than %d against a single host", p.Concurrency, maxConcurrency))
	}

	return problems
}
