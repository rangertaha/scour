// SPDX-License-Identifier: GPL-3.0-or-later

// Package plugin turns what a job asked for into a chain that can run.
//
// It is the seam between two halves that deliberately know nothing about each
// other. [engine] reads a document and can say a job wants a plugin called
// "cache" at position 900; [chain] runs an ordered set of middleware and does
// not care where the set came from. Neither can answer whether "cache" is a
// thing that exists.
//
// This is where that is answered, and it is the first place a job naming a
// plugin nothing implements is refused rather than validated. Validation
// deliberately does not do it: `scour validate` runs offline and in CI, so it
// cannot know what a node somewhere else has registered. Building the chain
// can, because by then there is a process with the implementations compiled in.
//
// # A plugin decodes its own configuration
//
// What arrives here is the block's body, undecoded. A plugin decodes it against
// its own schema, which is what lets somebody else write one without this
// package knowing its fields, and is why a bad field gets an error with a line
// and a column rather than being silently ignored.
//
// It is also why a secret is safe. `secret("acme-s3-key")` is an unevaluated
// expression everywhere it travels: the stored job, the diff, `scour show`. It
// is only resolved here, on the node that builds the plugin, against the
// evaluation context handed in.
package plugin

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/hcl/v2"

	"github.com/rangertaha/scour/internal/chain"
	"github.com/rangertaha/scour/internal/engine"
	"github.com/rangertaha/scour/internal/registry"
)

// Config is what one plugin is built from.
type Config struct {
	// Name is what the job called it.
	Name string

	// Order is where it sits in its chain, after the catalogued default has
	// been applied.
	Order int

	// Job is the job that asked for it, for errors and for anything that needs
	// to scope itself.
	Job string

	// Body is everything else in the block, undecoded. Decode it against your
	// own schema with gohcl and the diagnostics carry positions.
	Body hcl.Body

	// Eval is the context to decode against. It is where `secret()` resolves,
	// so decoding must happen when the plugin is built and not before.
	Eval *hcl.EvalContext
}

// Decode reads the plugin's own fields into a schema of its choosing.
//
// A thin wrapper over gohcl. The diagnostics come back as they are: whoever
// called the factory already knows which plugin it was building, and says so.
func (c Config) Decode(into any) error {
	if c.Body == nil {
		return nil
	}
	if diags := decode(c.Body, c.Eval, into); diags.HasErrors() {
		return diags
	}
	return nil
}

// Registry is what a stage keeps its middleware in.
//
// One per stage rather than one shared, because the stages carry different
// things: a downloader link wraps a request into a response, a spider link
// wraps a response into items. A single registry would need a cast at every
// call and would let a spider plugin be loaded into a downloader.
type Registry[In, Out any] = registry.Registry[Config, chain.Wrapper[In, Out]]

// NewRegistry returns a registry for one stage's middleware.
func NewRegistry[In, Out any](stage engine.Stage) *Registry[In, Out] {
	return registry.New[Config, chain.Wrapper[In, Out]](string(stage) + " middleware")
}

// Build turns the plugins a job listed for one stage into an ordered chain.
//
// The job has already decided what is in the chain and in what order:
// [engine.Job.Chain] drops what is disabled and applies the catalogued
// positions. This resolves each name against what is actually compiled in, and
// refuses the whole chain if any of them is missing.
//
// All at once, rather than the first: a job loading six plugins on a node that
// has four of them should be told which two, not sent round the loop twice.
func Build[In, Out any](
	ctx context.Context,
	reg *Registry[In, Out],
	job *engine.Job,
	stage engine.Stage,
	eval *hcl.EvalContext,
) ([]chain.Link[In, Out], error) {
	wanted := job.Chain(stage)
	if len(wanted) == 0 {
		return nil, nil
	}

	links := make([]chain.Link[In, Out], 0, len(wanted))
	var missing, failed []string

	for _, p := range wanted {
		if !reg.Has(p.Name) {
			missing = append(missing, p.Name)
			continue
		}

		wrap, err := reg.New(ctx, p.Name, Config{
			Name:  p.Name,
			Order: p.Position(),
			Job:   job.Name,
			Body:  p.Config,
			Eval:  eval,
		})
		if err != nil {
			failed = append(failed, err.Error())
			continue
		}

		links = append(links, chain.Link[In, Out]{
			Name:  p.Name,
			Order: p.Position(),
			Wrap:  wrap,
		})
	}

	if len(missing) > 0 {
		return nil, fmt.Errorf("job %q: the %s chain wants %s, which nothing on this node implements. Registered: %s",
			job.Name, stage, quoted(missing), available(reg.Names()))
	}
	if len(failed) > 0 {
		return nil, fmt.Errorf("job %q: %s", job.Name, strings.Join(failed, "; "))
	}
	return links, nil
}

func quoted(names []string) string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, fmt.Sprintf("%q", n))
	}
	return strings.Join(out, " and ")
}

func available(names []string) string {
	if len(names) == 0 {
		return "none at all, which usually means the package that registers them was never imported"
	}
	return strings.Join(names, ", ")
}
