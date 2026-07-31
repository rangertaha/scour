// SPDX-License-Identifier: GPL-3.0-or-later

// Package train induces an entity's extraction rules from its cached pages,
// and applies them.
//
// Induction is expensive and happens once; extraction is cheap and happens per
// page. wom draws that line for us, and this package is where scour's entities
// and labels meet it.
package train

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/rangertaha/scour/internal/cache"
	"github.com/rangertaha/scour/internal/config"
	"github.com/rangertaha/scour/internal/content"
	"github.com/rangertaha/scour/internal/parse"
	"github.com/rangertaha/scour/internal/store"
	"github.com/rangertaha/scour/internal/wom"
)

// ErrNoProperties is returned when an entity has nothing to look for.
var ErrNoProperties = errors.New("entity has no properties")

// Trainer induces and applies models.
type Trainer struct {
	cfg   config.Config
	store *store.Store
	cache *cache.Cache

	// classified is what the page classifier found during this run, kept here
	// because it is produced while labelling and reported with the result.
	classified *ClassifyResult
}

// New returns a trainer.
func New(cfg config.Config, s *store.Store, c *cache.Cache) *Trainer {
	return &Trainer{cfg: cfg, store: s, cache: c}
}

// Result reports what a training run did.
type Result struct {
	Pages     int
	Bytes     int64
	Skipped   int
	Rules     int
	Records   int
	Corrected int
	ModelPath string
	Score     *ScoreResult
	Chain     *ChainResult
	// Matcher is set only when a matcher other than the heuristic ran.
	Matcher *MatcherResult
	// Classify is set only when a page classifier ran.
	Classify *ClassifyResult
	Elapsed  time.Duration
}

// Options configures a training run.
type Options struct {
	// Limit caps how many cached pages induction reads.
	Limit int
	// Types restricts which formats are loaded.
	Types *content.Set
	// NoChain skips fitting the crawl chain, which leaves scoring to judge
	// each URL on its own tokens. It exists so the chain's contribution can be
	// measured rather than assumed.
	NoChain bool
}

