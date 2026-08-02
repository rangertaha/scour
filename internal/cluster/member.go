// SPDX-License-Identifier: GPL-3.0-or-later

package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/rangertaha/scour/internal/store"
)

// Membership is this process's own place in the fleet.
//
// Nil is a working value. A process that could not reach the registry holds
// one, and every method on it does nothing rather than failing, so no part of
// the registry can ever be the reason a crawl stops.
type Membership struct {
	reg    *Registry
	health Health

	// cancel stops the heartbeat and the watcher; wg is how Close knows they
	// have stopped, because a beat still in flight would write the entry back
	// after it had been removed.
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu   sync.Mutex
	node Node
	// mark and last are the previous beat's clock and page count, which the
	// rate is a difference of.
	mark time.Time
	last int64

	leaving chan struct{}
	once    sync.Once
}

// never is the channel a nil membership hands out. A process with no registry
// is never asked to leave through one, so waiting on this waits forever, which
// is the correct answer rather than an immediate false alarm.
var never = make(chan struct{})

// Join registers this process and starts its heartbeat.
//
// health is asked once per beat for what this process is carrying. It may be
// nil, which registers a node that reports no numbers: a node with an unknown
// queue depth is still a node worth listing.
//
// A nil registry joins nothing and returns a nil membership with no error,
// because the caller has already been told the registry is unreachable and
// telling it twice would only give it a second thing to ignore.
func (r *Registry) Join(ctx context.Context, self Node, health Health) (*Membership, error) {
	if r == nil {
		return nil, nil
	}

	if self.Name == "" {
		self.Name = DefaultName()
	}
	self.Name = Key(self.Name)
	self.State = StateUp
	self.Joined = time.Now().UTC()
	self.Seen = self.Joined

	name, err := r.claim(ctx, self)
	if err != nil {
		return nil, err
	}
	self.Name = name

	m := &Membership{
		reg:     r,
		health:  health,
		node:    self,
		mark:    self.Joined,
		leaving: make(chan struct{}),
	}
	// The counter's starting value, so the first rate is measured over the
	// first beat rather than over the whole life of a process that was already
	// running when it joined.
	if health != nil {
		m.last = health(ctx).Fetched
	}

	// The goroutines below stop when either the caller's context ends or Close
	// is called, whichever comes first.
	beat, cancel := context.WithCancel(ctx)
	m.cancel = cancel

	// Subscribed here rather than inside the goroutine, because the goroutine
	// is not running yet when Join returns and a watcher that only carries
	// updates cannot see one that landed before it existed. `scour node leave`
	// typed the instant a node came up was lost that way.
	watcher, err := r.kv.Watch(beat, Key(self.Name), jetstream.UpdatesOnly())
	if err != nil {
		// Without it a drain request arrives at the next heartbeat instead of
		// at once, which report covers. Not a reason to refuse to run.
		slog.Debug("not watching for a drain request", "node", self.Name, "err", err)
	} else {
		m.wg.Add(1)
		go m.watch(beat, watcher)
	}

	m.wg.Add(1)
	go m.beat(beat)

	slog.Info("joined the cluster", "node", m.node.Name, "role", m.node.Role.String())
	return m, nil
}

// claim takes a name, or a name close to it.
//
// A crashed process leaves its entry behind until the time to live clears it,
// and the replacement started on the same machine wants its own name back
// rather than a new one, so an entry nobody has heard from is taken over. An
// entry still beating belongs to a live node, and two nodes sharing a name
// would each overwrite the other's heartbeat and each look intermittently down,
// so a newcomer takes the process id as a suffix instead: unique on a machine,
// and still recognisable as which machine it is.
func (r *Registry) claim(ctx context.Context, self Node) (string, error) {
	existing, err := r.read(ctx, self.Name)
	switch {
	case errors.Is(err, store.ErrNotFound):
		// Nobody has the name.
	case err != nil:
		return "", err
	case time.Since(existing.Seen) <= r.opts.Down:
		self.Name = Key(fmt.Sprintf("%s-%d", self.Name, os.Getpid()))
	}
	if err := r.put(ctx, self); err != nil {
		return "", err
	}
	return self.Name, nil
}

// Name is what this node is registered as, which is not always the name that
// was asked for.
func (m *Membership) Name() string {
	if m == nil {
		return ""
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.node.Name
}

// Draining reports whether this node has been asked to stop taking new work.
// It is the check a component makes before accepting some.
func (m *Membership) Draining() bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.node.State == StateDraining
}

// Leaving is closed once this node has been asked to drain, from here or from
// somewhere else. It is what a caller selects on to shut its services down.
func (m *Membership) Leaving() <-chan struct{} {
	if m == nil {
		return never
	}
	return m.leaving
}

