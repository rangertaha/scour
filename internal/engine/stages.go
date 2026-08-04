// SPDX-License-Identifier: GPL-3.0-or-later

package engine

import (
	"slices"
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
