// SPDX-License-Identifier: MIT

package seq

import (
	"math"
	"testing"
)

func TestHMMPosteriorIsADistribution(t *testing.T) {
	t.Parallel()

	h := newHMM(3)
	scores := [][]float64{
		{0.9, 0.1, 0.1},
		{0.1, 0.8, 0.2},
		{0.1, 0.1, 0.7},
		{0.0, 0.0, 0.0},
	}
	post := h.posterior(emissions(scores, 3))
	if len(post) != len(scores) {
		t.Fatalf("posterior length = %d, want %d", len(post), len(scores))
	}
	for t0, row := range post {
		var sum float64
		for _, p := range row {
			if p < 0 || p > 1 {
				t.Errorf("position %d: probability %v outside [0,1]", t0, p)
			}
			sum += p
		}
		if math.Abs(sum-1) > 1e-9 {
			t.Errorf("position %d: probabilities sum to %v, want 1", t0, sum)
		}
	}
}

func TestHMMViterbiRecoversFieldOrder(t *testing.T) {
	t.Parallel()

	// Three fields appearing in schema order, surrounded by background text
	// that matches nothing.
	scores := [][]float64{
		{0.0, 0.0, 0.0},
		{0.8, 0.1, 0.1},
		{0.1, 0.8, 0.1},
		{0.1, 0.1, 0.8},
		{0.0, 0.0, 0.0},
	}
	path := newHMM(3).viterbi(emissions(scores, 3))
	want := []int{backgroundState, 1, 2, 3, backgroundState}
	for i := range want {
		if path[i] != want[i] {
			t.Errorf("position %d: state = %d, want %d (full path %v)", i, path[i], want[i], path)
		}
	}
}

// TestHMMOrderBreaksTies is the reason the sequence layer exists: two
// positions score identically for two fields, and only the schema order can
// say which is which.
func TestHMMOrderBreaksTies(t *testing.T) {
	t.Parallel()

	scores := [][]float64{
		{0.6, 0.6}, // ambiguous between field 1 and field 2
		{0.1, 0.9}, // clearly field 2
	}
	path := newHMM(2).viterbi(emissions(scores, 2))
	if path[1] != 2 {
		t.Fatalf("second position = %d, want field 2", path[1])
	}
	if path[0] != 1 {
		t.Errorf("first position = %d, want field 1 resolved by order (path %v)", path[0], path)
	}
}

func TestHMMRefineBlendsWithScores(t *testing.T) {
	t.Parallel()

	regions := [][][]float64{{
		{0.9, 0.0},
		{0.0, 0.9},
	}}
	out := HMM{}.Refine(regions, 2)
	if len(out) != 1 || len(out[0]) != 2 || len(out[0][0]) != 2 {
		t.Fatalf("shape = %v, want 1x2x2", out)
	}
	// The chain agrees with the scores here, so the winners must survive.
	if out[0][0][0] <= out[0][0][1] {
		t.Errorf("position 0: field 1 (%v) should beat field 2 (%v)", out[0][0][0], out[0][0][1])
	}
	if out[0][1][1] <= out[0][1][0] {
		t.Errorf("position 1: field 2 (%v) should beat field 1 (%v)", out[0][1][1], out[0][1][0])
	}
	for i, row := range out[0] {
		for j, v := range row {
			if v < 0 || v > 1 {
				t.Errorf("out[%d][%d] = %v, outside [0,1]", i, j, v)
			}
		}
	}
}

func TestHMMRefineEmptyInput(t *testing.T) {
	t.Parallel()

	if got := (HMM{}).Refine(nil, 3); got != nil {
		t.Errorf("Refine(nil) = %v, want nil", got)
	}
	if got := (HMM{}).Refine([][][]float64{{{0.5}}}, 0); len(got) != 1 {
		t.Errorf("Refine with zero fields should pass regions through, got %v", got)
	}
}

// TestHMMTrainsFieldOrder is the point of training: the seeded prior assumes
// schema order, and the data says otherwise. After fitting, the chain should
// prefer what it actually saw.
func TestHMMTrainsFieldOrder(t *testing.T) {
	t.Parallel()

	// Every record puts field 2 before field 1, the reverse of schema order.
	var regions [][][]float64
	for i := 0; i < 12; i++ {
		regions = append(regions, [][]float64{
			{0.1, 0.85},
			{0.85, 0.1},
			{0.0, 0.0},
		})
	}

	seeded := newHMM(2)
	trained := newHMM(2)
	emits := make([][][]float64, 0, len(regions))
	for _, r := range regions {
		emits = append(emits, emissions(r, 2))
	}
	trained.train(emits, defaultIterations, defaultTolerance)

	// 2 -> 1 was the unlikely direction under the prior and is the observed
	// one, so training must have raised it.
	if trained.trans[2][1] <= seeded.trans[2][1] {
		t.Errorf("trained P(1|2) = %v, want above the seeded %v",
			trained.trans[2][1], seeded.trans[2][1])
	}
	for i, row := range trained.trans {
		var sum float64
		for _, lp := range row {
			sum += expOrZero(lp)
		}
		if sum < 0.999 || sum > 1.001 {
			t.Errorf("trained row %d sums to %v, want 1", i, sum)
		}
	}
}

// TestHMMTrainingIsRegularized checks the safeguard that makes fitting on very
// little data safe: one record must not be enough to overwrite the prior.
func TestHMMTrainingIsRegularized(t *testing.T) {
	t.Parallel()

	regions := [][][]float64{{
		{0.9, 0.0},
		{0.0, 0.9},
	}}
	emits := [][][]float64{emissions(regions[0], 2)}

	seeded := newHMM(2)
	trained := newHMM(2)
	trained.train(emits, defaultIterations, defaultTolerance)

	// The prior still dominates, so schema order survives a single record.
	if trained.trans[1][2] <= trained.trans[2][1] {
		t.Errorf("after one record, P(2|1)=%v should still exceed P(1|2)=%v",
			trained.trans[1][2], trained.trans[2][1])
	}
	if math.Abs(trained.trans[1][2]-seeded.trans[1][2]) > 1.0 {
		t.Errorf("one record moved P(2|1) from %v to %v, too far",
			seeded.trans[1][2], trained.trans[1][2])
	}
}

func TestChainPriorRoundTrip(t *testing.T) {
	t.Parallel()

	prior := DefaultChainPrior(3)
	if !prior.valid(3) {
		t.Fatalf("DefaultChainPrior(3) is not valid: %+v", prior)
	}
	if prior.valid(2) {
		t.Error("a 3-field prior should not validate against 2 fields")
	}
	if DefaultChainPrior(0) != nil {
		t.Error("DefaultChainPrior(0) should be nil")
	}

	restored := newHMM(3)
	restored.loadPrior(prior)
	original := newHMM(3)
	for i := range original.trans {
		for j := range original.trans[i] {
			if math.Abs(restored.trans[i][j]-original.trans[i][j]) > 1e-9 {
				t.Fatalf("trans[%d][%d] = %v after round trip, want %v",
					i, j, restored.trans[i][j], original.trans[i][j])
			}
		}
	}
}

func TestLogSumExp(t *testing.T) {
	t.Parallel()

	got := logSumExp(math.Log(0.3), math.Log(0.7))
	if math.Abs(got-math.Log(1.0)) > 1e-12 {
		t.Errorf("logSumExp(log .3, log .7) = %v, want %v", got, math.Log(1.0))
	}
	if got := logSumExp(logZero, math.Log(0.5)); math.Abs(got-math.Log(0.5)) > 1e-12 {
		t.Errorf("logSumExp with a zero term = %v, want log(0.5)", got)
	}
}
