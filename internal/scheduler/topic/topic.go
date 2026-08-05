// SPDX-License-Identifier: GPL-3.0-or-later

// Package topic is the `topic` middleware: it scores a URL against a subject
// before anything has fetched it.
//
// Import it for its side effect to make "topic" available to a job:
//
//	import _ "github.com/rangertaha/scour/internal/scheduler/topic"
//
// # A different question from the spider's
//
// The spider's half at 300 asks "is this page about the subject", and it has
// the page to answer with: the text, the title, everything the extractor found.
// It answers by keeping or dropping what was extracted, which is a filter.
//
// Here at 450 the question is "is this URL likely to be about the subject", and
// the evidence is only the URL and the page it was found on. Nothing has been
// fetched yet, and that is the point of asking here at all: a judgement made
// after the fetch has already spent the expensive part.
//
// So this is a scorer and not a filter. It sets Score on the request, which is
// what the `priority` ordering policy sorts by, and a crawl that fetches its
// best guesses first is what "focused" means. Dropping is available through
// `least`, but it is off by default: a guess made from a slug is a guess, and a
// wrong one that costs a URL its place in the queue can be recovered from,
// where a wrong one that discards the URL cannot.
//
// It sits at 450 because a score is worth only what sorts by it, and the
// ordering policies are at 500.
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
	"net/url"
	"strings"

	"github.com/rangertaha/scour/internal/chain"
	"github.com/rangertaha/scour/internal/classify"
	"github.com/rangertaha/scour/internal/classify/store"
	"github.com/rangertaha/scour/internal/plugin"
	"github.com/rangertaha/scour/internal/scheduler"
)

// Name is what this middleware registers as.
const Name = "topic"

func init() {
	scheduler.Register(Name, New)
}

// Config is what a `plugin "topic"` block may set.
type Config struct {
	// Subject is the classifier, with its version: "climate@7". The version is
	// required, because a job whose behaviour changed when somebody retrained,
	// with nothing in the document to show why, is the trap versions exist to
	// prevent.
	Subject string `hcl:"subject"`

	// Least is the score below which a URL is refused rather than merely
	// ranked low. Zero, the default, drops nothing and only scores, which is
	// what most focused crawls want: see the package documentation on why a
	// scorer here is safer than a filter.
	Least float64 `hcl:"least,optional"`

	// Weight is how much this contributes to the score, one by default, so a
	// job can blend a subject against whatever else scored the same request.
	//
	// A pointer because zero has to be distinguishable from unset: a weight of
	// zero is a job asking for the score to be recorded and to rank nothing,
	// which is a coherent thing to want while measuring a classifier.
	Weight *float64 `hcl:"weight,optional"`

	// Dir is where the trained classifiers are.
	Dir string `hcl:"dir,optional"`
}

// New builds the middleware. It is [scheduler.Middleware].
func New(ctx context.Context, cfg plugin.Config) (scheduler.Wrapper, error) {
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

	weight := 1.0
	if c.Weight != nil {
		weight = *c.Weight
	}
	if weight < 0 {
		return nil, fmt.Errorf("topic: weight = %v, and a subject cannot count against itself", weight)
	}

	dir := c.Dir
	if dir == "" {
		dir = store.DefaultDir
	}
	classifiers, err := store.Open(dir)
	if err != nil {
		return nil, err
	}

	// Loaded when the chain is built, so a job naming a classifier nobody has
	// trained is refused at the start of a run rather than on the first URL.
	scorer, err := classifiers.Get(ctx, ref)
	if err != nil {
		return nil, err
	}

	return func(next scheduler.Handler) scheduler.Handler {
		return scheduler.HandlerFunc(func(ctx context.Context, req *scheduler.Request) (*scheduler.Request, error) {
			score, err := scorer.Score(ctx, Text(req.URL, req.Parent))
			if err != nil {
				return nil, fmt.Errorf("topic: %s: %w", req.URL, err)
			}

			// Against the classifier's own score, not the weighted one, so that
			// `least` goes on meaning what it says when a job tunes `weight`,
			// and so that an unrelated scorer cannot lift a URL past a subject
			// threshold it failed.
			if score < c.Least {
				return nil, fmt.Errorf("topic: %s scored %.2f against %s: %w",
					req.URL, score, ref, chain.ErrDrop)
			}

			// Blended by taking the larger, not by replacing. Something may
			// have scored this request already, and a scorer sitting this near
			// the queue must not be able to undo one that ran before it: taking
			// the maximum means adding this plugin can promote a URL and never
			// demote one that something else deliberately promoted. It also
			// leaves the result the same whichever order two scorers run in,
			// which matters because several may share an order, where a sum or
			// an average would quietly depend on who went last.
			req.Score = max(req.Score, weight*score)

			return next.Handle(ctx, req)
		})
	}, nil
}

// Text is what a request is scored on: the words in its URL, followed by the
// words in the page it was found on.
//
// A slug is the most informative thing an unfetched URL has.
// "/climate/emissions-fall" and "/sport/late-goal" say what their pages are
// about before either is fetched, and that is very nearly the whole of what
// this middleware has to go on.
//
// The parent is included because a link's neighbourhood is evidence about the
// link, and it is included at its full weight rather than a discounted one:
// there is no principled discount to choose, and a guessed constant would be
// harder to explain than none. Pass an empty parent for a start URL, which was
// found on nothing.
//
// The scheme and host are dropped. Within one crawl they repeat on nearly every
// URL, so they would add the same amount to every score and order nothing; a
// host that genuinely ought to decide the matter belongs in the job's scope,
// where it is enforced rather than merely weighed.
func Text(raw, parent string) string {
	words := append(tokens(raw), tokens(parent)...)
	return strings.Join(words, " ")
}

// tokens splits one URL's path and query into scoreable words.
//
// It defers to [classify.Tokens] for the splitting, which lowercases, cuts on
// everything that is not a letter or a digit, and drops single characters. That
// is exactly the rule that will be applied to this text again when it is
// scored, and implementing it a second time here would be one more place for
// the two to drift apart.
//
// A URL too malformed to parse is tokenised whole rather than discarded,
// because it is still a string somebody may have written words into, and
// scoring it low is a better answer than scoring it zero by accident.
func tokens(raw string) []string {
	if raw == "" {
		return nil
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return classify.Tokens(raw)
	}
	// Path and query only. A fragment never reaches the server, so it cannot
	// describe a page the crawler has not seen.
	return classify.Tokens(parsed.Path + " " + parsed.RawQuery)
}
