// SPDX-License-Identifier: GPL-3.0-or-later

// Package bayes scores URLs with a naive Bayes model over their tokens.
//
// The model is a table of token counts, which is why it is stored as readable
// JSON: you can diff it, check it into version control, and see what the
// crawler has learned to chase and to avoid.
package bayes

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/rangertaha/scour/internal/score"
)

func init() {
	score.Register("bayes", build)
}

// build loads this item's model, or starts cold from its seed words.
//
// A missing model file is the normal state on a first crawl, not an error:
// there is nothing to have trained on yet. Starting from the aliases and
// property examples is what makes that first crawl better than random.
func build(cfg score.Config) (score.Scorer, error) {
	if cfg.Path != "" {
		model, err := Load(cfg.Path)
		switch {
		case err == nil:
			return model, nil
		case errors.Is(err, fs.ErrNotExist):
			// fall through to a cold start
		default:
			return nil, err
		}
	}

	cold := New()
	cold.Item = cfg.Item
	cold.Seed(cfg.Seed)
	return cold, nil
}

// Trained implements [score.Trained]. A model that has never been fitted has
// no TrainedAt, which is what separates a real ranking from a seeded guess.
func (m *Model) Trained() bool { return !m.TrainedAt.IsZero() }

// Version is the on-disk format version. Loading refuses anything newer, so an
// old binary fails loudly rather than misreading a file.
const Version = 1

// seedWeight is how many observations a token from the item's aliases and
// property examples is worth.
//
// It has to be enough to steer the first crawl, which has no evidence at all,
// and small enough that a few hundred real pages outvote it. Three is roughly
// "one page's worth of hint".
const seedWeight = 3.0

// smoothing is the Laplace constant. Without it a token seen only in negatives
// would drive the whole product to zero on its own.
const smoothing = 1.0

// Counts is the evidence for one token.
type Counts struct {
	Positive float64 `json:"positive"`
	Negative float64 `json:"negative"`
}

// Model is a naive Bayes classifier over URL tokens.
//
// It is safe for concurrent scoring; training is not concurrent with scoring.
type Model struct {
	mu sync.RWMutex

	Version   int               `json:"version"`
	Item      string            `json:"item,omitempty"`
	Tokens    map[string]Counts `json:"tokens"`
	Positives float64           `json:"positives"`
	Negatives float64           `json:"negatives"`
	Accuracy  float64           `json:"accuracy,omitempty"`
	Examples  int               `json:"examples,omitempty"`
	TrainedAt time.Time         `json:"trained_at,omitzero"`
}

// New returns an empty model, which scores everything at the prior of 0.5.
func New() *Model {
	return &Model{Version: Version, Tokens: map[string]Counts{}}
}

// Name implements [score.Scorer].
func (m *Model) Name() string { return "bayes" }

// Seed gives the model something to go on before any page has been crawled.
//
// The first crawl has no outcomes to learn from, so without this every link
// scores the same and the crawl is breadth-first. The words the user already
// supplied, the item name, its aliases and the property examples, are the
// only evidence available, and a URL containing them is a better bet than one
// that does not. Seeding is additive, so real observations accumulate on top
// and eventually outweigh it.
func (m *Model) Seed(words []string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, word := range words {
		for _, prefix := range []string{"path:", "anchor:"} {
			token := prefix + word
			c := m.Tokens[token]
			c.Positive += seedWeight
			m.Tokens[token] = c
		}
	}
	if len(words) > 0 && m.Positives == 0 {
		// A seeded model has to believe something is possible, or the prior
		// alone drives every score to zero.
		m.Positives = seedWeight
		m.Negatives = seedWeight
	}
}

// Observation is one training example: the features of a URL and whether it
// turned out to hold what was wanted.
type Observation struct {
	Features score.Features
	Relevant bool
}

// Train fits the model to observations, reserving a fraction for measuring
// accuracy. It replaces any counts from a previous run but keeps the seed,
// which is why callers seed after constructing and before training.
//
// Holdout must be in [0,1). A holdout that leaves fewer than four examples to
// measure is ignored, since an accuracy computed from three cases says nothing.
func (m *Model) Train(obs []Observation, holdout float64) error {
	if len(obs) == 0 {
		return errors.New("no observations to train on")
	}
	if holdout < 0 || holdout >= 1 {
		return fmt.Errorf("holdout must be in [0,1), got %v", holdout)
	}

	// Deterministic split: the same corpus must produce the same model, so
	// examples are ordered by URL rather than shuffled.
	sorted := make([]Observation, len(obs))
	copy(sorted, obs)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Features.URL < sorted[j].Features.URL })

	cut := len(sorted)
	if holdout > 0 {
		reserved := int(float64(len(sorted)) * holdout)
		if reserved >= 4 {
			cut = len(sorted) - reserved
		}
	}
	training, testing := sorted[:cut], sorted[cut:]

	m.mu.Lock()
	for _, o := range training {
		for _, token := range score.Tokens(o.Features) {
			c := m.Tokens[token]
			if o.Relevant {
				c.Positive++
			} else {
				c.Negative++
			}
			m.Tokens[token] = c
		}
		if o.Relevant {
			m.Positives++
		} else {
			m.Negatives++
		}
	}
	m.Examples = len(training)
	m.TrainedAt = time.Now().UTC()
	m.mu.Unlock()

	if len(testing) > 0 {
		m.Accuracy = m.measure(testing)
	}
	return nil
}

