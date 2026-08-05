// SPDX-License-Identifier: GPL-3.0-or-later

package event

import (
	"context"

	"github.com/rangertaha/scour/internal/registry"
)

// The event log is an interface with a registry of implementations, the same
// shape [entity.Store] and [cache.Store] have.
//
// Symmetry rather than taste: two stores that a service document treats the
// same way, one of which could be swapped and one of which could not, is the
// kind of difference nobody notices until they need the second one. The reasons
// for the shape are given at internal/entity's registry, including why the
// SQLite implementation is in this package and a driver-heavy one would not be.
//
// A time series is the case where a second backend is most likely to be wanted.
// Entities are bounded by definition, so SQLite holds them comfortably for a
// long time; events are not bounded at all, and the store that is right for a
// laptop is not the one that is right for a price every minute for a year.

// Backend is the name of the implementation this package registers itself as.
const Backend = "sqlite"

// Store is an event log, wherever it is kept.
//
// The same surface [eventtest] measures, so a backend that satisfies this is a
// backend the contract can be run against.
type Store interface {
	// Put records an event, or updates the one it is the same as, returning
	// its id. The create and the update of CRUD are one call because the
	// identity is derived from the observation rather than allocated.
	Put(ctx context.Context, e Event) (string, error)

	Get(ctx context.Context, id string) (*Event, error)
	List(ctx context.Context, q Query) ([]*Event, error)
	Delete(ctx context.Context, id string) error

	// Retract removes everything one job contributed, and says how much.
	Retract(ctx context.Context, job string) (int64, error)

	// Names is every measurement in the store, with how many points each has.
	Names(ctx context.Context) ([]Series, error)

	Close() error
}

// Config is what a log is opened from.
type Config struct {
	// Backend names the implementation. Empty means [Backend].
	Backend string

	// Dir is where a file-backed log lives. Empty opens an in-memory one,
	// which is what a test wants and what nothing else should use: an event
	// log that disappears when the process does is one every writer believes
	// it wrote to.
	Dir string

	// DSN is how a backend that talks to a server is reached. Ignored by the
	// ones that do not.
	DSN string
}

// Factory builds a log from a config.
type Factory = registry.Factory[Config, Store]

var reg = registry.New[Config, Store]("event backend").Default(Backend)

// Register adds a backend. Implementations call it from an init function, and
// something has to import them: see internal/plugins.
func Register(name string, f Factory) { reg.Register(name, f) }

// Unregister removes a backend, and exists for tests. See
// [registry.Registry.Unregister].
func Unregister(name string) { reg.Unregister(name) }

// New opens the log a config names.
func New(ctx context.Context, cfg Config) (Store, error) { return reg.New(ctx, cfg.Backend, cfg) }

// Has reports whether a backend is registered.
func Has(name string) bool { return reg.Has(name) }

// Backends are the names this build can open.
func Backends() []string { return reg.Names() }

func init() {
	Register(Backend, func(_ context.Context, cfg Config) (Store, error) { return Open(cfg.Dir) })
}
