// SPDX-License-Identifier: GPL-3.0-or-later

package engine

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// Stage is one step of the pipeline, and one place somebody can substitute
// their own code.
//
// # Bringing your own
//
// Because the stages talk over NATS rather than by calling each other, a stage
// is replaceable without touching scour: subscribe to what it consumes,
// publish what it produces, and scour cannot tell the difference. A spider in
// Python that understands one site better than induction ever will is a
// subscriber, not a fork.
//
// A job says which stages it is bringing by listing them in
// [Components.External]. scour then publishes that stage's input and waits for
// its output on the job's subjects, instead of running its own.
//
// The subjects are per job, so an external stage subscribes to the work it was
// submitted for rather than to everything the cluster is doing.
type Stage string

// The stages of the pipeline, in the order a page passes through them.
const (
	// StageDownloader turns a request into a response, and caches the body.
	StageDownloader Stage = "downloader"
	// StageSpider turns a response into records and new requests.
	StageSpider Stage = "spider"
	// StagePipeline turns a record into whatever is done with it.
	StagePipeline Stage = "pipeline"
)

// Stages is every stage that can be replaced.
//
// The scheduler is not among them. It owns the frontier, dedup and politeness,
// and two schedulers handing out the same host cannot honour a crawl delay
// between them, so it is not a thing a job may bring its own of.
var Stages = []Stage{StageDownloader, StageSpider, StagePipeline}

// Valid reports whether a stage is one scour knows.
func (s Stage) Valid() bool { return slices.Contains(Stages, s) }

// Components says who runs what.
type Components struct {
	// External lists the stages this job does not run itself.
	//
	// Empty, which is the default, means scour runs all of them, and a
	// single-process job never touches the network between stages.
	External []Stage `json:"external,omitempty"`

	// Timeout is how long a stage's input may go unanswered before the job is
	// considered stuck. It applies only to external stages: an internal one
	// cannot fail to reply without the process itself having died.
	//
	// Zero means [DefaultExternalTimeout].
	Timeout Duration `json:"timeout,omitempty"`
}

// DefaultExternalTimeout is how long an external stage has to answer.
//
// Generous, because the reason to bring your own spider is usually that it
// does something slow: a model, a browser, a service somewhere else. A stage
// that is merely slow must not look like a stage that has died.
const DefaultExternalTimeout = 5 * time.Minute

// IsExternal reports whether this job expects somebody else to run a stage.
func (c Components) IsExternal(s Stage) bool { return slices.Contains(c.External, s) }

func (c Components) validate() []error {
	var problems []error

	seen := map[Stage]bool{}
	for _, s := range c.External {
		if !s.Valid() {
			problems = append(problems, fmt.Errorf("components.external: %q is not a stage, have %s", s, strings.Join(stageNames(), ", ")))
			continue
		}
		if seen[s] {
			problems = append(problems, fmt.Errorf("components.external: %q listed twice", s))
		}
		seen[s] = true
	}

	if c.Timeout < 0 {
		problems = append(problems, fmt.Errorf("components.timeout: %s is negative", c.Timeout))
	}

	return problems
}

func stageNames() []string {
	out := make([]string, 0, len(Stages))
	for _, s := range Stages {
		out = append(out, string(s))
	}
	return out
}
