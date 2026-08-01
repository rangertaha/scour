// SPDX-License-Identifier: GPL-3.0-or-later

package matcher

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"github.com/rangertaha/scour/internal/score/embed"
	"github.com/rangertaha/scour/internal/wom"
)

func init() {
	Register("embed", NewEmbed)
}

// Embed nudges the heuristic's score by how close a property's meaning is to
// the words naming a node, using the same static word vectors the embed scorer
// ranks links with.
//
// It exists because the heuristic compares spellings. A page labelling its
// field "Manufacturer" scores zero against a schema calling it "make", and a
// Greek page labelling it anything at all scores zero against every English
// word in the schema. Those are not weak matches, they are invisible ones, and
// no weight on a token overlap can find them. Comparing meanings can.
//
// It is the cheap half of what the llm matcher does, and it is bounded the
// same way and for a different reason. The llm matcher consults a model only in
// the undecided band because asking is expensive. Asking here is free, so the
// band is doing something else: outside it the heuristic already has real
// evidence, and a dictionary that has never seen the page should not be allowed
// to argue with it. Inside the band the heuristic has nothing, and a synonym is
// the only evidence there is.
type Embed struct {
	vectors *embed.Vectors
	base    wom.Matcher

	floor   float64
	ceiling float64
	weight  float64

	mu        sync.RWMutex
	cache     map[string]float64
	consulted int
	hits      int
	misses    int
}

// DefaultWeight is how far a similarity may move the base score.
//
// The arithmetic is what picks it. The undecided band runs from DefaultFloor to
// DefaultCeiling, 0.23 wide, and the heuristic's best decision cut sits near
// 0.31 inside it. Related text scores between 0.5 and about 0.9 against a
// property's own words, so the evidence term runs from 0 to roughly 0.4. At
// 0.5, a strong synonym moves a node by up to 0.2: enough to carry it across
// the cut and out of the band, and not enough for a weak one to.
const DefaultWeight = 0.5

// NewEmbed builds the matcher.
func NewEmbed(cfg Config) (wom.Matcher, error) {
	if cfg.Vectors == "" {
		return nil, fmt.Errorf("the embed matcher needs vectors: set vectors in [model]")
	}

	vecs, err := embed.Load(cfg.Vectors)
	if err != nil {
		return nil, err
	}

	floor, ceiling := cfg.Floor, cfg.Ceiling
	if floor == 0 && ceiling == 0 {
		floor, ceiling = DefaultFloor, DefaultCeiling
	}
	if floor > ceiling {
		return nil, fmt.Errorf("matcher floor %v is above ceiling %v", floor, ceiling)
	}

	weight := cfg.Weight
	if weight == 0 {
		weight = DefaultWeight
	}

	return &Embed{
		vectors: vecs,
		base:    base(cfg),
		floor:   floor,
		ceiling: ceiling,
		weight:  weight,
		cache:   map[string]float64{},
	}, nil
}

// Score implements [wom.Matcher].
//
// The similarity is applied as evidence rather than as a verdict: it moves the
// heuristic's score by how far it sits from [embed.Neutral], which is where
// unrelated text lands. Vectors that recognise nothing, or recognise everything
// equally, therefore change nothing. That is the property that makes this safe
// to leave on, because the failure mode of a word vector file is not a wrong
// answer but an absent one, and an absent answer has to cost nothing.
func (e *Embed) Score(ctx context.Context, p wom.Prop, n *wom.Node) float64 {
	heuristic := e.base.Score(ctx, p, n)
	if heuristic <= e.floor || heuristic >= e.ceiling {
		return heuristic
	}

	labels := wom.Labels(n)
	if len(labels) == 0 {
		return heuristic
	}

	e.mu.Lock()
	e.misses++
	e.mu.Unlock()

	ev, ok := e.evidence(p, labels)
	if !ok {
		return heuristic
	}
	return clamp(heuristic + e.weight*ev)
}

// Stats returns the counters, so the cost of a matcher can be reported rather
// than assumed. Errors is always zero: a vector file is read once at startup,
// so there is nothing left that can fail per node.
func (e *Embed) Stats() Stats {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return Stats{Calls: e.consulted, Hits: e.hits, Misses: e.misses}
}

