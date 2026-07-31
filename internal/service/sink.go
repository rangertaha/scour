// SPDX-License-Identifier: GPL-3.0-or-later

package service

import (
	"context"

	"github.com/rangertaha/scour/internal/bus"
	"github.com/rangertaha/scour/internal/store"
)

// BusSink publishes crawl results instead of writing them.
//
// It is the other half of [StoreService]: together they are the same write the
// direct path does, with a broker in the middle. The crawler cannot tell which
// it has.
type BusSink struct {
	bus    *bus.Bus
	entity string
}

// NewBusSink returns a sink publishing for one entity.
func NewBusSink(b *bus.Bus, entity string) *BusSink {
	return &BusSink{bus: b, entity: entity}
}

// Fetched implements crawl.Sink.
//
// The message id is the URL hash, so a redelivery or a re-publish inside the
// duplicate window collapses to one message rather than one write per attempt.
func (s *BusSink) Fetched(ctx context.Context, f store.Fetched) error {
	return s.bus.Publish(ctx,
		bus.Subject(s.entity, bus.SubjectFetched),
		"fetched:"+store.URLHash(f.EntityID, f.URL)+":"+string(f.Status),
		bus.Fetched{
			Entity:      s.entity,
			EntityID:    f.EntityID,
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
func (s *BusSink) Discovered(ctx context.Context, entityID uint, rawURL, parentURL string, depth int, score float64) error {
	return s.bus.Publish(ctx,
		bus.Subject(s.entity, bus.SubjectDiscovered),
		"discovered:"+store.URLHash(entityID, rawURL),
		bus.Discovered{
			Entity:    s.entity,
			EntityID:  entityID,
			URL:       rawURL,
			ParentURL: parentURL,
			Depth:     depth,
			Score:     score,
		})
}
