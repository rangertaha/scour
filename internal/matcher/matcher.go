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
	"fmt"
	"sort"
	"sync"

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
}

// Factory builds a matcher.
type Factory func(Config) (wom.Matcher, error)

var (
	mu       sync.RWMutex
	registry = map[string]Factory{}
)

// Register adds an implementation, from init.
func Register(name string, f Factory) {
	mu.Lock()
	defer mu.Unlock()
	registry[name] = f
}

// New builds a registered matcher. An empty name is the heuristic, which is
// what an unconfigured scour uses and what everything else is compared to.
func New(name string, cfg Config) (wom.Matcher, error) {
	if name == "" {
		name = "heuristic"
	}

	mu.RLock()
	f, ok := registry[name]
	mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown matcher %q, have %v", name, Names())
	}
	return f(cfg)
}

// Names lists the registered matchers.
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

// Has reports whether a matcher is registered.
func Has(name string) bool {
	mu.RLock()
	defer mu.RUnlock()
	_, ok := registry[name]
	return ok
}

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