// evidence is how strongly a node's labels argue for a property, as a signed
// quantity centred on zero: positive argues for, zero argues nothing.
//
// Each label is compared on its own and the strongest wins, rather than every
// label being averaged into one bag. That mirrors what the heuristic's
// labelScore does, and for the same reason: a node carries several names from
// several sources, and one of them being right is the answer. Averaging would
// let four generic wrappers bury the one class that says "byline".
//
// A label's weight scales its evidence rather than its similarity. Scaling the
// similarity would turn a half-trusted source into an argument against the
// match, when what a weak source means is that it argues less either way.
//
// A site repeats its markup on every page, so the same property meets the same
// labels hundreds of times. Caching on the pair turns nearly all of a corpus
// into a map lookup, exactly as the judgement cache does for a model, and
// without needing to be durable: this is cheap to redo between runs and a model
// call is not.
func (e *Embed) evidence(p wom.Prop, labels []wom.Label) (float64, bool) {
	key := propKey(p) + "\x01" + labelKey(labels)

	e.mu.RLock()
	cached, seen := e.cache[key]
	e.mu.RUnlock()
	if seen {
		e.mu.Lock()
		e.hits++
		e.mu.Unlock()
		return cached, cached != unknown
	}

	vocabulary := words(p.Labels())
	best, found := 0.0, false
	for _, l := range labels {
		named := words([]string{l.Text})
		if len(named) == 0 {
			continue
		}
		sim, ok := e.vectors.Similar(vocabulary, named)
		if !ok {
			continue
		}
		if ev := l.Weight * (sim - embed.Neutral); !found || ev > best {
			best, found = ev, true
		}
	}

	stored := best
	if !found {
		stored = unknown
	}

	e.mu.Lock()
	e.consulted++
	if len(e.cache) < maxComparisons {
		e.cache[key] = stored
	}
	e.mu.Unlock()

	return best, found
}

// labelKey identifies a set of labels by what the comparison actually reads,
// which is each name together with how far its source is trusted.
func labelKey(labels []wom.Label) string {
	var b strings.Builder
	for _, l := range labels {
		b.WriteString(l.Text)
		b.WriteByte(0)
		b.WriteString(strconv.FormatFloat(l.Weight, 'f', 2, 64))
		b.WriteByte(0)
	}
	return b.String()
}

// propKey identifies a property by everything that feeds the comparison rather
// than by its name.
//
// One schema describes the same field differently per site: a taught example
// belongs to the site it was taught on, and so do the words a site uses for it.
// Two props called "make" can therefore carry different aliases and different
// descriptions, and a cache keyed on the name alone would hand one site's
// answer to the other and never notice.
func propKey(p wom.Prop) string {
	return p.Name + "\x00" + p.Description + "\x00" + strings.Join(p.Aliases, "\x00")
}

// unknown marks a comparison the vectors could not make, so that a question
// already asked and answered "no idea" is not asked again. Evidence is a
// weighted distance from [embed.Neutral] and so lies in [-0.5, 0.5], which
// leaves nothing real to collide with it.
const unknown = -1

// maxComparisons bounds the cache. A corpus repeats its templates but not
// without limit, and an unbounded map keyed on label sets would outgrow the
// run that filled it.
const maxComparisons = 1 << 16

// minWord is the shortest token worth looking up. Below it a label fragment is
// structure rather than vocabulary: the "h" of h1, the "og" of og:title, the
// "id" of an attribute name.
const minWord = 3

// words splits labels into the vocabulary a vector file is written in.
//
// Labels are written as identifiers, not as prose: entry-title, articleAuthor,
// og:published_time, "model year". A vector file holds none of those. It holds
// "title", "article", "author", "published". Splitting on case changes and on
// everything that is not a letter is what turns one into the other, and it
// strips a vocabulary prefix for free: og:title yields "og", which no file
// knows and [embed.Vectors.Mean] therefore skips, and "title", which every
// file knows.
func words(labels []string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, l := range labels {
		for _, w := range splitIdent(l) {
			if len(w) < minWord || seen[w] {
				continue
			}
			seen[w] = true
			out = append(out, w)
		}
	}
	return out
}

// splitIdent breaks an identifier into its lowercased words, handling
// snake_case, kebab-case, camelCase, dotted and colon-qualified names, and
// plain prose, without needing to know which it was given.
func splitIdent(s string) []string {
	var out []string
	var b strings.Builder
	flush := func() {
		if b.Len() > 0 {
			out = append(out, b.String())
			b.Reset()
		}
	}

	var prev rune
	for _, r := range s {
		switch {
		case unicode.IsUpper(r) && unicode.IsLower(prev):
			flush()
			b.WriteRune(unicode.ToLower(r))
		case unicode.IsLetter(r):
			b.WriteRune(unicode.ToLower(r))
		default:
			flush()
		}
		prev = r
	}
	flush()
	return out
}

// clamp holds a score to the range a matcher promises.
func clamp(f float64) float64 {
	switch {
	case f < 0:
		return 0
	case f > 1:
		return 1
	default:
		return f
	}
}
