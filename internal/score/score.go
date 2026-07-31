// SPDX-License-Identifier: GPL-3.0-or-later

// Package score predicts how likely a URL is to lead to a match.
//
// This is what makes scour a focused crawler rather than a crawler: the
// frontier pops in score order, so the budget is spent on the promising part
// of a site. Scoring happens once per discovered link, millions of times over a
// large crawl, so it must be cheap.
package score

import (
	"fmt"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"
)

// Features are what a scorer sees about a link. It is deliberately everything
// knowable before fetching: a scorer that needed the page would be useless for
// deciding whether to fetch it.
type Features struct {
	URL    string
	Anchor string
	Depth  int
	// Parent is the page the link was found on. It is not tokenised: it is
	// there for scorers that reason about where a link sits in the crawl
	// rather than what it says.
	Parent string
}

// Scorer predicts the probability, in [0,1], that a URL leads to a match.
type Scorer interface {
	// Name identifies the implementation, for logs and `scour list`.
	Name() string
	// Score returns the probability that this link is worth fetching.
	Score(f Features) float64
}

// Config is what a scorer is built from.
//
// It carries the entity rather than a database handle on purpose: a scorer
// decides whether a link is worth fetching and should not be able to reach
// anything else.
type Config struct {
	// Entity is the name being crawled.
	Entity string
	// Seed are the words describing the entity: its aliases and the example
	// values of its properties. They are what a scorer starts from before any
	// crawl has happened.
	Seed []string
	// Path is where this entity's trained model is kept. A scorer that finds
	// nothing there is expected to start cold rather than fail, because the
	// first crawl of a new entity has no model by definition.
	Path string
	// Vectors is where a vector-based scorer loads its word vectors from.
	Vectors string
}

// Factory builds a scorer. Registered implementations are selected by name
// from the configuration.
type Factory func(Config) (Scorer, error)

// Trained is implemented by scorers that can say whether they were fitted to
// a crawl or are still working from their seed words. `scour crawl` reports
// the difference, because a cold scorer's rankings mean much less.
type Trained interface {
	Trained() bool
}

var (
	mu       sync.RWMutex
	registry = map[string]Factory{}
)

// Register adds an implementation. It is called from init, so a plugin is a
// blank import.
func Register(name string, f Factory) {
	mu.Lock()
	defer mu.Unlock()
	registry[name] = f
}

// New builds a registered scorer by name. An empty name is the default.
func New(name string, cfg Config) (Scorer, error) {
	if name == "" {
		name = Default
	}

	mu.RLock()
	f, ok := registry[name]
	mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown scorer %q, have %s", name, strings.Join(Names(), ", "))
	}
	return f(cfg)
}

// Default is the scorer used when configuration names none. It needs no model
// file, no vectors and no network, which is what makes it the right default.
const Default = "bayes"

// Has reports whether a scorer is registered.
func Has(name string) bool {
	mu.RLock()
	defer mu.RUnlock()
	_, ok := registry[name]
	return ok
}

// Names lists the registered scorers.
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

// Fixed gives every URL the same score. It is what a crawl uses when scoring
// is switched off, and keeping it named is what stops an unscored crawl from
// looking scored.
type Fixed float64

// Name implements [Scorer].
func (Fixed) Name() string { return "fixed" }

// Score implements [Scorer].
func (f Fixed) Score(Features) float64 { return float64(f) }

// Tokens turns a link into the features a bag-of-words model counts.
//
// Every token is prefixed with what it came from, so a word in a URL path is
// not confused with the same word in anchor text: sites that put "careers" in
// the path are a different signal from pages that link to one.
func Tokens(f Features) []string {
	var out []string

	u, err := url.Parse(f.URL)
	if err != nil {
		return out
	}

	if host := strings.ToLower(u.Hostname()); host != "" {
		out = append(out, "host:"+host)
	}

	for _, seg := range strings.Split(u.EscapedPath(), "/") {
		for _, word := range words(seg) {
			out = append(out, "path:"+word)
		}
	}

	if ext := strings.ToLower(strings.TrimPrefix(path.Ext(u.Path), ".")); ext != "" {
		out = append(out, "ext:"+ext)
	}

	for key := range u.Query() {
		for _, word := range words(key) {
			out = append(out, "query:"+word)
		}
	}

	for _, word := range words(f.Anchor) {
		out = append(out, "anchor:"+word)
	}

	// Depth is bucketed rather than exact: the difference between level 7 and
	// level 8 is noise, the difference between level 1 and level 5 is not.
	out = append(out, "depth:"+strconv.Itoa(bucket(f.Depth)))

	return out
}

// words splits a string into lowercase alphanumeric tokens, dropping the ones
// too short or too numeric to carry meaning.
func words(s string) []string {
	fields := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})

	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if len(f) < 2 || len(f) > 32 {
			continue
		}
		out = append(out, f)
	}
	return out
}

func bucket(depth int) int {
	switch {
	case depth <= 1:
		return 1
	case depth <= 2:
		return 2
	case depth <= 4:
		return 4
	case depth <= 8:
		return 8
	default:
		return 16
	}
}

// FuncScorer adapts a plain function to [Scorer], for tests and for callers
// with a rule of thumb rather than a model.
type FuncScorer func(Features) float64

// Name implements [Scorer].
func (FuncScorer) Name() string { return "func" }

// Score implements [Scorer].
func (f FuncScorer) Score(features Features) float64 { return f(features) }
