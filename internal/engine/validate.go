// SPDX-License-Identifier: GPL-3.0-or-later

package engine

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strings"

	"github.com/rangertaha/scour/internal/classify"
	"github.com/rangertaha/scour/internal/scope"
	"github.com/rangertaha/scour/internal/urls"
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
	for _, err := range j.validateStartIsInScope() {
		problems = append(problems, prefix(err))
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

	for _, err := range j.Scheduler.validate() {
		problems = append(problems, prefix(err))
	}
	for _, err := range j.Downloader.validate() {
		problems = append(problems, prefix(err))
	}
	for _, err := range j.Spider.validate() {
		problems = append(problems, prefix(err))
	}
	for _, err := range j.Pipeline.validate() {
		problems = append(problems, prefix(err))
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
	for _, err := range j.validateSteps() {
		problems = append(problems, prefix(err))
	}
	for _, err := range j.validateExporters() {
		problems = append(problems, prefix(err))
	}

	return problems
}

// validateStartIsInScope refuses a job whose own boundary excludes every URL it
// starts from.
//
// A crawl seeds by handing its start URLs to the scheduler, which drops what is
// out of scope before it is queued. If all of them are dropped the frontier is
// empty, nothing is ever fetched, and the run finishes in a millisecond and
// exits zero. It is the worst shape a mistake can take: a success that did
// nothing.
//
// It is not hypothetical. Three of the four templates scour ships had
// `included = ["*.example.com"]` beside `domains = ["example.com"]`, written
// meaning "this site and below it", which is what `domains` already says. As an
// inclusion pattern it means something else: if any are given a URL has to
// match one, and `*.example.com` matches the subdomains and refuses the apex.
// So `scour init news > news.hcl && scour run news.hcl`, which is the first
// thing anybody does, crawled nothing and said it was fine. The book's own
// example had it too.
//
// Refused rather than warned about, because there is no reading of a document
// under which this is what somebody meant, and a warning on a crawl nobody is
// watching is a warning nobody sees.
func (j *Job) validateStartIsInScope() []error {
	if len(j.Start) == 0 {
		return nil
	}
	if len(j.Domains) == 0 && len(j.Included) == 0 && len(j.Excluded) == 0 {
		return nil
	}

	bounds, err := scope.New(j.Domains, j.Included, j.Excluded)
	if err != nil {
		// The scope is unusable for its own reasons, which is a problem the
		// scheduler reports when it builds one. Saying it twice helps nobody.
		return nil
	}

	var refused []string
	for _, raw := range j.Start {
		normalised, err := urls.Normalise(raw, urls.Options{})
		if err != nil {
			continue // already reported by checkStartURL
		}
		if !bounds.Allows(normalised) {
			refused = append(refused, raw)
		}
	}

	// Some refused is a job that starts in more places than it may go, which is
	// a reasonable thing to write. All refused is a job that cannot begin.
	if len(refused) < len(j.Start) {
		return nil
	}
	return []error{fmt.Errorf(
		"every start URL is outside this job's own scope, so the crawl would seed nothing and finish immediately: %s.\n"+
			"  domains  = %v  (a host matches if it is one of these or a subdomain)\n"+
			"  included = %v  (if any are given, every URL has to match one)\n"+
			"  excluded = %v  (these never, whatever else matches)",
		strings.Join(refused, ", "), j.Domains, j.Included, j.Excluded)}
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
	problems = append(problems, validateRelations(i.Relations, "item "+i.Name)...)

	// The time has to be a date this item actually extracts, or the event
	// carries a timestamp from nowhere.
	if i.Time != "" {
		found := false
		for _, p := range i.Properties {
			if p.Name != i.Time {
				continue
			}
			found = true
			if p.PropertyType() != TypeDate {
				problems = append(problems, fmt.Errorf(
					"item %q: time is %q, which is typed %s rather than date",
					i.Name, i.Time, p.PropertyType()))
			}
		}
		if !found {
			problems = append(problems, fmt.Errorf(
				"item %q: time is %q, and no such property is declared", i.Name, i.Time))
		}
	}
	return problems
}

func validateRelations(relations []*Relation, where string) []error {
	var problems []error

	seen := map[string]bool{}
	for _, r := range relations {
		path := where + " relation " + r.Name

		if strings.TrimSpace(r.Name) == "" {
			problems = append(problems, fmt.Errorf("%s: a relation needs a name", where))
		}
		if seen[r.Name] {
			problems = append(problems, fmt.Errorf("%s: declared twice", path))
		}
		seen[r.Name] = true

		if strings.TrimSpace(r.Entity) == "" {
			problems = append(problems, fmt.Errorf("%s: does not say what is at the other end", path))
		}
		if r.Property != "" && !slices.Contains(Sources, r.Property) {
			problems = append(problems, fmt.Errorf("%s: property %q is not one of self.%s",
				path, r.Property, strings.Join(Sources, ", self.")))
		}

		// A classifier reference without a version would let retraining change
		// what the graph records, with nothing in the document to show why.
		for _, ref := range r.Topic {
			if _, err := classify.ParseRef(ref); err != nil {
				problems = append(problems, fmt.Errorf("%s: topic %w", path, err))
			}
		}

		// What the relation says about itself gets the same checks an item's
		// properties get, which it was not getting at all: a duplicate, an
		// empty name and a type nobody defined all validated clean. One named
		// `topic` was worse than clean, because the graph reserves that name
		// and refuses it at run time, so a document that submitted happily
		// aborted the wave on the first page that filled it and threw away
		// every record the crawl had produced.
		problems = append(problems, validateProperties(r.Properties, path)...)
		for _, p := range r.Properties {
			if p.Name == ReservedTopic {
				problems = append(problems, fmt.Errorf(
					"%s property %q: that name is what the graph records a classifier's verdict under. Rename it",
					path, p.Name))
			}
		}
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

		// A dimension has to be one value, or it is not something anybody can
		// group by. It is also how a time-series store is destroyed: every
		// distinct tag value is another series.
		if p.Tag {
			switch p.PropertyType() {
			case TypeObject, TypeList:
				problems = append(problems, fmt.Errorf(
					"%s: is a tag but typed %s, and a dimension has to be one value",
					path, p.PropertyType()))
			case TypeEntity:
				problems = append(problems, fmt.Errorf(
					"%s: is a tag already, because an entity reference is one", path))
			}
		}

		// An entity reference has to say what kind of thing it refers to.
		// Without it there is nothing to resolve against, and the property
		// would quietly behave as a string.
		if Type(p.Type) == TypeEntity && strings.TrimSpace(p.Entity) == "" {
			problems = append(problems, fmt.Errorf(
				"%s: typed entity but does not say which kind, as in entity = \"person\"", path))
		}
		if p.Entity != "" && p.Type != "" && Type(p.Type) != TypeEntity {
			problems = append(problems, fmt.Errorf(
				"%s: names an entity kind but is typed %s, so nothing would resolve it", path, p.Type))
		}
		// A property with children describes an object, whatever it says. The
		// mismatch is an error rather than an inference, because silently
		// changing a declared type is how a document stops meaning what it
		// reads as.
		//
		// An entity is the exception, and refusing it made the entity graph's
		// properties unreachable from any document: an entity reference is a
		// name that refers to something, and its children describe the thing
		// referred to rather than the item. `author.role` is the person's role,
		// which is exactly what the entities step reads and what nothing could
		// express until this stopped refusing it.
		if len(p.Properties) > 0 && p.Type != "" &&
			Type(p.Type) != TypeObject && Type(p.Type) != TypeEntity {
			problems = append(problems, fmt.Errorf(
				"%s: has nested properties but is typed %s, which only object and entity can hold",
				path, p.Type))
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

	for _, p := range j.Plugins() {
		stage := p.Stage()
		where := fmt.Sprintf("%s plugin %q", stage, p.Name)

		key := slot{stage, p.Name}
		if seen[key] {
			problems = append(problems, fmt.Errorf("%s: loaded twice into the same stage", where))
		}
		seen[key] = true

		// A plugin scour has no conventional position for is fine, that is the
		// point of plugins, but it has to say where in the chain it goes
		// because we cannot guess.
		//
		// This checks placement, not existence. Whether anything implements the
		// name is the registry's business, and it is asked later, when the
		// chain is built.
		if _, placed := DefaultOrder(stage, p.Name); !placed && p.Order == 0 {
			problems = append(problems, fmt.Errorf(
				"%s: no conventional order for this name, so it needs an explicit one. Catalogued for %s: %s",
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
// Nothing is added that the document did not ask for, so a chain can be read
// off the job and no reader has to know a list kept somewhere else.
//
// `enabled = false` therefore means precisely what leaving the block out means.
// Both spellings exist because they are written for different reasons: deleting
// a block throws away its configuration, and turning it off keeps it, which is
// what you want when the setting took an afternoon to work out.
func (j *Job) Chain(stage Stage) []*Plugin {
	var from []*Plugin
	switch stage {
	case StageScheduler:
		from = j.Scheduler.plugins()
	case StageDownloader:
		from = j.Downloader.plugins()
	case StageSpider:
		from = j.Spider.plugins()
	}

	out := make([]*Plugin, 0, len(from))
	for _, p := range from {
		if p.IsEnabled() {
			out = append(out, p)
		}
	}

	sort.SliceStable(out, func(a, b int) bool { return out[a].order() < out[b].order() })
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

// Position is where this plugin sits in its chain, after the catalogued
// default has been applied. It is what the chain is ordered by.
func (p *Plugin) Position() int { return p.order() }

// order is the plugin's position, falling back to the catalogued default.
func (p *Plugin) order() int {
	if p.Order != 0 {
		return p.Order
	}
	if def, ok := DefaultOrder(p.Stage(), p.Name); ok {
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
