// SPDX-License-Identifier: GPL-3.0-or-later

// Package pipeline runs the work on an extracted item, as a graph.
//
// Every other stage is a chain, because a request has one path through it. This
// one is not, because the work on a record is a dependency graph and pretending
// otherwise costs concurrency for nothing: `validate` and `dedupe` both need
// `clean` to have run and neither needs the other, so they run at the same time
// and a list could not say so.
//
// # Waves are computed, not written
//
// A step says what it requires and nothing says what runs when.
// [engine.Job.Waves] groups the steps whose dependencies have already run, and
// each wave runs concurrently. That is the whole scheduler: no numbers, no
// priorities, and a cycle is refused when the document is validated rather than
// discovered at three in the morning.
//
// # A step does not mutate what it was handed
//
// Each is given the records and returns them, and the runner passes the result
// on. A step that edited in place would make the graph's order observable
// between steps that are supposed to be independent, which is exactly the
// property the graph exists to have.
package pipeline

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/hashicorp/hcl/v2/gohcl"

	"github.com/rangertaha/scour/internal/engine"
	"github.com/rangertaha/scour/internal/record"
	"github.com/rangertaha/scour/internal/registry"
)

// Step is one node in the graph.
//
// It takes what it was given and returns what should go on. Returning fewer
// records is how `dedupe` works; returning them in another order is how `rank`
// works; returning an error stops the run, because a step that cannot do its
// job is not a step that should be silently skipped.
type Step interface {
	Run(ctx context.Context, records []*record.Record) ([]*record.Record, error)
}

// Func adapts an ordinary function to [Step].
type Func func(ctx context.Context, records []*record.Record) ([]*record.Record, error)

// Run implements [Step].
func (f Func) Run(ctx context.Context, records []*record.Record) ([]*record.Record, error) {
	return f(ctx, records)
}

// Config is what a step is built from.
type Config struct {
	// Kind is the first label: what sort of step this is.
	Kind string

	// Name is the second: which item it works on, or what it is for.
	Name string

	// Job is the job it belongs to.
	Job string

	// Item is the shape it works on, if the name is one. Nil otherwise,
	// because a `python` step called "enrich" is not about one item.
	Item *engine.Item

	// Body is everything else in the block, undecoded.
	Body Body
}

// Body is a step's own configuration, decoded by the step.
type Body interface {
	Decode(into any) error
}

// Factory builds one step from its configuration.
type Factory = registry.Factory[Config, Step]

// reg holds the kinds. Registered from each kind's own package, the same way
// every other extension point in scour works.
var reg = registry.New[Config, Step]("pipeline step")

// Register adds a kind.
func Register(kind string, f Factory) { reg.Register(kind, f) }

// Registered lists what this build has, sorted.
func Registered() []string { return reg.Names() }

// Has reports whether a kind is registered.
func Has(kind string) bool { return reg.Has(kind) }

// Pipeline is a job's graph, built.
type Pipeline struct {
	job   string
	waves [][]*engine.Step
	steps map[string]Step
}

// New builds the pipeline a job configured.
//
// Every step is built here, so a job naming a kind nothing implements is
// refused before the first record rather than in the middle of a run, and every
// missing kind is reported at once.
func New(ctx context.Context, job *engine.Job) (*Pipeline, error) {
	if job == nil {
		return nil, errors.New("pipeline: no job")
	}

	waves, err := job.Waves()
	if err != nil {
		return nil, fmt.Errorf("pipeline: job %q: %w", job.Name, err)
	}

	p := &Pipeline{job: job.Name, waves: waves, steps: map[string]Step{}}

	var missing, failed []string
	for _, wave := range waves {
		for _, declared := range wave {
			if !reg.Has(declared.Kind) {
				missing = append(missing, declared.Kind)
				continue
			}
			built, err := reg.New(ctx, declared.Kind, Config{
				Kind: declared.Kind,
				Name: declared.Name,
				Job:  job.Name,
				Item: itemNamed(job, declared.Name),
				Body: body{declared},
			})
			if err != nil {
				failed = append(failed, fmt.Sprintf("%s (%v)", declared.Address(), err))
				continue
			}
			p.steps[declared.Address()] = built
		}
	}

	if len(missing) > 0 {
		return nil, fmt.Errorf("job %q: no pipeline step of kind %s. This build has %s",
			job.Name, quoted(missing), strings.Join(Registered(), ", "))
	}
	if len(failed) > 0 {
		return nil, fmt.Errorf("job %q: %s", job.Name, strings.Join(failed, "; "))
	}
	return p, nil
}

