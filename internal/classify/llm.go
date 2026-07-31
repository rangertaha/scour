// SPDX-License-Identifier: GPL-3.0-or-later

package classify

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
)

func init() {
	Register("llm", NewLLM)
}

// DefaultBudget caps how many pages one run classifies.
//
// A page takes most of a second on a small local model, so a thousand pages is
// a quarter of an hour. That is affordable once and absurd on every retrain,
// which is what the cache is for; the budget is the backstop for the first run
// over a corpus larger than expected.
const DefaultBudget = 500

// LLM classifies a page by asking a language model.
type LLM struct {
	provider ai.Provider
	cache    Cache

	mu     sync.Mutex
	budget int
	calls  int
	hits   int
	errs   int
}

// NewLLM builds the classifier.
func NewLLM(cfg Config) (Classifier, error) {
	provider, ok := cfg.Provider.(ai.Provider)
	if !ok || provider == nil {
		return nil, fmt.Errorf("the llm classifier needs a provider: add an [[ai]] block and name it in [model]")
	}

	budget := cfg.Budget
	if budget == 0 {
		budget = DefaultBudget
	}
	cache := cfg.Cache
	if cache == nil {
		cache = &MemoryCache{}
	}

	return &LLM{provider: provider, cache: cache, budget: budget}, nil
}

// Name implements [Classifier].
func (l *LLM) Name() string { return "llm:" + l.provider.Name() }

// Stats reports what a run cost.
type Stats struct {
	Calls  int
	Hits   int
	Errors int
}

// Stats returns the counters.
func (l *LLM) Stats() Stats {
	l.mu.Lock()
	defer l.mu.Unlock()
	return Stats{Calls: l.calls, Hits: l.hits, Errors: l.errs}
}

// ErrBudgetSpent is returned once a run has classified all it may.
//
// It is an error rather than a silent default because the caller has to be
// able to tell "this page is not relevant" from "nobody looked", and treating
// the second as the first would poison the training labels this exists to
// improve.
var ErrBudgetSpent = fmt.Errorf("classification budget spent")

// Classify implements [Classifier].
func (l *LLM) Classify(ctx context.Context, topic Topic, page Page) (Category, error) {
	text := strings.TrimSpace(page.Text)
	if text == "" {
		// Nothing to read is not a subject page, and asking a model about an
		// empty string would be paying for a guess.
		return OtherSubject, nil
	}

	key := verdictKey(l.provider.Name(), topic, page)
	if cached, ok, err := l.cache.Verdict(ctx, key); err == nil && ok {
		l.mu.Lock()
		l.hits++
		l.mu.Unlock()

		if c := Category(cached); c.Valid() {
			return c, nil
		}
	} else if err != nil {
		slog.Debug("verdict cache unavailable", "err", err)
	}

	if !l.spend() {
		return "", ErrBudgetSpent
	}

	category, err := l.ask(ctx, topic, page)
	if err != nil {
		l.mu.Lock()
		l.errs++
		l.mu.Unlock()
		return "", err
	}

	if err := l.cache.Remember(ctx, key, l.provider.Name(), string(category)); err != nil {
		slog.Debug("could not cache a verdict", "err", err)
	}
	return category, nil
}

func (l *LLM) spend() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.budget >= 0 && l.calls >= l.budget {
		return false
	}
	l.calls++
	return true
}

// maxText is how much of a page is sent.
//
// The opening of a page says what it is: the title, the heading, the first
// values. Sending the rest costs tokens and time to confirm what the first
// paragraph already established, and on a small model a long prompt makes the
// answer worse rather than better.
const maxText = 1500

const system = `You identify a web page's own subject matter.

Answer with the single category that best describes what the page is about. If
the page is not about any of the listed subjects, answer other_subject.`

// verdict is the shape the model must answer in.
//
// The subject category is named after the item rather than called "subject",
// because the measured difference between this working and not is whether the
// model is recognising a topic it knows or resolving a pronoun. "vehicle" is
// something a model has seen; "the subject" is a referent it has to track.
func verdictSchema(subjects []string) map[string]any {
	enum := append([]string{}, subjects...)
	enum = append(enum,
		string(ContactOrAbout), string(LegalOrPrivacy),
		string(ArticleOrGuide), string(OtherSubject),
	)

	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"subject": map[string]any{"type": "string", "enum": enum},
		},
		"required":             []string{"subject"},
		"additionalProperties": false,
	}
}

