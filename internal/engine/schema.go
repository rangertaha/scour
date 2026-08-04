// SPDX-License-Identifier: GPL-3.0-or-later

// Package engine reads what a client submitted: the jobs, what each one is
// looking for, and the plugins it runs to find it.
//
// # A job carries everything
//
// Scope, schema, engine settings, plugins, pipelines and exporters all live
// inside the job block. Nothing is inherited from the server, so two jobs
// submitted to the same cluster can cache to different buckets, extract
// different shapes and run different plugins, and a job resubmitted next month
// does what it did today.
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
	Jobs []*Job `hcl:"job,block"`
}

// Job is one crawl.
type Job struct {
	Name string `hcl:"name,label"`

	// Scope: where the crawl may go.
	Domains  []string `hcl:"domains,optional"`
	Start    []string `hcl:"start,optional"`
	Included []string `hcl:"included,optional"`
	Excluded []string `hcl:"excluded,optional"`

	// Items are the shapes this job extracts.
	Items []*Item `hcl:"item,block"`

	// Engine is how the crawl behaves. Optional: everything in it has a
	// default.
	Engine *Engine `hcl:"engine,block"`

	// Monitoring is what the job reports about itself.
	Monitoring *Monitoring `hcl:"monitoring,block"`

	// Plugins extend a stage. Ordered by their order attribute, not by where
	// they appear, so a document can be rearranged without changing what it
	// does.
	Plugins []*Plugin `hcl:"plugin,block"`

	// Pipelines process extracted items, as a dependency graph.
	Pipelines []*Pipeline `hcl:"pipelines,block"`

	// Exporters each receive a copy of every item.
	Exporters []*Exporter `hcl:"exporter,block"`
}

// Item is a shape the job is looking for: an article, a product, a person.
type Item struct {
	Name        string `hcl:"name,label"`
	Type        string `hcl:"type,optional"`
	Description string `hcl:"description,optional"`

	Properties []*Property `hcl:"property,block"`
}

// Property is one field of an item.
//
// Everything about how to find it is here and is teachable: the names it goes
// by, the patterns its value matches, the locators that address it, and the
// transforms that turn what was found into what was asked for. Nothing about a
// property is compiled in.
type Property struct {
	Name string `hcl:"name,label"`

	Type        string `hcl:"type,optional"`
	Description string `hcl:"description,optional"`
	Required    bool   `hcl:"required,optional"`

	// Aliases are the other names this property goes by, which is what lets a
	// locator be induced from a page that calls it something else.
	Aliases []string `hcl:"aliases,optional"`

	// Regexes are patterns the value matches.
	Regexes []string `hcl:"regexes,optional"`

	// Transforms are registered functions applied to what was found, in order.
	Transforms []string `hcl:"transforms,optional"`

	// XPath and CSS are locators taught rather than induced. A property with
	// neither is one the model is expected to find on its own.
	XPath []string `hcl:"xpath,optional"`
	CSS   []string `hcl:"css,optional"`

	// Properties nest, so a property can be an object with fields of its own.
	Properties []*Property `hcl:"property,block"`
}

// Engine is how the crawl behaves, as opposed to what it is looking for.
//
// Caching is not here. It is a downloader plugin, because a cache is something
// that sits between a request and the network, which is exactly what a
// downloader middleware is. Putting it here would have made it the one part of
// the fetch path that could not be reordered, replaced or turned off.
type Engine struct {
	Limits     *Limits     `hcl:"limits,block"`
	Politeness *Politeness `hcl:"politeness,block"`
	Components *Components `hcl:"components,block"`
}

// Monitoring is what a job reports while it runs.
type Monitoring struct {
	Metrics bool   `hcl:"metrics,optional"`
	Logging bool   `hcl:"logging,optional"`
	Level   string `hcl:"level,optional"`
}

// Plugin loads one middleware into one stage.
//
//	plugin "downloader" "cache" {
//	  order   = 1
//	  backend = "s3"
//	  bucket  = "pages"
//	}
//
// The first label is the stage, the second is the implementation.
type Plugin struct {
	Stage string `hcl:"stage,label"`
	Name  string `hcl:"name,label"`

	// Order is the position in its stage's chain, lowest first. Explicit
	// rather than positional because a chain whose order depends on where a
	// block was written is a chain that changes when somebody tidies the file.
	Order int `hcl:"order,optional"`

	// Enabled turns a plugin off without deleting its configuration, which is
	// what you want at three in the morning.
	Enabled *bool `hcl:"enabled,optional"`

	// raw is the block's source text, kept so two submissions can be compared.
	raw string

	// Config is everything else in the block, left undecoded.
	//
	// A plugin's own fields belong to the plugin, and this package must not
	// need to know them: that is what makes a plugin something somebody else
	// can write. It is decoded against the plugin's own schema when the plugin
	// is built, which is also when a bad field gets an error with a line
	// number on it.
	Config hcl.Body `hcl:",remain"`
}

// Pipeline is one step in the item processing graph.
//
//	pipelines "rank" "article" {
//	  requires = [clean.article]
//	}
//
// The first label is the implementation, the second names this instance of it.
// Together they are the address other steps depend on.
type Pipeline struct {
	Kind string `hcl:"kind,label"`
	Name string `hcl:"name,label"`

	// Requires are the steps that must finish before this one, written as
	// kind.name. Captured as an expression rather than as strings because
	// clean.article is a reference, and reading it as one is what lets a
	// misspelled dependency be reported with a line number.
	Requires hcl.Expression `hcl:"requires,optional"`

	// Inline is a script written in the document; Script is a path to one.
	// Which of them a step uses, and whether it uses either, is the
	// implementation's business.
	Inline string `hcl:"inline,optional"`
	Script string `hcl:"script,optional"`

	// Config is everything else, left undecoded for the implementation.
	Config hcl.Body `hcl:",remain"`

	// requires holds the parsed dependencies, filled by [Job.resolve].
	requires []string

	// raw is the block's source text, kept so two submissions can be compared.
	raw string
}

// Address is how other steps refer to this one.
func (p *Pipeline) Address() string { return p.Kind + "." + p.Name }

// Requirements are the addresses this step depends on, available after the
// document has been resolved.
func (p *Pipeline) Requirements() []string { return p.requires }

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
//
// Every exporter named for an item receives a copy of every one of those
// items, so writing the same item to json and to sqlite is two blocks.
type Exporter struct {
	Format string `hcl:"format,label"`
	Item   string `hcl:"item,label"`

	// Config is the exporter's own fields, left undecoded for the same reason
	// a plugin's are.
	Config hcl.Body `hcl:",remain"`

	// raw is the block's source text, kept so two submissions can be compared.
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

	// Keep the source text of every block that carries an undecoded body, so
	// two submissions of one job can be compared. A plugin's configuration is
	// opaque here on purpose, which means the only honest way to tell whether
	// it changed is to compare what was written.
	for _, job := range doc.Jobs {
		job.snapshot(src)
	}

	// Dependencies are read here rather than during validation, because a
	// requires list that is not a list of references is a syntax problem and
	// deserves to be reported as one, with the position HCL still has.
	for _, job := range doc.Jobs {
		if err := job.resolve(); err != nil {
			return nil, err
		}
	}
	return &doc, nil
}

// resolve reads each pipeline's requires expression into addresses.
func (j *Job) resolve() error {
	for _, p := range j.Pipelines {
		reqs, err := traversalsOf(p.Requires)
		if err != nil {
			return fmt.Errorf("job %q: pipelines %q: requires: %w", j.Name, p.Address(), err)
		}
		p.requires = reqs
	}
	return nil
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