// Run puts records through the graph and returns what came out.
//
// Steps in one wave run at the same time, each on its own copy, and their
// results are merged in the order the wave declares them so that two runs over
// one corpus produce the same output. Concurrency that changed the answer would
// be concurrency nobody could use.
func (p *Pipeline) Run(ctx context.Context, records []*record.Record) ([]*record.Record, error) {
	current := records

	for _, wave := range p.waves {
		if len(wave) == 1 {
			out, err := p.runOne(ctx, wave[0], current)
			if err != nil {
				return nil, err
			}
			current = out
			continue
		}

		results := make([][]*record.Record, len(wave))
		problems := make([]error, len(wave))

		var wg sync.WaitGroup
		for i, declared := range wave {
			wg.Add(1)
			go func() {
				defer wg.Done()
				results[i], problems[i] = p.runOne(ctx, declared, clone(current))
			}()
		}
		wg.Wait()

		if err := errors.Join(problems...); err != nil {
			return nil, err
		}
		current = merge(results)
	}
	return current, nil
}

func (p *Pipeline) runOne(ctx context.Context, declared *engine.Step, records []*record.Record) ([]*record.Record, error) {
	step, ok := p.steps[declared.Address()]
	if !ok {
		return nil, fmt.Errorf("pipeline: job %q: %s was never built", p.job, declared.Address())
	}

	out, err := step.Run(ctx, records)
	if err != nil {
		return nil, fmt.Errorf("pipeline: job %q: %s: %w", p.job, declared.Address(), err)
	}
	return out, nil
}

// Width is how many steps can run at once, which is what a run reports and what
// says whether the graph is worth having.
func (p *Pipeline) Width() int {
	widest := 0
	for _, wave := range p.waves {
		widest = max(widest, len(wave))
	}
	return widest
}

// Waves is how many rounds the graph takes.
func (p *Pipeline) Waves() int { return len(p.waves) }

// Order lists the steps in the order they run, wave by wave.
func (p *Pipeline) Order() []string {
	var out []string
	for _, wave := range p.waves {
		for _, step := range wave {
			out = append(out, step.Address())
		}
	}
	return out
}

// merge combines what a wave's steps returned.
//
// A record is kept if every step that saw it kept it, which is what makes two
// filters in one wave behave like the same two in sequence. Order follows the
// first step that returned it, so the result does not depend on which goroutine
// finished first.
func merge(results [][]*record.Record) []*record.Record {
	if len(results) == 0 {
		return nil
	}

	counts := map[*record.Record]int{}
	byIdentity := map[string]*record.Record{}
	var order []string

	for _, list := range results {
		seen := map[string]bool{}
		for _, r := range list {
			id := identity(r)
			if seen[id] {
				continue
			}
			seen[id] = true
			if _, known := byIdentity[id]; !known {
				byIdentity[id] = r
				order = append(order, id)
			}
			counts[byIdentity[id]]++
		}
	}

	out := make([]*record.Record, 0, len(order))
	for _, id := range order {
		if counts[byIdentity[id]] == len(results) {
			out = append(out, byIdentity[id])
		}
	}
	return out
}

// identity is what makes two records the same record across steps that each
// worked on their own copy.
func identity(r *record.Record) string {
	names := r.Names()
	parts := make([]string, 0, len(names)+2)
	parts = append(parts, r.Item, r.URL)
	for _, name := range names {
		parts = append(parts, name+"="+r.Values[name])
	}
	return strings.Join(parts, "\x00")
}

func clone(records []*record.Record) []*record.Record {
	out := make([]*record.Record, len(records))
	for i, r := range records {
		out[i] = r.Clone()
	}
	return out
}

func itemNamed(job *engine.Job, name string) *engine.Item {
	for _, item := range job.Items {
		if item.Name == name {
			return item
		}
	}
	return nil
}

func quoted(names []string) string {
	sort.Strings(names)

	seen := map[string]bool{}
	out := make([]string, 0, len(names))
	for _, name := range names {
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, fmt.Sprintf("%q", name))
	}
	return strings.Join(out, " or ")
}

// body adapts a declared step to [Body], so a step decodes its own
// configuration the same way a plugin does: against its own schema, with
// diagnostics that carry a line and a column.
type body struct{ step *engine.Step }

func (b body) Decode(into any) error {
	if b.step.Config == nil {
		return nil
	}
	if diags := gohcl.DecodeBody(b.step.Config, nil, into); diags.HasErrors() {
		return diags
	}
	return nil
}

// Inline is the script written in the document, and Script the path to one, for
// the kinds that run one.
func (b body) Inline() string { return b.step.Inline }
func (b body) Script() string { return b.step.Script }
