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

// A field describes its record, so its value changes from one record to the
// next. A location that says the same thing about every record is describing
// the site.
//
// On the news corpus `section` resolved to
// <p class="kicker">Other items that may interest you</p>, a related-articles
// heading, filled on 211 records with one distinct value. The label was not
// wrong: kicker is a real name for a section line and that site uses it for
// furniture. Only the values tell them apart.
func TestVarietyDiscountsALocationThatNeverChanges(t *testing.T) {
	t.Parallel()

	same := group{spread: 200, distinct: 1}
	varied := group{spread: 200, distinct: 19}
	if same.variety() >= varied.variety() {
		t.Errorf("a constant location scored %.2f against a varying one at %.2f",
			same.variety(), varied.variety())
	}

	// A penalty, not a veto: a paper with one reporter has one byline, and it
	// should still win when nothing else is there.
	if same.variety() == 0 {
		t.Error("a constant location must still be able to win unopposed")
	}

	// Below the floor one value is a small sample rather than evidence.
	small := group{spread: monotonyFloor - 1, distinct: 1}
	if small.variety() != 1 {
		t.Errorf("a location seen %d times was discounted for having one value", small.spread)
	}
}
