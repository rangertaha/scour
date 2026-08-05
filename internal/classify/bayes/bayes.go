// SPDX-License-Identifier: GPL-3.0-or-later

// Package bayes scores a page against a subject it was shown examples of.
//
// # Why this one and not something cleverer
//
// It needs no model file, no service and no key, it trains in the time it takes
// to read the corpus, and what it learns can be printed as a list of words with
// numbers beside them. That last property is the one that keeps deciding these
// arguments: a classifier you can look at is one you can find the mistake in.
//
// It also learns from labels that already exist. A mark on a record is a
// person saying this one is right or this one is wrong, and those are exactly
// the labels this wants, so a crawl somebody is already reviewing produces
// training data as a side effect of being reviewed.
//
// What it cannot do is understand a word it has never seen. A subject described
// by tone rather than vocabulary, or a page written in a language the examples
// did not cover, is where an embedding earns its dependency.
package bayes

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/rangertaha/scour/internal/classify"
)

// Name is what this registers as.
const Name = "bayes"

func init() {
	classify.Register(Name, func(_ context.Context, cfg classify.Config) (classify.Classifier, error) {
		return Load(cfg)
	})
}

// Document is one labelled example.
type Document struct {
	// Text is the page, after the boilerplate has been taken off it. Training
	// on raw markup learns the navigation menu.
	Text string
	// About says whether this is the subject.
	About bool
}

// Bayes is a trained classifier.
//
// The fields are exported so a trained model serialises to JSON, which is how
// it is stored and how anybody reads it without running scour.
type Bayes struct {
	Subject  string `json:"subject"`
	Trained  int    `json:"version"`
	Examples int    `json:"examples"`

	// LogOdds is the evidence one occurrence of a word carries: positive means
	// the subject, negative means not. This is the whole model, and it is
	// readable, which is the point.
	LogOdds map[string]float64 `json:"log_odds"`

	// Prior is the log-odds of the subject before any word is read.
	Prior float64 `json:"prior"`

	// Midpoint is the evidence that scores exactly a half: the halfway point
	// between what the two sides of the corpus averaged. Fitted, because the
	// evidence has a middle of its own that is only zero when the corpus was
	// perfectly balanced, and a scale can stretch around a middle but cannot
	// move one. See [fit].
	//
	// Absent from a model trained before this existed, which decodes as zero
	// and scores as that model always did.
	Midpoint float64 `json:"midpoint"`

	// Scale converts summed evidence into the contract's range. Fitted during
	// training rather than guessed, because the spread of log-odds depends on
	// the corpus and a constant that suited one would misjudge the next.
	Scale float64 `json:"scale"`
}

// Train fits a classifier to labelled examples.
//
// Multinomial naive Bayes with add-one smoothing, which is the standard answer
// and is standard because it works on little data and cannot be made to explode
// by an unseen word.
func Train(subject string, version int, docs []Document) (*Bayes, error) {
	if len(docs) == 0 {
		return nil, fmt.Errorf("classifier %q: nothing to learn from", subject)
	}

	var about, against int
	aboutCounts := map[string]int{}
	againstCounts := map[string]int{}
	var aboutTotal, againstTotal int

	for _, d := range docs {
		counts := classify.Counts(classify.Tokens(d.Text))
		if d.About {
			about++
			for term, n := range counts {
				aboutCounts[term] += n
				aboutTotal += n
			}
			continue
		}
		against++
		for term, n := range counts {
			againstCounts[term] += n
			againstTotal += n
		}
	}

	if about == 0 || against == 0 {
		// One-sided examples teach nothing: every word looks like evidence for
		// whichever side was shown, and the result is a classifier that says
		// yes to everything or no to everything with great confidence.
		return nil, fmt.Errorf(
			"classifier %q: %d examples of the subject and %d of anything else, and it needs both",
			subject, about, against)
	}

	vocabulary := map[string]bool{}
	for term := range aboutCounts {
		vocabulary[term] = true
	}
	for term := range againstCounts {
		vocabulary[term] = true
	}

	size := float64(len(vocabulary))
	b := &Bayes{
		Subject:  subject,
		Trained:  version,
		Examples: len(docs),
		LogOdds:  make(map[string]float64, len(vocabulary)),
		Prior:    math.Log(float64(about) / float64(against)),
	}

	for term := range vocabulary {
		pAbout := (float64(aboutCounts[term]) + 1) / (float64(aboutTotal) + size)
		pAgainst := (float64(againstCounts[term]) + 1) / (float64(againstTotal) + size)
		b.LogOdds[term] = math.Log(pAbout / pAgainst)
	}

	b.Midpoint, b.Scale = fit(b, docs)
	return b, nil
}

