// SPDX-License-Identifier: GPL-3.0-or-later

package engine

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strings"
)

// Validate reports every problem in the document at once.
//
// At once, rather than the first: a client fixing a submission one error per
// round trip is a client we have made an enemy of.
func (d *Document) Validate() error {
	var problems []error

	if len(d.Jobs) == 0 {
		problems = append(problems, errors.New("no jobs"))
	}

	seen := map[string]bool{}
	for _, job := range d.Jobs {
		if seen[job.Name] {
			// Names are how a resubmission finds its job, so two jobs sharing
			// one would make that lookup ambiguous the moment it mattered.
			problems = append(problems, fmt.Errorf("job %q: submitted twice in the same document", job.Name))
		}
		seen[job.Name] = true
		problems = append(problems, job.validate()...)
	}

	return errors.Join(problems...)
}

// joinErrors is errors.Join over a slice, kept here because several validators
// build their problems the same way.
func joinErrors(problems []error) error { return errors.Join(problems...) }

// Names lists the jobs, in the order they were written.
func (d *Document) Names() []string {
	out := make([]string, 0, len(d.Jobs))
	for _, j := range d.Jobs {
		out = append(out, j.Name)
	}
	return out
}

func (j *Job) validate() []error {
	var problems []error

	if strings.TrimSpace(j.Name) == "" {
		problems = append(problems, errors.New("a job needs a name"))
	}

	prefix := func(err error) error { return fmt.Errorf("job %q: %w", j.Name, err) }

	if len(j.Start) == 0 {
		problems = append(problems, prefix(errors.New("no start URLs, so there is nowhere to begin")))
	}
	for i, raw := range j.Start {
		if err := checkStartURL(raw); err != nil {
			problems = append(problems, prefix(fmt.Errorf("start[%d] %q: %w", i, raw, err)))
		}
	}

	if len(j.Items) == 0 {
		problems = append(problems, prefix(errors.New("no item blocks, so there is nothing to extract")))
	}

	items := map[string]bool{}
	for _, item := range j.Items {
		if items[item.Name] {
			problems = append(problems, prefix(fmt.Errorf("item %q: declared twice", item.Name)))
		}
		items[item.Name] = true
		for _, err := range item.validate() {
			problems = append(problems, prefix(err))
		}
	}

	if e := j.Engine; e != nil {
		for _, err := range e.Limits.validate() {
			problems = append(problems, prefix(err))
		}
		for _, err := range e.Politeness.validate() {
			problems = append(problems, prefix(err))
		}
		for _, err := range e.Components.validate() {
			problems = append(problems, prefix(err))
		}
	}

	for _, err := range j.Monitoring.validate() {
		problems = append(problems, prefix(err))
	}
	for _, err := range j.Mutation.validate() {
		problems = append(problems, prefix(err))
	}
	for _, err := range j.validatePlugins() {
		problems = append(problems, prefix(err))
	}
	for _, err := range j.validatePipelines() {
		problems = append(problems, prefix(err))
	}
	for _, err := range j.validateExporters() {
		problems = append(problems, prefix(err))
	}

	return problems
}

// checkStartURL refuses anything that is not a page on the web.
//
// A crawler that followed file:// would read the disk of whichever machine
// picked the job up, which is a submitted job reaching somewhere it was never
// given.
func checkStartURL(raw string) error {
	u, err := url.Parse(raw)
	switch {
	case err != nil:
		return err
	case u.Scheme != "http" && u.Scheme != "https":
		return errors.New("only http and https are crawled")
	case u.Host == "":
		return errors.New("no host")
	}
	return nil
}

func (i *Item) validate() []error {
	var problems []error

	if strings.TrimSpace(i.Name) == "" {
		problems = append(problems, errors.New("an item needs a name"))
	}
	if i.Type != "" && !Type(i.Type).Valid() {
		problems = append(problems, fmt.Errorf("item %q: type %q is not one of %s",
			i.Name, i.Type, strings.Join(TypeNames(), ", ")))
	}
	if len(i.Properties) == 0 {
		problems = append(problems, fmt.Errorf("item %q: no properties, so there is nothing to look for", i.Name))
	}

	for _, err := range validateProperties(i.Properties, "item "+i.Name) {
		problems = append(problems, err)
	}
	return problems
}

func validateProperties(props []*Property, where string) []error {
	var problems []error

	seen := map[string]bool{}
	for _, p := range props {
		path := where + "." + p.Name

		if strings.TrimSpace(p.Name) == "" {
			problems = append(problems, fmt.Errorf("%s: a property needs a name", where))
		}
		if seen[p.Name] {
			problems = append(problems, fmt.Errorf("%s: declared twice", path))
		}
		seen[p.Name] = true

		if p.Type != "" && !Type(p.Type).Valid() {
			problems = append(problems, fmt.Errorf("%s: type %q is not one of %s",
				path, p.Type, strings.Join(TypeNames(), ", ")))
		}

		for _, t := range p.Transforms {
			if !slices.Contains(Transforms, t) {
				problems = append(problems, fmt.Errorf("%s: transform %q is not one of %s",
					path, t, strings.Join(TransformNames(), ", ")))
			}
		}

		// A property with children describes an object, whatever it says. The
		// mismatch is an error rather than an inference, because silently
		// changing a declared type is how a document stops meaning what it
		// reads as.
		if len(p.Properties) > 0 && p.Type != "" && Type(p.Type) != TypeObject {
			problems = append(problems, fmt.Errorf(
				"%s: has nested properties but is typed %s, which only object can hold", path, p.Type))
		}

		problems = append(problems, validateProperties(p.Properties, path)...)
	}

	return problems
}

