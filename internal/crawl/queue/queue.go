// SPDX-License-Identifier: GPL-3.0-or-later

// Package queue implements colly's queue.Storage over scour's database.
//
// This is the seam that turns colly's crawl order into scour's decision. colly
// keeps its loop, its threads and its politeness; what changes is which
// request comes out next. The database backing also means a crawl resumes from
// where it stopped instead of starting the queue again.
package queue

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/rangertaha/scour/internal/store"
)

// Storage is a colly queue backed by the database, scoped to one item.
//
// Requests are popped highest score first, so once a model exists the crawler
// spends its budget on the promising part of a site. Until then every score is
// equal and the tie-break on insertion order gives the same breadth-first walk
// colly would have done on its own.
type Storage struct {
	ctx    context.Context
	store  *store.Store
	itemID uint

	// scoreOf is consulted when a request is added, to decide its priority.
	// It is set by the crawler, which is the only part that knows how to
	// score, so this package stays free of scoring logic.
	scoreOf func(data []byte) float64

	// refill adds more seeds when the queue runs dry, returning how many it
	// added. It is set by the crawler, which is the only part that knows what
	// the seeds are.
	refill func() int

	// afford is asked, before a request is handed out, whether the crawl can
	// still pay for one, and claims the slot when it says yes. It is called
	// under deq, so the claim and the lease it pays for cannot interleave.
	//
	// The check belongs here and nowhere later. A callback that decides a
	// request is unaffordable has only one way to stop it, and aborting marks
	// the URL visited in this same storage, so a page nobody fetched is
	// remembered as done. Declining to hand it out leaves it queued for the
	// next run, which is the whole point of stopping on a budget rather than
	// on exhaustion.
	afford func() bool

	mu sync.Mutex
	// deq serialises handing a request out. Without it two threads can both
	// find the budget affordable and both take one, and the crawl overshoots
	// by however many were asking at once.
	deq    sync.Mutex
	frozen atomic.Bool
}

// New returns a queue storage for one item.
func New(ctx context.Context, s *store.Store, itemID uint) *Storage {
	return &Storage{ctx: ctx, store: s, itemID: itemID}
}

// SetScorer installs the function that assigns a priority to a serialised
// request. Without one every request has the same score, which is the M2
// behaviour.
func (s *Storage) SetScorer(f func(data []byte) float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scoreOf = f
}

// SetRefill installs the function consulted when the queue is empty.
//
// Seeds are queued a batch at a time rather than all at once, so an empty queue
// does not necessarily mean an exhausted crawl: it may only mean the next batch
// has not been asked for yet.
func (s *Storage) SetRefill(f func() int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refill = f
}

// SetBudget installs the function consulted before each request is handed out.
//
// Passing nil, or never calling this, leaves the queue unmetered.
func (s *Storage) SetBudget(f func() bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.afford = f
}

// topUp adds the next batch of seeds, returning how many were added.
func (s *Storage) topUp() int {
	s.mu.Lock()
	f := s.refill
	s.mu.Unlock()
	if f == nil {
		return 0
	}
	return f()
}

// Init implements colly's queue.Storage. The schema comes from the store's
// migrations.
func (s *Storage) Init() error { return nil }

// Freeze makes the queue report itself empty without discarding anything.
//
// This is how a crawl stops early and stays resumable. Aborting requests in a
// callback does not work: colly marks a request visited before the callback
// runs, so an aborted URL is remembered as done and never retried. Refusing to
// hand any more out leaves them queued and unvisited, and colly's loop ends
// cleanly once the requests already in flight finish.
func (s *Storage) Freeze() {
	s.frozen.Store(true)
}

// Frozen reports whether the queue has been frozen.
func (s *Storage) Frozen() bool { return s.frozen.Load() }

// AddRequest implements colly's queue.Storage.
func (s *Storage) AddRequest(data []byte) error {
	score := 0.0
	s.mu.Lock()
	f := s.scoreOf
	s.mu.Unlock()
	if f != nil {
		score = f(data)
	}
	return s.store.PushQueue(s.ctx, s.itemID, score, s.hashOf(data), data)
}

// hashOf recovers the URL from a serialised request and keys it the way the
// frontier does, so the item can be released when its fetch is recorded.
func (s *Storage) hashOf(data []byte) string {
	var req struct {
		URL string `json:"URL"`
	}
	if err := json.Unmarshal(data, &req); err != nil || req.URL == "" {
		return ""
	}
	return store.URLHash(s.itemID, req.URL)
}

// GetRequest implements colly's queue.Storage.
//
// colly reads any error as "the queue is empty and the crawl is over", so an
// exhausted queue and a broken database are indistinguishable to it. Both are
// returned as an error here, and the distinction is preserved for scour's own
// callers by [store.ErrQueueEmpty].
func (s *Storage) GetRequest() ([]byte, error) {
	if s.frozen.Load() {
		return nil, store.ErrQueueEmpty
	}
	s.deq.Lock()
	defer s.deq.Unlock()

	s.mu.Lock()
	afford := s.afford
	s.mu.Unlock()

	// Asked before the lease, so a request the crawl cannot pay for is never
	// taken out of the queue in the first place.
	if afford != nil && !afford() {
		return nil, store.ErrQueueEmpty
	}

	data, err := s.store.LeaseQueue(s.ctx, s.itemID, 0)
	if errors.Is(err, store.ErrQueueEmpty) && s.topUp() > 0 {
		// Empty only meant the next batch of seeds had not been queued yet.
		data, err = s.store.LeaseQueue(s.ctx, s.itemID, 0)
	}
	if err != nil {
		return nil, err
	}
	return data, nil
}

// QueueSize implements colly's queue.Storage. A frozen queue reports zero, so
// colly's loop winds down instead of spinning on a queue it cannot read.
func (s *Storage) QueueSize() (int, error) {
	if s.frozen.Load() {
		return 0, nil
	}
	n, err := s.store.QueueSize(s.ctx, s.itemID)
	if err != nil || n > 0 {
		return n, err
	}
	// colly stops its loop when the queue reports empty, so an empty queue with
	// seeds still unqueued has to top up here too, not only on the read.
	if s.topUp() > 0 {
		return s.store.QueueSize(s.ctx, s.itemID)
	}
	return 0, nil
}

// IsEmpty reports whether the queue has been drained, distinguishing that from
// a failed read in a way colly's own interface cannot.
func (s *Storage) IsEmpty() (bool, error) {
	n, err := s.QueueSize()
	if err != nil {
		if errors.Is(err, store.ErrQueueEmpty) {
			return true, nil
		}
		return false, err
	}
	return n == 0, nil
}