// measure reports the share of held-out examples the model classifies
// correctly, taking 0.5 as the decision boundary.
func (m *Model) measure(obs []Observation) float64 {
	var right int
	for _, o := range obs {
		if (m.Score(o.Features) >= 0.5) == o.Relevant {
			right++
		}
	}
	return float64(right) / float64(len(obs))
}

// Score implements [score.Scorer].
//
// The result is the posterior probability that the link is relevant. Work is
// done in log space, because multiplying a few dozen small probabilities
// underflows to zero long before the answer is reached.
func (m *Model) Score(f score.Features) float64 {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.Positives == 0 && m.Negatives == 0 {
		// Nothing learned and nothing seeded: every link is equally worth a
		// look, which is an honest answer rather than a confident one.
		return 0.5
	}

	total := m.Positives + m.Negatives
	logPos := math.Log(m.Positives / total)
	logNeg := math.Log(m.Negatives / total)

	vocab := float64(len(m.Tokens))
	for _, token := range score.Tokens(f) {
		c, seen := m.Tokens[token]
		if !seen {
			// An unknown token is evidence of nothing, and counting it would
			// make long URLs score lower than short ones for no reason.
			continue
		}
		logPos += math.Log((c.Positive + smoothing) / (m.Positives + smoothing*vocab))
		logNeg += math.Log((c.Negative + smoothing) / (m.Negatives + smoothing*vocab))
	}

	// Convert the log odds back to a probability without ever exponentiating a
	// large positive number.
	diff := logNeg - logPos
	switch {
	case diff > 40:
		return 0
	case diff < -40:
		return 1
	default:
		return 1 / (1 + math.Exp(diff))
	}
}

// Top returns the tokens that most strongly indicate relevance, and the ones
// that most strongly indicate the opposite. It is what `scour train` prints.
func (m *Model) Top(n int) (positive, negative []TokenWeight) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	weights := make([]TokenWeight, 0, len(m.Tokens))
	for token, c := range m.Tokens {
		if c.Positive+c.Negative < 2 {
			continue // a token seen once is a coincidence
		}
		weight := math.Log((c.Positive + smoothing) / (c.Negative + smoothing))
		weights = append(weights, TokenWeight{Token: token, Weight: weight})
	}
	sort.Slice(weights, func(i, j int) bool {
		if weights[i].Weight != weights[j].Weight {
			return weights[i].Weight > weights[j].Weight
		}
		return weights[i].Token < weights[j].Token
	})

	for _, w := range weights {
		switch {
		case w.Weight > 0 && len(positive) < n:
			positive = append(positive, w)
		case w.Weight < 0:
			negative = append(negative, w)
		}
	}
	if len(negative) > n {
		negative = negative[len(negative)-n:]
	}
	// Strongest negative first.
	for i, j := 0, len(negative)-1; i < j; i, j = i+1, j-1 {
		negative[i], negative[j] = negative[j], negative[i]
	}
	return positive, negative
}

// TokenWeight is one token's pull, as log odds.
type TokenWeight struct {
	Token  string  `json:"token"`
	Weight float64 `json:"weight"`
}

// Save writes the model as JSON, creating the directory if needed.
func (m *Model) Save(path string) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return fmt.Errorf("create model directory: %w", err)
		}
	}

	m.mu.RLock()
	buf, err := json.MarshalIndent(m, "", "  ")
	m.mu.RUnlock()
	if err != nil {
		return fmt.Errorf("encode model: %w", err)
	}

	if err := os.WriteFile(path, append(buf, '\n'), 0o600); err != nil {
		return fmt.Errorf("write model: %w", err)
	}
	return nil
}

// Load reads a model. A missing file reports fs.ErrNotExist, so a caller can
// tell "never trained" from "cannot read".
func Load(path string) (*Model, error) {
	buf, err := os.ReadFile(path) //nolint:gosec // operator supplied path
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		return nil, fmt.Errorf("read model %s: %w", path, err)
	}

	m := New()
	if err := json.Unmarshal(buf, m); err != nil {
		return nil, fmt.Errorf("decode model %s: %w", path, err)
	}
	if m.Version > Version {
		return nil, fmt.Errorf("model %s is version %d, this build understands %d", path, m.Version, Version)
	}
	if m.Tokens == nil {
		m.Tokens = map[string]Counts{}
	}
	return m, nil
}
