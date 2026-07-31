// SPDX-License-Identifier: GPL-3.0-or-later

package matcher

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/rangertaha/scour/internal/ai"
	"github.com/rangertaha/scour/internal/wom"
)

// stub answers with a fixed verdict and counts how often it was asked.
type stub struct {
	verdict string
	err     error
	calls   atomic.Int64

	mu      sync.Mutex
	prompts []string
}

func (s *stub) Name() string { return "stub" }

func (s *stub) JSON(_ context.Context, req ai.Request) ([]byte, error) {
	s.calls.Add(1)
	s.mu.Lock()
	s.prompts = append(s.prompts, req.Prompt)
	s.mu.Unlock()

	if s.err != nil {
		return nil, s.err
	}
	verdict := s.verdict
	if verdict == "" {
		verdict = "exact"
	}
	return []byte(fmt.Sprintf(`{"verdict": %q}`, verdict)), nil
}

func (s *stub) seen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.prompts...)
}

// fixed is a base matcher that always returns the same score, so a test can
// place a node precisely inside or outside the undecided band.
type fixed float64

func (f fixed) Score(context.Context, wom.Prop, *wom.Node) float64 { return float64(f) }

// page builds a graph from HTML and returns the node holding the given text.
func page(t *testing.T, html, text string) *wom.Node {
	t.Helper()

	w := wom.New()
	if err := w.AddBody("http://example.com/cars/1/", "text/html", []byte(html)); err != nil {
		t.Fatal(err)
	}

	var found *wom.Node
	w.Root().Walk(func(n *wom.Node) bool {
		if found == nil && n.Kind.HoldsValue() && strings.TrimSpace(n.Text()) == text {
			found = n
		}
		return true
	})
	if found == nil {
		t.Fatalf("no node holding %q", text)
	}
	return found
}

const doc = `<html><body>
<div class="vehicle"><dt>Make</dt><dd class="make">Ford</dd>
<span class="price">$42,000</span></div>
</body></html>`

func prop() wom.Prop {
	return wom.Prop{Name: "make", Aliases: []string{"manufacturer"}, Examples: []string{"Toyota"}}
}

