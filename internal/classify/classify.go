// SPDX-License-Identifier: GPL-3.0-or-later

// Package classify decides what a fetched page is about.
//
// It exists to break a circularity. A page counts as worth crawling if
// extraction found records in it, and extraction only works once induction has
// learned where the fields are, which it does from pages the crawl already
// decided were worth fetching. On the first crawl of a new site none of that
// has happened, so every page looks equally unpromising and the scorer learns
// from labels that are mostly noise.
//
// Reading the page and saying what it is breaks the loop: a listing page is a
// listing page whether or not the rules can yet pull a price out of it.
package classify

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Category is what a page turned out to be about.
//
// These name subject matter, not the page's relationship to a subject, and
// that distinction is the whole design. The first version asked for the
// relational categories a crawler naturally wants, detail / listing / related /
// unrelated, and a small model answered "detail" for every page on a mixed
// corpus: the recipe, the privacy policy, the about page, all of it. Reversing
// the enum order changed nothing and simplifying the subject changed nothing,
// because the question itself was the problem. Deciding how a page relates to a
// described subject is abstract reasoning; saying what a page is about is
// recognition, and a 270M model can do the second and not the first.
//
// Measured on the same five pages: relational categories scored 1 of 5,
// subject-matter categories scored 5 of 5 on the relevance question that
// actually matters.
type Category string

// The categories every page is sorted into. Subject is filled in per entity
// with the thing being hunted; the rest are the kinds of page every site has
// that are not it.
const (
	// Subject means the page is about the thing being collected.
	Subject Category = "subject"
	// ContactOrAbout is address, opening hours, who we are.
	ContactOrAbout Category = "contact_or_about"
	// LegalOrPrivacy is terms, privacy notices, cookie policy.
	LegalOrPrivacy Category = "legal_or_privacy"
	// ArticleOrGuide is prose: news, help, an explainer.
	ArticleOrGuide Category = "article_or_guide"
	// OtherSubject is a different subject entirely.
	OtherSubject Category = "other_subject"
)

// Categories lists them in the order the prompt presents them.
var Categories = []Category{Subject, ContactOrAbout, LegalOrPrivacy, ArticleOrGuide, OtherSubject}

// Valid reports whether a category is one of the known set.
func (c Category) Valid() bool {
	for _, known := range Categories {
		if c == known {
			return true
		}
	}
	return false
}

// Relevant reports whether a page of this category is worth having crawled.
//
// Only the subject itself counts. An earlier version also counted articles
// about the subject, on the reasoning that a review links to the records; that
// is true, but it is the crawl chain's job to credit a page for where it leads,
// and doing it here as well would double-count the same evidence.
func (c Category) Relevant() bool { return c == Subject }

// Topic is what the page is being judged against.
type Topic struct {
	// Name is the entity's name.
	Name string
	// Aliases are the other words a page might use for it.
	Aliases []string
	// Fields are the properties a record should have, which is what separates
	// a page holding a record from a page merely discussing the subject.
	Fields []string
}

// Describe renders the topic for a prompt.
func (t Topic) Describe() string {
	var b strings.Builder
	b.WriteString(t.Name)
	if len(t.Aliases) > 0 {
		b.WriteString(" (also called ")
		b.WriteString(strings.Join(t.Aliases, ", "))
		b.WriteString(")")
	}
	if len(t.Fields) > 0 {
		b.WriteString(", described by ")
		b.WriteString(strings.Join(t.Fields, ", "))
	}
	return b.String()
}

// Page is what is being classified.
type Page struct {
	URL   string
	Title string
	Text  string
}

// Classifier says what a page is.
type Classifier interface {
	// Name identifies the implementation, for logs and reports.
	Name() string
	// Classify reads a page and returns its category.
	Classify(ctx context.Context, topic Topic, page Page) (Category, error)
}

// Config is what a classifier is built from.
type Config struct {
	// Provider is the model to consult, for classifiers that consult one.
	Provider any
	// Cache remembers verdicts between runs.
	Cache Cache
	// Budget caps how many pages one run may classify. Zero is the
	// implementation's default; negative means no limit.
	Budget int
}

// Cache remembers what a classifier said, keyed by a hash of the question.
//
// Pages are re-read on every retrain, and a corpus does not change between
// them, so without this the second training run would pay the whole bill
// again for answers it already has.
type Cache interface {
	Verdict(ctx context.Context, key string) (string, bool, error)
	Remember(ctx context.Context, key, model, verdict string) error
}

// Factory builds a classifier.
type Factory func(Config) (Classifier, error)

var (
	mu       sync.RWMutex
	registry = map[string]Factory{}
)

// Register adds an implementation, from init.
func Register(name string, f Factory) {
	mu.Lock()
	defer mu.Unlock()
	registry[name] = f
}

// New builds a registered classifier by name.
func New(name string, cfg Config) (Classifier, error) {
	mu.RLock()
	f, ok := registry[name]
	mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown classifier %q, have %s", name, strings.Join(Names(), ", "))
	}
	return f(cfg)
}

// Names lists the registered classifiers.
func Names() []string {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]string, 0, len(registry))
	for name := range registry {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Has reports whether a classifier is registered.
func Has(name string) bool {
	mu.RLock()
	defer mu.RUnlock()
	_, ok := registry[name]
	return ok
}

// MemoryCache is a Cache that lives for one run.
type MemoryCache struct {
	mu       sync.RWMutex
	verdicts map[string]string
}

// Verdict implements [Cache].
func (m *MemoryCache) Verdict(_ context.Context, key string) (string, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	v, ok := m.verdicts[key]
	return v, ok, nil
}

// Remember implements [Cache].
func (m *MemoryCache) Remember(_ context.Context, key, _, verdict string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.verdicts == nil {
		m.verdicts = map[string]string{}
	}
	m.verdicts[key] = verdict
	return nil
}
