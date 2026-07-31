// SPDX-License-Identifier: MIT

package seq

import "math"

// Package seq is the sequence layer.
//
// Per-node scoring answers "does this node look like a make?" but ignores the
// fact that a make is followed by a model, which is followed by a year. That
// ordering is a real and highly informative regularity, and recovering it is
// the classic hidden-Markov formulation of information extraction: one state
// per field plus a background state, emissions from a local scorer, and
// transitions carrying the field order.
//
// wom uses it as a refinement rather than as the primary mechanism. For
// markup the record container is directly observable from repeated subtree
// shape, so structure does the heavy lifting and the chain only re-weights
// the fields inside a container. For PDF there is no tree at all — a page is
// a flat run of lines — and the chain is the only structural signal there is.
//
// # Training
//
// The transition matrix starts from a seeded prior and is then fitted to the
// regions actually found, by Baum-Welch. Three constraints keep that useful
// with very little data:
//
//  1. Only transitions are trained. Emissions come from the Matcher, which
//     knows which prop each state stands for. Fitting emissions too would let
//     the states drift off the schema and permute freely — unsupervised HMM
//     states carry no inherent identity, and the emission model is the only
//     thing anchoring state 3 to "year".
//
//  2. Estimation is MAP, not maximum likelihood. The seeded prior enters the
//     M-step as pseudo-counts, so a handful of records leaves the prior mostly
//     intact while a few hundred lets the data speak. This is what makes
//     fitting safe on one page rather than merely possible.
//
//  3. Training runs over candidate regions, never whole documents. Fitted to a
//     whole page the likelihood is dominated by navigation and boilerplate,
//     which repeat far more reliably than the records do.
//
// What this buys over a fixed prior is records with missing fields and records
// whose field order varies: a prior assumes the schema order always holds, a
// fitted matrix discovers that it holds seven times in ten.

// backgroundState is the state index for "this position is not any field".
// Field states are 1..n, matching the order of the props they came from.
const backgroundState = 0

// logZero stands in for log(0) and is small enough to never be selected while
// still supporting arithmetic without producing NaN.
const logZero = -1e18

// Transition weights, normalized per row into probabilities. They encode the
// prior that fields appear in schema order, may span several adjacent nodes,
// and are separated by stretches of unrelated text.
const (
	weightBGToBG      = 6.0 // background text tends to continue
	weightBGToField   = 4.0 // total mass leaving background, split over fields
	weightFieldRepeat = 1.5 // a field may span multiple adjacent nodes
	weightFieldToNext = 4.5 // the schema-order successor is the likely follower
	weightFieldToAny  = 1.0 // total mass for out-of-order fields, split
	weightFieldToBG   = 2.0 // a record ends and ordinary text resumes
)

// Training defaults.
const (
	// defaultIterations is enough for the transition matrix to settle on the
	// small region sets wom trains on; the tolerance check usually stops it
	// sooner.
	defaultIterations = 12

	// defaultTolerance is the log-likelihood gain below which training stops.
	defaultTolerance = 1e-4

	// priorWeight is how many pseudo-observations the seeded prior is worth in
	// the M-step. Raising it makes training more conservative on little data.
	priorWeight = 4.0

	// chainWeight is how far the sequence posterior may pull the raw Matcher
	// score down at one position. A field the chain considers the best reading
	// of a position keeps its score; the rest are damped towards it.
	chainWeight = 0.4
)

// Sequence refines per-node field scores using the order in which fields
// appear. Implementations receive every candidate region at once so they can
// fit to the whole set before decoding any of it.
//
// regions is indexed region → position → field, holding Matcher scores in
// [0,1]. The returned value must have the same shape; wom falls back to the
// unrefined scores if it does not.
type Sequence interface {
	Refine(regions [][][]float64, fields int) [][][]float64
}

// HMM is the built-in Sequence: a first-order chain over one background state
// and one state per field, fitted to the observed regions by Baum-Welch. The
// zero value is usable and trains with the default settings.
type HMM struct {
	// Iterations caps the Baum-Welch passes. Zero uses the default; a
	// negative value disables training and decodes with the seeded prior
	// alone, which is the right choice when regions are too few to learn from.
	Iterations int

	// Tolerance is the log-likelihood improvement below which training stops
	// early. Zero uses the default.
	Tolerance float64

	// Prior seeds the chain with a previously trained transition model instead
	// of the built-in one. It is ignored when its field count does not match
	// the schema being inferred.
	Prior *ChainPrior
}

func (h HMM) iterations() int {
	switch {
	case h.Iterations < 0:
		return 0
	case h.Iterations == 0:
		return defaultIterations
	}
	return h.Iterations
}

