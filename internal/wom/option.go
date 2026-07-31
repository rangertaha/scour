// SPDX-License-Identifier: MIT

package wom

import (
	"github.com/rangertaha/scour/internal/wom/internal/match"
	"github.com/rangertaha/scour/internal/wom/internal/seq"
)

// Option configures a WOM at construction time.
type Option func(*options)

type options struct {
	matcher  match.Matcher
	sequence seq.Sequence
	minProb  float64
	maxBody  int64
}

// Defaults for the tunable knobs.
const (
	defaultMinProbability = 0.25
	defaultMaxBody        = 32 << 20 // 32 MiB
)

func defaultOptions() options {
	return options{
		matcher:  match.Heuristic{},
		sequence: seq.HMM{},
		minProb:  defaultMinProbability,
		maxBody:  defaultMaxBody,
	}
}

// WithMatcher replaces the scoring model, which decides how strongly a node
// satisfies a prop. The default is Heuristic: deterministic, no network
// access, no training data. Because the Matcher also supplies the emissions
// the sequence model decodes, replacing it replaces the semantic judgement of
// the whole engine without touching graph construction or locator synthesis.
func WithMatcher(m match.Matcher) Option {
	return func(o *options) {
		if m != nil {
			o.matcher = m
		}
	}
}

// WithSequence replaces the sequence model, which refines per-node scores
// using the order fields appear in. The default is a trained HMM. Passing nil
// disables sequence refinement and leaves scoring purely local.
func WithSequence(s seq.Sequence) Option {
	return func(o *options) { o.sequence = s }
}

// WithHMM selects the built-in chain and caps its training passes. A positive
// count fits the transition matrix to the regions found; zero uses the default
// number of passes; a negative count disables training and decodes with the
// seeded prior alone, which is the right choice when there are too few records
// to learn anything from.
func WithHMM(iterations int) Option {
	return func(o *options) { o.sequence = seq.HMM{Iterations: iterations} }
}

// WithChainPrior seeds the sequence model with a previously trained chain
// instead of the built-in prior. Chains describe field ordering rather than
// page structure, so unlike locators they do transfer between sites.
func WithChainPrior(prior *seq.ChainPrior) Option {
	return func(o *options) { o.sequence = seq.HMM{Prior: prior} }
}

// WithMinProbability sets the confidence below which an inferred location is
// discarded rather than returned. The default is 0.25. Values outside [0,1]
// are ignored.
func WithMinProbability(p float64) Option {
	return func(o *options) {
		if p >= 0 && p <= 1 {
			o.minProb = p
		}
	}
}

// WithMaxBody caps how many bytes of a response body are read and parsed. The
// default is 32 MiB. Non-positive values are ignored.
func WithMaxBody(n int64) Option {
	return func(o *options) {
		if n > 0 {
			o.maxBody = n
		}
	}
}
