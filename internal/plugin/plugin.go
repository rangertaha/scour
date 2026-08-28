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
// deliberately does not do it: `scour job valid` runs offline and in CI, so it
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
// expression everywhere it travels: the stored job, the diff, `scour job show`. It
// is only resolved here, on the node that builds the plugin, against the
// evaluation context handed in.
package plugin

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"

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

	// cleanup collects what the plugins opened. Unexported: a plugin registers
	// through [Config.Defer] and nothing else needs to see the list.
	cleanup *cleanups
}

// Defer registers something to close when the chain is torn down.
//
// A plugin that opens a bucket, a database or a connection registers its close
// here, and nothing else can: the seam hands back a middleware function, and a
// function has nowhere to keep a Close method. Without this a server building a
// chain per job leaks one handle per job.
//
// A Config built by hand rather than by [Build] has nowhere to keep it and
// drops it, which is what a test constructing one middleware wants.
func (c Config) Defer(close func() error) {
	if c.cleanup == nil || close == nil {
		return
	}
	c.cleanup.add(close)
}

// Decode reads the plugin's own fields into a schema of its choosing.
//
// A thin wrapper over gohcl. The diagnostics come back as they are: whoever
// called the factory already knows which plugin it was building, and says so.
func (c Config) Decode(into any) error {
	if c.Body == nil {
		return nil
	}

	eval := c.Eval
	if eval == nil {
		// Not nil. A nil context makes HCL refuse any function call with
		// "Functions may not be called here", which tells somebody whose job
		// used secret() nothing about what went wrong or what to do. This one
		// refuses by name, and says what is missing.
		eval = NoSecrets
	}

	if diags := decode(c.Body, eval, into); diags.HasErrors() {
		return diags
	}
	return nil
}

// NoSecrets is the evaluation context a plugin is decoded against on a node
// that has no secret store.
//
// It knows `secret` exists and refuses to answer it, which is the difference
// between "this node cannot read secrets, and you asked for acme-s3-key" and
// HCL's own "Functions may not be called here". The second is what a nil
// context produces, and it is what every binary produced before anything built
// a real one.
var NoSecrets = &hcl.EvalContext{
	Functions: map[string]function.Function{
		"secret": function.New(&function.Spec{
			Params: []function.Parameter{{Name: "name", Type: cty.String}},
			Type:   function.StaticReturnType(cty.String),
			Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
				return cty.NilVal, fmt.Errorf(
					"this node has no secrets, and %q was asked for", args[0].AsString())
			},
		}),
	},
}

// Registry is what a stage keeps its middleware in.
//
// One per stage rather than one shared, because the stages carry different
// things: a downloader link wraps a request into a response, a spider link
// wraps a response into items. A single registry would need a cast at every
// call and would let a spider plugin be loaded into a downloader.
type Registry[In, Out any] = registry.Registry[Config, chain.Wrapper[In, Out]]

// Factory builds one middleware from its configuration. It is what a stage's
// Register takes, and what a plugin package writes.
type Factory[In, Out any] = registry.Factory[Config, chain.Wrapper[In, Out]]

// NewRegistry returns a registry for one stage's middleware.
func NewRegistry[In, Out any](stage engine.Stage) *Registry[In, Out] {
	return registry.New[Config, chain.Wrapper[In, Out]](string(stage) + " middleware")
}

// Chain is a stage's middleware, built, together with whatever it holds open.
//
// The two travel together because they have the same lifetime: the chain is
// built when a job starts on this node and torn down when it stops, and the
// bucket a cache plugin opened has to close at exactly that moment.
type Chain[In, Out any] struct {
	// Links are the middleware. [chain.Build] sorts them, so the order here is
	// the order the job listed.
	Links []chain.Link[In, Out]

	cleanup *cleanups
}

// Handler wraps core in the chain.
func (c *Chain[In, Out]) Handler(core chain.Handler[In, Out]) chain.Handler[In, Out] {
	return chain.Build(core, c.Links)
}

// Names lists the middleware in the order it runs on the way out.
func (c *Chain[In, Out]) Names() []string { return chain.Names(c.Links) }

// Close releases what the middleware opened, last opened first, and reports
// every failure rather than stopping at the first: a bucket that will not close
// must not keep a database open.
//
// Closing twice is not an error, so a caller may defer it and close again on a
// path that has already done so.
func (c *Chain[In, Out]) Close() error {
	if c == nil || c.cleanup == nil {
		return nil
	}
	return c.cleanup.run()
}

// cleanups is what [Config.Defer] fills and [Chain.Close] empties.
type cleanups struct {
	mu  sync.Mutex
	fns []func() error
}

func (c *cleanups) add(fn func() error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fns = append(c.fns, fn)
}

func (c *cleanups) run() error {
	c.mu.Lock()
	fns := c.fns
	c.fns = nil
	c.mu.Unlock()

	var problems []error
	for i := len(fns) - 1; i >= 0; i-- {
		if err := fns[i](); err != nil {
			problems = append(problems, err)
		}
	}
	return errors.Join(problems...)
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
//
// The caller closes what comes back. On failure there is nothing to close,
// because a refused chain releases whatever it had already opened before it
// returns: a job refused for its sixth plugin must not leave the bucket its
// first one opened.
func Build[In, Out any](
	ctx context.Context,
	reg *Registry[In, Out],
	job *engine.Job,
	stage engine.Stage,
	eval *hcl.EvalContext,
) (*Chain[In, Out], error) {
	wanted := job.Chain(stage)
	held := &cleanups{}
	built := &Chain[In, Out]{cleanup: held}
	if len(wanted) == 0 {
		return built, nil
	}

	built.Links = make([]chain.Link[In, Out], 0, len(wanted))
	var missing, failed []string

	for _, p := range wanted {
		if !reg.Has(p.Name) {
			missing = append(missing, p.Name)
			continue
		}

		wrap, err := reg.New(ctx, p.Name, Config{
			Name:    p.Name,
			Order:   p.Position(),
			Job:     job.Name,
			Body:    p.Config,
			Eval:    eval,
			cleanup: held,
		})
		if err != nil {
			failed = append(failed, err.Error())
			continue
		}

		built.Links = append(built.Links, chain.Link[In, Out]{
			Name:  p.Name,
			Order: p.Position(),
			Wrap:  wrap,
		})
	}

	if len(missing) > 0 {
		return nil, closeAfter(built, fmt.Errorf(
			"job %q: the %s chain wants %s, which nothing on this node implements. Registered: %s",
			job.Name, stage, quoted(missing), available(reg.Names())))
	}
	if len(failed) > 0 {
		return nil, closeAfter(built, fmt.Errorf("job %q: %s", job.Name, strings.Join(failed, "; ")))
	}
	return built, nil
}

// closeAfter releases what was opened before the chain was refused. The plugins
// that built are already holding whatever they opened, and the caller has no
// chain to close them with.
func closeAfter[In, Out any](c *Chain[In, Out], err error) error {
	if closeErr := c.Close(); closeErr != nil {
		return errors.Join(err, fmt.Errorf("closing what was already open: %w", closeErr))
	}
	return err
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