func (h HMM) tolerance() float64 {
	if h.Tolerance <= 0 {
		return defaultTolerance
	}
	return h.Tolerance
}

// Refine implements Sequence. It fits the transition matrix to every region,
// decodes each with forward-backward, and uses the resulting posteriors to damp
// the incoming scores.
//
// The chain knows about order but nothing about content, so a confident literal
// match must not be overturned by a sequence prior alone. It therefore adjusts
// the score rather than replacing part of it: the field the chain reads a
// position as keeps its score, and the alternatives are damped in proportion to
// how far the chain prefers its own reading.
//
// Averaging the two directly, which is what this did before, subtracted a
// roughly constant amount from every field instead. A posterior is a
// distribution over states, so with seven fields and a background state it sits
// near an eighth wherever the chain is unsure, and forty per cent of every score
// was replaced by that eighth. Scores fell by about a third across the board,
// and by more as the schema grew, since a wider schema spreads the same
// probability mass thinner. On the Guardian's feed it put author at 0.240
// against a threshold of 0.25: the byline was located correctly in every one of
// forty-five items and then discarded for want of a hundredth.
func (h HMM) Refine(regions [][][]float64, fields int) [][][]float64 {
	if fields == 0 || len(regions) == 0 {
		return regions
	}

	chain := newHMM(fields)
	if h.Prior != nil {
		chain.loadPrior(h.Prior)
	}
	emits := make([][][]float64, 0, len(regions))
	for _, r := range regions {
		emits = append(emits, emissions(r, fields))
	}
	if iters := h.iterations(); iters > 0 {
		chain.train(emits, iters, h.tolerance())
	}

	out := make([][][]float64, len(regions))
	for i, emit := range emits {
		post := chain.posterior(emit)
		out[i] = make([][]float64, len(regions[i]))
		for t := range regions[i] {
			// The chain's own reading of this position sets the reference, so
			// what counts is how each field compares with it rather than the
			// absolute size of a probability spread over every state.
			var top float64
			for j := 0; j < fields; j++ {
				if post[t][j+1] > top {
					top = post[t][j+1]
				}
			}
			row := make([]float64, fields)
			for j := 0; j < fields; j++ {
				agreement := 1.0
				if top > 0 {
					agreement = post[t][j+1] / top
				}
				row[j] = clamp(regions[i][t][j] * (1 - chainWeight + chainWeight*agreement))
			}
			out[i][t] = row
		}
	}
	return out
}

// hmm is a first-order chain over one background state and n field states.
// All probabilities are stored as natural logs.
type hmm struct {
	n     int         // number of field states
	start []float64   // log start probability per state
	trans [][]float64 // log transition probability, trans[from][to]
}

// newHMM builds a chain for n fields with transitions seeded from schema
// order. The seed is both the starting point for training and the prior that
// regularizes it.
func newHMM(n int) *hmm {
	size := n + 1
	h := &hmm{
		n:     n,
		start: make([]float64, size),
		trans: make([][]float64, size),
	}

	// Starting in background is as likely as starting in any one field.
	startW := make([]float64, size)
	startW[backgroundState] = 1.0
	for s := 1; s <= n; s++ {
		startW[s] = 1.0 / float64(n)
	}
	normalizeLog(startW, h.start)

	for from := 0; from < size; from++ {
		row := make([]float64, size)
		if from == backgroundState {
			row[backgroundState] = weightBGToBG
			for to := 1; to <= n; to++ {
				row[to] = weightBGToField / float64(n)
			}
		} else {
			row[backgroundState] = weightFieldToBG
			row[from] = weightFieldRepeat
			next := from + 1
			if next > n {
				next = 1 // records repeat, so the last field leads back to the first
			}
			if next != from {
				row[next] += weightFieldToNext
			} else {
				row[from] += weightFieldToNext
			}
			// Remaining mass spread over the other fields, so an unexpected
			// order is penalized but never impossible.
			others := n - 1
			if next != from {
				others--
			}
			if others > 0 {
				for to := 1; to <= n; to++ {
					if to != from && to != next {
						row[to] += weightFieldToAny / float64(others)
					}
				}
			}
		}
		h.trans[from] = make([]float64, size)
		normalizeLog(row, h.trans[from])
	}
	return h
}

