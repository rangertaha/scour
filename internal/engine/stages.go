// SPDX-License-Identifier: GPL-3.0-or-later

package engine

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// Stage is one step of the pipeline, one chain of middleware, and one place
// somebody can substitute their own code.
type Stage string

// The stages.
const (
	// StageScheduler owns the frontier: dedup, depth, politeness, ordering.
	StageScheduler Stage = "scheduler"
	// StageDownloader turns a request into a response.
	StageDownloader Stage = "downloader"
	// StageSpider turns a response into items and new requests.
	StageSpider Stage = "spider"
	// StagePipeline turns an item into whatever is done with it.
	StagePipeline Stage = "pipeline"
)

// PluginStages is every stage a plugin can extend, in the order a page passes
// through them.
//
// The scheduler is here even though it cannot be replaced, because extending a
// stage and replacing it are different things: a priority queue or a cron
// trigger adds to the frontier's behaviour without meaning somebody else is
// running it.
//
// The pipeline is not here. Its extensions are written as `pipelines` blocks,
// because a pipeline step is a node in a graph with dependencies rather than a
// link in a chain with an order, and giving one stage two spellings would only
// leave everybody guessing which one they wanted.
var PluginStages = []Stage{StageScheduler, StageDownloader, StageSpider}

// ExternalStages is every stage a job may hand to somebody else.
//
// The scheduler is not among them. Two schedulers handing out the same host
// cannot honour a crawl delay between them, so politeness forces one decision
// point per host.
var ExternalStages = []Stage{StageDownloader, StageSpider, StagePipeline}

// ValidPlugin reports whether a plugin may name this stage.
func (s Stage) ValidPlugin() bool { return slices.Contains(PluginStages, s) }

// ValidExternal reports whether a job may bring its own of this stage.
func (s Stage) ValidExternal() bool { return slices.Contains(ExternalStages, s) }

// Components says which stages a job runs itself, and which it expects
// somebody else to run over the bus.
//
// Because the stages talk over NATS rather than calling each other, a stage is
// replaceable without touching scour: subscribe to what it consumes, publish
// what it produces, and scour cannot tell the difference. A spider in another
// language that understands one site better than induction ever will is a
// subscriber, not a fork.
type Components struct {
	// External lists the stages this job does not run itself. Empty, the
	// default, means scour runs all of them and a single-process job never
	// touches the network between stages.
	External []string `hcl:"external,optional"`

	// Timeout is how long an external stage has to answer before the job is
	// considered stuck. It does not apply to internal stages, which cannot
	// fail to reply without the process itself having died.
	Timeout string `hcl:"timeout,optional"`
}

// IsExternal reports whether this job expects somebody else to run a stage.
func (c *Components) IsExternal(s Stage) bool {
	if c == nil {
		return false
	}
	return slices.Contains(c.External, string(s))
}

// ExternalTimeout is how long to wait, defaulted.
func (c *Components) ExternalTimeout() (time.Duration, error) {
	if c == nil || c.Timeout == "" {
		return DefaultExternalTimeout, nil
	}
	d, err := time.ParseDuration(c.Timeout)
	if err != nil {
		return 0, fmt.Errorf("components.timeout: %w", err)
	}
	return d, nil
}

func (c *Components) validate() []error {
	if c == nil {
		return nil
	}
	var problems []error

	seen := map[string]bool{}
	for _, name := range c.External {
		stage := Stage(name)
		switch {
		case !stage.ValidExternal() && stage.ValidPlugin():
			problems = append(problems, fmt.Errorf(
				"components.external: %q cannot be replaced, only extended with a plugin", name))
		case !stage.ValidExternal():
			problems = append(problems, fmt.Errorf(
				"components.external: %q is not a stage, have %s", name, strings.Join(stageNames(ExternalStages), ", ")))
		case seen[name]:
			problems = append(problems, fmt.Errorf("components.external: %q listed twice", name))
		}
		seen[name] = true
	}

	if _, err := c.ExternalTimeout(); err != nil {
		problems = append(problems, err)
	}

	return problems
}

func stageNames(stages []Stage) []string {
	out := make([]string, 0, len(stages))
	for _, s := range stages {
		out = append(out, string(s))
	}
	return out
}
