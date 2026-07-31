// SPDX-License-Identifier: GPL-3.0-or-later

package train

import (
	"context"
	"fmt"
	"strings"

	"github.com/rangertaha/scour/internal/classify"
	"github.com/rangertaha/scour/internal/config"

	"github.com/rangertaha/scour/internal/score"
	"github.com/rangertaha/scour/internal/score/bayes"
	"github.com/rangertaha/scour/internal/store"
)

// ScoreResult reports what training the URL scorer learned.
type ScoreResult struct {
	Positive int
	Negative int
	Accuracy float64
	Path     string
	Top      []bayes.TokenWeight
	Worst    []bayes.TokenWeight
}

// trainScorer fits the URL scorer from what the crawl actually found.
//
// The labels come from the crawl itself: a page that yielded a record is a
// positive, a page that was fetched and yielded nothing is a negative. Records
// a person marked invalid turn their page into a negative even if extraction
// found something there, because the point of the label is that it should not
// have.
func (t *Trainer) trainScorer(ctx context.Context, entity *store.Entity) (*ScoreResult, error) {
	obs, pos, neg, err := t.observations(ctx, entity)
	if err != nil {
		return nil, err
	}

	model := bayes.New()
	model.Entity = entity.Name
	model.Seed(seedWords(entity))

	if len(obs) > 0 {
		if err := model.Train(obs, t.cfg.Model.Holdout); err != nil {
			return nil, fmt.Errorf("train scorer: %w", err)
		}
	}

	path := t.cfg.ScoreModelPath(entity.Name)
	if err := model.Save(path); err != nil {
		return nil, err
	}

	top, worst := model.Top(5)
	return &ScoreResult{
		Positive: pos,
		Negative: neg,
		Accuracy: model.Accuracy,
		Path:     path,
		Top:      top,
		Worst:    worst,
	}, nil
}

// observations turns the crawl's outcomes into training examples.
func (t *Trainer) observations(ctx context.Context, entity *store.Entity) ([]bayes.Observation, int, int, error) {
	rows, err := t.store.FetchedURLs(ctx, entity.ID)
	if err != nil {
		return nil, 0, 0, err
	}

	invalid, err := t.invalidURLs(ctx, entity)
	if err != nil {
		return nil, 0, 0, err
	}

	// A classifier, when configured, reads the pages and says what they are.
	// Without one the only evidence is whether extraction already succeeded,
	// which on a first crawl it cannot have.
	var categories map[string]classify.Category
	classifier, err := t.classifierFor()
	if err != nil {
		return nil, 0, 0, err
	}
	if classifier != nil {
		categories, t.classified = t.classifyPages(ctx, entity, rows, classifier)
	}

	var obs []bayes.Observation
	var pos, neg int
	for _, row := range rows {
		relevant := row.Matches > 0

		// A page a person marked wrong is wrong whatever anything else thinks:
		// a human label is the only evidence here that is not a guess.
		switch {
		case invalid[row.URL]:
			relevant = false
		case !relevant:
			if category, ok := categories[row.URL]; ok && category.Relevant() {
				relevant = true
				if t.classified != nil {
					t.classified.Rescued++
				}
			}
		}

		obs = append(obs, bayes.Observation{
			Features: score.Features{URL: row.URL, Depth: row.Depth},
			Relevant: relevant,
		})
		if relevant {
			pos++
		} else {
			neg++
		}
	}
	return obs, pos, neg, nil
}

// invalidURLs is the set of pages whose every record was marked invalid. A
// page with one bad record and three good ones is still worth crawling.
func (t *Trainer) invalidURLs(ctx context.Context, entity *store.Entity) (map[string]bool, error) {
	rows, _, err := t.store.SearchRecords(ctx, entity.ID, store.RecordQuery{})
	if err != nil {
		return nil, err
	}

	total := map[string]int{}
	bad := map[string]int{}
	for _, r := range rows {
		if r.URL == "" {
			continue
		}
		total[r.URL]++
		if r.Label == store.Invalid {
			bad[r.URL]++
		}
	}

	out := map[string]bool{}
	for url, n := range bad {
		if n == total[url] {
			out[url] = true
		}
	}
	return out, nil
}

// seedWords is what the model knows before it has seen anything: the entity's
// name, its aliases, and the words in its property names and examples.
func seedWords(entity *store.Entity) []string {
	var out []string
	add := func(s string) {
		for _, word := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
			return !('a' <= r && r <= 'z' || '0' <= r && r <= '9')
		}) {
			if len(word) >= 2 {
				out = append(out, word)
			}
		}
	}

	add(entity.Name)
	for _, a := range entity.Aliases {
		add(a.Word)
	}
	for _, p := range entity.Properties {
		add(p.Name)
		add(p.Example)
	}
	return out
}

// Scorer returns the scorer to crawl an entity with: its trained model when
// there is one, and a model seeded from its own words when there is not. The
// bool reports which of the two it is, so the crawl can say so.
//
// The cold start matters. A first crawl has no outcomes to learn from, so
// without the seed every link would score identically and the crawl would be
// breadth-first over the whole site.
func Scorer(cfg config.Config, entity *store.Entity) (score.Scorer, bool, error) {
	name := cfg.Model.Scorer
	if name != "" && !score.Has(name) {
		return nil, false, fmt.Errorf("unknown scorer %q in [model], have %s",
			name, strings.Join(score.Names(), ", "))
	}

	scorer, err := score.New(name, score.Config{
		Entity:  entity.Name,
		Seed:    seedWords(entity),
		Path:    cfg.ScoreModelPath(entity.Name),
		Vectors: cfg.Model.Vectors,
	})
	if err != nil {
		return nil, false, err
	}

	// A scorer that cannot say whether it was trained is treated as untrained,
	// so a crawl never claims a ranking it cannot back up.
	trained := false
	if t, ok := scorer.(score.Trained); ok {
		trained = t.Trained()
	}
	return scorer, trained, nil
}