// train fits the transition and start distributions to the given regions by
// Baum-Welch, holding emissions fixed. It returns once the log-likelihood gain
// falls below tol or the iteration cap is reached.
func (h *hmm) train(emits [][][]float64, iterations int, tol float64) {
	size := h.n + 1

	// The seeded matrix is kept as the prior; it is not updated in place, so
	// every M-step is smoothed towards the same starting point rather than
	// towards the previous iteration.
	priorTrans := make([][]float64, size)
	for i := range h.trans {
		priorTrans[i] = append([]float64(nil), h.trans[i]...)
	}
	priorStart := append([]float64(nil), h.start...)

	prevLL := math.Inf(-1)
	for iter := 0; iter < iterations; iter++ {
		transCounts := make([][]float64, size)
		for i := range transCounts {
			transCounts[i] = make([]float64, size)
		}
		startCounts := make([]float64, size)

		var totalLL float64
		var used int
		for _, emit := range emits {
			if len(emit) < 2 {
				continue
			}
			alpha, logLik := h.forward(emit)
			if math.IsInf(logLik, -1) || math.IsNaN(logLik) {
				continue
			}
			beta := h.backward(emit)
			totalLL += logLik
			used++

			for s := 0; s < size; s++ {
				startCounts[s] += math.Exp(alpha[0][s] + beta[0][s] - logLik)
			}
			for t := 0; t+1 < len(emit); t++ {
				for i := 0; i < size; i++ {
					for j := 0; j < size; j++ {
						transCounts[i][j] += math.Exp(
							alpha[t][i] + h.trans[i][j] + emit[t+1][j] + beta[t+1][j] - logLik)
					}
				}
			}
		}
		if used == 0 {
			return
		}

		// M-step. Expected counts are combined with the prior as
		// pseudo-counts, which is what keeps a two-record fit from collapsing
		// onto a degenerate matrix.
		for i := 0; i < size; i++ {
			row := make([]float64, size)
			for j := 0; j < size; j++ {
				row[j] = transCounts[i][j] + priorWeight*math.Exp(priorTrans[i][j])
			}
			normalizeLog(row, h.trans[i])
		}
		row := make([]float64, size)
		for s := 0; s < size; s++ {
			row[s] = startCounts[s] + priorWeight*math.Exp(priorStart[s])
		}
		normalizeLog(row, h.start)

		if iter > 0 && totalLL-prevLL < tol {
			return
		}
		prevLL = totalLL
	}
}

// trainLabeled fits the chain from sequences whose states are already known,
// which is what corrected items provide. Supervised counting is strictly
// better than Baum-Welch when labels exist: it cannot land in a local optimum
// and it cannot permute the states away from the props they stand for.
//
// The seeded prior is still mixed in as pseudo-counts, so a correction
// covering three records adjusts the prior rather than replacing it.
func (h *hmm) trainLabeled(sequences [][]int) {
	size := h.n + 1
	priorTrans := make([][]float64, size)
	for i := range h.trans {
		priorTrans[i] = append([]float64(nil), h.trans[i]...)
	}
	priorStart := append([]float64(nil), h.start...)

	transCounts := make([][]float64, size)
	for i := range transCounts {
		transCounts[i] = make([]float64, size)
	}
	startCounts := make([]float64, size)

	for _, seq := range sequences {
		if len(seq) == 0 {
			continue
		}
		if s := seq[0]; s >= 0 && s < size {
			startCounts[s]++
		}
		for t := 0; t+1 < len(seq); t++ {
			from, to := seq[t], seq[t+1]
			if from >= 0 && from < size && to >= 0 && to < size {
				transCounts[from][to]++
			}
		}
	}

	for i := 0; i < size; i++ {
		row := make([]float64, size)
		for j := 0; j < size; j++ {
			row[j] = transCounts[i][j] + priorWeight*math.Exp(priorTrans[i][j])
		}
		normalizeLog(row, h.trans[i])
	}
	row := make([]float64, size)
	for s := 0; s < size; s++ {
		row[s] = startCounts[s] + priorWeight*math.Exp(priorStart[s])
	}
	normalizeLog(row, h.start)
}

// normalizeLog turns a row of non-negative weights into log probabilities.
func normalizeLog(weights, dst []float64) {
	var sum float64
	for _, w := range weights {
		sum += w
	}
	for i, w := range weights {
		if sum <= 0 || w <= 0 {
			dst[i] = logZero
			continue
		}
		dst[i] = math.Log(w / sum)
	}
}

// emissions builds the log emission matrix for a run of nodes. Field states
// emit the Matcher's score; the background state emits the mass the best
// field left over, so a position that matches nothing strongly prefers
// background.
func emissions(scores [][]float64, n int) [][]float64 {
	const eps = 1e-6
	out := make([][]float64, len(scores))
	for i, row := range scores {
		e := make([]float64, n+1)
		best := 0.0
		for s := 0; s < n && s < len(row); s++ {
			v := clamp(row[s])
			if v > best {
				best = v
			}
			e[s+1] = math.Log(math.Max(v, eps))
		}
		e[backgroundState] = math.Log(math.Max(1-best, eps))
		out[i] = e
	}
	return out
}

