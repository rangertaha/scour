// SPDX-License-Identifier: GPL-3.0-or-later

// Package registry is the extension point every pluggable part of scour is
// built on.
//
// scour has several kinds of thing you can add to it: a way of caching, of
// fetching, of scheduling, of extracting, of exporting. They differ in what
// they produce and in what they are built from, and in nothing else. Written
// out separately, each would carry its own copy of the same forty lines, which
// means as many places to keep in step and another to write by hand every time
// a new kind of extension appears.
//
// One generic registry serves them all. A package declares what it registers
// and gets Register, New, Names and Has for free:
//
//	var reg = registry.New[Config, Store]("cache backend").Default("local")
//
//	func Register(name string, f registry.Factory[Config, Store]) {
//		reg.Register(name, f)
//	}
//
// The wrappers are kept because they are what callers import: scour's own
// packages say cache.New rather than reaching through a registry value, and an
// implementation registers itself from init without knowing this package
// exists.
//
// # What is registered, and what is merely conventional
//
// A registry answers whether something exists. It is deliberately not the place
// that says where a middleware sits in its chain or what its defaults are:
// those are conventions about configuration, and a name that has a conventional
// order is not the same claim as a name something implements. Keeping them
// apart is what stops a catalogue of intentions from validating as a set of
// working parts.
package registry

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Factory builds one implementation from its configuration.
type Factory[C, T any] func(ctx context.Context, cfg C) (T, error)

// Registry holds the implementations of one kind of extension.
//
// The zero value is not usable; call [New].
type Registry[C, T any] struct {
	// kind names what this registers, for error messages: "cache backend",
	// "downloader middleware". It is a noun phrase, because it is read as one.
	kind string

	mu        sync.RWMutex
	factories map[string]Factory[C, T]

	// fallback is the name used when a caller asks for "". Empty means a
	// caller must name one.
	fallback string
}

// New returns an empty registry for one kind of extension.
func New[C, T any](kind string) *Registry[C, T] {
	if kind == "" {
		panic("registry: created without a kind")
	}
	return &Registry[C, T]{
		kind:      kind,
		factories: map[string]Factory[C, T]{},
	}
}

// Default sets the name used when a caller asks for the empty string. It
// returns the registry so it can be chained onto [New].
func (r *Registry[C, T]) Default(name string) *Registry[C, T] {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fallback = name
	return r
}

// Register adds an implementation, from an init function in its own package.
//
// Registration is not import-order sensitive: nothing reads the table until a
// name is looked up. An implementation is therefore chosen by importing its
// package, which is what keeps a build that never wanted S3 from carrying its
// SDK.
//
// Registering the same name twice panics. It is a programming mistake rather
// than a runtime condition: two implementations answering to one name means
// which one you get depends on import order, and that is not a thing to
// discover in production.
func (r *Registry[C, T]) Register(name string, f Factory[C, T]) {
	if name == "" {
		panic(r.kind + ": registered without a name")
	}
	if f == nil {
		panic(r.kind + " " + name + ": registered with no factory")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, dup := r.factories[name]; dup {
		panic(r.kind + " " + name + ": registered twice")
	}
	r.factories[name] = f
}

// New builds a registered implementation. An empty name means the default.
func (r *Registry[C, T]) New(ctx context.Context, name string, cfg C) (T, error) {
	var zero T

	r.mu.RLock()
	if name == "" {
		name = r.fallback
	}
	f, ok := r.factories[name]
	r.mu.RUnlock()

	if name == "" {
		return zero, fmt.Errorf("%s: none named, and there is no default. Have %s", r.kind, r.list())
	}
	if !ok {
		return zero, fmt.Errorf("%s %q: not registered. Have %s", r.kind, name, r.list())
	}

	built, err := f(ctx, cfg)
	if err != nil {
		return zero, fmt.Errorf("%s %q: %w", r.kind, name, err)
	}
	return built, nil
}

// Has reports whether a name is registered. An empty name asks about the
// default.
func (r *Registry[C, T]) Has(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if name == "" {
		name = r.fallback
	}
	if name == "" {
		return false
	}
	_, ok := r.factories[name]
	return ok
}

// Names lists what is registered, sorted.
//
// Sorted rather than in registration order, because registration order is
// import order, and a list that changes when somebody adds an import is a list
// nobody can diff.
func (r *Registry[C, T]) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]string, 0, len(r.factories))
	for name := range r.factories {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Kind is what this registry holds, as a noun phrase.
func (r *Registry[C, T]) Kind() string { return r.kind }

// list renders what is available for an error message.
func (r *Registry[C, T]) list() string {
	out := make([]string, 0, len(r.factories))
	for name := range r.factories {
		out = append(out, name)
	}
	if len(out) == 0 {
		// Almost always a missing side-effect import rather than a typo, so
		// the message says so instead of listing nothing.
		return "none: import the package that registers one"
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}
