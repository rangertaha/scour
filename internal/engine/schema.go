// SPDX-License-Identifier: GPL-3.0-or-later

// Package engine reads what a client submitted: the jobs, what each one is
// looking for, and how each stage should behave.
//
// # A job carries everything
//
// Scope, schema, stages, plugins and exporters all live inside the job block.
// Nothing is inherited from the server, so two jobs submitted to the same
// cluster can cache to different buckets, extract different shapes and run
// different plugins, and a job resubmitted next month does what it did today.
//
// # A stage is a block, and its plugins are inside it
//
// Each stage is a block holding what that stage is and what has been added to
// it:
//
//	downloader {
//	  robots     = true          # what the downloader is
//	  user_agent = "scour"
//
//	  plugin "cache" {           # what has been added to it
//	    backend = "s3"
//	  }
//	}
//
// That division is the point of the shape. An attribute is behaviour the stage
// always has: no meaningful "off", no meaningful position in an order, and
// nowhere else it could have been written. A nested plugin is something you
// added: it reorders, it turns off, and somebody else can write it.
//
// It also stops a setting drifting away from whatever enforces it. A max_body
// kept somewhere else would be a number the downloader might or might not be
// reading, and the way you would find out is by downloading four gigabytes.
package engine

import (
	"fmt"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/hashicorp/hcl/v2/hclparse"
)

// Document is one submitted file: every job in it.
//
// Jobs are accepted or refused together, so a document whose third job names a
// plugin that does not exist does not leave the first two running.
type Document struct {
	Jobs []*Job `hcl:"job,block" json:"jobs"`
}

// Job is one crawl.
type Job struct {
	Name string `hcl:"name,label" json:"name"`

	// Scope: where the crawl may go.
	Domains  []string `hcl:"domains,optional" json:"domains,omitempty"`
	Start    []string `hcl:"start,optional" json:"start,omitempty"`
	Included []string `hcl:"included,optional" json:"included,omitempty"`
	Excluded []string `hcl:"excluded,optional" json:"excluded,omitempty"`

	// Items are the shapes this job extracts.
	Items []*Item `hcl:"item,block" json:"items,omitempty"`

	// The stages. Every one is optional: a stage nobody configured is a stage
	// running on its defaults, which is the common case.
	Scheduler  *Scheduler  `hcl:"scheduler,block" json:"scheduler,omitempty"`
	Downloader *Downloader `hcl:"downloader,block" json:"downloader,omitempty"`
	Spider     *Spider     `hcl:"spider,block" json:"spider,omitempty"`
	Pipeline   *Pipeline   `hcl:"pipeline,block" json:"pipeline,omitempty"`

	// Monitoring is what the job reports about itself.
	Monitoring *Monitoring `hcl:"monitoring,block" json:"monitoring,omitempty"`

	// Mutation is what to do when this job is resubmitted under a name that is
	// already running.
	Mutation *Mutation `hcl:"mutation,block" json:"mutation,omitempty"`

	// Exporters each write one item.
	Exporters []*Exporter `hcl:"exporter,block" json:"exporters,omitempty"`
}

// Scheduler owns the frontier: what is queued, in what order, and how hard one
// host may be leaned on.
//
// It has no external attribute, and that is a decision rather than an omission.
// Two schedulers handing out the same host cannot honour a crawl delay between
// them, so politeness forces one decision point per host and this stage cannot
// be somebody else's. Leaving the attribute out makes writing one a parse error
// with a line and a column, which is a better way to learn that than a rule
// buried in a validator.
//
// Politeness lives here rather than in the downloader because pacing is decided
// when work is handed out, not when it is fetched, and because a rate is per
// host and shared: two jobs crawling one site must not each get their own
// allowance.
type Scheduler struct {
	// Policy is the order the frontier is drained in.
	Policy string `hcl:"policy,optional" json:"policy,omitempty"`

	// Rate is the least time between two requests to one host.
	Rate string `hcl:"rate,optional" json:"rate,omitempty"`
	// Concurrency is how many requests may be in flight to one host.
	Concurrency int `hcl:"concurrency,optional" json:"concurrency,omitempty"`

	// The budget. Pages and time default to no limit because a budget is a
	// thing a person chooses; depth does not, because an unbounded depth is
	// not a crawl but the whole web.
	MaxDepth int    `hcl:"max_depth,optional" json:"max_depth,omitempty"`
	MaxPages int    `hcl:"max_pages,optional" json:"max_pages,omitempty"`
	MaxTime  string `hcl:"max_time,optional" json:"max_time,omitempty"`

	Plugins []*Plugin `hcl:"plugin,block" json:"plugins,omitempty"`
}

