// SPDX-License-Identifier: GPL-3.0-or-later

package service

import (
	"context"
	"sync/atomic"

	"github.com/rangertaha/scour/internal/bus"
	"github.com/rangertaha/scour/internal/store"
)

// BusSink publishes crawl results instead of writing them.
//
// It is the other half of [StoreService]: together they are the same write the
// direct path does, with a broker in the middle. The crawler cannot tell which
// it has.
type BusSink struct {
	bus  *bus.Bus
	item string
	// fetched, when set, is where this sink tallies the pages that went over
	// the network. See [BusSink.Counting].
	fetched *atomic.Int64
}

// NewBusSink returns a sink publishing for one item.
func NewBusSink(b *bus.Bus, item string) *BusSink {
	return &BusSink{bus: b, item: item}
}

// Counting makes the sink tally what this process fetched.
//
// A node's throughput is pages a second, and this is the one point every page
// this process fetched passes through. Counting here rather than subscribing to
// the metrics stream is what keeps the number this node's own: that stream
// carries every node's fetches, so a subscriber would report the whole fleet's
// rate on every line of the listing.
func (s *BusSink) Counting(n *atomic.Int64) *BusSink {
	s.fetched = n
	return s
}

// Fetched implements crawl.Sink.
//
// The message id is the URL hash, so a redelivery or a re-publish inside the
// duplicate window collapses to one message rather than one write per attempt.
func (s *BusSink) Fetched(ctx context.Context, f store.Fetched) error {
	// Counted before the publish, because the question the count answers is
	// what this machine fetched, and a page whose outcome the broker refused
	// was still fetched. Skipped URLs are not: nothing went over the network
	// for them, and counting them would report a rate for a crawl doing
	// nothing but rejecting links.
	if s.fetched != nil && f.Status != store.URLSkipped && f.Status != store.URLQueued {
		s.fetched.Add(1)
	}
	return s.bus.Publish(ctx,
		bus.Subject(s.item, bus.SubjectFetched),
		"fetched:"+store.URLHash(f.ItemID, f.URL)+":"+string(f.Status),
		bus.Fetched{
			Item:        s.item,
			ItemID:      f.ItemID,
			JobID:       f.JobID,
			URL:         f.URL,
			ParentURL:   f.ParentURL,
			Depth:       f.Depth,
			Score:       f.Score,
			Status:      string(f.Status),
			StatusCode:  f.StatusCode,
			ContentType: f.ContentType,
			Size:        f.Size,
			Latency:     f.Latency,
			CacheKey:    f.CacheKey,
		})
}

// Discovered implements crawl.Sink.
func (s *BusSink) Discovered(ctx context.Context, itemID uint, rawURL, parentURL string, depth int, score float64) error {
	return s.bus.Publish(ctx,
		bus.Subject(s.item, bus.SubjectDiscovered),
		"discovered:"+store.URLHash(itemID, rawURL),
		bus.Discovered{
			Item:      s.item,
			ItemID:    itemID,
			URL:       rawURL,
			ParentURL: parentURL,
			Depth:     depth,
			Score:     score,
		})
}
