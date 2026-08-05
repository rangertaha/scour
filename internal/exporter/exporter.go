// SPDX-License-Identifier: GPL-3.0-or-later

// Package exporter writes records out.
//
// An exporter is named `exporter "<format>" "<item>"`, one per item, and one
// that names an item the job does not extract is refused when the document is
// validated. Silently writing nothing is the failure nobody notices until they
// go looking for the output.
//
// # Exports are copies
//
// Not the record of truth. The records database holds what a crawl found; an
// export is a rendering of it for whatever is going to read it next, and it can
// be deleted and produced again. That is what lets a job add a format without
// re-crawling, and why an exporter that fails is worth reporting but not worth
// stopping a crawl for.
//
// # Streaming and archiving are the same thing
//
// There is no archive component and no event component. Saving to storage and
// publishing to a stream are both deliveries, so both are exporters, and an
// item declared once can be written as a file for whatever reads the archive
// and published as a measurement for whatever is listening now.
package exporter

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/hashicorp/hcl/v2/gohcl"

	"github.com/rangertaha/scour/internal/engine"
	"github.com/rangertaha/scour/internal/record"
	"github.com/rangertaha/scour/internal/registry"
)

// Exporter writes records somewhere.
//
// Write may be called many times as a crawl produces records, and Close is what
// finishes the file: a JSON array has to be closed, a CSV's header has to have
// been written before its first row, and a stream has to be flushed.
type Exporter interface {
	// Write delivers records. It is called with whatever a run has ready, so
	// an implementation that batches should batch here.
	Write(ctx context.Context, records ...*record.Record) error

	// Close finishes the delivery and releases what it holds.
	Close() error
}

// Config is what an exporter is built from.
type Config struct {
	// Format is the first label: json, csv, parquet.
	Format string

	// Item is the second: which shape it writes.
	Item string

	// Job is the job it belongs to.
	Job string

	// Shape is the item's declaration, which is what a measurement's tags and
	// fields are derived from.
	Shape *engine.Item

	// Body is everything else in the block, undecoded.
	Body Body

	// Out overrides the file, so a test or a `--stdout` run can read what was
	// written without going near a disk.
	Out io.WriteCloser
}

// Body is an exporter's own configuration, decoded by the exporter.
type Body interface {
	Decode(into any) error
}

// Factory builds one exporter from its configuration.
type Factory = registry.Factory[Config, Exporter]

var reg = registry.New[Config, Exporter]("exporter")

// Register adds a format, from an init function in its own package.
func Register(format string, f Factory) { reg.Register(format, f) }

// Registered lists what this build can write, sorted.
func Registered() []string { return reg.Names() }

// Has reports whether a format is registered.
func Has(format string) bool { return reg.Has(format) }

// Set is every exporter a job declared, built.
type Set struct {
	job     string
	writers []named
}

type named struct {
	address  string
	item     string
	exporter Exporter
}

// New builds a job's exporters.
//
// All of them or none: a job that asked for two formats and got one has
// produced an export somebody will believe is complete.
func New(ctx context.Context, job *engine.Job, out map[string]io.WriteCloser) (*Set, error) {
	if job == nil {
		return nil, fmt.Errorf("exporter: no job")
	}

	set := &Set{job: job.Name}

	var missing, failed []string
	for _, declared := range job.Exporters {
		address := declared.Address()
		if !reg.Has(declared.Format) {
			missing = append(missing, declared.Format)
			continue
		}

		built, err := reg.New(ctx, declared.Format, Config{
			Format: declared.Format,
			Item:   declared.Item,
			Job:    job.Name,
			Shape:  itemNamed(job, declared.Item),
			Body:   body{declared},
			Out:    out[address],
		})
		if err != nil {
			failed = append(failed, fmt.Sprintf("%s (%v)", address, err))
			continue
		}
		set.writers = append(set.writers, named{address: address, item: declared.Item, exporter: built})
	}

	if len(missing) > 0 || len(failed) > 0 {
		set.Close()
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("job %q: nothing writes %s. This build writes %s",
			job.Name, quoted(missing), strings.Join(Registered(), ", "))
	}
	if len(failed) > 0 {
		return nil, fmt.Errorf("job %q: %s", job.Name, strings.Join(failed, "; "))
	}
	return set, nil
}

// Write hands each record to the exporters for its item.
//
// A record whose item nobody exports is not an error: a job may extract
// something for a pipeline step to use and never write it out.
func (s *Set) Write(ctx context.Context, records ...*record.Record) error {
	var problems []error

	for _, writer := range s.writers {
		var mine []*record.Record
		for _, r := range records {
			if r.Item == writer.item {
				mine = append(mine, r)
			}
		}
		if len(mine) == 0 {
			continue
		}
		if err := writer.exporter.Write(ctx, mine...); err != nil {
			problems = append(problems, fmt.Errorf("%s: %w", writer.address, err))
		}
	}
	return joinAll(problems)
}

// Close finishes every export, and reports every failure rather than the first:
// a JSON file that would not close must not stop a CSV from being flushed.
func (s *Set) Close() error {
	var problems []error
	for _, writer := range s.writers {
		if err := writer.exporter.Close(); err != nil {
			problems = append(problems, fmt.Errorf("%s: %w", writer.address, err))
		}
	}
	s.writers = nil
	return joinAll(problems)
}

// Addresses lists what this set writes, which is what a run reports at the end.
func (s *Set) Addresses() []string {
	out := make([]string, 0, len(s.writers))
	for _, writer := range s.writers {
		out = append(out, writer.address)
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

// body adapts a declared exporter to [Body], so an exporter decodes its own
// configuration against its own schema and a field it does not know comes back
// with a line and a column.
type body struct{ exporter *engine.Exporter }

func (b body) Decode(into any) error {
	if b.exporter.Config == nil {
		return nil
	}
	if diags := gohcl.DecodeBody(b.exporter.Config, nil, into); diags.HasErrors() {
		return diags
	}
	return nil
}

func joinAll(problems []error) error {
	if len(problems) == 0 {
		return nil
	}
	return errors.Join(problems...)
}

func quoted(names []string) string {
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
