// SPDX-License-Identifier: GPL-3.0-or-later

// Package nodeclass classifies nodes of the crawl graph.
//
// A node is a URL, and the graph is how the crawl reached it: the path back to
// the seed, what came back, and what the page turned out to link to. A
// classifier reads that and says something about the node.
//
// This is deliberately not classification of HTML. Whether a page holds records
// is a fact about the URL and its place in the site, not about any element on
// it, and answering it from the markup means asking the same question of every
// node in a document instead of once per page. The crawl graph is also the only
// place where "this page links to records but holds none" can be seen at all.
//
// # Several classifiers, several questions
//
// A node carries more than one answer. What role it plays in the crawl, what
// topic it is about, how fresh it is: these are different questions with
// different vocabularies, decided by different evidence, and a node has one
// answer to each. So a classifier declares the question it answers with Kind,
// and verdicts are stored per kind, which is what lets a second classifier
// arrive without displacing the first.
//
// # Adding one
//
// Implement Classifier and register it from init, the same as every other
// extension point in scour. See internal/registry for the shape they share,
// and docs/engine.md for what exists.
package nodeclass

import (
	"context"

	"github.com/rangertaha/scour/internal/registry"
)

// Kind is the question a classifier answers about a node.
//
// It is a string rather than an enum because the set is open: that is the point
// of the registry. These are the ones scour ships.
type Kind string

const (
	// KindRole is what part a page plays in the crawl: whether it holds
	// records, lists them, or leads nowhere.
	KindRole Kind = "role"
	// KindTopic is what a page is about, against the item being hunted.
	KindTopic Kind = "topic"
	// KindRecency is how fresh a page's content is.
	KindRecency Kind = "recency"
)

// Node is one URL and what the crawl knows about it.
//
// It carries the crawl's evidence rather than the page body: a classifier that
// needs the body fetches it through the cache in Config, so a classifier that
// does not need it costs nothing to run over a large frontier.
type Node struct {
	// URL is the node's identity.
	URL string
	// Depth is how many links from a seed it was found.
	Depth int
	// Path is the route taken to reach it, seed first, ending at URL. It is
	// what a sequence model reads.
	Path []string
	// Status is the HTTP status the fetch returned, 0 when it was not fetched.
	Status int
	// ContentType is the shorthand form: html, feed, pdf, json.
	ContentType string
	// Size is the body's length in bytes.
	Size int64
	// Links is how many links the page held, which separates a hub from a leaf
	// without reading either.
	Links int
	// FetchedAt is when the body was retrieved, zero when it was not.
	FetchedAt string
}

// Verdict is one classifier's answer about one node.
type Verdict struct {
	// Label is a value from the classifier's own vocabulary.
	Label string
	// Confidence is how sure it is, in [0,1]. A classifier with no meaningful
	// notion of confidence reports 1.
	Confidence float64
}

// Classifier labels nodes of the crawl graph.
type Classifier interface {
	// Name identifies the implementation, for logs and for configuration.
	Name() string

	// Kind is the question it answers. Two classifiers of one kind are
	// alternatives; two of different kinds both run.
	Kind() Kind

	// Labels is the vocabulary it may return, for validating what comes back
	// and for reporting what a corpus was sorted into.
	Labels() []string

	// Classify labels what it can, keyed by URL.
	//
	// The whole graph is passed rather than one node at a time because the
	// interesting classifiers are not per node: a role is decoded over a path,
	// and a recency verdict is relative to the rest of the corpus. A classifier
	// that is per node simply loops.
	//
	// Nodes it has no opinion about are left out rather than given a filler
	// label, so a caller can tell "not a record page" from "not looked at".
	Classify(ctx context.Context, nodes []Node) (map[string]Verdict, error)
}

// Config is what a classifier is built from.
type Config struct {
	// Item is the name of the thing being hunted, which a topic classifier
	// judges pages against.
	Item string
	// Aliases are the other words for it.
	Aliases []string
	// Provider is the model to consult, for classifiers that consult one.
	Provider any
	// Bodies reads a cached page body, for classifiers that need the content.
	// Nil when no cache is available, which a classifier must tolerate.
	Bodies BodyReader
	// Budget caps how many nodes one run may classify. Zero is the
	// implementation's default; negative means no limit.
	Budget int
}

// BodyReader fetches a cached page body.
type BodyReader interface {
	Body(ctx context.Context, url string) ([]byte, error)
}

// reg holds the implementations. See internal/registry for the shape every
// extension point in scour shares, and for how to add one.
var reg = registry.New[Config, Classifier]("node classifier")

// Register adds an implementation, from init.
func Register(name string, f registry.Factory[Config, Classifier]) { reg.Register(name, f) }

// New builds a registered implementation.
func New(name string, cfg Config) (Classifier, error) { return reg.New(name, cfg) }

// Names lists what is registered.
func Names() []string { return reg.Names() }

// Has reports whether a name is registered.
func Has(name string) bool { return reg.Has(name) }

// OfKind lists the registered classifiers answering one question.
//
// Building each to ask its kind is the only way to know: the registry holds
// factories, and a factory does not declare what it makes. They are cheap to
// build, and this runs once when a crawl or a training run starts.
func OfKind(kind Kind, cfg Config) ([]Classifier, error) {
	var out []Classifier
	for _, name := range Names() {
		c, err := New(name, cfg)
		if err != nil {
			return nil, err
		}
		if c.Kind() == kind {
			out = append(out, c)
		}
	}
	return out, nil
}
