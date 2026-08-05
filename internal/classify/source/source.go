// SPDX-License-Identifier: GPL-3.0-or-later

// Package source opens the classifier a job named, from wherever the job said
// it lives.
//
// # Why this is its own package
//
// Because both `topic` middlewares need it and neither may import the other.
// The scheduler's and the spider's are separate packages on purpose: importing
// one from the other would register that middleware on a node which asked only
// for the other, which is the reason [store.DefaultDir] lives where it does.
// Their shared package cannot be [classify/store] either, since the bus imports
// that and the dependency would run backwards.
//
// So the shared thing is here, and it exists because there would otherwise be
// two copies of it. Two copies is the shape this repository keeps paying for:
// three exporters derived their columns identically until one of them silently
// lost a check, and the fix was one derivation rather than three. A new caller
// asks here rather than writing a third.
//
// # What it does, and what it deliberately does not
//
// It fetches the model once and returns a scorer that runs on this machine.
// Scoring never crosses the bus: the scheduler scores every URL it is offered
// and the spider every page it reads, so a request per page would put the
// network in the hottest loop in the crawl. That is the same rule that keeps a
// fetched body off the bus and sends a cache key instead.
package source

import (
	"context"
	"fmt"
	"time"

	"github.com/rangertaha/scour/internal/bus"
	"github.com/rangertaha/scour/internal/classify"
	"github.com/rangertaha/scour/internal/classify/store"
	"github.com/rangertaha/scour/internal/plugin"
)

// Wait bounds fetching a model from a service.
//
// Generous, because this happens once when the chain is built and a crawl that
// failed to start because a model took four seconds would be a worse outcome
// than one that waited.
const Wait = 30 * time.Second

// Open returns the classifier a job named.
//
// With a url, from the topic service on that bus; otherwise from a directory on
// this machine. Either way the model is loaded here, when the chain is built,
// so a job naming a classifier nobody has trained is refused at the start of a
// run rather than on the first page.
//
// The connection, when there is one, is closed with the chain: a server
// building a chain per job would otherwise leak one per job, which is what
// [plugin.Config.Defer] exists for.
func Open(ctx context.Context, cfg plugin.Config, url, dir string, ref classify.Ref) (classify.Classifier, error) {
	if url == "" {
		if dir == "" {
			dir = store.DefaultDir
		}
		classifiers, err := store.Open(dir)
		if err != nil {
			return nil, err
		}
		return classifiers.Get(ctx, ref)
	}

	conn, err := bus.Connect(bus.Options{URL: url, Name: "scour-topic-client"})
	if err != nil {
		return nil, fmt.Errorf("topic: %s: %w", url, err)
	}
	cfg.Defer(conn.Close)

	return conn.NewTopics(Wait).Classifier(ctx, ref)
}