// Drain puts this node into the draining state on its own initiative, which is
// what a process does when it has decided to stop rather than been told to.
//
// The heartbeat carries on, because a draining node is still a running node and
// letting it age out would report it as down while it was still finishing work.
func (m *Membership) Drain(ctx context.Context) error {
	if m == nil {
		return nil
	}
	m.drained("this node asked to leave")

	m.mu.Lock()
	node := m.node
	m.mu.Unlock()
	return m.reg.put(ctx, node)
}

// drained records that this node is leaving, however it found out.
//
// Idempotent, because it has three callers that can all be right at once: the
// process itself, the watcher seeing somebody else's request, and the
// heartbeat seeing the same request written under it.
func (m *Membership) drained(why string) {
	m.mu.Lock()
	already := m.node.State == StateDraining
	m.node.State = StateDraining
	name := m.node.Name
	m.mu.Unlock()

	if !already {
		slog.Info("draining", "node", name, "why", why)
	}
	m.once.Do(func() { close(m.leaving) })
}

// Close stops the heartbeat and takes this node off the listing.
//
// The entry is removed rather than left to age out, because a process that got
// this far shut down cleanly and there is nothing to be learned from listing it
// as down for the next few minutes. A process that did not get this far is
// exactly the case the time to live is for.
//
// ctx bounds the removal, so it must not be the one that was just cancelled to
// stop the services: a cancelled context removes nothing and leaves the node
// looking alive until it ages out.
func (m *Membership) Close(ctx context.Context) {
	if m == nil {
		return
	}
	m.cancel()
	m.wg.Wait()

	if err := m.reg.kv.Purge(ctx, Key(m.Name())); err != nil {
		// Nothing to be done about it, and nothing broken by it: the entry
		// stops being written to and ages out like any other node that went
		// away without saying so.
		slog.Debug("node entry left behind", "node", m.Name(), "err", err)
	}
}

// beat writes the heartbeat until it is told to stop.
func (m *Membership) beat(ctx context.Context) {
	defer m.wg.Done()

	ticker := time.NewTicker(m.reg.opts.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := m.report(ctx); err != nil && ctx.Err() == nil {
				// A beat that does not land is not worth stopping for. The
				// listing goes stale and then the entry ages out, which is
				// what should happen to a node nobody can reach, while the
				// node itself carries on fetching pages.
				slog.Debug("heartbeat not written", "node", m.Name(), "err", err)
			}
		}
	}
}

// report writes one heartbeat, carrying the numbers this beat's health call
// returned.
//
// The rate is worked out here, from a counter the reporter only ever adds to.
// Asking a component for a rate instead would mean every component remembering
// when it last reported and to whom; differencing a monotonic counter puts that
// in one place, and a beat that was missed then averages over the gap rather
// than reporting a spike at the next one.
func (m *Membership) report(ctx context.Context) error {
	// Read before writing, because a heartbeat would otherwise overwrite a
	// drain request that landed between two beats, and the request would be
	// lost with nobody the wiser. The watcher normally sees it first and this
	// finds nothing new; when there is no watcher, or it was still starting
	// when the request arrived, this is what makes the request land anyway.
	// One read per beat is cheap next to silently ignoring `scour node leave`.
	if stored, err := m.reg.read(ctx, Key(m.Name())); err == nil && stored.State == StateDraining {
		m.drained("asked to leave")
	}

	var load Load
	if m.health != nil {
		load = m.health(ctx)
	}
	now := time.Now().UTC()

	m.mu.Lock()
	// A counter that went backwards is a component that restarted its count,
	// and dividing by the negative would report a rate below zero.
	if elapsed := now.Sub(m.mark).Seconds(); elapsed > 0 && load.Fetched >= m.last {
		m.node.Rate = float64(load.Fetched-m.last) / elapsed
	}
	m.mark, m.last = now, load.Fetched
	m.node.Queue = load.Queue
	m.node.Seen = now
	node := m.node
	m.mu.Unlock()

	return m.reg.put(ctx, node)
}

// watch notices somebody else asking this node to leave.
//
// `scour node leave` is typed in another process, so the only thing it can do
// is write draining into this node's entry. A KV bucket is a stream underneath,
// so watching our own key is how that request arrives here immediately rather
// than never, and closing the channel is what lets the caller stop its services
// and finish what is in flight.
func (m *Membership) watch(ctx context.Context, watcher jetstream.KeyWatcher) {
	defer m.wg.Done()
	defer func() {
		if err := watcher.Stop(); err != nil {
			slog.Debug("drain watcher not stopped", "node", m.Name(), "err", err)
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case entry, ok := <-watcher.Updates():
			if !ok {
				return
			}
			// A nil entry marks the end of the initial values, and a delete
			// marker carries no value to read.
			if entry == nil || entry.Operation() != jetstream.KeyValuePut {
				continue
			}
			var node Node
			if err := json.Unmarshal(entry.Value(), &node); err != nil {
				continue
			}
			if node.State != StateDraining {
				continue
			}
			// Our own heartbeats come back through here too once we are
			// draining, which is why this is idempotent rather than
			// conditional on who wrote the update.
			m.drained("asked to leave")
		}
	}
}