// newLLM builds a matcher with an explicit band, so these tests describe
// behaviour rather than track whatever the tuned defaults currently are.
func newLLM(t *testing.T, cfg Config) *LLM {
	t.Helper()
	if cfg.Floor == 0 && cfg.Ceiling == 0 {
		cfg.Floor, cfg.Ceiling = 0.2, 0.6
	}
	m, err := NewLLM(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return m.(*LLM)
}

func TestConfidentHeuristicNeverReachesTheModel(t *testing.T) {
	node := page(t, doc, "Ford")

	for _, score := range []float64{0.0, 0.05, 0.2, 0.6, 0.9, 1.0} {
		provider := &stub{verdict: "unrelated"}
		l := newLLM(t, Config{Provider: provider, Base: fixed(score)})

		if got := l.Score(context.Background(), prop(), node); got != score {
			t.Errorf("heuristic %v was overridden with %v", score, got)
		}
		if provider.calls.Load() != 0 {
			t.Errorf("the model was asked about a node scored %v, which needed no second opinion", score)
		}
	}
}

func TestUndecidedNodesAreJudged(t *testing.T) {
	node := page(t, doc, "Ford")
	provider := &stub{verdict: "exact"}
	l := newLLM(t, Config{Provider: provider, Base: fixed(0.4)})

	if got := l.Score(context.Background(), prop(), node); got != verdicts["exact"] {
		t.Errorf("score = %v, want the exact verdict's %v", got, verdicts["exact"])
	}
	if provider.calls.Load() != 1 {
		t.Errorf("model called %d times, want once", provider.calls.Load())
	}

	// The question has to carry enough to answer it.
	seen := provider.seen()[0]
	for _, want := range []string{"make", "manufacturer", "Toyota", "Ford"} {
		if !strings.Contains(seen, want) {
			t.Errorf("the prompt never mentions %q:\n%s", want, seen)
		}
	}
}

// The cache is what makes this affordable on a real corpus, where a site's
// markup repeats on every page.
func TestTheSameQuestionIsAskedOnce(t *testing.T) {
	provider := &stub{verdict: "likely"}
	l := newLLM(t, Config{Provider: provider, Base: fixed(0.4)})

	for range 20 {
		// A fresh graph each time, as a fresh page would be: different nodes,
		// same question.
		node := page(t, doc, "Ford")
		if got := l.Score(context.Background(), prop(), node); got != verdicts["likely"] {
			t.Fatalf("score = %v", got)
		}
	}

	if provider.calls.Load() != 1 {
		t.Errorf("model called %d times for 20 identical questions", provider.calls.Load())
	}
	if stats := l.Stats(); stats.Hits != 19 || stats.Misses != 20 {
		t.Errorf("stats = %+v, want 20 undecided and 19 served from cache", stats)
	}
}

func TestDifferentValuesAreDifferentQuestions(t *testing.T) {
	provider := &stub{verdict: "likely"}
	l := newLLM(t, Config{Provider: provider, Base: fixed(0.4)})

	ctx := context.Background()
	l.Score(ctx, prop(), page(t, doc, "Ford"))
	l.Score(ctx, prop(), page(t, doc, "$42,000"))

	if provider.calls.Load() != 2 {
		t.Errorf("model called %d times for two different values", provider.calls.Load())
	}
}

func TestBudgetIsAHardCeiling(t *testing.T) {
	provider := &stub{verdict: "exact"}
	l := newLLM(t, Config{Provider: provider, Base: fixed(0.4), Budget: 3})

	ctx := context.Background()
	for i := range 10 {
		// Distinct values, so the cache cannot absorb them and the budget is
		// the only thing standing between this and ten calls.
		node := page(t, fmt.Sprintf(`<html><body><dd class="make">Ford %d</dd></body></html>`, i), fmt.Sprintf("Ford %d", i))
		l.Score(ctx, prop(), node)
	}

	if got := provider.calls.Load(); got != 3 {
		t.Errorf("model called %d times against a budget of 3", got)
	}
}

func TestBudgetExhaustionFallsBackRatherThanFailing(t *testing.T) {
	provider := &stub{verdict: "exact"}
	l := newLLM(t, Config{Provider: provider, Base: fixed(0.4), Budget: 1})

	ctx := context.Background()
	l.Score(ctx, prop(), page(t, `<html><body><dd>Ford</dd></body></html>`, "Ford"))

	node := page(t, `<html><body><dd>Ram</dd></body></html>`, "Ram")
	if got := l.Score(ctx, prop(), node); got != 0.4 {
		t.Errorf("past the budget the score was %v, want the heuristic's 0.4", got)
	}
}

// A model that is down should degrade induction, not stop it.
func TestAModelFailureKeepsTheHeuristic(t *testing.T) {
	provider := &stub{err: errors.New("connection refused")}
	l := newLLM(t, Config{Provider: provider, Base: fixed(0.4)})

	node := page(t, doc, "Ford")
	if got := l.Score(context.Background(), prop(), node); got != 0.4 {
		t.Errorf("score = %v, want the heuristic's 0.4", got)
	}
	if stats := l.Stats(); stats.Errors != 1 {
		t.Errorf("errors = %d, want 1", stats.Errors)
	}
}

// Every verdict has to map to a score, and they have to stay ordered, or the
// matcher would rank a rejection above a match.
func TestVerdictsMapToOrderedScores(t *testing.T) {
	node := page(t, doc, "Ford")

	prev := -1.0
	for _, verdict := range []string{"not_a_value", "unrelated", "likely", "exact"} {
		l := newLLM(t, Config{Provider: &stub{verdict: verdict}, Base: fixed(0.4)})

		got := l.Score(context.Background(), prop(), node)
		if got != verdicts[verdict] {
			t.Errorf("%s produced %v, want %v", verdict, got, verdicts[verdict])
		}
		if got <= prev {
			t.Errorf("%s scored %v, not above the previous %v", verdict, got, prev)
		}
		if got < 0 || got > 1 {
			t.Errorf("%s scored %v, outside [0,1]", verdict, got)
		}
		prev = got
	}
}

// A provider that ignores the schema must not land silently on zero, which
// would read as a confident rejection rather than as a broken answer.
func TestAnUnknownVerdictKeepsTheHeuristic(t *testing.T) {
	l := newLLM(t, Config{Provider: &stub{verdict: "banana"}, Base: fixed(0.4)})

	if got := l.Score(context.Background(), prop(), page(t, doc, "Ford")); got != 0.4 {
		t.Errorf("score = %v, want the heuristic's 0.4", got)
	}
	if stats := l.Stats(); stats.Errors != 1 {
		t.Errorf("errors = %d, want the bad answer counted", stats.Errors)
	}
}

// Induction scores nodes concurrently, so the counters and the cache have to
// hold up under it.
func TestConcurrentScoring(t *testing.T) {
	provider := &stub{verdict: "likely"}
	l := newLLM(t, Config{Provider: provider, Base: fixed(0.4)})

	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			node := page(t, doc, "Ford")
			l.Score(context.Background(), prop(), node)
		}()
	}
	wg.Wait()

	if stats := l.Stats(); stats.Misses != 16 {
		t.Errorf("stats = %+v, want 16 undecided", stats)
	}
}

