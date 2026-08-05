// SPDX-License-Identifier: GPL-3.0-or-later

// Package topic is the `topic` middleware: it scores a page against a subject
// and drops what is off it.
//
// Import it for its side effect to make "topic" available to a job:
//
//	import _ "github.com/rangertaha/scour/internal/spider/topic"
//
// # Two placements, because they are two questions
//
// In the spider at 300, the question is "is this page about the subject", and
// the answer decides whether to keep what was extracted. It sits after
// `httperror` and before anything expensive, and it scores the page's text
// rather than its markup: a classifier trained on raw HTML learns the menu.
//
// In the scheduler at 450 the question is different: "is this URL likely to be
// about the subject", which has only the URL and the page it was found on to go
// on. That is a scorer rather than a filter, and it is what makes a focused
// crawl focused. This is the first of the two.
//
// # Optional throughout
//
// A node running no topiced jobs loads no classifier and reads no model. That
// is why this is a plugin and not a stage: most crawls do not want it, and the
// ones that do should not make everybody else carry it.
package topic

import (
	"context"
	"fmt"

	"github.com/rangertaha/scour/internal/chain"
	"github.com/rangertaha/scour/internal/classify"
	"github.com/rangertaha/scour/internal/classify/store"
	"github.com/rangertaha/scour/internal/downloader"
	"github.com/rangertaha/scour/internal/plugin"
	"github.com/rangertaha/scour/internal/spider"
)

// Name is what this middleware registers as.
const Name = "topic"

// Property is where the score is put on every item the page produced, so a
// record says how confident the crawl was rather than only that it kept it.
const Property = "topic_score"

func init() {
	spider.Register(Name, New)
}

// Config is what a `plugin "topic"` block may set.
type Config struct {
	// Subject is the classifier, with its version: "climate@7". The version is
	// required, because a job whose behaviour changed when somebody retrained,
	// with nothing in the document to show why, is the trap versions exist to
	// prevent.
	Subject string `hcl:"subject"`

	// Least is the score below which a page is dropped. Zero keeps every page
	// and only records the score, which is what a job measuring a classifier
	// before trusting it wants.
	Least float64 `hcl:"least,optional"`

	// Dir is where the trained classifiers are.
	Dir string `hcl:"dir,optional"`

	// Record puts the score on every item as a property. On by default: a
	// record that says what it scored can be filtered later by somebody who
	// disagrees with the threshold.
	Record *bool `hcl:"record,optional"`
}

// DefaultDir is where classifiers live when a job does not say. It is
// [store.DefaultDir], which is where the literal lives now that the scheduler's
// half of this middleware needs the same default.
const DefaultDir = store.DefaultDir

// New builds the middleware. It is [spider.Middleware].
func New(ctx context.Context, cfg plugin.Config) (spider.Wrapper, error) {
	var c Config
	if err := cfg.Decode(&c); err != nil {
		return nil, err
	}

	ref, err := classify.ParseRef(c.Subject)
	if err != nil {
		return nil, err
	}
	if c.Least < 0 || c.Least > 1 {
		return nil, fmt.Errorf("topic: least = %v, and a score is between 0 and 1", c.Least)
	}

	dir := c.Dir
	if dir == "" {
		dir = DefaultDir
	}
	classifiers, err := store.Open(dir)
	if err != nil {
		return nil, err
	}

	// Loaded when the chain is built, so a job naming a classifier nobody has
	// trained is refused at the start of a run rather than on the first page.
	scorer, err := classifiers.Get(ctx, ref)
	if err != nil {
		return nil, err
	}

	record := c.Record == nil || *c.Record

	return func(next spider.Handler) spider.Handler {
		return spider.HandlerFunc(func(ctx context.Context, resp *downloader.Response) (*spider.Output, error) {
			out, err := next.Handle(ctx, resp)
			if err != nil {
				return nil, err
			}

			// The page's text, not its markup. A classifier trained on raw
			// HTML learns the menu, and one asked to score raw HTML is being
			// asked about a different document from the one it learnt on.
			text, _ := resp.Text()

			score, err := scorer.Score(ctx, string(text))
			if err != nil {
				return nil, fmt.Errorf("topic: %s: %w", resp.URL, err)
			}

			if score < c.Least {
				return nil, fmt.Errorf("topic: %s scored %.2f against %s: %w",
					resp.URL, score, ref, chain.ErrDrop)
			}

			if record {
				for _, item := range out.Items {
					if item.Values == nil {
						continue
					}
					item.Values[Property] = value(score, ref)
				}
			}
			return out, nil
		})
	}, nil
}
