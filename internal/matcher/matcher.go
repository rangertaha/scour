// SPDX-License-Identifier: GPL-3.0-or-later

// Package matcher decides how strongly a node satisfies a property.
//
// It is the seam where scour's intelligence is chosen. wom locates fields by
// scoring candidate nodes; swapping the scorer swaps what the engine
// understands, without touching graph construction, locator synthesis, or a
// line of crawl code. The heuristic implementation needs no network, no key
// and no training data, and everything richer is measured against it.
package matcher

import (
	"context"
	"sync"

	"github.com/rangertaha/scour/internal/registry"
	"github.com/rangertaha/scour/internal/score"

	"github.com/rangertaha/scour/internal/ai"
	"github.com/rangertaha/scour/internal/wom"
)

// Config is what a matcher is built from.
type Config struct {
	// Provider is the model to consult, for matchers that consult one.
	Provider ai.Provider
	// Cache remembers judgements between runs. A nil cache means every
	// judgement is made fresh, which is correct but expensive.
	Cache Cache
	// Budget caps how many model calls one run may make. Zero means the
	// implementation's default; a negative budget means no limit.
	Budget int
	// Base is the matcher consulted first, and the one a richer matcher falls
	// back to. A nil base means the heuristic.
	Base wom.Matcher
	// Floor and Ceiling bound the band in which the base matcher counts as
	// undecided, and so the only band a model is consulted in. Both zero means
	// the measured defaults.
	Floor, Ceiling float64
	// Vectors is where a matcher that compares meanings loads its word vectors
	// from. It is the same file the embed scorer reads, and naming it once in
	// [model] is what keeps a link and a node judged against the same
	// vocabulary.
	Vectors string
	// Weight is how far a second opinion may move the base score, for matchers
	// that adjust it rather than replace it. Zero means the implementation's
	// default.
	Weight float64
}

// Ranks is the sort of node this registry's implementations score.
//
// A matcher is the document node scorer: it ranks an element, attribute or text
// against a property, where internal/score ranks a URL against an item. The two
// are the same kind of extension over different graphs, and naming that here is
// what stops them looking unrelated because one was called a matcher.
const Ranks = score.KindDocument

// reg holds the implementations. See internal/registry for the shape every
// extension point in scour shares, and for how to add one.
var reg = registry.New[Config, wom.Matcher]("matcher").Default("heuristic")

// Register adds an implementation, from init.
func Register(name string, f registry.Factory[Config, wom.Matcher]) { reg.Register(name, f) }

// New builds a registered implementation.
func New(name string, cfg Config) (wom.Matcher, error) { return reg.New(name, cfg) }

// Names lists what is registered.
func Names() []string { return reg.Names() }

// Has reports whether a name is registered.
func Has(name string) bool { return reg.Has(name) }

// Cache remembers what a model said about a node, keyed by a hash of the
// question rather than by identity.
//
// This is what makes a model affordable during induction. A site repeats its
// markup on every page, so the same question is asked hundreds of times; the
// distinct set is small even when the corpus is not. Persisting it means a
// retrain costs nothing it has already paid for.
type Cache interface {
	// Judgement returns a cached score.
	Judgement(ctx context.Context, key string) (float64, bool, error)
	// Remember stores one.
	Remember(ctx context.Context, key, model string, score float64) error
}

// MemoryCache is a Cache that lives for one run. It is the default when
// nothing durable is configured, and is what the tests use.
type MemoryCache struct {
	mu     sync.RWMutex
	scores map[string]float64
}

// Judgement implements [Cache].
func (m *MemoryCache) Judgement(_ context.Context, key string) (float64, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	score, ok := m.scores[key]
	return score, ok, nil
}

// Remember implements [Cache].
func (m *MemoryCache) Remember(_ context.Context, key, _ string, score float64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.scores == nil {
		m.scores = map[string]float64{}
	}
	m.scores[key] = score
	return nil
}