// Run induces an entity's model from its cached pages, saves it, stores the
// rules it produced, and extracts records with it.
//
// Induction and extraction happen together because a model nobody has applied
// tells you nothing about whether it works. The records it produces are what
// `scour search` then shows and what labelling corrects.
func (t *Trainer) Run(ctx context.Context, entity *store.Entity, opts Options) (*Result, error) {
	start := time.Now()

	props := schemaOf(entity)
	if len(props) == 0 {
		return nil, fmt.Errorf("%w: scour add %s -p <prop> -e <example>", ErrNoProperties, entity.Name)
	}

	// The field-order chain describes how people write records rather than how
	// one site marks them up, so it transfers: every entity's induction is
	// seeded with what every previous one learned.
	prior, err := t.loadFieldChain(ctx)
	if err != nil {
		return nil, err
	}
	var womOpts []wom.Option
	if prior != nil {
		womOpts = append(womOpts, wom.WithChainPrior(prior))
	}

	// The matcher is the one seam where scour's understanding of a page can be
	// replaced. It is fixed when the graph is built, so it has to be decided
	// here rather than at the call site.
	m, matcherStats, err := t.matcherFor()
	if err != nil {
		return nil, err
	}
	if m != nil {
		womOpts = append(womOpts, wom.WithMatcher(m))
	}

	loaded, err := parse.Load(ctx, t.store, t.cache, entity.ID, parse.Options{
		Limit: opts.Limit,
		Types: opts.Types,
		WOM:   womOpts,
	})
	if err != nil {
		return nil, err
	}

	model, err := loaded.Graph.Model(props...)
	if err != nil {
		return nil, fmt.Errorf("induce model for %s: %w", entity.Name, err)
	}

	if err := t.saveFieldChain(ctx, model); err != nil {
		return nil, err
	}

	// Corrections are authoritative in wom, so labelled records feed straight
	// back into the chain. Without labels there is nothing to correct and the
	// prior stands.
	corrected, err := t.applyLabels(ctx, entity, model, loaded.Graph)
	if err != nil {
		return nil, err
	}

	path, err := t.saveModel(entity, model)
	if err != nil {
		return nil, err
	}

	rules := flatten(model.Items)
	if err := t.store.ReplaceRules(ctx, entity.ID, rules); err != nil {
		return nil, err
	}

	records, err := t.extract(ctx, entity, model, loaded)
	if err != nil {
		return nil, err
	}

	// The scorer is trained last, because it learns from which pages produced
	// records, which is only known once extraction has run.
	scoring, err := t.trainScorer(ctx, entity)
	if err != nil {
		return nil, err
	}

	// The chain then reads the same outcomes as a sequence rather than a set,
	// which is what lets it credit a page for where it leads.
	var chain *ChainResult
	if !opts.NoChain {
		if chain, err = t.trainChain(ctx, entity); err != nil {
			return nil, err
		}
	}

	meta := store.ModelMeta{
		EntityID:     entity.ID,
		Path:         scoring.Path,
		Algorithm:    t.cfg.Model.Scorer,
		Accuracy:     scoring.Accuracy,
		Observations: scoring.Positive + scoring.Negative,
		TrainedAt:    time.Now().UTC(),
	}
	if err := t.store.SaveModelMeta(ctx, meta); err != nil {
		return nil, err
	}

	result := &Result{
		Pages:     loaded.Pages,
		Bytes:     loaded.Bytes,
		Skipped:   loaded.Skipped,
		Rules:     len(rules),
		Records:   records,
		Corrected: corrected,
		ModelPath: path,
		Score:     scoring,
		Chain:     chain,
		Elapsed:   time.Since(start),
	}
	if matcherStats != nil {
		result.Matcher = matcherStats()
	}
	result.Classify = t.classified
	return result, nil
}

// applyLabels feeds corrections back into the model's chain. A record marked
// invalid is not evidence of where a field lives, so only valid ones are kept.
func (t *Trainer) applyLabels(ctx context.Context, entity *store.Entity, model *wom.Model, graph *wom.WOM) (int, error) {
	_, total, err := t.store.SearchRecords(ctx, entity.ID, store.RecordQuery{Label: store.Valid})
	if err != nil {
		return 0, err
	}
	if total == 0 {
		return 0, nil
	}

	// wom fits the chain from the items it already holds; passing none keeps
	// the induced locators and trains only the field order.
	if err := model.Train(graph); err != nil {
		if errors.Is(err, wom.ErrNoRecord) || errors.Is(err, wom.ErrNoTrainingData) {
			return 0, nil
		}
		return 0, fmt.Errorf("train chain: %w", err)
	}
	return int(total), nil
}

func (t *Trainer) saveModel(entity *store.Entity, model *wom.Model) (string, error) {
	dir := t.cfg.ModelsDir()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", fmt.Errorf("create models directory: %w", err)
	}
	path := t.cfg.ExtractModelPath(entity.Name)
	if err := model.Save(path); err != nil {
		return "", fmt.Errorf("save model: %w", err)
	}
	return path, nil
}

// extract applies the model and stores what it finds.
func (t *Trainer) extract(ctx context.Context, entity *store.Entity, model *wom.Model, loaded *parse.Result) (int, error) {
	found := model.Extract(loaded.Graph)
	if len(found) == 0 {
		return 0, nil
	}

	formats := formatByURL(loaded.URLs)
	counts := map[string]int{}
	records := make([]store.Extracted, 0, len(found))

	for _, rec := range found {
		values := valuesOf(rec)
		if len(values) == 0 {
			continue
		}
		url := rec.URI
		records = append(records, store.Extracted{
			URL:        url,
			Confidence: confidenceFor(model.Items, rec.Name),
			Format:     formats[url],
			Values:     values,
		})
		if url != "" {
			counts[url]++
		}
	}

	saved, err := t.store.SaveRecords(ctx, entity.ID, records)
	if err != nil {
		return 0, err
	}
	if err := t.store.SetURLMatches(ctx, entity.ID, counts); err != nil {
		return 0, err
	}
	return saved, nil
}