func (j *Job) validatePlugins() []error {
	var problems []error

	type slot struct {
		stage Stage
		name  string
	}
	seen := map[slot]bool{}

	for _, p := range j.Plugins {
		stage := Stage(p.Stage)
		where := fmt.Sprintf("plugin %q %q", p.Stage, p.Name)

		if !stage.ValidPlugin() {
			// The pipeline is a stage, but its extensions are not plugins.
			// Saying so is worth more than listing what is, because somebody
			// writing this has the right idea and the wrong spelling.
			if stage == StagePipeline {
				problems = append(problems, fmt.Errorf(
					`%s: a pipeline step is a node in a graph, not a link in a chain. Write pipelines %q "<item>" and give it requires instead of order`,
					where, p.Name))
				continue
			}
			problems = append(problems, fmt.Errorf("%s: %q is not a stage, have %s",
				where, p.Stage, strings.Join(stageNames(PluginStages), ", ")))
			continue
		}

		key := slot{stage, p.Name}
		if seen[key] {
			problems = append(problems, fmt.Errorf("%s: loaded twice into the same stage", where))
		}
		seen[key] = true

		// A plugin scour does not ship is fine, that is the point of plugins,
		// but it has to say where in the chain it goes because we cannot guess.
		if _, builtin := DefaultOrder(stage, p.Name); !builtin && p.Order == 0 {
			problems = append(problems, fmt.Errorf(
				"%s: not a built-in, so it needs an explicit order. Built-ins for %s are %s",
				where, stage, strings.Join(PlacementNames(stage), ", ")))
		}
		if p.Order < 0 {
			problems = append(problems, fmt.Errorf("%s: order %d is negative", where, p.Order))
		}
	}

	return problems
}

// Chain returns a stage's plugins in the order they run, lowest first.
//
// # A job gets exactly the chain it lists
//
// Nothing is added that the document did not ask for. There is no implicit set
// of middleware that is on unless you turn it off, so a chain can be read off
// the job and no reader has to know a list kept somewhere else.
//
// `enabled = false` therefore means precisely what leaving the block out means:
// the link is not in the chain. The two spellings exist because they are
// written for different reasons. Deleting a block throws away its
// configuration; setting enabled = false keeps the configuration and stops the
// link, which is what you want at three in the morning and what you want when
// the setting took an afternoon to work out.
func (j *Job) Chain(stage Stage) []*Plugin {
	var out []*Plugin
	for _, p := range j.Plugins {
		if Stage(p.Stage) != stage {
			continue
		}
		if !p.IsEnabled() {
			continue
		}
		out = append(out, p)
	}

	sort.SliceStable(out, func(a, b int) bool {
		return out[a].order() < out[b].order()
	})
	return out
}

// Names lists a chain's plugins in the order they run.
func Names(chain []*Plugin) []string {
	out := make([]string, 0, len(chain))
	for _, p := range chain {
		out = append(out, p.Name)
	}
	return out
}

// order is the plugin's position, falling back to the catalogued default.
func (p *Plugin) order() int {
	if p.Order != 0 {
		return p.Order
	}
	if def, ok := DefaultOrder(Stage(p.Stage), p.Name); ok {
		return def
	}
	return 0
}

func (j *Job) validateExporters() []error {
	var problems []error

	items := make(map[string]bool, len(j.Items))
	for _, item := range j.Items {
		items[item.Name] = true
	}

	seen := map[string]bool{}
	for _, e := range j.Exporters {
		where := fmt.Sprintf("exporter %q %q", e.Format, e.Item)

		if seen[e.Address()] {
			problems = append(problems, fmt.Errorf("%s: declared twice", where))
		}
		seen[e.Address()] = true

		// An exporter naming an item that is not extracted writes nothing, and
		// silently writing nothing is the failure mode nobody notices until
		// they go looking for the output.
		if !items[e.Item] {
			problems = append(problems, fmt.Errorf(
				"%s: no item %q is declared. Declared: %s",
				where, e.Item, strings.Join(itemNames(j.Items), ", ")))
		}
	}
	return problems
}

// ExportersFor returns the exporters that receive copies of one item, in the
// order they were written.
func (j *Job) ExportersFor(item string) []*Exporter {
	var out []*Exporter
	for _, e := range j.Exporters {
		if e.Item == item {
			out = append(out, e)
		}
	}
	return out
}

func itemNames(items []*Item) []string {
	if len(items) == 0 {
		return []string{"none"}
	}
	out := make([]string, 0, len(items))
	for _, i := range items {
		out = append(out, i.Name)
	}
	sort.Strings(out)
	return out
}
