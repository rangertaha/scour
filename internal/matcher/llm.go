// SPDX-License-Identifier: GPL-3.0-or-later

package matcher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/rangertaha/scour/internal/ai"
	"github.com/rangertaha/scour/internal/wom"
)

func init() {
	Register("llm", NewLLM)
}

// The band within which the heuristic is considered undecided.
//
// This is the whole economics of the matcher. Induction scores tens of
// thousands of nodes; asking a model about each would be absurd in both time
// and money. Below the floor the node is plainly not the field, above the
// ceiling it plainly is, and in neither case does a second opinion change the
// outcome.
//
// The values are measured rather than chosen. The heuristic's scores do not
// spread over [0,1]: on the benchmark corpus they cluster between 0 and 0.9
// with a best decision cut near 0.31, so a band of 0.15 to 0.65 caught 61% of
// candidates, which is not a cascade. Centring a narrower band on the observed
// cut is what makes the model a second opinion rather than the first one.
const (
	DefaultFloor   = 0.22
	DefaultCeiling = 0.45
)

// DefaultBudget caps model calls per run. It is a backstop against a corpus
// whose ambiguity is much wider than expected, not a target.
const DefaultBudget = 400

// LLM scores a node by asking a language model, but only when the heuristic
// cannot decide.
//
// Three things keep it affordable, and all three are necessary. The cascade
// means most nodes never reach the model. The cache means a repeated question
// is asked once, which matters because a site's markup repeats on every page.
// The budget means a corpus that defeats both fails cheaply rather than
// expensively.
type LLM struct {
	provider ai.Provider
	base     wom.Matcher
	cache    Cache

	floor   float64
	ceiling float64

	mu     sync.Mutex
	budget int
	calls  int
	hits   int
	misses int
	errs   int
}

// NewLLM builds the matcher.
func NewLLM(cfg Config) (wom.Matcher, error) {
	if cfg.Provider == nil {
		return nil, fmt.Errorf("the llm matcher needs a provider: set matcher in [model] and add an [[ai]] block")
	}

	budget := cfg.Budget
	if budget == 0 {
		budget = DefaultBudget
	}

	cache := cfg.Cache
	if cache == nil {
		cache = &MemoryCache{}
	}

	floor, ceiling := cfg.Floor, cfg.Ceiling
	if floor == 0 && ceiling == 0 {
		floor, ceiling = DefaultFloor, DefaultCeiling
	}
	if floor > ceiling {
		return nil, fmt.Errorf("matcher floor %v is above ceiling %v", floor, ceiling)
	}

	return &LLM{
		provider: cfg.Provider,
		base:     base(cfg),
		cache:    cache,
		floor:    floor,
		ceiling:  ceiling,
		budget:   budget,
	}, nil
}

// Stats reports what one run cost.
type Stats struct {
	// Calls is how many times the model was actually consulted.
	Calls int
	// Hits is how many judgements came from the cache.
	Hits int
	// Misses is how many nodes fell in the undecided band.
	Misses int
	// Errors is how many calls failed and fell back to the heuristic.
	Errors int
}

// Stats returns the counters, so the cost of a matcher can be reported rather
// than assumed.
func (l *LLM) Stats() Stats {
	l.mu.Lock()
	defer l.mu.Unlock()
	return Stats{Calls: l.calls, Hits: l.hits, Misses: l.misses, Errors: l.errs}
}

// Score implements [wom.Matcher].
//
// A model failure is never fatal: the heuristic's own score stands. Induction
// over a few hundred pages should degrade when a model is unreachable, not
// collapse.
func (l *LLM) Score(ctx context.Context, p wom.Prop, n *wom.Node) float64 {
	heuristic := l.base.Score(ctx, p, n)
	if heuristic <= l.floor || heuristic >= l.ceiling {
		return heuristic
	}

	text := strings.TrimSpace(n.Text())
	if text == "" || len(text) > maxValue {
		return heuristic
	}

	l.mu.Lock()
	l.misses++
	l.mu.Unlock()

	key := judgementKey(l.provider.Name(), p, n)
	if score, ok, err := l.cache.Judgement(ctx, key); err == nil && ok {
		l.mu.Lock()
		l.hits++
		l.mu.Unlock()
		return score
	} else if err != nil {
		slog.Debug("judgement cache unavailable", "err", err)
	}

	if !l.spend() {
		return heuristic
	}

	score, err := l.ask(ctx, p, n)
	if err != nil {
		l.mu.Lock()
		l.errs++
		l.mu.Unlock()
		slog.Debug("model judgement failed, keeping the heuristic", "prop", p.Name, "err", err)
		return heuristic
	}

	if err := l.cache.Remember(ctx, key, l.provider.Name(), score); err != nil {
		slog.Debug("could not cache a judgement", "err", err)
	}
	return score
}

// spend claims one call from the budget, reporting whether there was one.
func (l *LLM) spend() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.budget >= 0 && l.calls >= l.budget {
		if l.calls == l.budget {
			// Once, not once per node: a spent budget is a fact about the run,
			// and repeating it per candidate would bury everything else.
			slog.Warn("model judgement budget spent, falling back to the heuristic", "budget", l.budget)
			l.calls++
		}
		return false
	}
	l.calls++
	return true
}

// maxValue bounds the text sent to a model. A field's value is short; a node
// holding a page of prose is a container, not a value, and sending it would
// cost more than the answer is worth.
const maxValue = 300

