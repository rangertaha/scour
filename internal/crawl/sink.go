// SPDX-License-Identifier: GPL-3.0-or-later

package crawl

import (
	"context"

	"github.com/rangertaha/scour/internal/store"
)

// Sink is where the crawler puts what it finds.
//
// This is the seam the bus goes behind. The crawler does not know whether its
// results are written straight to the database or published for another
// process to write, which is what makes the two topologies the same code
// rather than two implementations to keep in step.
//
// Declared here because this package is the consumer.
type Sink interface {
	// Fetched records the outcome of one fetch.
	Fetched(ctx context.Context, f store.Fetched) error
	// Discovered records a link found on a page, with the score it was queued
	// at.
	Discovered(ctx context.Context, entityID uint, rawURL, parentURL string, depth int, score float64) error
}

// DirectSink writes to the database from the crawler's own goroutine. It is
// the single-process path, and the reference the bus path is compared against.
type DirectSink struct{ Store *store.Store }

// Fetched implements [Sink].
func (d DirectSink) Fetched(ctx context.Context, f store.Fetched) error {
	return d.Store.RecordFetch(ctx, f)
}

// Discovered implements [Sink].
func (d DirectSink) Discovered(ctx context.Context, entityID uint, rawURL, parentURL string, depth int, score float64) error {
	return d.Store.Discovered(ctx, entityID, rawURL, parentURL, depth, score)
}
