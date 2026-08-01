// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"strings"
	"testing"
)

// A mark is a person's verdict on a record. It is not a label, because a label
// is a tag: the words a page might name a property with.
func TestMarkRecordsRightAndWrong(t *testing.T) {
	dir, _ := trained(t)
	runOK(t, dir, "train", "vehicle")

	out := runOK(t, dir, "mark", "vehicle", "1", "--invalid")
	if !strings.Contains(out, "marked invalid") {
		t.Fatalf("marking did not report what it did:\n%s", out)
	}
	// Nothing is confirmed yet, so the chain still cannot be fitted, and that
	// is the one thing worth saying at this point.
	if !strings.Contains(out, "--valid") {
		t.Errorf("marking only wrong should say the chain needs a valid mark:\n%s", out)
	}

	only := runOK(t, dir, "stream", "vehicle", "--marked", "invalid")
	if !strings.Contains(only, "showing 1 of 1") {
		t.Errorf("filtering by verdict did not find the marked record:\n%s", only)
	}

	// --label is the old spelling of --marked and still works.
	if alias := runOK(t, dir, "stream", "vehicle", "--label", "invalid"); alias != only {
		t.Errorf("--label and --marked disagree:\n%s\n%s", alias, only)
	}
}

// A verdict survives retraining, and so does the id it was given to.
func TestMarkSurvivesRetraining(t *testing.T) {
	dir, _ := trained(t)
	runOK(t, dir, "train", "vehicle")
	runOK(t, dir, "mark", "vehicle", "1", "--valid")

	out := runOK(t, dir, "train", "vehicle")
	if !strings.Contains(out, "marks") {
		t.Errorf("training did not report the marks it fed back:\n%s", out)
	}

	after := runOK(t, dir, "stream", "vehicle", "--marked", "valid")
	if !strings.Contains(after, "showing 1 of 1") {
		t.Errorf("the mark was lost by retraining:\n%s", after)
	}
}

// Clearing puts a record back, for a verdict given in error.
func TestMarkClearPutsItBack(t *testing.T) {
	dir, _ := trained(t)
	runOK(t, dir, "train", "vehicle")

	runOK(t, dir, "mark", "vehicle", "1", "--valid")
	out := runOK(t, dir, "mark", "vehicle", "1", "--clear")
	if !strings.Contains(out, "unmarked") {
		t.Errorf("clearing did not say so:\n%s", out)
	}
	if left := runOK(t, dir, "stream", "vehicle", "--marked", "valid"); !strings.Contains(left, "no records matched") {
		t.Errorf("the verdict outlived the clear:\n%s", left)
	}
}

// The three verdicts are alternatives, and a record holds one.
func TestMarkNeedsExactlyOneVerdict(t *testing.T) {
	dir, _ := trained(t)
	runOK(t, dir, "train", "vehicle")

	for _, args := range [][]string{
		{"mark", "vehicle", "1"},
		{"mark", "vehicle", "1", "--valid", "--invalid"},
		{"mark", "vehicle", "1", "--valid", "--clear"},
	} {
		if _, err := run(t, dir, args...); err == nil {
			t.Errorf("scour %s should have failed", strings.Join(args, " "))
		}
	}
	if _, err := run(t, dir, "mark", "vehicle", "9999", "--valid"); err == nil {
		t.Error("marking a record that is not there must fail")
	}
	if _, err := run(t, dir, "mark", "vehicle", "not-a-number", "--valid"); err == nil {
		t.Error("a non-numeric id must fail")
	}
	if _, err := run(t, dir, "mark", "vehicle"); err == nil {
		t.Error("mark with no ids must fail")
	}
}
