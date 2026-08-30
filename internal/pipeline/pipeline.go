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
	"io"
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

// Unregister removes a step kind, and exists for tests. See
// [registry.Registry.Unregister].
func Unregister(kind string) { reg.Unregister(kind) }

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

	// A pipeline that will not be returned still has to give back what its
	// built steps opened.
	//
	// The loop keeps going after the first failure, so several steps can be
	// built before a later one is refused, and the caller gets nil and no
	// handle: `run.New` then calls close with a nil graph and Pipeline.Close
	// never runs. The entities step opens SQLite, so a job with an entities
	// step and a later step that fails validation held a database handle and
	// its write-ahead files for the life of the process — which is the file
	// lock a second run cannot take, named in Close's own documentation.
	if len(missing) > 0 || len(failed) > 0 {
		_ = p.Close()
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

	// A crawl can hand in two records with one identity, and it is not a bug
	// when it does: two URLs that redirect to one page are two frontier entries
	// and one fetched page, so the same item is extracted from it twice. The
	// merge cannot carry a duplicate through a wave, and this used to abort the
	// whole run rather than say so, throwing away every record the crawl had
	// produced because a site had a trailing-slash redirect. They are the same
	// page, so collapsing them is not a loss; the first is kept, which is
	// deterministic because the crawl order is.
	current = collapse(current)

	for _, wave := range p.waves {
		if len(wave) == 1 {
			out, err := p.runOne(ctx, wave[0], current)
			if err != nil {
				return nil, err
			}
			// After a wave the uniqueness is the pipeline's own invariant
			// rather than the crawl's, so a step that invented a duplicate is a
			// step bug and is refused. Checked after every wave and not only
			// before a merge, because a duplicate that reaches an exporter is
			// two rows nobody can tell apart.
			if err := unique(out); err != nil {
				return nil, fmt.Errorf("pipeline: job %q: step %s.%s: %w", p.job, wave[0].Kind, wave[0].Name, err)
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
		current = merge(current, results)
		if err := unique(current); err != nil {
			return nil, fmt.Errorf("pipeline: job %q: %w", p.job, err)
		}
	}
	return current, nil
}

// collapse drops all but the first record of any identity.
func collapse(records []*record.Record) []*record.Record {
	seen := make(map[string]bool, len(records))
	out := make([]*record.Record, 0, len(records))
	for _, r := range records {
		if seen[r.Identity()] {
			continue
		}
		seen[r.Identity()] = true
		out = append(out, r)
	}
	return out
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

// Close releases the steps that hold something.
//
// Most hold nothing, so most are not closers and nothing here has to know which
// are. The ones that do exist because a step is allowed to write somewhere other
// than the records it returns: `entities` writes the graph, and a database
// handle nobody gives back is a file lock a second run cannot take.
func (p *Pipeline) Close() error {
	var problems []error
	for _, step := range p.steps {
		closer, ok := step.(io.Closer)
		if !ok {
			continue
		}
		if err := closer.Close(); err != nil {
			problems = append(problems, err)
		}
	}
	return errors.Join(problems...)
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
// Two rules, and the first one is the one that used to be wrong.
//
// **A record is the same record if [record.Identity] says so**, which is the
// item and the page and not the values. Steps transform values, so identifying
// them by value meant a step's output was a different record from its input:
// two independent `clean` steps in one wave produced four identities where
// there were two records, neither reached every step's output, and the wave
// returned nothing at all. Silently, reporting success.
//
// **A record is kept if every step kept it**, which is what makes two filters
// in one wave behave like the same two in sequence.
//
// **The order taken is the one that changed**, for the same reason. A step
// that reorders is a step whose entire output is its ordering: taking the
// input's order back off it silently undid every `rank` that shared a wave,
// and with a limit set it kept an arbitrary N records rather than the top N.
// A step that merely dropped records is not reordering, so what counts as a
// change is an output that is not a subsequence of the input.
//
// **The version taken is the one that changed**, because a step is named for
// the item it works on and leaves the others alone: of the copies handed out,
// at most one has been edited. If more than one was, the earlier step in wave
// order wins, deterministically, so that two runs over one corpus produce the
// same output whichever goroutine finished first. Two steps in one wave editing
// one item is a job saying two contradictory things about it, and neither
// ordering is more correct than the other; what matters is that it is not the
// scheduler's whim.
func merge(before []*record.Record, results [][]*record.Record) []*record.Record {
	if len(results) == 0 {
		return nil
	}

	// What each record looked like going in, and which of them were there at
	// all. The second is not the same question as the first once a step can add
	// one, and conflating them is what made an added record unkeepable.
	original := make(map[string]*record.Record, len(before))
	wasThere := make(map[string]bool, len(before))
	for _, r := range before {
		original[r.Identity()] = r
		wasThere[r.Identity()] = true
	}

	// The order to return them in: the input's, with every step that reordered
	// applying what it actually moved and leaving the rest alone.
	//
	// The first such step used to decide for the whole wave and the others were
	// discarded. That is defensible for two steps ordering the same records -
	// somebody has to win - and wrong for two ordering different ones, which is
	// the ordinary case: a rank is declared per item, so two ranks on two items
	// have no requires between them and land in one wave. The second item came
	// out in crawl order with nothing logged.
	//
	// What a step moved is read from its output against the wave's input, not
	// against the running order, because a step that returned its records
	// untouched must not be read as undoing the step before it.
	input := identities(before)
	order := input
	for _, list := range results {
		claimed := moved(input, identities(list))
		if len(claimed) == 0 {
			continue
		}
		order = reorder(order, claimed)
	}
	// Anything the ordering step did not mention still needs a place, so that
	// the kept rule below is what decides its fate and not the ordering.
	inOrder := make(map[string]bool, len(order))
	for _, id := range order {
		inOrder[id] = true
	}
	for _, r := range before {
		if !inOrder[r.Identity()] {
			inOrder[r.Identity()] = true
			order = append(order, r.Identity())
		}
	}

	kept := map[string]int{}
	changed := map[string]*record.Record{}

	for _, list := range results {
		seen := map[string]bool{}
		for _, r := range list {
			id := r.Identity()
			if seen[id] {
				continue
			}
			seen[id] = true
			kept[id]++

			// A record this step never had is one it added. Keep it at the end
			// rather than dropping it. It cannot be held to "every step kept
			// it", because the other steps of this wave never saw it.
			if !wasThere[id] {
				if _, already := original[id]; !already {
					original[id] = r
					if !inOrder[id] {
						inOrder[id] = true
						order = append(order, id)
					}
				}
				continue
			}
			if _, already := changed[id]; !already && !same(original[id], r) {
				changed[id] = r
			}
		}
	}

	out := make([]*record.Record, 0, len(order))
	for _, id := range order {
		if wasThere[id] {
			if kept[id] != len(results) {
				continue
			}
		} else if kept[id] == 0 {
			continue
		}
		if edited, ok := changed[id]; ok {
			out = append(out, edited)
			continue
		}
		out = append(out, original[id])
	}
	return out
}

// same reports whether a step left a record as it found it.
func same(before, after *record.Record) bool {
	if len(before.Values) != len(after.Values) {
		return false
	}
	for name, value := range before.Values {
		if after.Values[name] != value {
			return false
		}
	}
	return before.Item == after.Item && before.URL == after.URL &&
		before.Spec == after.Spec && before.Fetched.Equal(after.Fetched)
}

// moved is the records a step actually reordered, in the order it put them.
//
// A step is handed every record in the wave and returns every record it kept,
// so its output asserts an order over records it never looked at. Comparing
// against the wave's input says which ones it really moved: an id whose place
// changed, and nothing else. A step that reordered nothing claims nothing, so
// it cannot undo the step beside it.
func moved(input, output []string) []string {
	// Compared over the records the two have in common, not by index.
	//
	// An index says nothing on its own: dropping one record shifts every record
	// after it, and adding one at the front shifts all of them. Read that way, a
	// step that only filtered claimed everything below its first drop and
	// re-imposed the input's order on it - so a `validate` that removed one
	// invalid record silently undid the `rank` sharing its wave, which is the
	// defect this function was written to fix, arriving by the other door.
	//
	// Restricting each list to what the other also holds leaves two orderings
	// of one set, and then a difference really is a move.
	kept := make(map[string]bool, len(output))
	for _, id := range output {
		kept[id] = true
	}
	held := make(map[string]bool, len(input))
	for _, id := range input {
		held[id] = true
	}

	var was []string
	for _, id := range input {
		if kept[id] {
			was = append(was, id)
		}
	}

	now := make([]string, 0, len(was))
	seen := make(map[string]bool, len(output))
	for _, id := range output {
		// Invented by this step, or repeated: where it sits is not a
		// reordering of anything, and the wave's keep rule decides its fate.
		if !held[id] || seen[id] {
			continue
		}
		seen[id] = true
		now = append(now, id)
	}

	var claimed []string
	for i, id := range now {
		if i < len(was) && was[i] != id {
			claimed = append(claimed, id)
		}
	}
	return claimed
}

// reorder puts one step's records back into the places they occupy now,
// leaving every other record exactly where it was.
//
// Two steps that moved different records therefore compose, and neither undoes
// the other. Two that moved the same records still means the later one wins,
// which is the rule the rest of the merge keeps.
func reorder(order, claimed []string) []string {
	place := make(map[string]bool, len(claimed))
	for _, id := range claimed {
		place[id] = true
	}

	out := make([]string, 0, len(order))
	next := 0
	for _, id := range order {
		if !place[id] {
			out = append(out, id)
			continue
		}
		if next < len(claimed) {
			out = append(out, claimed[next])
			next++
		}
	}
	return out
}

// identities is the identity of each record, in order.
func identities(records []*record.Record) []string {
	out := make([]string, len(records))
	for i, r := range records {
		out[i] = r.Identity()
	}
	return out
}

// unique refuses a set of records in which two share an identity.
//
// One page produces at most one record per item, so this cannot happen from
// extraction. A step that invented a second record for one item on one page
// could, and the merge would then have to guess which of them a later step's
// output referred to. Refusing loudly beats guessing quietly, which is the
// lesson of the bug this function was written alongside.
func unique(records []*record.Record) error {
	seen := make(map[string]bool, len(records))
	for _, r := range records {
		if seen[r.Identity()] {
			return fmt.Errorf("two records share one identity: item %q from %s", r.Item, r.URL)
		}
		seen[r.Identity()] = true
	}
	return nil
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
