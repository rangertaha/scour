// SPDX-License-Identifier: GPL-3.0-or-later

package engine

import (
	"fmt"
	"time"

	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/hashicorp/hcl/v2/hclparse"
)

// A service document configures something that outlives a crawl.
//
// # Why this is not a block in the job document
//
// Because the entity graph is shared between jobs, and that is the whole of its
// value: two jobs crawling different sites should agree about who Acme is. A
// job document carries everything one crawl needs, so that a job resubmitted
// next month does what it did today, and putting a shared service's address in
// it would break both halves of that. Whichever job was submitted last would
// silently be deciding where every other job's entities went, and a job moved
// between clusters would carry the old cluster's address with it.
//
// So this is a document of its own, read by whoever runs the service and by
// nothing else. A job says it wants entities; it does not say where they live.
//
// It is HCL and not flags for the same reason the job document is: a
// configuration somebody can read back, diff and put in version control beats
// one reconstructed from a shell history.
type Service struct {
	Entity *EntityService `hcl:"entity,block" json:"entity,omitempty"`
	Event  *EventService  `hcl:"event,block" json:"event,omitempty"`
}

// EntityService configures the entity graph and the service in front of it.
type EntityService struct {
	// Dir is where the graph lives. One directory, one graph.
	//
	// Required, because the in-memory store exists for tests and a service
	// holding a graph that disappears when it restarts is worse than no
	// service: everything that asserted into it believes the assertions
	// landed.
	Dir string `hcl:"dir" json:"dir"`

	// URL is the bus to answer on. Empty starts one in this process, which is
	// what a single machine needs and what makes trying it out require nothing
	// installed.
	URL string `hcl:"url,optional" json:"url,omitempty"`

	// Timeout is how long one request may take. Empty means
	// [DefaultServiceTimeout].
	Timeout string `hcl:"timeout,optional" json:"timeout,omitempty"`
}

// EventService configures the event store and the service in front of it.
//
// Events are what an item becomes when it is a measurement rather than a
// document: a name, the dimensions to group by, the numbers measured, and when.
// They are separate from entities because they answer a different question,
// they grow without bound where entities do not, and a store shaped for one is
// wrong for the other.
type EventService struct {
	// Dir is where the events live.
	Dir string `hcl:"dir" json:"dir"`

	// URL is the bus to answer on. Empty starts one in this process.
	URL string `hcl:"url,optional" json:"url,omitempty"`

	// Timeout is how long one request may take. Empty means
	// [DefaultServiceTimeout].
	Timeout string `hcl:"timeout,optional" json:"timeout,omitempty"`
}

// Wait is how long one request to the event service may take.
func (e *EventService) Wait() (time.Duration, error) {
	if e == nil || e.Timeout == "" {
		return DefaultServiceTimeout, nil
	}

	d, err := time.ParseDuration(e.Timeout)
	if err != nil {
		return 0, fmt.Errorf("event: timeout: %w", err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("event: timeout: %s is not a length of time to wait", e.Timeout)
	}
	return d, nil
}

// DefaultServiceTimeout bounds one request to a service.
//
// Shorter than a stage's, because these are database reads and writes on one
// machine rather than a fetch across the internet: a graph query that has taken
// half a minute is not slow, it is stuck.
const DefaultServiceTimeout = 30 * time.Second

// Wait is how long one request to the entity service may take.
func (e *EntityService) Wait() (time.Duration, error) {
	if e == nil || e.Timeout == "" {
		return DefaultServiceTimeout, nil
	}

	d, err := time.ParseDuration(e.Timeout)
	if err != nil {
		return 0, fmt.Errorf("entity: timeout: %w", err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("entity: timeout: %s is not a length of time to wait", e.Timeout)
	}
	return d, nil
}

// ParseService reads a service document.
//
// Separate from [Parse] rather than a mode of it, because the two documents
// answer different questions and a file that is one is never the other. Reading
// a job document here says so by name instead of decoding to an empty service
// and starting nothing.
func ParseService(src []byte, filename string) (*Service, error) {
	parser := hclparse.NewParser()

	parsed, diags := parser.ParseHCL(src, filename)
	if diags.HasErrors() {
		return nil, diagError(diags)
	}

	var doc Service
	if diags := gohcl.DecodeBody(parsed.Body, evalContext(), &doc); diags.HasErrors() {
		return nil, diagError(diags)
	}
	return &doc, nil
}

// Validate reports everything wrong with a service document at once, the way a
// job document does: a person fixing a file wants the whole list.
func (s *Service) Validate() error {
	var problems []error

	if s.Entity == nil && s.Event == nil {
		problems = append(problems, fmt.Errorf(
			"a service document configures nothing. Add an `entity` or an `event` block saying where the store lives"))
	}
	if s.Entity != nil {
		if s.Entity.Dir == "" {
			problems = append(problems, fmt.Errorf("entity: no dir, and a graph has to live somewhere"))
		}
		if _, err := s.Entity.Wait(); err != nil {
			problems = append(problems, err)
		}
	}
	if s.Event != nil {
		if s.Event.Dir == "" {
			problems = append(problems, fmt.Errorf("event: no dir, and events have to live somewhere"))
		}
		if _, err := s.Event.Wait(); err != nil {
			problems = append(problems, err)
		}
	}

	return joinErrors(problems)
}