// forward runs the forward pass, returning the alpha trellis and the total
// log-likelihood of the observation sequence.
func (h *hmm) forward(emit [][]float64) ([][]float64, float64) {
	size := h.n + 1
	alpha := make([][]float64, len(emit))
	for t := range alpha {
		alpha[t] = make([]float64, size)
	}
	for s := 0; s < size; s++ {
		alpha[0][s] = h.start[s] + emit[0][s]
	}
	for t := 1; t < len(emit); t++ {
		for to := 0; to < size; to++ {
			acc := logZero
			for from := 0; from < size; from++ {
				acc = logSumExp(acc, alpha[t-1][from]+h.trans[from][to])
			}
			alpha[t][to] = acc + emit[t][to]
		}
	}

	logLik := logZero
	for s := 0; s < size; s++ {
		logLik = logSumExp(logLik, alpha[len(emit)-1][s])
	}
	return alpha, logLik
}

// backward runs the backward pass, returning the beta trellis.
func (h *hmm) backward(emit [][]float64) [][]float64 {
	size := h.n + 1
	beta := make([][]float64, len(emit))
	for t := range beta {
		beta[t] = make([]float64, size)
	}
	// beta at the final position is log(1) = 0 for every state.
	for t := len(emit) - 2; t >= 0; t-- {
		for from := 0; from < size; from++ {
			acc := logZero
			for to := 0; to < size; to++ {
				acc = logSumExp(acc, h.trans[from][to]+emit[t+1][to]+beta[t+1][to])
			}
			beta[t][from] = acc
		}
	}
	return beta
}

// viterbi returns the most likely state sequence for the given emissions.
func (h *hmm) viterbi(emit [][]float64) []int {
	if len(emit) == 0 {
		return nil
	}
	size := h.n + 1
	delta := make([]float64, size)
	next := make([]float64, size)
	back := make([][]int, len(emit))

	for s := 0; s < size; s++ {
		delta[s] = h.start[s] + emit[0][s]
	}
	for t := 1; t < len(emit); t++ {
		back[t] = make([]int, size)
		for to := 0; to < size; to++ {
			bestVal, bestFrom := math.Inf(-1), 0
			for from := 0; from < size; from++ {
				if v := delta[from] + h.trans[from][to]; v > bestVal {
					bestVal, bestFrom = v, from
				}
			}
			next[to] = bestVal + emit[t][to]
			back[t][to] = bestFrom
		}
		delta, next = next, delta
	}

	last, bestVal := 0, math.Inf(-1)
	for s := 0; s < size; s++ {
		if delta[s] > bestVal {
			bestVal, last = delta[s], s
		}
	}
	path := make([]int, len(emit))
	path[len(emit)-1] = last
	for t := len(emit) - 1; t > 0; t-- {
		path[t-1] = back[t][path[t]]
	}
	return path
}

// posterior returns the per-position marginal probability of each state,
// computed with the forward-backward algorithm. This is what turns a scoring
// heuristic into a calibrated confidence: unlike a weighted sum, the values
// for a position sum to 1 and account for the whole surrounding sequence.
func (h *hmm) posterior(emit [][]float64) [][]float64 {
	if len(emit) == 0 {
		return nil
	}
	size := h.n + 1
	alpha, _ := h.forward(emit)
	beta := h.backward(emit)

	out := make([][]float64, len(emit))
	for t := range emit {
		norm := logZero
		for s := 0; s < size; s++ {
			norm = logSumExp(norm, alpha[t][s]+beta[t][s])
		}
		out[t] = make([]float64, size)
		for s := 0; s < size; s++ {
			if norm <= logZero/2 {
				continue
			}
			out[t][s] = math.Exp(alpha[t][s] + beta[t][s] - norm)
		}
	}
	return out
}

// logSumExp returns log(exp(a) + exp(b)) without underflowing.
func logSumExp(a, b float64) float64 {
	if a <= logZero/2 {
		return b
	}
	if b <= logZero/2 {
		return a
	}
	if a > b {
		return a + math.Log1p(math.Exp(b-a))
	}
	return b + math.Log1p(math.Exp(a-b))
}

// clamp confines a score to [0,1].
func clamp(f float64) float64 {
	switch {
	case f < 0:
		return 0
	case f > 1:
		return 1
	}
	return f
}
