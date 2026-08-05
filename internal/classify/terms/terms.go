// SPDX-License-Identifier: GPL-3.0-or-later

// Package terms scores a page against a list of words and phrases.
//
// The only classifier that needs nothing: no corpus, no training, no model. It
// is what a topic starts as, before there are pages to learn from, and it is
// what everything else eventually gets written back into, because a list of
// words is the one form of a learned subject that a person can read and argue
// with.
//
// It is crude, and crude in a predictable direction: it finds subjects with
// their own vocabulary, and misses ones defined by tone or by treatment. A term
// list will pick out carbon capture and will not pick out investigative
// journalism.
package terms

import (
	"context"
	"strings"

	"github.com/rangertaha/scour/internal/classify"
)

// Name is what this registers as.
const Name = "terms"

func init() {
	classify.Register(Name, func(_ context.Context, cfg classify.Config) (classify.Classifier, error) {
		return New(cfg)
	})
}

// Terms scores against a fixed vocabulary.
type Terms struct {
	name    string
	version int

	// single are one-word terms, with their weights.
	single map[string]float64
	// phrases are multi-word terms, kept as their token sequences because a
	// phrase has to match in order to mean what it says.
	phrases []phrase
	// total is the weight a page scores if it uses everything, which is what
	// the result is divided by.
	total float64
}

type phrase struct {
	tokens []string
	weight float64
}

// New builds a classifier from a term list.
//
// An empty list is allowed and scores everything zero. That is the honest
// answer for a subject nobody has described yet, and it is better than refusing
// to build: a job referencing an untrained subject should crawl nothing on
// topic rather than fail to start.
func New(cfg classify.Config) (*Terms, error) {
	t := &Terms{
		name:    cfg.Name,
		version: cfg.Version,
		single:  map[string]float64{},
	}

	for _, term := range cfg.Terms {
		weight := 1.0
		if w, ok := cfg.Weights[term]; ok && w > 0 {
			weight = w
		}

		tokens := classify.Tokens(term)
		switch len(tokens) {
		case 0:
			continue // punctuation, or a single character
		case 1:
			// A term repeated in the list counts once, at its highest weight,
			// rather than twice: saying a word matters does not become truer
			// for saying it again.
			if weight > t.single[tokens[0]] {
				t.total += weight - t.single[tokens[0]]
				t.single[tokens[0]] = weight
			}
		default:
			t.phrases = append(t.phrases, phrase{tokens: tokens, weight: weight})
			t.total += weight
		}
	}
	return t, nil
}

// Name implements [classify.Classifier].
func (t *Terms) Name() string { return t.name }

// Version implements [classify.Classifier].
func (t *Terms) Version() int { return t.version }

// Score implements [classify.Classifier].
//
// Every term contributes its weight, diminished by how often it appears, and
// the total is divided by the weight of the whole vocabulary. So a page using
// every term scores near one, a page using a tenth of them scores near a tenth,
// and a page using none scores zero.
//
// Diminishing the repeats is what stops one word in a navigation bar
// outscoring a page that discusses the subject properly in varied language.
func (t *Terms) Score(_ context.Context, text string) (float64, error) {
	if t.total == 0 || strings.TrimSpace(text) == "" {
		return 0, nil
	}

	tokens := classify.Tokens(text)
	counts := classify.Counts(tokens)

	var found float64
	for term, weight := range t.single {
		found += weight * classify.Saturate(counts[term])
	}
	for _, p := range t.phrases {
		found += p.weight * classify.Saturate(countPhrase(tokens, p.tokens))
	}

	return classify.Clamp(found / t.total), nil
}

// Vocabulary is the terms this scores against, highest weighted first. It is
// how a classifier prints itself.
func (t *Terms) Vocabulary() []string {
	weights := make(map[string]float64, len(t.single)+len(t.phrases))
	for term, weight := range t.single {
		weights[term] = weight
	}
	for _, p := range t.phrases {
		weights[strings.Join(p.tokens, " ")] = p.weight
	}
	return classify.Top(weights, len(weights))
}

// countPhrase counts how often a token sequence appears in order.
func countPhrase(tokens, want []string) int {
	if len(want) == 0 || len(tokens) < len(want) {
		return 0
	}

	var n int
	for i := 0; i+len(want) <= len(tokens); i++ {
		match := true
		for j, w := range want {
			if tokens[i+j] != w {
				match = false
				break
			}
		}
		if match {
			n++
		}
	}
	return n
}
