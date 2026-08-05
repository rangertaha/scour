// SPDX-License-Identifier: GPL-3.0-or-later

// Package entities is the pipeline step that feeds the entity store.
//
// Import it for its side effect to make the kind available:
//
//	import _ "github.com/rangertaha/scour/internal/pipeline/entities"
//
//	pipeline {
//	  step "entities" "article" {
//	    dir = "./graph"
//	  }
//	}
//
// It is not in internal/pipeline/steps with the other four because it is not
// the same idea. Those work on the records and hand them on; this one reads
// them and writes somewhere else entirely, and it is the only step that holds a
// database open. Putting it there would have made that package's claim, the
// work every job does to every item, quietly untrue.
//
// # What it asserts
//
// A property typed entity is a name that refers to something, so the name is
// asserted as an entity of the kind the property declared. A relation block is
// an edge to something that is usually not on the page at all, so its other end
// comes from `self`: a publisher is the site.
//
// The edges run from each relation's entity to each entity property, named for
// the property. `publisher --author--> person`, which is the shape the store
// exists to answer questions about and which nobody had to name: the relation
// is the property's own name, so two jobs cannot invent two words for it.
//
// An item that declares no relation still asserts its entity properties. The
// nodes are worth having on their own, and dropping a byline because the job
// never said who published it would be this step deciding what counts.
//
// # It never removes a record, or changes one
//
// The records go out exactly as they came in. That is not tidiness, it is the
// guard the entity store needs: known entities must raise confidence and never
// gate extraction. A step that dropped or held back a byline it did not
// recognise would make the store converge on what it already believed, and it
// would look like rising accuracy the whole time.
package entities

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/rangertaha/scour/internal/engine"
	"github.com/rangertaha/scour/internal/entity"
	"github.com/rangertaha/scour/internal/pipeline"
	"github.com/rangertaha/scour/internal/record"
	"github.com/rangertaha/scour/internal/urls"
)

func init() {
	pipeline.Register("entities", newStep)
}

// Config is what an entities step's block may set.
type Config struct {
	// Backend names which implementation to open. Empty means the default,
	// which is the file-backed one.
	//
	// A job says which kind of store it wants and never how to build one: the
	// registry is what turns a name into an implementation, so a backend
	// somebody adds later is reachable from a document without this package
	// knowing it exists.
	Backend string `hcl:"backend,optional"`

	// Dir is where the store lives. Empty opens the in-memory one, which is
	// what a test wants and what nothing else should use: the value of this
	// store is that it accumulates across jobs and across runs, so a job that
	// wants that has to say where.
	Dir string `hcl:"dir,optional"`

	// DSN is how a backend that talks to a server is reached. Ignored by the
	// ones that do not.
	DSN string `hcl:"dsn,optional"`

	// Merge applies the merges the store proposes. Off by default.
	//
	// Off because a crawl is the worst place to decide two people are one: it
	// would make thousands of those decisions unattended, and a wrong one
	// corrupts the graph in a way nobody notices. On, it makes only what
	// [entity.Store.Candidates] is willing to propose, which is the
	// unambiguous initial-and-surname rule and nothing fuzzy, and it records
	// each merge against this job so that a job which turns out to have been
	// merging badly is one delete like any other.
	Merge bool `hcl:"merge,optional"`
}

// step asserts one item's entities.
type step struct {
	cfg   pipeline.Config
	item  *engine.Item
	store entity.Store
	merge bool
}

func newStep(ctx context.Context, cfg pipeline.Config) (pipeline.Step, error) {
	var c Config
	if err := cfg.Body.Decode(&c); err != nil {
		return nil, err
	}

	// Named for the item it asserts, because the shape is where the entity
	// kinds and the relations are written and there is nothing to assert
	// without it. Refused at build time rather than doing nothing at run time,
	// which is the failure that taught this repository to check its plugins.
	if cfg.Item == nil {
		return nil, errors.New(
			`is not named for an item, as in step "entities" "article", so there is no shape saying what to assert`)
	}

	store, err := entity.New(ctx, entity.Config{Backend: c.Backend, Dir: c.Dir, DSN: c.DSN})
	if err != nil {
		return nil, err
	}
	return &step{cfg: cfg, item: cfg.Item, store: store, merge: c.Merge}, nil
}

// Run asserts what each record refers to and returns the records untouched.
func (s *step) Run(ctx context.Context, records []*record.Record) ([]*record.Record, error) {
	for _, r := range records {
		if !mine(s.cfg, r) {
			continue
		}
		if err := s.assert(ctx, r); err != nil {
			return nil, err
		}
	}
	return records, nil
}

// Close gives the store back.
func (s *step) Close() error { return s.store.Close() }

// tail is one end of an edge: an entity a relation block named, and the topics
// that relation says to record the edge under.
type tail struct {
	id       string
	topics   []string
	declared *engine.Relation
}

