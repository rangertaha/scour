// SPDX-License-Identifier: GPL-3.0-or-later

package engine

import (
	"fmt"
	"time"
)

// Limits is what stops a crawl. Zero means no limit, except where noted.
type Limits struct {
	// MaxPages stops the crawl after this many fetches.
	MaxPages int `hcl:"max_pages,optional"`
	// MaxDepth is how far from a start URL a page may be. Zero means the
	// default, because an unbounded depth is not a crawl, it is the whole web.
	MaxDepth int `hcl:"max_depth,optional"`
	// MaxTime stops the crawl after this long. Reaching it is a normal end:
	// everything fetched is kept and the frontier stays resumable.
	MaxTime string `hcl:"max_time,optional"`
	// MaxBodyBytes refuses a body larger than this. Zero means the default
	// rather than unlimited, because the size of the largest page on the web
	// is not a number anyone should discover by filling a disk.
	MaxBodyBytes int64 `hcl:"max_body_bytes,optional"`
}

// Politeness is how hard a job may lean on one host.
type Politeness struct {
	// Rate is the least time between two requests to one host.
	Rate string `hcl:"rate,optional"`
	// Concurrency is how many requests may be in flight to one host.
	Concurrency int `hcl:"concurrency,optional"`
	// Robots obeys robots.txt. A pointer so a job can turn it off explicitly,
	// which a bare bool could not express: false and unset would be the same
	// value, and unset has to mean on.
	Robots *bool `hcl:"robots,optional"`
	// UserAgent identifies the crawler.
	UserAgent string `hcl:"user_agent,optional"`
}

// MaxTimeDuration is the crawl budget, defaulted and parsed.
func (l *Limits) MaxTimeDuration() (time.Duration, error) {
	if l == nil || l.MaxTime == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(l.MaxTime)
	if err != nil {
		return 0, fmt.Errorf("limits.max_time: %w", err)
	}
	return d, nil
}

// Depth is the depth ceiling, defaulted.
func (l *Limits) Depth() int {
	if l == nil || l.MaxDepth == 0 {
		return DefaultMaxDepth
	}
	return l.MaxDepth
}

// BodyBytes is the body ceiling, defaulted.
func (l *Limits) BodyBytes() int64 {
	if l == nil || l.MaxBodyBytes == 0 {
		return DefaultMaxBodyBytes
	}
	return l.MaxBodyBytes
}

// Pages is the fetch ceiling. Zero means no limit, which is the one place an
// unset value is not replaced by a default.
func (l *Limits) Pages() int {
	if l == nil {
		return 0
	}
	return l.MaxPages
}

func (l *Limits) validate() []error {
	if l == nil {
		return nil
	}
	var problems []error

	if l.MaxPages < 0 {
		problems = append(problems, fmt.Errorf("limits.max_pages: %d is negative", l.MaxPages))
	}
	if l.MaxDepth < 0 {
		problems = append(problems, fmt.Errorf("limits.max_depth: %d is negative", l.MaxDepth))
	}
	if l.MaxBodyBytes < 0 {
		problems = append(problems, fmt.Errorf("limits.max_body_bytes: %d is negative", l.MaxBodyBytes))
	}
	if d, err := l.MaxTimeDuration(); err != nil {
		problems = append(problems, err)
	} else if d < 0 {
		problems = append(problems, fmt.Errorf("limits.max_time: %s is negative", d))
	}

	return problems
}

// RateDuration is the per-host delay, defaulted.
func (p *Politeness) RateDuration() (time.Duration, error) {
	if p == nil || p.Rate == "" {
		return DefaultRate, nil
	}
	d, err := time.ParseDuration(p.Rate)
	if err != nil {
		return 0, fmt.Errorf("politeness.rate: %w", err)
	}
	return d, nil
}

// Parallelism is the per-host concurrency, defaulted.
func (p *Politeness) Parallelism() int {
	if p == nil || p.Concurrency == 0 {
		return DefaultConcurrency
	}
	return p.Concurrency
}

// Agent is the User-Agent, defaulted.
func (p *Politeness) Agent() string {
	if p == nil || p.UserAgent == "" {
		return DefaultUserAgent
	}
	return p.UserAgent
}

// ObeysRobots reports whether this job honours robots.txt.
func (p *Politeness) ObeysRobots() bool {
	return p == nil || p.Robots == nil || *p.Robots
}

func (p *Politeness) validate() []error {
	if p == nil {
		return nil
	}
	var problems []error

	if p.Concurrency < 0 {
		problems = append(problems, fmt.Errorf("politeness.concurrency: %d is negative", p.Concurrency))
	}
	if p.Concurrency > MaxConcurrency {
		problems = append(problems, fmt.Errorf(
			"politeness.concurrency: %d is more than %d against a single host", p.Concurrency, MaxConcurrency))
	}
	if d, err := p.RateDuration(); err != nil {
		problems = append(problems, err)
	} else if d < 0 {
		problems = append(problems, fmt.Errorf("politeness.rate: %s is negative", d))
	}

	return problems
}