// Downloader turns a request into a response.
//
// The attributes are what a downloader always does. None of them is a plugin,
// because none has a meaningful "off" or a meaningful position in an order, and
// because a crawl that quietly stopped obeying robots.txt would be harming
// somebody else's server rather than its own. A thing whose absence hurts a
// third party must not be opt-in.
type Downloader struct {
	// External hands this stage to somebody else over the bus.
	External bool `hcl:"external,optional" json:"external,omitempty"`
	// ExternalTimeout is how long that somebody has to answer.
	ExternalTimeout string `hcl:"external_timeout,optional" json:"external_timeout,omitempty"`

	// Robots obeys robots.txt. A pointer, because false and unset are the same
	// bool and unset has to mean on.
	Robots *bool `hcl:"robots,optional" json:"robots,omitempty"`
	// UserAgent identifies the crawler to whoever looks it up.
	UserAgent string `hcl:"user_agent,optional" json:"user_agent,omitempty"`
	// Timeout is how long one request may take.
	Timeout string `hcl:"timeout,optional" json:"timeout,omitempty"`
	// MaxBody refuses a body larger than this, before it is downloaded.
	MaxBody int64 `hcl:"max_body,optional" json:"max_body,omitempty"`

	Plugins []*Plugin `hcl:"plugin,block" json:"plugins,omitempty"`
}

// Spider turns a response into items and new requests.
type Spider struct {
	External        bool   `hcl:"external,optional" json:"external,omitempty"`
	ExternalTimeout string `hcl:"external_timeout,optional" json:"external_timeout,omitempty"`

	Plugins []*Plugin `hcl:"plugin,block" json:"plugins,omitempty"`
}

// Pipeline processes extracted items, as a graph.
//
// Its steps are not plugins and have no order. A step runs once the steps it
// requires have run, and giving it a number as well would be two ways of saying
// the same thing, which would disagree.
type Pipeline struct {
	External        bool   `hcl:"external,optional" json:"external,omitempty"`
	ExternalTimeout string `hcl:"external_timeout,optional" json:"external_timeout,omitempty"`

	Steps []*Step `hcl:"step,block" json:"steps,omitempty"`
}

// Item is a shape the job is looking for: an article, a product, a person.
type Item struct {
	Name        string `hcl:"name,label" json:"name"`
	Type        string `hcl:"type,optional" json:"type,omitempty"`
	Description string `hcl:"description,optional" json:"description,omitempty"`

	Properties []*Property `hcl:"property,block" json:"properties,omitempty"`
}

// Property is one field of an item.
//
// Everything about how to find it is here and is teachable: the names it goes
// by, the patterns its value matches, the locators that address it, and the
// transforms that turn what was found into what was asked for. Nothing about a
// property is compiled in.
type Property struct {
	Name string `hcl:"name,label" json:"name"`

	Type        string `hcl:"type,optional" json:"type,omitempty"`
	Description string `hcl:"description,optional" json:"description,omitempty"`
	Required    bool   `hcl:"required,optional" json:"required,omitempty"`

	// Aliases are the other names this property goes by, which is what lets a
	// locator be induced from a page that calls it something else.
	Aliases []string `hcl:"aliases,optional" json:"aliases,omitempty"`

	// Regexes are patterns the value matches.
	Regexes []string `hcl:"regexes,optional" json:"regexes,omitempty"`

	// Transforms are registered functions applied to what was found, in order.
	Transforms []string `hcl:"transforms,optional" json:"transforms,omitempty"`

	// XPath and CSS are locators taught rather than induced.
	XPath []string `hcl:"xpath,optional" json:"xpath,omitempty"`
	CSS   []string `hcl:"css,optional" json:"css,omitempty"`

	// Properties nest, so a property can be an object with fields of its own.
	Properties []*Property `hcl:"property,block" json:"properties,omitempty"`
}

