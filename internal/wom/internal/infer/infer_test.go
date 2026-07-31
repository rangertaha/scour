// SPDX-License-Identifier: MIT

package infer

import "testing"

func TestSupportFactorMonotonic(t *testing.T) {
	t.Parallel()

	if got := supportFactor(0); got != 0 {
		t.Errorf("supportFactor(0) = %v, want 0", got)
	}
	prev := 0.0
	for n := 1; n <= 20; n++ {
		got := supportFactor(n)
		if got < prev {
			t.Errorf("supportFactor(%d) = %v, decreased from %v", n, got, prev)
		}
		if got > 1 {
			t.Errorf("supportFactor(%d) = %v, exceeds 1", n, got)
		}
		prev = got
	}
}
