// SPDX-License-Identifier: GPL-3.0-or-later

package train

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/rangertaha/scour/internal/score"
	"github.com/rangertaha/scour/internal/score/hmm"
	"github.com/rangertaha/scour/internal/store"
	"github.com/rangertaha/scour/internal/wom"
)

// ChainResult reports what fitting the crawl chain learned.
type ChainResult struct {
	Paths        int
	Pages        int
	Roles        map[string]int
	Observations int
}

// trainChain fits the page-role chain to the crawl's own paths, decodes a role
// for every page, and stores both.
//
// The roles are what the next crawl scores against: a link found on a page the
// chain calls a hub is credited for where it leads, which is how a page with
// no records on it stops being a dead end.
func (t *Trainer) trainChain(ctx context.Context, entity *store.Entity) (*ChainResult, error) {
	paths, err := t.store.Paths(ctx, entity.ID)
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return &ChainResult{Roles: map[string]int{}}, nil
	}

	chain, err := t.loadChain(ctx, entity)
	if err != nil {
		return nil, err
	}

	observed := make([][]hmm.Observation, 0, len(paths))
	for _, p := range paths {
		observed = append(observed, observationsOf(p))
	}
	if err := chain.Fit(observed); err != nil {
		return nil, fmt.Errorf("fit crawl chain: %w", err)
	}

	// Decode after fitting, so the roles reflect the chain that will be used.
	roles := map[string]string{}
	counts := map[string]int{}
	for i, p := range paths {
		decoded := chain.Decode(observed[i])
		for j, url := range p.URLs {
			if j >= len(decoded) {
				break
			}
			// A page can appear in several paths; the first decoding wins,
			// which is the one reached by the shortest route.
			if _, taken := roles[url]; taken {
				continue
			}
			roles[url] = decoded[j].String()
			counts[decoded[j].String()]++
		}
	}

	if err := t.store.SetRoles(ctx, entity.ID, roles); err != nil {
		return nil, err
	}

	encoded, err := json.Marshal(chain)
	if err != nil {
		return nil, fmt.Errorf("encode crawl chain: %w", err)
	}
	if err := t.store.SaveChain(ctx, &entity.ID, store.ChainCrawl, encoded, chain.Observations); err != nil {
		return nil, err
	}

	return &ChainResult{
		Paths:        len(paths),
		Pages:        len(roles),
		Roles:        counts,
		Observations: chain.Observations,
	}, nil
}

func (t *Trainer) loadChain(ctx context.Context, entity *store.Entity) (*hmm.Chain, error) {
	stored, err := t.store.LoadChain(ctx, &entity.ID, store.ChainCrawl)
	if err != nil {
		return nil, err
	}
	return hmm.Parse(stored)
}

// observationsOf turns one crawl path into the symbols the chain reads.
func observationsOf(p store.Path) []hmm.Observation {
	out := make([]hmm.Observation, 0, len(p.URLs))
	for i := range p.URLs {
		switch {
		case p.Statuses[i] >= 400 || p.Statuses[i] == 0:
			out = append(out, hmm.Failed)
		case p.Matches[i] > 0:
			out = append(out, hmm.Records)
		case p.Links[i] > 0:
			out = append(out, hmm.Links)
		default:
			out = append(out, hmm.Barren)
		}
	}
	return out
}

// ChainScorer wraps a base scorer with the entity's crawl chain, when one has
// been fitted. The bool reports whether the chain is in play.
func ChainScorer(ctx context.Context, s *store.Store, entity *store.Entity, base score.Scorer) (score.Scorer, bool, error) {
	stored, err := s.LoadChain(ctx, &entity.ID, store.ChainCrawl)
	if err != nil {
		return base, false, err
	}
	if len(stored) == 0 {
		return base, false, nil
	}

	chain, err := hmm.Parse(stored)
	if err != nil {
		return base, false, err
	}

	storedRoles, err := s.Roles(ctx, entity.ID)
	if err != nil {
		return base, false, err
	}
	if len(storedRoles) == 0 {
		// A chain with nothing decoded to hang it on cannot credit anything,
		// and would only add noise.
		return base, false, nil
	}

	roles := make(map[string]hmm.Role, len(storedRoles))
	for url, name := range storedRoles {
		if r, ok := hmm.ParseRole(name); ok {
			roles[url] = r
		}
	}
	return hmm.NewScorer(base, chain, roles), true, nil
}

// loadFieldChain returns the shared field-order chain, or nil when none has
// been fitted yet.
//
// Unlike locators, a chain over field order is a property of how records are
// written rather than of any one site's markup, so it is stored once with no
// entity attached and seeded into every induction.
func (t *Trainer) loadFieldChain(ctx context.Context) (*wom.ChainPrior, error) {
	stored, err := t.store.LoadChain(ctx, nil, store.ChainExtract)
	if err != nil || len(stored) == 0 {
		return nil, err
	}

	var prior wom.ChainPrior
	if err := json.Unmarshal(stored, &prior); err != nil {
		// A chain we cannot read is not worth failing a training run over; the
		// built-in prior is a working fallback.
		return nil, nil //nolint:nilerr // deliberate: degrade to the default
	}
	return &prior, nil
}

// saveFieldChain stores the chain induction fitted, for the next entity to
// start from.
func (t *Trainer) saveFieldChain(ctx context.Context, model *wom.Model) error {
	if model.Chain == nil {
		return nil
	}
	encoded, err := json.Marshal(model.Chain)
	if err != nil {
		return fmt.Errorf("encode field chain: %w", err)
	}
	return t.store.SaveChain(ctx, nil, store.ChainExtract, encoded, 0)
}