// Monitoring is what a job reports while it runs.
type Monitoring struct {
	Metrics bool   `hcl:"metrics,optional" json:"metrics,omitempty"`
	Logging bool   `hcl:"logging,optional" json:"logging,omitempty"`
	Level   string `hcl:"level,optional" json:"level,omitempty"`
}

// Plugin is one middleware, added to whichever stage block holds it.
//
//	downloader {
//	  plugin "cache" {
//	    order   = 900
//	    backend = "s3"
//	  }
//	}
//
// It carries no stage label: the block it is written in says which stage it
// belongs to, so the two cannot disagree.
type Plugin struct {
	Name string `hcl:"name,label" json:"name"`

	// Order is the position in its stage's chain, lowest first. Explicit
	// rather than positional, because a chain whose order depends on where a
	// block was written changes when somebody tidies the file.
	Order int `hcl:"order,optional" json:"order,omitempty"`

	// Enabled turns a plugin off without deleting its configuration, which is
	// what you want when the setting took an afternoon to work out.
	Enabled *bool `hcl:"enabled,optional" json:"enabled,omitempty"`

	// Config is everything else in the block, left undecoded.
	//
	// A plugin's own fields belong to the plugin, and this package must not
	// need to know them: that is what makes a plugin something somebody else
	// can write. It is decoded against the plugin's own schema when the plugin
	// is built, which is also when a bad field gets an error with a line
	// number on it.
	Config hcl.Body `hcl:",remain" json:"-"`

	// stage is the block this was written in, filled after decoding.
	stage Stage
	// raw is the block's source text, kept so two submissions can be compared.
	raw string
}

// Stage is which chain this plugin belongs to.
func (p *Plugin) Stage() Stage { return p.stage }

// Step is one node in the item processing graph.
//
//	pipeline {
//	  step "rank" "article" {
//	    requires = [clean.article]
//	  }
//	}
//
// The first label is the implementation, the second names this instance of it.
// Together they are the address other steps depend on.
type Step struct {
	Kind string `hcl:"kind,label" json:"kind"`
	Name string `hcl:"name,label" json:"name"`

	// Requires are the steps that must finish before this one, written as
	// kind.name. Captured as an expression rather than as strings because
	// clean.article is a reference, and reading it as one is what lets a
	// misspelled dependency be reported with a line number.
	Requires hcl.Expression `hcl:"requires,optional" json:"-"`

	// Inline is a script written in the document; Script is a path to one.
	Inline string `hcl:"inline,optional" json:"inline,omitempty"`
	Script string `hcl:"script,optional" json:"script,omitempty"`

	// Config is everything else, left undecoded for the implementation.
	Config hcl.Body `hcl:",remain" json:"-"`

	// requires holds the parsed dependencies, filled by [Job.resolveSteps].
	requires []string
	// raw is the block's source text, kept so two submissions can be compared.
	raw string
}

// Address is how other steps refer to this one.
func (s *Step) Address() string { return s.Kind + "." + s.Name }

// Requirements are the addresses this step depends on.
func (s *Step) Requirements() []string { return s.requires }

// Exporter writes one item out.
//
//	exporter "json" "article" {
//	  dir = "./out"
//	}
//
// The first label is the format, the second is the item it exports. Exporters
// are per item rather than per job: a job that extracts articles and comments
// usually wants them in different files, and an exporter that received both
// would have to be told which was which anyway.
type Exporter struct {
	Format string `hcl:"format,label" json:"format"`
	Item   string `hcl:"item,label" json:"item"`

	Config hcl.Body `hcl:",remain" json:"-"`

	raw string
}

// Address is how one exporter is named in an error.
func (e *Exporter) Address() string { return e.Format + "." + e.Item }