// fit chooses where the middle of the score is and how sharply evidence turns
// into one.
//
// The spread of per-word log-odds depends entirely on the corpus: a subject
// whose vocabulary barely overlaps with everything else produces large numbers,
// one that shares most of its words produces small ones. A fixed constant would
// therefore make one subject's scores cluster at the ends and another's cluster
// in the middle, and a threshold written for one would be nonsense for the
// other.
//
// So it is fitted: take the mean evidence of the examples on each side, put the
// middle of the score halfway between them, and pick a scale that puts them
// either side of it by a comfortable margin.
//
// # Why a midpoint and not only a scale
//
// Because multiplying cannot move a middle, it can only stretch around one, and
// the evidence has a middle of its own that is not zero: [Bayes.evidence] adds
// the prior, which is the log of how many examples were on each side. Only a
// corpus with exactly as many of each has a prior of zero, and review labels
// never do. Trained on twenty pages about the subject and two hundred about
// anything else, the prior is -2.3, both class means are negative, and every
// page scores below 0.1 including the classifier's own positive examples: a job
// with `least = 0.5` dropped the whole crawl while reporting well-formed
// scores. The reverse imbalance put everything above 0.9.
//
// A model trained before this has no midpoint, so it decodes with zero and
// scores exactly as it did. That is right for the balanced corpus it was
// probably trained on and wrong for any other, and retraining is what fixes it.
func fit(b *Bayes, docs []Document) (midpoint, scale float64) {
	var aboutSum, againstSum float64
	var about, against int

	for _, d := range docs {
		e := b.evidence(d.Text)
		if d.About {
			aboutSum += e
			about++
			continue
		}
		againstSum += e
		against++
	}

	aboutMean := aboutSum / float64(about)
	againstMean := againstSum / float64(against)

	midpoint = (aboutMean + againstMean) / 2
	if math.IsNaN(midpoint) {
		midpoint = 0
	}

	gap := aboutMean - againstMean
	if gap <= 0 || math.IsNaN(gap) {
		// The examples do not separate. A scale of one is as good as any, and
		// the scores will sit near the middle, which is the honest report of a
		// classifier that has not learned anything.
		return midpoint, 1
	}

	// Put the average example about two logistic units from the middle, which
	// is roughly 0.88 and 0.12: confident without claiming certainty it has
	// not earned.
	return midpoint, 4 / gap
}

// evidence is the summed log-odds per word, before it is squashed.
//
// Divided by the length of the document, because a long page about the subject
// and a short one are both about the subject: without this, evidence grows with
// word count and every long page scores high whatever it says.
// # What "nothing to say" is worth
//
// [Bayes.Midpoint] and not zero. The two are the same number only on a
// perfectly balanced corpus, and reading zero as neutral is what this did until
// the midpoint existed: a page whose every word was unseen in training then
// scored 0.96, higher than the classifier's own positive examples, because zero
// sat above a midpoint the corpus had pulled down to -1.66. A focused crawl
// therefore kept every off-vocabulary page and, in the scheduler, put them at
// the front of the frontier: it prioritised exactly the pages it knew nothing
// about. The midpoint is the point that scores a half, so it is what "I
// recognise nothing here" has to return.
func (b *Bayes) evidence(text string) float64 {
	tokens := classify.Tokens(text)
	if len(tokens) == 0 {
		return b.Midpoint
	}

	var sum float64
	var seen int
	for term, n := range classify.Counts(tokens) {
		odds, ok := b.LogOdds[term]
		if !ok {
			continue // never seen in training, so it says nothing
		}
		// Diminishing repeats, for the same reason the term classifier does
		// it: a word in a menu on every page must not outvote the article.
		sum += odds * classify.Saturate(n)
		seen++
	}
	if seen == 0 {
		return b.Midpoint
	}
	return b.Prior + sum/math.Sqrt(float64(seen))
}

// Score implements [classify.Classifier].
func (b *Bayes) Score(_ context.Context, text string) (float64, error) {
	if strings.TrimSpace(text) == "" {
		return 0, nil
	}
	return classify.Clamp(classify.Sigmoid((b.evidence(text) - b.Midpoint) * b.Scale)), nil
}

// Name implements [classify.Classifier].
func (b *Bayes) Name() string { return b.Subject }

// Version implements [classify.Classifier].
func (b *Bayes) Version() int { return b.Trained }

// Words are the terms that most say this is the subject, strongest first. It is
// how a trained classifier prints itself, and it is what gets written back into
// a document when somebody wants a term list instead of a model.
func (b *Bayes) Words(n int) []string { return classify.Top(b.LogOdds, n) }

// Against are the terms that most say it is not, which is as informative and
// is usually more surprising.
func (b *Bayes) Against(n int) []string {
	flipped := make(map[string]float64, len(b.LogOdds))
	for term, odds := range b.LogOdds {
		flipped[term] = -odds
	}
	return classify.Top(flipped, n)
}

// Bytes serialises a trained classifier.
func (b *Bayes) Bytes() ([]byte, error) { return json.Marshal(b) }

// Load rebuilds one from [classify.Config.Model].
func Load(cfg classify.Config) (*Bayes, error) {
	if len(cfg.Model) == 0 {
		return nil, fmt.Errorf("classifier %q: no trained model", cfg.Name)
	}

	var b Bayes
	if err := json.Unmarshal(cfg.Model, &b); err != nil {
		return nil, fmt.Errorf("classifier %q: %w", cfg.Name, err)
	}
	if b.Scale == 0 {
		b.Scale = 1
	}
	return &b, nil
}