func (s *step) assert(ctx context.Context, r *record.Record) error {
	// The page's own fetch time, not now. Two runs over one cached corpus have
	// to produce the same store, and a timestamp that moved between them would
	// make that untestable, which is the same argument [record.Record] makes
	// about its own field.
	said := entity.Provenance{Job: s.cfg.Job, URL: r.URL, Spec: r.Spec, At: r.Fetched}

	tails := make([]tail, 0, len(s.item.Relations))
	for _, declared := range s.item.Relations {
		name := other(declared, r)
		if name == "" {
			continue
		}
		id, err := s.store.Assert(ctx, declared.Entity, name, said)
		if err != nil {
			return err
		}
		tails = append(tails, tail{id: id, topics: declared.Topic, declared: declared})
	}

	for at, p := range s.item.Properties {
		// Top level only. An entity reference nested inside another property is
		// a field of that property rather than an edge of the item, and giving
		// it one would put a dotted name into a vocabulary the whole graph
		// shares.
		if engine.Type(p.Type) != engine.TypeEntity {
			continue
		}
		name := strings.TrimSpace(r.Values[p.Name])
		if name == "" {
			continue
		}

		id, err := s.store.Assert(ctx, p.Entity, name, said)
		if err != nil {
			return err
		}
		if err := s.resolve(ctx, p.Entity, name, said); err != nil {
			return err
		}

		// What the page said about the thing itself. A property nested inside
		// an entity property describes the entity rather than the item: an
		// article's `author.role` is the person's role, not the article's, and
		// the record already carries it under that dotted name because that is
		// how nested values are flattened.
		//
		// Nothing did this before. The graph had entities with a kind and a
		// name and nothing else, while the page that named them had said more.
		if err := s.describe(ctx, id, p.Properties, p.Name, r, said); err != nil {
			return err
		}

		for _, from := range tails {
			// No topic is one edge under no topic, rather than none: the edge
			// happened whether or not anybody scored the page.
			topics := from.topics
			if len(topics) == 0 {
				topics = []string{""}
			}
			for _, topic := range topics {
				// The position is the property's own, because the edge is
				// named for the property and a shape that lists the author
				// before the publisher means it.
				edge, err := s.store.Relate(ctx, from.id, id, p.Name, topic, at, said)
				if err != nil {
					return err
				}
				// The edge carries what the relation block declared about it,
				// which is a statement about the relationship rather than
				// about either end: when it started, what role it was in.
				if err := s.describe(ctx, edge, from.declared.Properties, from.declared.Name, r, said); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// describe records what the page said about a subject, from the properties
// nested inside the block that named it.
//
// One function for an entity and for an edge, because the two are the same
// statement about different subjects, and the position each is given is where
// it was declared: ordering is part of what a shape says.
func (s *step) describe(ctx context.Context, subject string, declared []*engine.Property, prefix string, r *record.Record, said entity.Provenance) error {
	if subject == "" {
		return nil
	}
	for at, n := range declared {
		value := strings.TrimSpace(r.Values[prefix+"."+n.Name])
		if value == "" {
			continue
		}
		if err := s.store.Describe(ctx, subject, n.Name, value, at, said); err != nil {
			return err
		}
	}
	return nil
}

// resolve applies the merges the store proposes, when the job asked for it.
//
// Only ever the unambiguous ones, because the store proposes nothing else. The
// merge is recorded against this job, so a job that turns out to have been
// merging badly is one delete like any other.
func (s *step) resolve(ctx context.Context, kind, name string, said entity.Provenance) error {
	if !s.merge {
		return nil
	}

	proposed, err := s.store.Candidates(ctx, kind, name)
	if err != nil {
		return err
	}
	for _, one := range proposed {
		if err := s.store.Merge(ctx, one.From, one.To, one.Rule, said); err != nil {
			return err
		}
	}
	return nil
}

// other is what is at the far end of a relation.
//
// A field of `self` when the block names one, because a publisher is not on the
// page: it is the site. An extracted value of the relation's own name
// otherwise, which is the case where the page does say who it is.
func other(declared *engine.Relation, r *record.Record) string {
	switch declared.Property {
	case "":
		return strings.TrimSpace(r.Values[declared.Name])
	case engine.SourceURL:
		return r.URL
	case engine.SourceHost:
		return urls.Host(r.URL)
	case engine.SourceDomain:
		return urls.Domain(urls.Host(r.URL))
	case engine.SourcePath:
		parsed, err := url.Parse(r.URL)
		if err != nil {
			return ""
		}
		return parsed.Path
	case engine.SourceFetchedAt:
		return r.Fetched.UTC().Format(time.RFC3339)
	}
	// Unreachable from a validated document, which restricts `property` to the
	// fields of self. An empty name asserts nothing rather than asserting
	// something wrong.
	return ""
}

// mine reports whether this step should touch a record, the same rule the other
// step kinds follow: a step is named for the item it works on.
func mine(cfg pipeline.Config, r *record.Record) bool {
	if cfg.Item == nil {
		return true
	}
	return r.Item == cfg.Name
}
