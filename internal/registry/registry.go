// SPDX-License-Identifier: GPL-3.0-or-later

// Package registry is the one extension point every pluggable part of scour is
// built on.
//
// scour has several kinds of thing you can add to it: a way of fetching, of
// scoring, of matching, of classifying, of storing, of exporting. They differ
// in what they produce and in what they are built from, and in nothing else.
// Each used to carry its own copy of the same forty lines, which meant six
// places to keep in step and a seventh to write by hand every time a new kind
// of extension appeared.
//
// One generic registry serves them all. A package declares what it registers
// and gets Register, New, Names and Has for free:
//
//	var reg = registry.New[Config, Scorer]("scorer")
//
//	func Register(name string, f registry.Factory[Config, Scorer]) {
//		reg.Register(name, f)
//	}
//
// The wrappers are kept because they are what callers import: scour's own
// packages say score.New rather than reaching through a registry value, and an
// implementation registers itself from init without knowing this package
// exists.
//
// # Adding a kind of extension
//
// Declare the interface the extension satisfies and the config it is built
// from, make a registry for the pair, and export the four wrappers. Nothing
// else is required.
//
// What exists is discoverable from the code: every extension point calls
// registry.New, so grepping for it lists them, which a document cannot go
// stale against.
//
// # Adding an implementation
//
// Register it from an init function in its own package, and have whatever
// selects it import that package for its side effects. Registration is not
// import order sensitive: nothing reads the registry until a name is looked up.
package registry

import (
	"fmt"
	"sort"
	"sync"
)

// Factory builds one implementation from its configuration.
type Factory[C, T any] func(C) (T, error)

// Registry holds the implementations of one kind of extension.
//
// The zero value is not usable; call New, which names the kind so that an
// unknown name reports which registry it was not found in.
type Registry[C, T any] struct {
	kind string

	mu   sync.RWMutex
	made map[string]Factory[C, T]

	// fallback is the name used when a caller asks for "", which is how an
	// unconfigured scour gets a working default without every caller
	// repeating which default that is.
	fallback string
}

// New returns an empty registry for a kind of extension. The kind is the word
// that appears in errors: "unknown scorer", "unknown transport".
func New[C, T any](kind string) *Registry[C, T] {
	return &Registry[C, T]{kind: kind, made: map[string]Factory[C, T]{}}
}

// Default names the implementation an empty name resolves to. It returns the
// registry so it can be chained onto New.
func (r *Registry[C, T]) Default(name string) *Registry[C, T] {
	r.fallback = name
	return r
}

// Register adds an implementation under a name, replacing any previous one.
//
// Called from init, where returning an error has nowhere to go, so the last
// registration of a name wins rather than panicking. Two implementations
// claiming one name is a build that imported both, which is a decision
// somebody made on purpose.
func (r *Registry[C, T]) Register(name string, f Factory[C, T]) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.made[name] = f
}

// New builds a registered implementation.
//
// An empty name resolves to the registry's default when it has one, so callers
// can pass configuration through unexamined.
func (r *Registry[C, T]) New(name string, cfg C) (T, error) {
	if name == "" {
		name = r.fallback
	}

	r.mu.RLock()
	f, ok := r.made[name]
	r.mu.RUnlock()
	if !ok {
		var zero T
		return zero, fmt.Errorf("unknown %s %q, have %v", r.kind, name, r.Names())
	}
	return f(cfg)
}

// Names lists what is registered, sorted, so help text and error messages do
// not depend on map order.
func (r *Registry[C, T]) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.made))
	for name := range r.made {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Has reports whether a name is registered. An empty name asks after the
// default, which is registered like anything else.
func (r *Registry[C, T]) Has(name string) bool {
	if name == "" {
		name = r.fallback
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.made[name]
	return ok
}

// Kind is the word this registry uses for what it holds.
func (r *Registry[C, T]) Kind() string { return r.kind }
