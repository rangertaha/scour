// SPDX-License-Identifier: MIT

package seq

import "math"

// Priors are the only knowledge wom ships with, and the distinction between
// what may and may not be bundled is deliberate.
//
// Locators are never bundled. An XPath induced on one site is meaningless on
// another — that is the entire reason induction exists — and a bundled locator
// would produce confident, wrong answers rather than no answer.
//
// Two things do transfer, and both are shipped:
//
//   - The chain prior. Field ordering is a property of how people write
//     records, not of any one site's markup: a make tends to precede a model,
//     which tends to precede a year.
//   - Value shapes. A year is four digits wherever it appears, a price carries
//     a currency mark, an email has an @ before a dot.

// ChainPrior is a serializable transition model over one background state and
// one state per field. It is what Model.Train produces and what
// WithChainPrior consumes.
//
// Start and Trans hold ordinary probabilities rather than logs so a saved
// model stays readable, and index 0 is always the background state.
type ChainPrior struct {
	Fields int         `json:"fields"`
	Start  []float64   `json:"start"`
	Trans  [][]float64 `json:"trans"`
}

// DefaultChainPrior returns the built-in transition prior for a record of n
// fields: fields tend to appear in schema order, may span several adjacent
// nodes, and are separated by stretches of unrelated text. This is the seed
// training starts from and the fallback when no trained chain is supplied.
func DefaultChainPrior(n int) *ChainPrior {
	if n <= 0 {
		return nil
	}
	return newHMM(n).prior()
}

// valid reports whether the prior is well formed for the given field count.
func (c *ChainPrior) valid(fields int) bool {
	if c == nil || c.Fields != fields || fields <= 0 {
		return false
	}
	size := fields + 1
	if len(c.Start) != size || len(c.Trans) != size {
		return false
	}
	for _, row := range c.Trans {
		if len(row) != size {
			return false
		}
	}
	return true
}

// prior exports the chain as a serializable ChainPrior.
func (h *hmm) prior() *ChainPrior {
	size := h.n + 1
	out := &ChainPrior{
		Fields: h.n,
		Start:  make([]float64, size),
		Trans:  make([][]float64, size),
	}
	for s := 0; s < size; s++ {
		out.Start[s] = expOrZero(h.start[s])
	}
	for i := 0; i < size; i++ {
		out.Trans[i] = make([]float64, size)
		for j := 0; j < size; j++ {
			out.Trans[i][j] = expOrZero(h.trans[i][j])
		}
	}
	return out
}

// loadPrior replaces the chain's distributions with a saved one. It
// renormalizes rather than trusting the input to sum to 1, so a hand-edited
// model file cannot produce a broken decoder.
func (h *hmm) loadPrior(c *ChainPrior) {
	if !c.valid(h.n) {
		return
	}
	normalizeLog(append([]float64(nil), c.Start...), h.start)
	for i, row := range c.Trans {
		normalizeLog(append([]float64(nil), row...), h.trans[i])
	}
}

func expOrZero(logP float64) float64 {
	if logP <= logZero/2 {
		return 0
	}
	return math.Exp(logP)
}

// BackgroundState is the state index meaning "this position is not any field".
// Callers building label sequences for TrainLabeled use it for the positions
// that hold no field.
const BackgroundState = backgroundState

// TrainLabeled fits a chain from sequences whose states are already known,
// which is what corrected items provide. Supervised counting is strictly
// better than Baum-Welch when labels exist: it cannot land in a local optimum
// and it cannot permute the states away from the props they stand for.
//
// prior seeds the fit and is mixed in as pseudo-counts; pass nil to start from
// the built-in prior for that field count.
func TrainLabeled(fields int, prior *ChainPrior, sequences [][]int) *ChainPrior {
	if fields <= 0 {
		return nil
	}
	h := newHMM(fields)
	if prior != nil {
		h.loadPrior(prior)
	}
	h.trainLabeled(sequences)
	return h.prior()
}
