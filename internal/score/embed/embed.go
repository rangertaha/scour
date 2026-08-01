// SPDX-License-Identifier: GPL-3.0-or-later

// Package embed scores a link by how close its words are, in meaning, to the
// item being hunted.
//
// The bag-of-words scorer can only reward words it has already seen pay off.
// A link reading "saloons" is worthless to it until a crawl has proved that
// saloons are cars, and on a site that never uses the word "car" that proof
// never arrives. Comparing meanings instead of spellings is what closes that
// gap, and it is most valuable exactly where the counting model is weakest: on
// the first crawl, before there are any outcomes to count.
//
// The vectors are static and loaded from a file. That is a deliberate choice
// over calling an embedding service: scoring happens once per discovered link,
// millions of times over a large crawl, and a network round trip per link
// would cost more than the crawl it was meant to direct. A lookup and an
// average is measured in microseconds and needs no server, no key, and no cgo.
package embed

import (
	"fmt"
	"math"
	"strings"
	"sync"

	"github.com/rangertaha/scour/internal/score"
)

func init() {
	score.Register("embed", build)
}

func build(cfg score.Config) (score.Scorer, error) {
	if cfg.Vectors == "" {
		return nil, fmt.Errorf("the embed scorer needs vectors: set vectors in [model]")
	}

	vecs, err := Load(cfg.Vectors)
	if err != nil {
		return nil, err
	}
	return New(vecs, cfg.Seed)
}

// Scorer ranks a link by cosine similarity to the item's topic.
type Scorer struct {
	vectors *Vectors
	// topic is the mean of the item's own words: what we are looking for,
	// as one direction in the vector space.
	topic []float32

	mu    sync.RWMutex
	cache map[string]float64
}

// New builds a scorer for an item described by seed words.
//
// It fails when none of those words are known to the vectors, because a topic
// vector averaged from nothing points nowhere: every link would score the same
// and the crawl would silently become breadth-first. Failing is better than
// ranking at random while appearing to rank.
func New(vectors *Vectors, seed []string) (*Scorer, error) {
	if vectors == nil || vectors.Len() == 0 {
		return nil, fmt.Errorf("embed: no vectors loaded")
	}

	topic, known := vectors.Mean(seed)
	if known == 0 {
		return nil, fmt.Errorf("embed: none of the item's %d words are in the vectors, so there is no topic to compare against", len(seed))
	}

	return &Scorer{
		vectors: vectors,
		topic:   topic,
		cache:   map[string]float64{},
	}, nil
}

// Name implements [score.Scorer].
func (s *Scorer) Name() string { return "embed" }

// Trained implements [score.Trained].
//
// It is always false: this scorer never learns from a crawl. Saying so keeps
// `scour run` honest about which of its rankings came from evidence and
// which came from a dictionary.
func (s *Scorer) Trained() bool { return false }

// Score implements [score.Scorer].
func (s *Scorer) Score(f score.Features) float64 {
	tokens := plainTokens(f)
	if len(tokens) == 0 {
		return Neutral
	}

	// Links repeat heavily across a site: the same anchor text, the same path
	// prefixes, page after page. Caching on the joined tokens turns most of a
	// crawl's scoring into a map lookup.
	key := strings.Join(tokens, " ")

	s.mu.RLock()
	cached, ok := s.cache[key]
	s.mu.RUnlock()
	if ok {
		return cached
	}

	vec, known := s.vectors.Mean(tokens)
	result := Neutral
	if known > 0 {
		result = rescale(cosine(s.topic, vec))
	}

	s.mu.Lock()
	if len(s.cache) < maxCache {
		s.cache[key] = result
	}
	s.mu.Unlock()

	return result
}

// Neutral is what text the vectors have nothing to say about scores: an
// unknown word here, two unrelated bags of words elsewhere.
//
// It is deliberately mid-range rather than zero, and for two reasons that meet
// at the same number. Not recognising a word is ignorance, and a crawler that
// treated ignorance as evidence of irrelevance would never explore vocabulary
// its vectors happen to lack. And [rescale] puts a cosine of zero here, so
// unrelated text lands on it too: in a real vector space almost nothing is
// genuinely opposite.
//
// It is exported because it is the pivot any caller needs to read a similarity
// as evidence. A score above it argues for a match, below it against, and one
// on it argues nothing at all.
const Neutral = 0.5

// maxCache bounds the score cache. A crawl can discover millions of distinct
// links, and an unbounded map keyed on their text would outgrow the crawl.
const maxCache = 1 << 16

// plainTokens is the link's words without the source prefixes the counting
// model uses.
//
// Those prefixes exist so "path:careers" and "anchor:careers" stay distinct
// features, which is right when counting occurrences. Here they would be a
// mistake: a prefixed token is not a word, and no vector file has one.
func plainTokens(f score.Features) []string {
	var out []string
	for _, t := range score.Tokens(f) {
		if i := strings.IndexByte(t, ':'); i >= 0 {
			t = t[i+1:]
		}
		// Depth buckets and file extensions are structure, not vocabulary.
		if t == "" || isNumber(t) {
			continue
		}
		out = append(out, t)
	}
	return out
}

func isNumber(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return len(s) > 0
}

// cosine is the similarity of two vectors, in [-1,1].
func cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}

	var dot, na, nb float64
	for i := range a {
		x, y := float64(a[i]), float64(b[i])
		dot += x * y
		na += x * x
		nb += y * y
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// rescale maps a cosine onto a probability.
//
// Similarity runs from -1 to 1 while a score must run from 0 to 1, and the
// mapping is not merely arithmetic: in a real vector space almost nothing is
// genuinely opposite, so unrelated text sits near zero rather than at -1.
//
// The consequence is that the useful range is the upper half. An unrelated
// link and an unrecognised one both land near 0.5, and this scorer cannot tell
// them apart: orthogonal is not the same as opposite. That is a real limit
// rather than a tuning problem, and it is survivable because the frontier
// needs an ordering rather than a calibrated probability. It is also why this
// scorer is an alternative to the counting model rather than a replacement:
// the counting model learns what is genuinely bad, which this one cannot.
func rescale(c float64) float64 {
	v := (c + 1) / 2
	switch {
	case v < 0:
		return 0
	case v > 1:
		return 1
	default:
		return v
	}
}