const system = `You decide what a value extracted from a web page actually is.

Choose the one verdict that fits best:

  exact          the value is this field, plainly
  likely         probably this field
  unrelated      a real value, but of some other field
  not_a_value    a label, a placeholder, or filler such as "N/A" or "call us"

Judge the value itself against the field's meaning, using the surrounding
markup only as evidence. A plausible value in unrelated markup is still a
match; an unrelated value under a matching label is not.`

// The verdicts, and the score each carries.
//
// It is a choice between named alternatives rather than a number, and that is
// not cosmetic. Asked "is this value a price?", a small model agrees: measured
// on a topic-classification set, one answered yes to every page, including the
// recipe, at a confidence of exactly 0.90 each time. Asked to choose a category
// instead, the same model got every one right. A question with only one
// plausible-sounding answer is not a question.
//
// The scores are spread rather than 1/0 so that a verdict still ranks: wom
// compares candidates, so two exact matches have to be separable by the
// evidence around them.
var verdicts = map[string]float64{
	"exact":       0.95,
	"likely":      0.70,
	"unrelated":   0.15,
	"not_a_value": 0.02,
}

// judgement is the shape the model must answer in.
var judgement = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"verdict": map[string]any{
			"type":        "string",
			"enum":        []string{"exact", "likely", "unrelated", "not_a_value"},
			"description": "What the value is, relative to the described field.",
		},
	},
	"required":             []string{"verdict"},
	"additionalProperties": false,
}

type answer struct {
	Verdict string `json:"verdict"`
}

// ask puts one question to the model.
func (l *LLM) ask(ctx context.Context, p wom.Prop, n *wom.Node) (float64, error) {
	raw, err := l.provider.JSON(ctx, ai.Request{
		System:    system,
		Prompt:    prompt(p, n),
		Schema:    judgement,
		MaxTokens: 64,
	})
	if err != nil {
		return 0, err
	}

	var out answer
	if err := json.Unmarshal(raw, &out); err != nil {
		return 0, fmt.Errorf("decode judgement %q: %w", raw, err)
	}

	score, ok := verdicts[strings.ToLower(strings.TrimSpace(out.Verdict))]
	if !ok {
		// The schema constrains this, but a provider that ignores the schema
		// would otherwise land silently on zero and read as a confident no.
		return 0, fmt.Errorf("unknown verdict %q", out.Verdict)
	}
	return score, nil
}

// prompt describes one field and one candidate value.
func prompt(p wom.Prop, n *wom.Node) string {
	var b strings.Builder

	b.WriteString("Field: ")
	b.WriteString(p.Name)
	if p.Type != "" {
		b.WriteString(" (")
		b.WriteString(string(p.Type))
		b.WriteString(")")
	}
	b.WriteString("\n")

	if len(p.Aliases) > 0 {
		b.WriteString("Also called: ")
		b.WriteString(strings.Join(p.Aliases, ", "))
		b.WriteString("\n")
	}
	if p.Description != "" {
		b.WriteString("Meaning: ")
		b.WriteString(p.Description)
		b.WriteString("\n")
	}
	if len(p.Examples) > 0 {
		b.WriteString("Known values: ")
		b.WriteString(strings.Join(trim(p.Examples, 5), ", "))
		b.WriteString("\n")
	}

	b.WriteString("\nValue: ")
	b.WriteString(truncate(strings.TrimSpace(n.Text()), maxValue))
	b.WriteString("\n")

	if ctx := markup(n); ctx != "" {
		b.WriteString("Found in: ")
		b.WriteString(ctx)
		b.WriteString("\n")
	}
	return b.String()
}

// markup describes where a node sits, in the terms a person reading the page
// source would use. It is deliberately short: the model is judging a value,
// not reading a document.
func markup(n *wom.Node) string {
	var parts []string

	if n.Name != "" {
		parts = append(parts, fmt.Sprintf("%s %q", n.Kind, n.Name))
	} else {
		parts = append(parts, n.Kind.String())
	}

	if parent := n.Parent; parent != nil {
		desc := parent.Name
		if class, ok := parent.Attr("class"); ok && class != "" {
			desc += "." + strings.Fields(class)[0]
		} else if id, ok := parent.Attr("id"); ok && id != "" {
			desc += "#" + id
		}
		if desc != "" {
			parts = append(parts, "inside <"+desc+">")
		}
	}
	return strings.Join(parts, " ")
}

// judgementKey identifies a question rather than a node.
//
// Two nodes on different pages with the same value, the same surrounding
// markup and the same field are the same question, and a site that repeats its
// template asks it on every page. Hashing the question rather than the node is
// what turns a corpus-sized bill into a template-sized one.
func judgementKey(model string, p wom.Prop, n *wom.Node) string {
	h := sha256.New()
	write := func(parts ...string) {
		for _, s := range parts {
			h.Write([]byte(s))
			h.Write([]byte{0})
		}
	}

	write(model, p.Name, string(p.Type), p.Description)
	write(p.Aliases...)
	write(p.Examples...)
	write(strings.TrimSpace(n.Text()), n.Kind.String(), n.Name, markup(n))

	return hex.EncodeToString(h.Sum(nil))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func trim(values []string, n int) []string {
	if len(values) <= n {
		return values
	}
	return values[:n]
}
