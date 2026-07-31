// SPDX-License-Identifier: GPL-3.0-or-later

package cache

import (
	"context"
	"fmt"
	"sort"
	"sync"
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

// Factory builds a store.
type Factory func(Config) (Store, error)

var (
	mu       sync.RWMutex
	registry = map[string]Factory{}
)

// Register adds an implementation, from init, so a driver is a blank import.
func Register(name string, f Factory) {
	mu.Lock()
	defer mu.Unlock()
	registry[name] = f
}

// New builds a registered store. An empty name is the local one, which is what
// an unconfigured scour uses and what everything else is compared against.
func New(name string, cfg Config) (Store, error) {
	if name == "" {
		name = driverLocal
	}
	mu.RLock()
	f, ok := registry[name]
	mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown cache driver %q, have %v", name, Names())
	}
	return f(cfg)
}

// Names lists the registered drivers.
func Names() []string {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]string, 0, len(registry))
	for name := range registry {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Has reports whether a driver is registered, so a bad name fails where it is
// configured rather than on the first page fetched.
func Has(name string) bool {
	mu.RLock()
	defer mu.RUnlock()
	_, ok := registry[name]
	return ok
}