// Parse reads a document.
//
// Nothing is validated beyond what HCL itself can tell: that is
// [Document.Validate], and it is separate because reading a file and accepting
// a submission are different decisions.
func Parse(src []byte, filename string) (*Document, error) {
	parser := hclparse.NewParser()

	parsed, diags := parser.ParseHCL(src, filename)
	if diags.HasErrors() {
		return nil, diagError(diags)
	}

	var doc Document
	if diags := gohcl.DecodeBody(parsed.Body, evalContext(), &doc); diags.HasErrors() {
		return nil, diagError(diags)
	}

	for _, job := range doc.Jobs {
		job.tagStages()
		job.snapshot(src)
		if err := job.resolveSteps(); err != nil {
			return nil, err
		}
	}
	return &doc, nil
}

// tagStages records which block each plugin was written in, so nothing later
// has to ask where it came from.
func (j *Job) tagStages() {
	for _, p := range j.Scheduler.plugins() {
		p.stage = StageScheduler
	}
	for _, p := range j.Downloader.plugins() {
		p.stage = StageDownloader
	}
	for _, p := range j.Spider.plugins() {
		p.stage = StageSpider
	}
}

// resolveSteps reads each step's requires expression into addresses.
func (j *Job) resolveSteps() error {
	for _, s := range j.Steps() {
		reqs, err := traversalsOf(s.Requires)
		if err != nil {
			return fmt.Errorf("job %q: step %q: requires: %w", j.Name, s.Address(), err)
		}
		s.requires = reqs
	}
	return nil
}

// An absent stage block behaves like an empty one, so no caller has to check
// for nil.

func (s *Scheduler) plugins() []*Plugin {
	if s == nil {
		return nil
	}
	return s.Plugins
}

func (d *Downloader) plugins() []*Plugin {
	if d == nil {
		return nil
	}
	return d.Plugins
}

func (s *Spider) plugins() []*Plugin {
	if s == nil {
		return nil
	}
	return s.Plugins
}

func (p *Pipeline) steps() []*Step {
	if p == nil {
		return nil
	}
	return p.Steps
}

// Steps are this job's pipeline steps.
func (j *Job) Steps() []*Step { return j.Pipeline.steps() }

// Plugins are every plugin in the job, across all stages.
func (j *Job) Plugins() []*Plugin {
	var out []*Plugin
	out = append(out, j.Scheduler.plugins()...)
	out = append(out, j.Downloader.plugins()...)
	out = append(out, j.Spider.plugins()...)
	return out
}

// traversalsOf reads [a.b, c.d] into ["a.b", "c.d"].
func traversalsOf(expr hcl.Expression) ([]string, error) {
	if expr == nil {
		return nil, nil
	}
	// An absent attribute still decodes to an expression, which evaluates to
	// null rather than to a list.
	if v, diags := expr.Value(nil); !diags.HasErrors() && v.IsNull() {
		return nil, nil
	}

	items, diags := hcl.ExprList(expr)
	if diags.HasErrors() {
		return nil, fmt.Errorf("must be a list of references such as [clean.article]: %w", diags)
	}

	out := make([]string, 0, len(items))
	for _, item := range items {
		traversal, diags := hcl.AbsTraversalForExpr(item)
		if diags.HasErrors() {
			return nil, fmt.Errorf("must be a reference such as clean.article: %w", diags)
		}
		address, err := addressOf(traversal)
		if err != nil {
			return nil, err
		}
		out = append(out, address)
	}
	return out, nil
}

func addressOf(t hcl.Traversal) (string, error) {
	if len(t) != 2 {
		return "", fmt.Errorf("%q is not a kind.name reference", t.RootName())
	}
	attr, ok := t[1].(hcl.TraverseAttr)
	if !ok {
		return "", fmt.Errorf("%q is not a kind.name reference", t.RootName())
	}
	return t.RootName() + "." + attr.Name, nil
}

// diagError turns HCL's diagnostics into one error, keeping all of them: HCL
// reports every problem with a line and a column, and returning only the first
// would throw away the best thing about it.
func diagError(diags hcl.Diagnostics) error {
	return fmt.Errorf("config: %w", diags)
}
