// SPDX-License-Identifier: GPL-3.0-or-later

package service

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/rangertaha/scour/internal/bus"
	"github.com/rangertaha/scour/internal/store"
)

// Dispatch settings.
const (
	// dispatchInterval is how often the frontier is checked for work. Short
	// enough that a crawler is never idle for long, long enough that an empty
	// frontier is not a busy loop.
	dispatchInterval = 250 * time.Millisecond

	// dispatchCeiling is how many handed-out URLs may be unacknowledged before
	// the store stops handing out more.
	//
	// This is the backpressure. Without it a fast dispatcher would empty the
	// frontier into the broker, which would put the queue back where it was
	// before seeding was made lazy: everything queued, nothing fetched, and no
	// way to stop early without losing the order it was queued in.
	dispatchCeiling = 200

	// dispatchBatch is how many items are leased in one pass.
	dispatchBatch = 50
)

// dispatch hands frontier work to crawlers until ctx is cancelled.
//
// Not yet started by the store service: see the note in Start. It is here
// because it is the half of the problem that belongs to the component holding
// the frontier, and it is finished; what is missing is something to consume it.
//
// The frontier stays here rather than becoming a stream because the order is
// the product: it pops highest score first, and a broker delivers in publish
// order. Keeping the queue in the one component that can sort it, and shipping
// only the next few, preserves that with no scheduler to write.
func (s *StoreService) dispatch(ctx context.Context) {
	ticker := time.NewTicker(dispatchInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.dispatchOnce(ctx); err != nil && ctx.Err() == nil {
				slog.Error("dispatch failed", "err", err)
			}
		}
	}
}

// dispatchOnce hands out at most one batch, for every entity with work.
func (s *StoreService) dispatchOnce(ctx context.Context) error {
	pending, err := s.bus.Pending(ctx, bus.StreamCrawl)
	if err != nil {
		return err
	}
	room := dispatchCeiling - int(pending)
	if room <= 0 {
		// The crawlers are behind. Leaving the work in the frontier is what
		// keeps it in score order.
		return nil
	}
	if room > dispatchBatch {
		room = dispatchBatch
	}

	entities, err := s.store.QueuedEntities(ctx)
	if err != nil {
		return err
	}

	for _, id := range entities {
		name, err := s.entityName(ctx, id)
		if err != nil {
			return err
		}
		for range room {
			data, err := s.store.LeaseQueue(ctx, id, 0)
			if err != nil {
				break // empty, or unreadable: either way stop on this entity
			}
			ev := bus.Work{
				Entity:   name,
				EntityID: id,
				URL:      urlOf(data),
				Request:  data,
			}
			// Keyed on the URL so a redelivery and a re-dispatch collapse to
			// one message inside the duplicate window.
			err = s.bus.Publish(ctx, bus.Subject(name, bus.SubjectWork),
				store.URLHash(id, ev.URL), ev)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// urlOf recovers the URL from a serialised colly request.
func urlOf(data []byte) string {
	var req struct {
		URL string `json:"URL"`
	}
	if err := json.Unmarshal(data, &req); err != nil {
		return ""
	}
	return req.URL
}

// entityName resolves an id to a name, which subjects are built from. Cached
// because it is asked once per dispatch pass and entities are not renamed.
func (s *StoreService) entityName(ctx context.Context, id uint) (string, error) {
	s.mu.Lock()
	if name, ok := s.names[id]; ok {
		s.mu.Unlock()
		return name, nil
	}
	s.mu.Unlock()

	e, err := s.store.EntityByID(ctx, id)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	s.names[id] = e.Name
	s.mu.Unlock()
	return e.Name, nil
}
