// SPDX-License-Identifier: GPL-3.0-or-later

package score_test

import (
	"testing"

	"github.com/rangertaha/scour/internal/matcher"
	"github.com/rangertaha/scour/internal/score"

	// The url scorers live in their own packages and register from init, so a
	// binary that never imports them has none. That is the contract, and
	// importing them here is what a real caller does.
	_ "github.com/rangertaha/scour/internal/score/bayes"
)

// scour ranks nodes of more than one graph, and the registries have to say
// which. They looked unrelated because one of them was called a matcher.
func TestEachRegistryDeclaresWhatItRanks(t *testing.T) {
	if score.Ranks != score.KindURL {
		t.Errorf("score ranks %q, want url", score.Ranks)
	}
	if matcher.Ranks != score.KindDocument {
		t.Errorf("matcher ranks %q, want document", matcher.Ranks)
	}
	if score.Ranks == matcher.Ranks {
		t.Error("two graphs, two node kinds: these must differ")
	}
}

// Kinds is the one answer to "what can be scored, and by what", so it has to
// name a registry that exists for every kind it lists.
func TestKindsNamesAHomeForEveryNodeKind(t *testing.T) {
	kinds := score.Kinds()
	for _, k := range []score.Kind{score.KindURL, score.KindDocument} {
		where, ok := kinds[k]
		if !ok {
			t.Errorf("no registry listed for %q", k)
			continue
		}
		if where == "" {
			t.Errorf("%q lists an empty location", k)
		}
	}
	if len(kinds) != 2 {
		t.Errorf("Kinds lists %d entries; add the registry when adding a kind", len(kinds))
	}
}

// Both registries answer the same four questions, which is what makes them one
// kind of extension over different graphs.
func TestBothRegistriesAnswerTheSameQuestions(t *testing.T) {
	if len(score.Names()) == 0 {
		t.Error("no url scorers registered")
	}
	if len(matcher.Names()) == 0 {
		t.Error("no document scorers registered")
	}
	// An empty name is how configuration that says nothing reaches a working
	// default, and both registries have one.
	if !score.Has("") {
		t.Error("the url registry has no default, with bayes imported")
	}
	if !matcher.Has("") {
		t.Error("the document registry has no default")
	}
}
