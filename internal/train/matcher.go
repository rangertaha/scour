// SPDX-License-Identifier: GPL-3.0-or-later

package train

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/rangertaha/scour/internal/ai"
	"github.com/rangertaha/scour/internal/config"
	"github.com/rangertaha/scour/internal/matcher"
	"github.com/rangertaha/scour/internal/store"
	"github.com/rangertaha/scour/internal/wom"
)

// matcherFor builds the matcher configured in [model], along with the stats
// hook that reports what it cost.
//
// A matcher that cannot be built is an error rather than a silent fallback: an
// operator who configured a model and got the heuristic would have no way to
// tell, and would conclude the model was useless rather than absent.
func (t *Trainer) matcherFor() (wom.Matcher, func() *MatcherResult, error) {
	name := t.cfg.Model.Matcher
	if name == "" || name == "heuristic" {
		return nil, nil, nil
	}
	if !matcher.Has(name) {
		return nil, nil, fmt.Errorf("unknown matcher %q in [model], have %v", name, matcher.Names())
	}

	cfg := matcher.Config{
		Cache:  judgementCache{t.store},
		Budget: t.cfg.Model.Budget,
	}

	// Only build a provider if one is configured. A matcher that needs one and
	// has none fails in matcher.New with a message naming the config it wants.
	if block, ok := t.aiBlock(); ok {
		provider, err := ai.New(block)
		if err != nil {
			return nil, nil, err
		}
		cfg.Provider = provider
	}

	m, err := matcher.New(name, cfg)
	if err != nil {
		return nil, nil, err
	}

	report := func() *MatcherResult {
		reporter, ok := m.(interface{ Stats() matcher.Stats })
		if !ok {
			return &MatcherResult{Name: name}
		}
		s := reporter.Stats()
		return &MatcherResult{
			Name:      name,
			Undecided: s.Misses,
			Calls:     s.Calls,
			Cached:    s.Hits,
			Errors:    s.Errors,
		}
	}
	return m, report, nil
}

// judgementCache adapts the store to [matcher.Cache].
//
// The names differ because they read differently in their own packages: a
// store has many things to remember, a cache has one. The adapter is where
// that difference belongs, rather than bending either name to the other.
type judgementCache struct{ store *store.Store }

func (c judgementCache) Judgement(ctx context.Context, key string) (float64, bool, error) {
	return c.store.Judgement(ctx, key)
}

func (c judgementCache) Remember(ctx context.Context, key, model string, score float64) error {
	return c.store.RememberJudgement(ctx, key, model, score)
}

// aiBlock finds the [[ai]] block the model config names, falling back to the
// only one when there is exactly one, since naming it would be ceremony.
func (t *Trainer) aiBlock() (ai.Config, bool) {
	want := t.cfg.Model.AI
	blocks := t.cfg.AI

	switch {
	case len(blocks) == 0:
		return ai.Config{}, false
	case want == "" && len(blocks) == 1:
		return providerConfig(blocks[0]), true
	case want == "":
		slog.Warn("several [[ai]] blocks and no ai= in [model], using the first", "name", blocks[0].Name)
		return providerConfig(blocks[0]), true
	}

	for _, block := range blocks {
		if block.Name == want {
			return providerConfig(block), true
		}
	}
	slog.Warn("no [[ai]] block by that name", "want", want)
	return ai.Config{}, false
}

func providerConfig(block config.AI) ai.Config {
	return ai.Config{
		Name:      block.Name,
		Provider:  block.Provider,
		Model:     block.Model,
		Effort:    block.Effort,
		Endpoint:  block.Endpoint,
		Path:      block.Path,
		APIKeyEnv: block.APIKeyEnv,
		Timeout:   block.Timeout.Duration(),
	}
}

// MatcherResult reports what the matcher cost during induction.
type MatcherResult struct {
	// Name is the matcher that ran.
	Name string
	// Undecided is how many candidates the heuristic could not settle, which
	// is the only population a model was ever asked about.
	Undecided int
	// Calls is how many of those actually reached the model.
	Calls int
	// Cached is how many were answered from a previous run or a repeated
	// template.
	Cached int
	// Errors is how many calls failed and fell back to the heuristic.
	Errors int
}
