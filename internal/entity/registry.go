// SPDX-License-Identifier: GPL-3.0-or-later

package entity

import (
	"context"

	"github.com/rangertaha/scour/internal/registry"
)

// The graph is an interface with a registry of implementations, the same shape
// [cache.Store] has and for the same reasons.
//
// # Why the SQLite backend is in this package and a second one would not be
//
// The cache puts every backend in a subpackage so that importing the one you
// use does not drag in the SDK of the one you do not, and S3's is large. That
// argument does not apply to SQLite here: modernc.org/sqlite is already in
// every build of this binary, because the frontier, the records exporter, the
// classifier store and the event log all use it. Splitting it out would move
// code without removing a dependency.
//
// It does apply to anything that brings a driver of its own. A Postgres backend
// belongs in internal/entity/postgres, registering itself the way the cloud
// caches do, so that a build which does not want pgx does not carry it.
//
// # What makes a backend believable
//
// Not this registry, which only says a name maps to a constructor. It is
// [entity/entitytest], which is the promises a graph makes in one place: two
// backends are interchangeable because both pass it, not because both compile.
// The cache learned that the expensive way, and so did the exporters.

// Backend is the name of the implementation this package registers itself as.
const Backend = "sqlite"

// Store is an entity graph, wherever it is kept.
//
// The same surface [entitytest.Graph] measures, so a backend that satisfies
// this is a backend the contract can be run against.
type Store interface {
	// Assert records that something exists, returning its id.
	Assert(ctx context.Context, kind, name string, said Provenance) (string, error)

	// Describe records something known about an entity or a relation.
	Describe(ctx context.Context, subject, name, value string, position int, said Provenance) error

	// Relate records an edge, returning its id so it can be described.
	Relate(ctx context.Context, from, to, kind, topic string, position int, said Provenance) (string, error)

	Get(ctx context.Context, id string) (*Entity, error)
	Find(ctx context.Context, kind, name string) (*Entity, error)
	Kind(ctx context.Context, kind string) ([]*Entity, error)
	Kinds(ctx context.Context) ([]Kind, error)
	RelationKinds(ctx context.Context) ([]RelationKind, error)
	Properties(ctx context.Context, subject string) ([]Property, error)
	Relations(ctx context.Context, id string) ([]Relation, error)
	Related(ctx context.Context, id, kind, topic string) ([]*Entity, error)
	Provenances(ctx context.Context, id string) ([]Provenance, error)

	Candidates(ctx context.Context, kind, name string) ([]Candidate, error)
	Merge(ctx context.Context, from, to, rule string, said Provenance) error
	Unmerge(ctx context.Context, alias string) error
	Aliases(ctx context.Context, id string) ([]Alias, error)

	// Tag records that a subject is about a topic, as a versioned reference:
	// "climate@7". The subject is an entity, a relation or a [KindID].
	Tag(ctx context.Context, subject, topic string, said Provenance) error

	// Topics is what a subject is about, most-asserted first.
	Topics(ctx context.Context, subject string) ([]Property, error)

	// About is every entity the graph says is about a topic.
	About(ctx context.Context, topic string) ([]*Entity, error)

	Retract(ctx context.Context, job string) (int64, error)
	Close() error
}

// Config is what a graph is opened from.
type Config struct {
	// Backend names the implementation. Empty means [Backend].
	Backend string

	// Dir is where a file-backed graph lives. Empty opens an in-memory one,
	// which is what a test wants and what nothing else should use: the value
	// of this store is that it accumulates across jobs and across runs.
	Dir string

	// DSN is how a backend that talks to a server is reached. Ignored by the
	// ones that do not.
	//
	// A string rather than a host and a port and a user, because every driver
	// already has a connection string format and reinventing it here would
	// mean supporting whichever parts of it somebody needed next.
	DSN string
}

// Factory builds a graph from a config.
type Factory = registry.Factory[Config, Store]

var reg = registry.New[Config, Store]("entity backend").Default(Backend)

// Register adds a backend. Implementations call it from an init function, and
// something has to import them: see internal/plugins.
func Register(name string, f Factory) { reg.Register(name, f) }

// Unregister removes a backend, and exists for tests. See
// [registry.Registry.Unregister].
func Unregister(name string) { reg.Unregister(name) }

// New opens the graph a config names.
func New(ctx context.Context, cfg Config) (Store, error) { return reg.New(ctx, cfg.Backend, cfg) }

// Has reports whether a backend is registered.
func Has(name string) bool { return reg.Has(name) }

// Backends are the names this build can open.
func Backends() []string { return reg.Names() }

func init() {
	Register(Backend, func(_ context.Context, cfg Config) (Store, error) { return Open(cfg.Dir) })
}
