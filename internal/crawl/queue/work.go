// SPDX-License-Identifier: GPL-3.0-or-later

package queue

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/rangertaha/scour/internal/store"
)

// waitForWork is how long a reader waits before letting colly's loop look
// around again.
//
// colly ignores an error from GetRequest and re-checks the queue, so a timeout
// is a harmless retry rather than a failure. Blocking indefinitely instead
// would stop the loop noticing that the crawl has been asked to stop.
const waitForWork = time.Second

// Work is a queue fed by the broker rather than by the database.
//
// It is the seam that lets a crawler run in its own process without changing
// how it crawls: colly keeps its loop, its threads, robots, cookies, redirects
// and the browser escalation, and only the source of requests changes. Writing
// a second fetcher would mean keeping two implementations of all of that in
// step.
//
// It is deliberately not a frontier. Ordering by score needs the whole queue
// visible at once, which is why that stays in the store; this holds only what
// has already been handed out.
type Work struct {
	mu    sync.Mutex
	ready [][]byte
	// wake is closed and replaced when work arrives, so a reader waits instead
	// of polling.
	wake chan struct{}

	// afford is asked before handing work out, so --max-pages means the same
	// thing to a crawler fed by the broker as to one reading the database.
	afford func() bool

	done atomic.Bool
}

// NewWork returns an empty work queue.
func NewWork() *Work {
	return &Work{wake: make(chan struct{})}
}

// Offer adds a request the store handed out.
func (w *Work) Offer(data []byte) {
	w.mu.Lock()
	w.ready = append(w.ready, data)
	close(w.wake)
	w.wake = make(chan struct{})
	w.mu.Unlock()
}

// Close stops the queue, which is what ends colly's loop. Until it is called
// an empty queue means "nothing yet", not "nothing more".
func (w *Work) Close() { w.done.Store(true) }

// Init implements colly's queue.Storage.
func (w *Work) Init() error { return nil }

// AddRequest implements colly's queue.Storage, and deliberately drops.
//
// Links a crawler finds are published as discovered and judged by the store,
// which is the only component holding the entity's scope. Queueing them here
// as well would fetch them without that check, and without the scoring that
// decides what is worth fetching next.
func (w *Work) AddRequest([]byte) error { return nil }

// GetRequest implements colly's queue.Storage, waiting briefly for work.
func (w *Work) GetRequest() ([]byte, error) {
	for {
		w.mu.Lock()
		if len(w.ready) > 0 {
			// Claimed under the same lock that hands the request out, so two
			// threads cannot both spend the last of the budget.
			if w.afford != nil && !w.afford() {
				w.mu.Unlock()
				return nil, store.ErrQueueEmpty
			}
			data := w.ready[0]
			w.ready = w.ready[1:]
			w.mu.Unlock()
			return data, nil
		}
		wake := w.wake
		w.mu.Unlock()

		if w.done.Load() {
			return nil, store.ErrQueueEmpty
		}
		select {
		case <-wake:
		case <-time.After(waitForWork):
			return nil, store.ErrQueueEmpty
		}
	}
}

// QueueSize implements colly's queue.Storage.
//
// A running crawler is never empty, because emptiness is how colly decides the
// crawl is over and a crawler waiting for its next URL is not over. It reports
// what it holds, or one, until it is closed.
func (w *Work) QueueSize() (int, error) {
	w.mu.Lock()
	n := len(w.ready)
	w.mu.Unlock()
	if n == 0 && !w.done.Load() {
		return 1, nil
	}
	return n, nil
}

// IsEmpty implements colly's queue.Storage.
func (w *Work) IsEmpty() (bool, error) {
	n, err := w.QueueSize()
	return n == 0, err
}

// Freeze implements the frontier a crawl expects. Closing is what stops this
// queue, so freezing it is the same thing.
func (w *Work) Freeze() { w.Close() }

// Frozen reports whether the queue has been closed.
func (w *Work) Frozen() bool { return w.done.Load() }

// SetScorer implements the frontier a crawl expects, and does nothing: the
// store decides what is worth fetching, and this queue only holds what it
// already chose.
func (w *Work) SetScorer(func([]byte) float64) {}

// SetRefill implements the frontier a crawl expects, and does nothing: there
// are no seeds here to top up.
func (w *Work) SetRefill(func() int) {}

// SetBudget installs the function consulted before each request is handed out.
func (w *Work) SetBudget(f func() bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.afford = f
}
