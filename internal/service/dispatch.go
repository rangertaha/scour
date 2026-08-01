// SPDX-License-Identifier: GPL-3.0-or-later

package service

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/url"
	"strings"
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

	// dispatchCeiling is how many URLs may be out with a crawler, per item,
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

// cooling lists the hosts asked for something too recently to be asked again,
// and records this pass's hand-outs as it goes.
//
// The per-host rate is the site's, not the crawl's: an override recorded for a
// host wins over the configured default, which is how a fragile server is
// treated gently without slowing everything else down.
func (s *StoreService) cooling(now time.Time, rates map[string]time.Duration) []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	var hot []string
	for host, last := range s.lastAsked {
		rate := s.hostRate
		if r, ok := rates[host]; ok {
			rate = r
		}
		if now.Sub(last) < rate {
			hot = append(hot, host)
			continue
		}
		// Long enough ago to be irrelevant, and keeping it would grow the map
		// by one entry per host for the life of the process.
		delete(s.lastAsked, host)
	}
	return hot
}

// asked records that a host has just been handed out.
func (s *StoreService) asked(host string, at time.Time) {
	if host == "" {
		return
	}
	s.mu.Lock()
	s.lastAsked[host] = at
	s.mu.Unlock()
}

// dispatchOnce hands out at most one batch, for every item with work.
func (s *StoreService) dispatchOnce(ctx context.Context) error {
	jobs, err := s.store.QueuedJobs(ctx)
	if err != nil {
		return err
	}
	rates, err := s.store.HostRates(ctx)
	if err != nil {
		return err
	}

	for _, jobID := range jobs {
		// The frontier is the job's; the item is what the work is for, and is
		// what the bus events and the scope are keyed by.
		job, err := s.store.JobByID(ctx, jobID)
		if err != nil {
			return err
		}
		id := job.ItemID

		inFlight, err := s.store.InFlight(ctx, jobID)
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

		name, err := s.itemName(ctx, id)
		if err != nil {
			return err
		}

		// What the frontier looks like from here, which is the pair of numbers
		// that says whether crawlers are keeping up: a queue growing while the
		// in-flight count sits at its ceiling means the crawl is discovering
		// faster than it can fetch.
		if depth, err := s.store.QueueSize(ctx, jobID); err == nil {
			s.bus.Emit(ctx, name, bus.Metric{
				ItemID: id, Name: bus.MetricQueueDepth,
				Value: float64(depth), Unit: "count",
			})
		}
		s.bus.Emit(ctx, name, bus.Metric{
			ItemID: id, Name: bus.MetricQueueFlight,
			Value: float64(inFlight), Unit: "count",
		})

		for range room {
			now := time.Now()
			data, err := s.store.LeaseQueueSkipping(ctx, jobID, 0, s.cooling(now, rates))
			if err != nil {
				// Empty, or every host still cooling. Either way there is
				// nothing to hand out for this item right now.
				break
			}
			s.asked(hostOf(data), now)
			ev := bus.Work{
				Item:    name,
				ItemID:  id,
				URL:     urlOf(data),
				Request: data,
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

// hostOf reads the host a serialised request will be sent to.
func hostOf(data []byte) string {
	u, err := url.Parse(urlOf(data))
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
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

// itemName resolves an id to a name, which subjects are built from. Cached
// because it is asked once per dispatch pass and items are not renamed.
func (s *StoreService) itemName(ctx context.Context, id uint) (string, error) {
	s.mu.Lock()
	if name, ok := s.names[id]; ok {
		s.mu.Unlock()
		return name, nil
	}
	s.mu.Unlock()

	e, err := s.store.ItemByID(ctx, id)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	s.names[id] = e.Name
	s.mu.Unlock()
	return e.Name, nil
}
