// SPDX-License-Identifier: GPL-3.0-or-later

package train

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"strings"

	"github.com/rangertaha/scour/internal/ai"
	"github.com/rangertaha/scour/internal/classify"
	"github.com/rangertaha/scour/internal/content"
	"github.com/rangertaha/scour/internal/store"
	"github.com/rangertaha/scour/internal/wom"
)

// ClassifyResult reports what reading the pages cost and found.
type ClassifyResult struct {
	// Name is the classifier that ran.
	Name string
	// Categories counts pages by what they turned out to be.
	Categories map[string]int
	// Rescued is how many pages the classifier called relevant that the
	// record count alone would have labelled negative. This is the number the
	// whole feature exists to produce.
	Rescued int
	// Calls is how many pages actually reached the model.
	Calls int
	// Cached is how many were answered from a previous run.
	Cached int
	// Errors is how many failed and fell back to the record count.
	Errors int
}

// classifierFor builds the page classifier named in [model], if any.
func (t *Trainer) classifierFor() (classify.Classifier, error) {
	name := t.cfg.Model.Classifier
	if name == "" || name == "none" {
		return nil, nil
	}
	if !classify.Has(name) {
		return nil, fmt.Errorf("unknown classifier %q in [model], have %s",
			name, strings.Join(classify.Names(), ", "))
	}

	cfg := classify.Config{
		Cache:  verdictCache{t.store},
		Budget: t.cfg.Model.Budget,
	}
	if block, ok := t.aiBlock(); ok {
		provider, err := ai.New(block)
		if err != nil {
			return nil, err
		}
		cfg.Provider = provider
	}
	return classify.New(name, cfg)
}

// topicOf describes an item in the terms a classifier judges against.
func topicOf(item *store.Item) classify.Topic {
	topic := classify.Topic{Name: item.Name}
	for _, a := range item.Aliases {
		topic.Aliases = append(topic.Aliases, a.Word)
	}
	for _, p := range item.Properties {
		topic.Fields = append(topic.Fields, p.Name)
	}
	return topic
}

// classifyPages reads each fetched page and says what it is.
//
// This is what breaks the circularity in label bootstrapping. Without it a page
// is relevant only if extraction already found a record in it, which cannot be
// true on a first crawl because the rules that would find one are induced from
// the very pages being labelled. Reading the page settles the question
// independently: a listing of vehicles is a listing of vehicles whether or not
// the rules can yet pull a price out of it.
func (t *Trainer) classifyPages(
	ctx context.Context,
	item *store.Item,
	rows []store.URL,
	classifier classify.Classifier,
) (map[string]classify.Category, *ClassifyResult) {
	topic := topicOf(item)
	result := &ClassifyResult{Name: classifier.Name(), Categories: map[string]int{}}
	out := make(map[string]classify.Category, len(rows))

	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			break
		}
		if !content.Extractable[row.ContentType] {
			continue
		}

		body, err := t.cache.Get(ctx, row.URL)
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				slog.Debug("page not read for classification", "url", row.URL, "err", err)
			}
			continue
		}

		category, err := classifier.Classify(ctx, topic, classify.Page{
			URL:   row.URL,
			Title: titleOf(body),
			Text:  textOf(row.ContentType, body),
		})
		if err != nil {
			if errors.Is(err, classify.ErrBudgetSpent) {
				// Every remaining page would fail the same way, and a page
				// nobody looked at must not be recorded as irrelevant.
				break
			}
			slog.Debug("page not classified", "url", row.URL, "err", err)
			continue
		}

		out[row.URL] = category
		result.Categories[string(category)]++
	}

	if reporter, ok := classifier.(interface{ Stats() classify.Stats }); ok {
		s := reporter.Stats()
		result.Calls, result.Cached, result.Errors = s.Calls, s.Hits, s.Errors
	}
	return out, result
}

// textOf pulls readable text out of a stored body, using the same graph the
// rest of scour reads documents with rather than a second parser.
func textOf(format string, body []byte) string {
	w := wom.New()
	if err := w.AddBody("http://classify.invalid/", mimeOf(format), body); err != nil {
		return string(body)
	}
	return w.Root().Text()
}

// titleOf finds a document's title, which is often the clearest single
// statement of what a page is.
func titleOf(body []byte) string {
	w := wom.New()
	if err := w.AddBody("http://classify.invalid/", "text/html", body); err != nil {
		return ""
	}

	var title string
	w.Root().Walk(func(n *wom.Node) bool {
		if title == "" && n.Kind == wom.KindElement && n.Name == "title" {
			title = n.Text()
		}
		return title == ""
	})
	return title
}

// mimeOf turns a stored shorthand back into a MIME type.
func mimeOf(format string) string {
	if mimes, ok := content.Shorthands[format]; ok && len(mimes) > 0 {
		return mimes[0]
	}
	return format
}

// verdictCache adapts the store to [classify.Cache].
type verdictCache struct{ store *store.Store }

func (c verdictCache) Verdict(ctx context.Context, key string) (string, bool, error) {
	return c.store.Verdict(ctx, key)
}

func (c verdictCache) Remember(ctx context.Context, key, model, verdict string) error {
	return c.store.RememberVerdict(ctx, key, model, verdict)
}
