// SPDX-License-Identifier: GPL-3.0-or-later

// Package classify decides how strongly a page is about a subject.
//
// # One number, and it has to mean the same thing
//
// A classifier returns a score between zero and one, and that range is a
// contract rather than a convention. Raw scores are not comparable: a term sum,
// a log-odds and a cosine similarity live on entirely different scales, so if a
// threshold written in a job document meant something different per
// implementation, swapping one for another would silently change what gets
// crawled and every threshold everywhere would become meaningless.
//
// Calibration is therefore the classifier's job, not the caller's. Whatever it
// computes internally, what comes out is a number a person can put a threshold
// on and keep when they change their mind about the mechanism.
//
// # Why the ones that ship need nothing installed
//
// Two implementations come with scour and neither has a dependency: terms,
// which needs no training at all, and bayes, which trains from labels you were
// producing anyway. Both can be printed and read, which matters for the same
// reason a learned locator goes back into the document: a guess somebody can
// look at is one they can correct.
//
// Anything heavier is a plugin. An embedding model or a language model is
// somebody else's service behind the remote contract, wanted by some jobs and
// not others, and never a thing a laptop has to have.
package classify

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/rangertaha/scour/internal/registry"
)

// Classifier scores text against one subject.
//
// Implementations must be safe for concurrent use: one classifier serves every
// job that references it, from every goroutine that is scoring a page.
type Classifier interface {
	// Score is how strongly this text is about the subject, from zero to one.
	//
	// It does not fail on text it finds strange. A page in an unexpected
	// language, or one that is mostly markup, scores low rather than
	// returning an error, because a crawl that stopped on a confusing page
	// would stop on the ones most worth looking at.
	Score(ctx context.Context, text string) (float64, error)

	// Name is the subject this was trained for.
	Name() string

	// Version identifies this particular training of it, so a job can pin one
	// and a record can say which it was scored under. Retraining produces a
	// new version rather than changing what an existing one means.
	Version() int
}

// Config is what a classifier is built from.
type Config struct {
	// Name is the subject: "climate", "insolvency".
	Name string
	// Version is which training of it this is.
	Version int
	// Terms are the words and phrases that mean a page is about the subject.
	// Used directly by the terms classifier and as a starting point by others.
	Terms []string
	// Weights are optional per-term weights, keyed by term. A term with no
	// weight counts as one.
	Weights map[string]float64
	// Model is a trained classifier's serialised state, for implementations
	// that have one.
	Model []byte
}

// Ref is a reference to a classifier, as a job writes it: "climate@7".
type Ref struct {
	Name    string
	Version int
}

// ParseRef reads "climate@7".
//
// A version is required. Referring to a subject without one would mean a job's
// behaviour changing when somebody retrains, with nothing in the document to
// show why, which is the trap that stored resolved jobs and fingerprinted specs
// both exist to avoid.
func ParseRef(s string) (Ref, error) {
	name, version, found := strings.Cut(strings.TrimSpace(s), "@")
	if !found {
		return Ref{}, fmt.Errorf("classifier %q: needs a version, as in %s@1", s, s)
	}

	// Atoi rather than Sscanf, which stops at the first thing it cannot read
	// and reports success for what came before it. "climate@7-experimental"
	// parsed as climate@7, and the store's listing, which strips ".json" and
	// parses what is left, therefore read a file named climate@9.bak.json as
	// the classifier climate@9: `scour topic list` announced a version whose
	// file was not there, and Get failed with "not trained".
	v, err := strconv.Atoi(version)
	if err != nil || v < 1 {
		return Ref{}, fmt.Errorf("classifier %q: %q is not a version", s, version)
	}
	if strings.TrimSpace(name) == "" {
		return Ref{}, fmt.Errorf("classifier %q: no name", s)
	}
	return Ref{Name: name, Version: v}, nil
}

// String renders a reference the way a job writes it.
func (r Ref) String() string { return fmt.Sprintf("%s@%d", r.Name, r.Version) }

// reg holds the implementations. See [registry] for the shape every extension
// point in scour shares.
var reg = registry.New[Config, Classifier]("classifier")

// Register adds an implementation, from an init function in its own package.
func Register(name string, f registry.Factory[Config, Classifier]) { reg.Register(name, f) }

// New builds a registered implementation.
func New(ctx context.Context, kind string, cfg Config) (Classifier, error) {
	return reg.New(ctx, kind, cfg)
}

// Kinds lists what is registered.
func Kinds() []string { return reg.Names() }

// Has reports whether an implementation is registered.
func Has(kind string) bool { return reg.Has(kind) }

// Tokens splits text the way every classifier here splits it.
//
// Letters and digits by the Unicode definition rather than the ASCII one,
// because the corpora this has to cope with are Greek, Russian, Arabic, Turkish
// and Malayalam as often as they are English, and a tokeniser that only knows
// a-z scores those pages at zero and calls it a confident answer.
//
// Single characters are dropped: they carry no subject and appear everywhere.
func Tokens(text string) []string {
	fields := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})

	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if len([]rune(f)) > 1 {
			out = append(out, f)
		}
	}
	return out
}

// Counts tallies tokens, which is the input every implementation here works
// from.
func Counts(tokens []string) map[string]int {
	out := make(map[string]int, len(tokens))
	for _, t := range tokens {
		out[t]++
	}
	return out
}

// Saturate maps a count to a diminishing contribution between zero and one.
//
// The first occurrence of a word says most of what repetition can say: a page
// that mentions a subject once is probably about it, and one that mentions it
// nine times is not nine times more about it. Without this a single word
// repeated in a navigation menu outscores a page that discusses the subject
// properly using varied language.
//
//	1 -> 0.63   2 -> 0.86   3 -> 0.95   5 -> 0.99
func Saturate(n int) float64 {
	if n <= 0 {
		return 0
	}
	return 1 - math.Exp(-float64(n))
}

// Clamp keeps a score inside the contract, which is the last line of defence
// against an implementation whose arithmetic surprised it.
func Clamp(score float64) float64 {
	switch {
	case math.IsNaN(score):
		return 0
	case score < 0:
		return 0
	case score > 1:
		return 1
	default:
		return score
	}
}

// Sigmoid squashes an unbounded score into the contract's range. It is what
// turns a log-odds into something a threshold can be written against.
func Sigmoid(x float64) float64 { return 1 / (1 + math.Exp(-x)) }

// Top returns the n highest-weighted entries, as a classifier prints itself.
//
// Sorted by weight and then by term, so printing the same model twice gives the
// same answer and a diff between two trainings is readable.
func Top(weights map[string]float64, n int) []string {
	type pair struct {
		term   string
		weight float64
	}

	pairs := make([]pair, 0, len(weights))
	for term, weight := range weights {
		pairs = append(pairs, pair{term, weight})
	}
	sort.Slice(pairs, func(a, b int) bool {
		if pairs[a].weight != pairs[b].weight {
			return pairs[a].weight > pairs[b].weight
		}
		return pairs[a].term < pairs[b].term
	})

	if n > len(pairs) {
		n = len(pairs)
	}
	out := make([]string, 0, n)
	for _, p := range pairs[:n] {
		out = append(out, p.term)
	}
	return out
}
