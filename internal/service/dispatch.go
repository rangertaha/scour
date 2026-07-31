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

	// dispatchCeiling is how many URLs may be out with a crawler, per entity,
	// before the store stops handing out more.
	//
	// This is the backpressure, and it counts leases rather than unacknowledged
	// broker messages because those measure different things. A crawler
	// acknowledges work when it queues it, not when it fetches it, so the
	// broker looks idle while the crawler holds thousands of URLs it has not
	// got to: measured on a live site the whole frontier ended up leased and
	// waiting in one process's memory. A lease is only released when a fetch is
	// reported, so counting leases counts work actually outstanding.
	//
	// Enough to keep every thread of several crawlers busy, small enough that
	// the frontier keeps its order instead of being emptied into memory.
	dispatchCeiling = 100

	// dispatchBatch is how many items are leased in one pass.
	dispatchBatch = 50
)

// dispatch hands frontier work to crawlers until ctx is cancelled.
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
	entities, err := s.store.QueuedEntities(ctx)
	if err != nil {
		return err
	}

	for _, id := range entities {
		inFlight, err := s.store.InFlight(ctx, id)
		if err != nil {
			return err
		}
		room := dispatchCeiling - inFlight
		if room <= 0 {
			// The crawlers are behind. Leaving the work in the frontier is
			// what keeps it in score order.
			continue
		}
		if room > dispatchBatch {
			room = dispatchBatch
		}

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