// schemaOf turns an entity into the wom schema that describes it: one record
// prop named after the entity, carrying its aliases, with a child per property.
func schemaOf(entity *store.Entity) []wom.Prop {
	if len(entity.Properties) == 0 {
		return nil
	}

	aliases := make([]string, 0, len(entity.Aliases))
	for _, a := range entity.Aliases {
		aliases = append(aliases, a.Word)
	}

	props := make([]wom.Prop, 0, len(entity.Properties))
	for _, p := range entity.Properties {
		prop := wom.Prop{Name: p.Name, Description: p.Description, Pattern: p.Regex}
		if p.Type != "" {
			prop.Type = wom.Type(p.Type)
		}
		if p.Example != "" {
			prop.Examples = []string{p.Example}
		}
		// Aliases and description are what the matcher scores a page's labels
		// against. Dropping them here would leave the heuristic judging on the
		// property's name alone, which is the one word a site is least likely
		// to have used.
		for _, alias := range p.Aliases {
			prop.Aliases = append(prop.Aliases, alias.Word)
		}
		props = append(props, prop)
	}

	return []wom.Prop{{
		Name:    entity.Name,
		Aliases: aliases,
		Props:   props,
	}}
}

// flatten turns wom's nested items into rule rows. A child's ParentID holds
// its parent's index in the returned slice, which the store resolves to a real
// id as it writes them.
func flatten(items []wom.Item) []store.Rule {
	var out []store.Rule
	var walk func(item wom.Item, parent *uint)
	walk = func(item wom.Item, parent *uint) {
		rule := store.Rule{
			ParentID:    parent,
			Prop:        item.Name,
			XPath:       item.XPath,
			Selector:    item.Selector,
			Path:        item.Path,
			Regex:       item.Regex,
			URIPattern:  item.URI,
			Probability: item.Probability,
			Support:     item.Support,
		}
		out = append(out, rule)

		idx := uint(len(out) - 1)
		for _, child := range item.Items {
			walk(child, &idx)
		}
	}
	for _, item := range items {
		walk(item, nil)
	}
	return out
}

// valuesOf flattens one extracted record into prop and text pairs.
//
// A locator can match more than once inside a record: an unindexed path such
// as ./dd/text() hits every definition in the list. The first match is kept,
// because wom returns them in document order and a field's own value comes
// before the ones below it. Overwriting instead would silently report the
// last field's text under the first field's name.
func valuesOf(rec wom.Record) map[string]string {
	values := map[string]string{}
	for _, field := range rec.Items {
		if _, taken := values[field.Name]; taken {
			continue
		}
		if text := strings.TrimSpace(field.Value); text != "" {
			values[field.Name] = text
		}
	}
	if len(values) == 0 && strings.TrimSpace(rec.Value) != "" {
		values[rec.Name] = strings.TrimSpace(rec.Value)
	}
	return values
}

// confidence is the probability of the strongest item, which is the closest
// thing a model has to a single quality number.
func confidence(items []wom.Item) float64 {
	var best float64
	for _, item := range items {
		if item.Probability > best {
			best = item.Probability
		}
	}
	return best
}

// confidenceFor returns the induced probability for one named item.
func confidenceFor(items []wom.Item, name string) float64 {
	for _, item := range items {
		if strings.EqualFold(item.Name, name) {
			return item.Probability
		}
		if v := confidenceFor(item.Items, name); v > 0 {
			return v
		}
	}
	return 0
}

// formatByURL indexes the crawl's record of what format each page was.
func formatByURL(urls []store.URL) map[string]string {
	out := make(map[string]string, len(urls))
	for _, u := range urls {
		out[u.URL] = u.ContentType
	}
	return out
}
