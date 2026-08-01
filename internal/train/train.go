// SPDX-License-Identifier: GPL-3.0-or-later

// Package train induces an item's extraction rules from its cached pages,
// and applies them.
//
// Induction is expensive and happens once; extraction is cheap and happens per
// page. wom draws that line for us, and this package is where scour's items
// and labels meet it.
package train

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/rangertaha/scour/internal/bus"
	"github.com/rangertaha/scour/internal/cache"
	"github.com/rangertaha/scour/internal/config"
	"github.com/rangertaha/scour/internal/content"
	"github.com/rangertaha/scour/internal/parse"
	"github.com/rangertaha/scour/internal/store"
	"github.com/rangertaha/scour/internal/wom"
)

// ErrNoProperties is returned when an item has nothing to look for.
var ErrNoProperties = errors.New("item has no properties")

// Trainer induces and applies models.
type Trainer struct {
	cfg   config.Config
	store *store.Store
	cache cache.Store

	// classified is what the page classifier found during this run, kept here
	// because it is produced while labelling and reported with the result.
	classified *ClassifyResult

	// meter reports what training produced. Nil measures nothing, which is what
	// a one-shot `scour model train` does.
	meter Meter
}

// Meter records measurements taken while training. Declared here because this
// package is the consumer, and fire and forget for the same reason the crawl's
// is: a number nobody could publish must not fail a training run.
type Meter interface {
	Measure(ctx context.Context, name string, value float64, unit string, labels map[string]string)
}

// WithMeter returns a trainer that reports what it produced.
func (t *Trainer) WithMeter(m Meter) *Trainer {
	clone := *t
	clone.meter = m
	return &clone
}

func (t *Trainer) measure(ctx context.Context, name string, value float64, labels map[string]string) {
	if t.meter == nil {
		return
	}
	t.meter.Measure(ctx, name, value, "count", labels)
}

// New returns a trainer.
func New(cfg config.Config, s *store.Store, c cache.Store) *Trainer {
	return &Trainer{cfg: cfg, store: s, cache: c}
}

