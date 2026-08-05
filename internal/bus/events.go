// SPDX-License-Identifier: GPL-3.0-or-later

package bus

import (
	"context"
	"time"

	"github.com/rangertaha/scour/internal/event"
)

// The event store as a service: create, read, update, delete.
//
// Create and update are one operation, because an event's identity is derived
// from what it is an observation of rather than allocated: the name, the tags
// and the time. A caller that had to know whether a point already existed would
// have to read before every write, and a crawl re-reading a page is the
// ordinary case rather than the exception. See [event.Store.Put].

// EventQueue is the queue group the event service answers in.
const EventQueue = "scour-event"

// EventSubject is where one event operation is asked for:
// scour.event.<operation>.
func EventSubject(op string) string { return "scour.event." + op }

// Log is what an event store can do, here or somewhere else.
//
// Both [event.Store] and [Events] satisfy it, for the reason [Graph] gives.
type Log interface {
	Put(ctx context.Context, e event.Event) (string, error)
	Get(ctx context.Context, id string) (*event.Event, error)
	List(ctx context.Context, q event.Query) ([]*event.Event, error)
	Delete(ctx context.Context, id string) error
	Retract(ctx context.Context, job string) (int64, error)
	Names(ctx context.Context) ([]event.Series, error)
	Close() error
}

type (
	putAsk struct {
		Event event.Event `json:"event"`
	}
	listAsk struct {
		Query event.Query `json:"query"`
	}
)

// ServeEvents answers for an event store until the returned service is closed.
//
// The store is not closed with it, for the reason [Conn.ServeEntities] gives.
func (c *Conn) ServeEvents(store Log) (*Service, error) {
	s := &Service{}

	serving(c, s, EventSubject("put"), EventQueue,
		func(ctx context.Context, a putAsk) (string, error) {
			return store.Put(ctx, a.Event)
		})
	serving(c, s, EventSubject("get"), EventQueue,
		func(ctx context.Context, a idAsk) (*event.Event, error) {
			return store.Get(ctx, a.ID)
		})
	serving(c, s, EventSubject("list"), EventQueue,
		func(ctx context.Context, a listAsk) ([]*event.Event, error) {
			return store.List(ctx, a.Query)
		})
	serving(c, s, EventSubject("delete"), EventQueue,
		func(ctx context.Context, a idAsk) (none, error) {
			return none{}, store.Delete(ctx, a.ID)
		})
	serving(c, s, EventSubject("retract"), EventQueue,
		func(ctx context.Context, a jobAsk) (int64, error) {
			return store.Retract(ctx, a.Job)
		})
	serving(c, s, EventSubject("names"), EventQueue,
		func(ctx context.Context, _ nothingAsk) ([]event.Series, error) {
			return store.Names(ctx)
		})

	if s.err != nil {
		s.Close()
		return nil, s.err
	}
	return s, nil
}

// Events is an event store that is somewhere else. It satisfies [Log].
type Events struct {
	conn *Conn
	wait time.Duration
}

// NewEvents returns a client for the event service.
func (c *Conn) NewEvents(wait time.Duration) *Events {
	return &Events{conn: c, wait: wait}
}

func (e *Events) Put(ctx context.Context, one event.Event) (string, error) {
	return call[putAsk, string](ctx, e.conn, EventSubject("put"), e.wait, putAsk{Event: one})
}

func (e *Events) Get(ctx context.Context, id string) (*event.Event, error) {
	return call[idAsk, *event.Event](ctx, e.conn, EventSubject("get"), e.wait, idAsk{ID: id})
}

func (e *Events) List(ctx context.Context, q event.Query) ([]*event.Event, error) {
	return call[listAsk, []*event.Event](ctx, e.conn, EventSubject("list"), e.wait, listAsk{Query: q})
}

func (e *Events) Delete(ctx context.Context, id string) error {
	_, err := call[idAsk, none](ctx, e.conn, EventSubject("delete"), e.wait, idAsk{ID: id})
	return err
}

func (e *Events) Retract(ctx context.Context, job string) (int64, error) {
	return call[jobAsk, int64](ctx, e.conn, EventSubject("retract"), e.wait, jobAsk{Job: job})
}

func (e *Events) Names(ctx context.Context) ([]event.Series, error) {
	return call[nothingAsk, []event.Series](ctx, e.conn, EventSubject("names"), e.wait, nothingAsk{})
}

// Close releases the client and not the store, for the reason
// [Entities.Close] gives.
func (e *Events) Close() error { return nil }
