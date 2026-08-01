// SPDX-License-Identifier: GPL-3.0-or-later

package nodeclass

import (
	"context"
	"errors"
)

// ErrNotImplemented is returned by a classifier that is registered but not
// written yet.
//
// Registering it is not a placeholder for its own sake. A name in a
// configuration file has to resolve to something that says "this is planned"
// rather than to "unknown node classifier", which reads as a typo and sends
// somebody looking for the spelling. It also fixes the name and the vocabulary
// now, so what stores verdicts and what reports them can be built against it
// before the classifier itself exists.
//
// A caller must treat this as "no opinion", not as a failure: an unwritten
// classifier is not a reason to abandon a crawl.
var ErrNotImplemented = errors.New("nodeclass: classifier not implemented yet")

// planned is a classifier that answers nothing, so the name and the vocabulary
// can be settled before the work is done.
type planned struct {
	name   string
	kind   Kind
	labels []string
}

func (p planned) Name() string     { return p.name }
func (p planned) Kind() Kind       { return p.kind }
func (p planned) Labels() []string { return p.labels }

func (p planned) Classify(context.Context, []Node) (map[string]Verdict, error) {
	return nil, ErrNotImplemented
}

// The recency and topic vocabularies, fixed now so that storage, reporting and
// configuration can be written against them.
var (
	// RecencyLabels sort a page by how current its content is. The question is
	// not when the page was fetched, which the crawl already knows, but whether
	// what it holds is the latest: a front page carrying today's stories and an
	// archive of the same shape are the same markup and different answers.
	RecencyLabels = []string{"latest", "recent", "dated", "undated"}

	// TopicLabels sort a page by what it is about, against the item being
	// hunted. Deliberately coarse: the question is whether a page is on the
	// subject, not what its subject is, and a finer vocabulary would be a
	// judgement about the corpus that a generic crawler does not get to make.
	TopicLabels = []string{"subject", "related", "other", "none"}
)

func init() {
	// A classifier for the latest content, so a crawl can tell a live index
	// from an archive of the same shape and spend its budget on the former.
	Register("recency", func(Config) (Classifier, error) {
		return planned{name: "recency", kind: KindRecency, labels: RecencyLabels}, nil
	})

	// A classifier for what a node is about. internal/classify already answers
	// this per page by asking a model; this is the seat for answering it over
	// the crawl graph, where a node's neighbours are evidence and most nodes
	// can be settled without reading anything.
	Register("topic", func(Config) (Classifier, error) {
		return planned{name: "topic", kind: KindTopic, labels: TopicLabels}, nil
	})
}