// answer is deliberately only the category.
//
// Asking a small model for its confidence produces a number it likes rather
// than a probability: measured across ten pages one returned exactly 0.90 every
// time, including on the four it got wrong. A field that is always the same
// value is not evidence, and carrying it would invite someone to threshold on
// it.
type answer struct {
	Subject string `json:"subject"`
}

func (l *LLM) ask(ctx context.Context, topic Topic, page Page) (Category, error) {
	names := subjectNames(topic)

	raw, err := l.provider.JSON(ctx, ai.Request{
		System:    system,
		Prompt:    prompt(topic, page),
		Schema:    verdictSchema(names),
		MaxTokens: 32,
	})
	if err != nil {
		return "", err
	}

	var out answer
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("decode verdict %q: %w", raw, err)
	}

	answered := strings.ToLower(strings.TrimSpace(out.Subject))
	for _, name := range names {
		if answered == name {
			return Subject, nil
		}
	}

	category := Category(answered)
	if !category.Valid() {
		return "", fmt.Errorf("unknown subject %q", out.Subject)
	}
	return category, nil
}

// subjectNames is what the model may answer to mean "this is the subject".
//
// Only the item's name, deliberately, and that is a measured choice rather
// than a simplification. Offering the aliases as well seems obviously helpful
// and is not: on a ten-page corpus, name alone scored 9 with no false
// positives, while name plus aliases scored 7 with three, calling the recipe,
// the privacy notice and the about page all on topic. More ways to say yes is
// the assent bias again, wearing a different hat.
//
// The cost is that the item's name carries the whole question. Named after
// what it actually is, the classifier scores 9 of 10; named "api-cars" or
// "proj7" it scores 2 or 6, because a coined word is not something a model can
// recognise. That is worth knowing before turning this on, and it is what the
// benchmark in this package measures.
func subjectNames(topic Topic) []string {
	seen := map[string]bool{}
	var out []string

	add := func(raw string) {
		name := strings.Map(func(r rune) rune {
			if r == ' ' || r == '\t' {
				return '_'
			}
			return r
		}, strings.TrimSpace(strings.ToLower(raw)))

		// A name colliding with one of the fixed categories would make the
		// answer ambiguous, and the fixed ones have to keep their meaning.
		if name == "" || seen[name] || Category(name).Valid() {
			return
		}
		seen[name] = true
		out = append(out, name)
	}

	add(topic.Name)
	if len(out) == 0 {
		return []string{"subject"}
	}
	return out
}

// prompt asks what the page is about.
//
// It deliberately does not describe the subject, list its fields, or explain
// what a record looks like. Every one of those was in an earlier version, and
// removing them changed nothing measurable while adding them gave the model
// more to resolve before it could answer a question that is fundamentally
// recognition.
func prompt(topic Topic, page Page) string {
	var b strings.Builder

	if page.Title != "" {
		b.WriteString("Page title: ")
		b.WriteString(page.Title)
		b.WriteString("\n\n")
	}

	b.WriteString("Page text:\n")
	b.WriteString(truncate(strings.TrimSpace(page.Text), maxText))
	b.WriteString("\n\nWhat is this page about?")

	return b.String()
}

// verdictKey identifies the question rather than the page.
//
// Keyed on the text rather than the URL, so a page that has not changed
// between crawls is not re-read, and two URLs serving the same content are one
// question.
func verdictKey(model string, topic Topic, page Page) string {
	h := sha256.New()
	write := func(parts ...string) {
		for _, s := range parts {
			h.Write([]byte(s))
			h.Write([]byte{0})
		}
	}

	// Only what the prompt actually contains: the subject name and the page.
	// Hashing the aliases and fields too would miss the cache every time a
	// property was added, for an answer that would not have changed.
	write(model)
	write(subjectNames(topic)...)
	write(page.Title, truncate(strings.TrimSpace(page.Text), maxText))

	return hex.EncodeToString(h.Sum(nil))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
