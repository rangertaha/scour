// SPDX-License-Identifier: GPL-3.0-or-later

package store

import (
	"context"
	"testing"
)

func TestJudgementsAreBoughtOnce(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	if _, ok, err := s.Judgement(ctx, "unseen"); err != nil || ok {
		t.Fatalf("a fresh store returned a judgement: %v, %v", ok, err)
	}

	if err := s.RememberJudgement(ctx, "k1", "ollama:gemma3:270m", 0.8); err != nil {
		t.Fatal(err)
	}

	for i := range 3 {
		score, ok, err := s.Judgement(ctx, "k1")
		if err != nil || !ok {
			t.Fatalf("read %d: %v, %v", i, ok, err)
		}
		if score != 0.8 {
			t.Errorf("score = %v, want 0.8", score)
		}
	}

	cached, reused, err := s.JudgementStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cached != 1 {
		t.Errorf("cached = %d, want 1", cached)
	}
	// Three reads of one cached answer is three calls not made.
	if reused != 3 {
		t.Errorf("reused = %d, want 3", reused)
	}
}

// Re-answering a question must update it, not duplicate it, or the cache would
// grow without bound and reads would become ambiguous.
func TestRememberingTwiceUpdatesInPlace(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	if err := s.RememberJudgement(ctx, "k1", "m", 0.2); err != nil {
		t.Fatal(err)
	}
	if err := s.RememberJudgement(ctx, "k1", "m", 0.9); err != nil {
		t.Fatal(err)
	}

	score, ok, err := s.Judgement(ctx, "k1")
	if err != nil || !ok {
		t.Fatal(err)
	}
	if score != 0.9 {
		t.Errorf("score = %v, want the newer 0.9", score)
	}
	if cached, _, _ := s.JudgementStats(ctx); cached != 1 {
		t.Errorf("cached = %d, want one row", cached)
	}
}

func TestForgettingJudgements(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	for _, k := range []string{"a", "b", "c"} {
		if err := s.RememberJudgement(ctx, k, "m", 0.5); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.ForgetJudgements(ctx); err != nil {
		t.Fatal(err)
	}

	cached, _, err := s.JudgementStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cached != 0 {
		t.Errorf("cached = %d after forgetting", cached)
	}
}
