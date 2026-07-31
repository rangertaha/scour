// SPDX-License-Identifier: GPL-3.0-or-later

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/rangertaha/scour/internal/bus"
	"github.com/rangertaha/scour/internal/crawl"
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

	// scopes caches one scope per entity. Deciding what is in scope belongs
	// here because this is the process that holds the targets: a crawler in
	// another process cannot be handed a scope built from a million of them,
	// so it reports every link it finds and the decision is made once, here.
	mu     sync.Mutex
	scopes map[uint]*crawl.Scope
	names  map[uint]string
}

// NewStore returns the store service.
func NewStore(b *bus.Bus, s *store.Store) *StoreService {
	return &StoreService{
		bus: b, store: s,
		scopes: map[uint]*crawl.Scope{},
		names:  map[uint]string{},
	}
}

// scopeFor returns the entity's scope, building it once.
func (s *StoreService) scopeFor(ctx context.Context, entityID uint) (*crawl.Scope, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sc, ok := s.scopes[entityID]; ok {
		return sc, nil
	}

	targets, err := s.store.TargetsFor(ctx, entityID)
	if err != nil {
		return nil, err
	}
	sc, err := crawl.NewScope(targets)
	if err != nil {
		return nil, err
	}
	s.scopes[entityID] = sc
	return sc, nil
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

	// Dispatch is deliberately not started. It is correct on its own and
	// harmful without the other half: handing work to a crawler that does not
	// exist yet would drain the frontier into the broker, where it would sit
	// until the messages aged out. It is wired up in the same change that adds
	// the crawl role.

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

// handleDiscovered records one discovered link that is inside the entity.
//
// The single-process crawler checks the scope itself before queueing, but the
// bus path never did, so a link to anywhere at all was recorded as discovered
// for the entity. Doing it here rather than in the crawler is also what lets a
// crawler stay stateless: it reports every link it saw and needs to know
// nothing about what the entity covers.
func (s *StoreService) handleDiscovered(ctx context.Context, data []byte) error {
	var ev bus.Discovered
	if err := json.Unmarshal(data, &ev); err != nil {
		return nil //nolint:nilerr // deliberate: poison message
	}

	sc, err := s.scopeFor(ctx, ev.EntityID)
	if err != nil {
		return fmt.Errorf("scope for entity %d: %w", ev.EntityID, err)
	}
	if !sc.Allows(ev.URL) {
		return nil
	}

	err = s.store.Discovered(ctx, ev.EntityID, ev.URL, ev.ParentURL, ev.Depth, ev.Score)
	if err != nil {
		return fmt.Errorf("store discovered %s: %w", ev.URL, err)
	}
	return nil
}