func TestALargeValueIsNotSentToTheModel(t *testing.T) {
	provider := &stub{verdict: "exact"}
	l := newLLM(t, Config{Provider: provider, Base: fixed(0.4)})

	// A node holding a page of prose is a container, not a field value.
	prose := strings.Repeat("a long paragraph of body copy. ", 40)
	node := page(t, "<html><body><p>"+prose+"</p></body></html>", strings.TrimSpace(prose))

	l.Score(context.Background(), prop(), node)
	if provider.calls.Load() != 0 {
		t.Error("a page of prose was sent to the model as a candidate value")
	}
}

// The shipped band is what production uses, so it is worth asserting it is
// coherent rather than only that a test-chosen band works.
func TestDefaultBandIsUsable(t *testing.T) {
	if DefaultFloor >= DefaultCeiling {
		t.Fatalf("floor %v is not below ceiling %v", DefaultFloor, DefaultCeiling)
	}
	if DefaultFloor <= 0 || DefaultCeiling >= 1 {
		t.Errorf("band [%v, %v] should sit inside (0,1), or it decides nothing",
			DefaultFloor, DefaultCeiling)
	}

	provider := &stub{verdict: "exact"}
	m, err := NewLLM(Config{Provider: provider, Base: fixed((DefaultFloor + DefaultCeiling) / 2)})
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Score(context.Background(), prop(), page(t, doc, "Ford")); got != verdicts["exact"] {
		t.Errorf("a score mid-band was not judged: got %v", got)
	}
}

func TestAnInvertedBandIsRejected(t *testing.T) {
	if _, err := NewLLM(Config{Provider: &stub{}, Floor: 0.8, Ceiling: 0.2}); err == nil {
		t.Error("a floor above the ceiling should be an error, not a matcher that never asks")
	}
}

func TestLLMNeedsAProvider(t *testing.T) {
	if _, err := NewLLM(Config{}); err == nil {
		t.Error("a matcher with no provider should say so rather than fail later")
	}
}

func TestRegistry(t *testing.T) {
	if !Has("heuristic") || !Has("llm") {
		t.Errorf("registered matchers = %v", Names())
	}

	m, err := New("", Config{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m.(wom.Heuristic); !ok {
		t.Errorf("the default matcher is %T, want the heuristic", m)
	}
	if _, err := New("nonexistent", Config{}); err == nil {
		t.Error("an unknown matcher must be an error")
	}
}
