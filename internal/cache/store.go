// SPDX-License-Identifier: GPL-3.0-or-later

package cache

import (
	"context"

	"github.com/rangertaha/scour/internal/registry"
)

// Store is where fetched bodies are kept.
//
// The database records that a page was fetched and what its key is; the body
// itself lives here. That split is why this is worth making pluggable: on one
// machine a directory is exactly right, and across several it is exactly wrong,
// because a crawler writing to its own disk leaves the trainer reading an empty
// cache with a database full of keys pointing at nothing.
//
// Implementations must be safe for concurrent use: several crawl threads write
// at once.
type Store interface {
	// Put stores a body and returns the key it can be read back by.
	Put(ctx context.Context, rawURL string, body []byte) (string, error)
	// Get returns a stored body.
	Get(ctx context.Context, rawURL string) ([]byte, error)
	// Has reports whether a body is stored, without fetching it.
	Has(ctx context.Context, rawURL string) bool
	// Stats reports how much is held.
	Stats(ctx context.Context) (Stats, error)
}

// Config is what a store is built from.
//
// One URL rather than a field per backend, because the backends do not agree on
// what they need and a union of their settings would be mostly empty whichever
// one was chosen. A location is a location:
//
//	/var/lib/scour/pages          a directory
//	file:///var/lib/scour/pages   the same
//	s3://bucket/prefix            an S3 bucket, or anything speaking S3
//	gs://bucket/prefix            Google Cloud Storage
type Config struct {
	// URL says where bodies go, in the driver's own dialect.
	URL string
	// Options carries whatever a driver needs beyond the location: a region, an
	// endpoint, a credentials profile.
	Options map[string]string
}

// reg holds the implementations. See internal/registry for the shape every
// extension point in scour shares, and for how to add one.
var reg = registry.New[Config, Store]("cache driver").Default(driverLocal)

// Register adds an implementation, from init.
func Register(name string, f registry.Factory[Config, Store]) { reg.Register(name, f) }

// New builds a registered implementation.
func New(name string, cfg Config) (Store, error) { return reg.New(name, cfg) }

// Names lists what is registered.
func Names() []string { return reg.Names() }

// Has reports whether a name is registered.
func Has(name string) bool { return reg.Has(name) }
