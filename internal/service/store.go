// SPDX-License-Identifier: GPL-3.0-or-later

package service

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/rangertaha/scour/internal/bus"
	"github.com/rangertaha/scour/internal/store"
)

// StoreService writes what the crawl produces to the database.
//
// It is the only component that touches the database, so nothing else has to
// know how the schema fits together, and a deployment can put the database
// behind exactly one process.
type StoreService struct {
	bus   *bus.Bus
	store *store.Store
}

// NewStore returns the store service.
func NewStore(b *bus.Bus, s *store.Store) *StoreService {
	return &StoreService{bus: b, store: s}
}

// Role implements [Service].
func (s *StoreService) Role() Role { return RoleStore }

// Start implements [Service]. It subscribes to the crawl subjects and returns
// when ctx is cancelled, draining whatever is in flight on the way out.
func (s *StoreService) Start(ctx context.Context) error {
	stopFetched, err := s.bus.Consume(ctx, bus.StreamCrawl, "store-fetched",
		bus.AllEntities(bus.SubjectFetched), s.handleFetched)
	if err != nil {
		return err
	}
	defer stopFetched()

	stopDiscovered, err := s.bus.Consume(ctx, bus.StreamCrawl, "store-discovered",
		bus.AllEntities(bus.SubjectDiscovered), s.handleDiscovered)
	if err != nil {
		return err
	}
	defer stopDiscovered()

	<-ctx.Done()
	return nil
}

// handleFetched writes one fetch outcome.
//
// Delivery is at-least-once, so this may run twice for the same page. The
// write is an upsert keyed on the URL hash, which makes a repeat harmless.
func (s *StoreService) handleFetched(ctx context.Context, data []byte) error {
	var ev bus.Fetched
	if err := json.Unmarshal(data, &ev); err != nil {
		// A message we cannot read will never become readable, so failing it
		// forever would block the queue. Drop it, loudly.
		return nil //nolint:nilerr // deliberate: poison message, see comment
	}

	err := s.store.RecordFetch(ctx, store.Fetched{
		EntityID:    ev.EntityID,
		URL:         ev.URL,
		ParentURL:   ev.ParentURL,
		Depth:       ev.Depth,
		Score:       ev.Score,
		Status:      store.URLStatus(ev.Status),
		StatusCode:  ev.StatusCode,
		ContentType: ev.ContentType,
		Size:        ev.Size,
		Latency:     ev.Latency,
		CacheKey:    ev.CacheKey,
	})
	if err != nil {
		return fmt.Errorf("store fetch %s: %w", ev.URL, err)
	}
	return nil
}

// handleDiscovered records one discovered link.
func (s *StoreService) handleDiscovered(ctx context.Context, data []byte) error {
	var ev bus.Discovered
	if err := json.Unmarshal(data, &ev); err != nil {
		return nil //nolint:nilerr // deliberate: poison message
	}

	err := s.store.Discovered(ctx, ev.EntityID, ev.URL, ev.ParentURL, ev.Depth, ev.Score)
	if err != nil {
		return fmt.Errorf("store discovered %s: %w", ev.URL, err)
	}
	return nil
}