// Result reports what a training run did.
type Result struct {
	// RunID is the history row this training wrote, zero when none could be.
	RunID      uint
	Pages      int
	Bytes      int64
	Skipped    int
	Rules      int
	Records    int
	Corrected  int
	ModelPaths []string
	Score      *ScoreResult
	Chain      *ChainResult
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

// Run induces an item's model from its cached pages, saves it, stores the
// rules it produced, and extracts records with it.
//
// Induction and extraction happen together because a model nobody has applied
// tells you nothing about whether it works. The records it produces are what
// `scour search` then shows and what labelling corrects.
func (t *Trainer) Run(ctx context.Context, item *store.Item, opts Options) (*Result, error) {
	// The history row opens before the work and closes after it, whichever way
	// it ends, so a training that died leaves a row saying it started rather
	// than no row at all. Training became a run for the reason crawling did:
	// without one, nothing recorded that last night's happened, how long it
	// took, or that it produced fewer rules than the run before it.
	//
	// It never fails the training it is recording. A run that induced a model
	// and could not write its own history row still induced the model, and the
	// model is the thing worth keeping.
	var run *store.Run
	if t.store != nil {
		opened, err := t.store.StartTrainingRun(ctx, item.ID)
		if err != nil {
			slog.Debug("could not open a training run", "item", item.Name, "err", err)
		} else {
			run = opened
		}
	}

	result, err := t.induce(ctx, item, opts)

	if run != nil {
		f := store.Finished{State: store.RunDone, Err: err}
		if err != nil {
			f.State = store.RunFailed
		}
		if result != nil {
			f.Records, f.Rules, f.Skipped = result.Records, result.Rules, result.Skipped
			result.RunID = run.ID
		}
		if ferr := t.store.FinishRun(ctx, run.ID, f); ferr != nil {
			slog.Debug("could not close a training run", "run", run.ID, "err", ferr)
		}
	}
	return result, err
}

// induce is the training run itself. Run wraps it so the history row is written
// on every path out, including the ones that return an error.
func (t *Trainer) induce(ctx context.Context, item *store.Item, opts Options) (*Result, error) {
	start := time.Now()

	props := schemaOf(item)
	if len(props) == 0 {
		return nil, fmt.Errorf("%w: scour item add %s -p <prop> -e <example>", ErrNoProperties, item.Name)
	}

	// The field-order chain describes how people write records rather than how
	// one site marks them up, so it transfers: every item's induction is
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

	// Induction runs once per format the corpus holds, because a rule set
	// describes a shape and two formats are not the same shape. Induced
	// together, the stronger signal took the whole item and the other format
	// got nothing: a feed's repeating <item> elements beat the article pages it
	// links to, and its XPaths then matched no article. That is the ordinary
	// shape of a news crawl rather than a corner case.
	formats, total, err := t.corpusFormats(ctx, item.ID, opts.Types)
	if err != nil {
		return nil, err
	}
	if len(formats) == 0 {
		return nil, parse.ErrNoPages
	}

	var (
		rules     []store.Rule
		paths     []string
		extracted []store.Extracted
		matches   = map[string]int{}
		pages     int
		read      int64
		corrected int
	)

	for _, format := range formats {
		only, err := content.New([]string{format}, nil)
		if err != nil {
			return nil, fmt.Errorf("select format %s: %w", format, err)
		}
		loaded, err := parse.Load(ctx, t.store, t.cache, item.ID, parse.Options{
			Limit: opts.Limit,
			Types: only,
			WOM:   womOpts,
		})
		if err != nil {
			return nil, err
		}
		if loaded.Pages == 0 {
			continue
		}
		pages += loaded.Pages
		read += loaded.Bytes

		model, err := loaded.Graph.Model(props...)
		if err != nil {
			return nil, fmt.Errorf("induce %s model for %s: %w", format, item.Name, err)
		}

		// A taught pattern is authoritative over the synthesized one. Induction
		// generalizes from what it saw, which is the right default and the
		// wrong answer whenever someone has looked at the site and knows
		// better.
		applyTaughtPatterns(model.Items, props)

		// Field order is a property of the schema rather than of a format, and
		// the chain accumulates across runs anyway, so each format refines the
		// same prior in turn.
		if err := t.saveFieldChain(ctx, model); err != nil {
			return nil, err
		}

		// Corrections are authoritative in wom, so labelled records feed
		// straight back into the chain. Without labels there is nothing to
		// correct and the prior stands.
		n, err := t.applyLabels(ctx, item, model, loaded.Graph)
		if err != nil {
			return nil, err
		}
		corrected += n

		path, err := t.saveModel(item, format, model)
		if err != nil {
			return nil, err
		}
		paths = append(paths, path)

		rules = append(rules, flatten(model.Items, format)...)

		found, counts := t.extract(model, loaded)
		extracted = append(extracted, found...)
		for url, n := range counts {
			matches[url] += n
		}
	}

	// Both are written once, after every format, because each replaces the
	// item's whole set: a per-format write would delete the format before it.
	if err := t.store.ReplaceRules(ctx, item.ID, rules); err != nil {
		return nil, err
	}
	records, err := t.store.SaveRecords(ctx, item.ID, extracted)
	if err != nil {
		return nil, err
	}
	if err := t.store.SetURLMatches(ctx, item.ID, matches); err != nil {
		return nil, err
	}

	// How much a training run understood: the rules are what it believes about
	// the site, and the records are what those beliefs actually produced. A
	// rule count that holds while records fall is the shape of a site changing
	// under a model that has not noticed.
	labels := map[string]string{"item": item.Name}
	t.measure(ctx, bus.MetricRules, float64(len(rules)), labels)
	t.measure(ctx, bus.MetricRecords, float64(records), labels)

	// The scorer is trained last, because it learns from which pages produced
	// records, which is only known once extraction has run.
	scoring, err := t.trainScorer(ctx, item)
	if err != nil {
		return nil, err
	}

	// The chain then reads the same outcomes as a sequence rather than a set,
	// which is what lets it credit a page for where it leads.
	var chain *ChainResult
	if !opts.NoChain {
		if chain, err = t.trainChain(ctx, item); err != nil {
			return nil, err
		}
	}

	meta := store.ModelMeta{
		ItemID:       item.ID,
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
		Pages:      pages,
		Bytes:      read,
		Skipped:    total - pages,
		Rules:      len(rules),
		Records:    records,
		Corrected:  corrected,
		ModelPaths: paths,
		Score:      scoring,
		Chain:      chain,
		Elapsed:    time.Since(start),
	}
	if matcherStats != nil {
		result.Matcher = matcherStats()
	}
	result.Classify = t.classified
	return result, nil
}

// applyLabels feeds corrections back into the model's chain. A record marked
// invalid is not evidence of where a field lives, so only valid ones are kept.
func (t *Trainer) applyLabels(ctx context.Context, item *store.Item, model *wom.Model, graph *wom.WOM) (int, error) {
	_, total, err := t.store.SearchRecords(ctx, item.ID, store.RecordQuery{Label: store.Valid})
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

func (t *Trainer) saveModel(item *store.Item, format string, model *wom.Model) (string, error) {
	dir := t.cfg.ModelsDir()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", fmt.Errorf("create models directory: %w", err)
	}
	path := t.cfg.ExtractModelPath(item.Name, format)
	if err := model.Save(path); err != nil {
		return "", fmt.Errorf("save model: %w", err)
	}
	return path, nil
}

// allowsFormat reports whether a restriction admits a shorthand, by asking it
// about the MIME types that shorthand stands for. A Set answers about MIME
// types and paths; a corpus is grouped by shorthand.
func allowsFormat(types *content.Set, format string) bool {
	for _, m := range content.Shorthands[format] {
		if types.AllowsMIME(m) {
			return true
		}
	}
	return false
}

// corpusFormats lists the extractable formats an item's fetched pages hold, in
// a stable order, along with how many fetched pages there are in total.
//
// The total is what makes the skipped count still mean what it did: loading one
// format at a time makes every other format look skipped, so it is counted once
// here rather than summed over the passes.
func (t *Trainer) corpusFormats(ctx context.Context, itemID uint, types *content.Set) ([]string, int, error) {
	rows, err := t.store.FetchedURLs(ctx, itemID)
	if err != nil {
		return nil, 0, err
	}

	seen := map[string]bool{}
	var out []string
	for _, row := range rows {
		f := row.ContentType
		if f == "" || seen[f] || !content.Extractable[f] {
			continue
		}
		if types != nil && !allowsFormat(types, f) {
			continue
		}
		seen[f] = true
		out = append(out, f)
	}
	sort.Strings(out)
	return out, len(rows), nil
}

// extract applies one format's model and returns what it found, along with how
// many records each URL yielded.
//
// It does not store anything. [store.Store.SaveRecords] replaces an item's
// whole set, deleting every record not in the batch handed to it, so saving
// once per format would leave only the last format's records behind. The
// batches are collected across formats and written once, which is the same
// reason the rules are.
func (t *Trainer) extract(model *wom.Model, loaded *parse.Result) ([]store.Extracted, map[string]int) {
	found := model.Extract(loaded.Graph)
	if len(found) == 0 {
		return nil, nil
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
	return records, counts
}

// resolveProps picks one row per property name.
//
// A property may be taught twice: once as the item's default and again scoped
// to a domain, which is how one site's word for a byline is kept from becoming
// every site's. The two rows carry the same name, and handing both to wom is
// handing it a schema with a duplicate field, which it rejects. Teaching a
// domain therefore did not merely fail to take effect, it stopped the item
// being trainable at all.
//
// The scoped row wins only when every target is that domain, because then the
// corpus is that site and its answer is the right one everywhere in it. Across
// several sites there is no single answer, so the default is kept and the
// scoped teaching is reported as unused rather than silently dropped: applying
// it per host means inducing per host, which the trainer does not do yet.
func resolveProps(item *store.Item) []store.Property {
	byName := make(map[string][]store.Property, len(item.Properties))
	order := make([]string, 0, len(item.Properties))
	for _, p := range item.Properties {
		if _, seen := byName[p.Name]; !seen {
			order = append(order, p.Name)
		}
		byName[p.Name] = append(byName[p.Name], p)
	}

	out := make([]store.Property, 0, len(order))
	for _, name := range order {
		rows := byName[name]
		best := rows[0]
		for _, r := range rows {
			// A row with no domain is the default and is the fallback.
			if r.Domain == "" {
				best = r
			}
		}
		for _, r := range rows {
			if r.Domain != "" && confinedTo(item, r.Domain) {
				best = r
				break
			}
		}
		for _, r := range rows {
			if r.Domain != "" && r.Domain != best.Domain {
				slog.Warn("property taught on a domain is not being applied",
					"item", item.Name, "property", name, "domain", r.Domain,
					"why", "the corpus covers more than that domain")
			}
		}
		out = append(out, best)
	}
	return out
}

// confinedTo reports whether every target of every job of an item sits on one
// domain, which is what makes that domain's teaching the right answer for the
// whole corpus.
func confinedTo(item *store.Item, domain string) bool {
	targets := item.AllTargets()
	if len(targets) == 0 {
		return false
	}
	for _, t := range targets {
		host := t.Value
		if t.Kind == store.TargetURL {
			u, err := url.Parse(t.Value)
			if err != nil {
				return false
			}
			host = u.Hostname()
		}
		host = strings.TrimPrefix(strings.ToLower(host), "www.")
		if host != domain && !strings.HasSuffix(host, "."+domain) {
			return false
		}
	}
	return true
}

// schemaOf turns an item into the wom schema that describes it: one record
// prop named after the item, carrying its aliases, with a child per property.
func schemaOf(item *store.Item) []wom.Prop {
	if len(item.Properties) == 0 {
		return nil
	}

	aliases := make([]string, 0, len(item.Aliases))
	for _, a := range item.Aliases {
		aliases = append(aliases, a.Word)
	}

	props := make([]wom.Prop, 0, len(item.Properties))
	for _, p := range resolveProps(item) {
		prop := wom.Prop{Name: p.Name, Description: p.Description, Pattern: p.Regex, Label: p.Label}
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
		Name:    item.Name,
		Aliases: aliases,
		Props:   props,
	}}
}

// flatten turns wom's nested items into rule rows. A child's ParentID holds
// its parent's index in the returned slice, which the store resolves to a real
// id as it writes them.
func flatten(items []wom.Item, format string) []store.Rule {
	var out []store.Rule
	var walk func(item wom.Item, parent *uint)
	walk = func(item wom.Item, parent *uint) {
		rule := store.Rule{
			ParentID:    parent,
			Format:      format,
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

// applyTaughtPatterns replaces induced extraction patterns with taught ones,
// matching by prop name through the nested item tree.
//
// The pattern has already vetoed candidates during scoring, which is what moved
// the choice of node. This is the other half of the same string: what the
// chosen node yields.
func applyTaughtPatterns(items []wom.Item, props []wom.Prop) {
	taught := map[string]string{}
	var collect func([]wom.Prop)
	collect = func(ps []wom.Prop) {
		for _, p := range ps {
			if p.Pattern != "" {
				taught[p.Name] = p.Pattern
			}
			collect(p.Props)
		}
	}
	collect(props)
	if len(taught) == 0 {
		return
	}

	var walk func([]wom.Item)
	walk = func(is []wom.Item) {
		for i := range is {
			if pat, ok := taught[is[i].Name]; ok {
				is[i].Regex = pat
			}
			walk(is[i].Items)
		}
	}
	walk(items)
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
