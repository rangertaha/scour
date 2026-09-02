// SPDX-License-Identifier: GPL-3.0-or-later

// Package nats publishes records to a NATS subject, as measurements.
//
// Import it for its side effect to make the format available:
//
//	import _ "github.com/rangertaha/scour/internal/exporter/nats"
//
// # Publishing is an export like any other
//
// Saving to storage and publishing to a stream are both deliveries, so both are
// exporters. An item declared once can be written as a file for whatever reads
// the archive and published here for whatever is listening now, and neither
// delivery is privileged.
//
// What travels is the measurement rather than the record: a name, the tags to
// group by, the fields measured, and the event's own time. The record is the
// shape an export has, the measurement is the shape a stream has, and the split
// between tags and fields is derived from the item's declaration rather than
// written out again here.
//
// # Two shapes, one mechanism
//
// A headline happens once. A price is the same thing measured again. The
// difference shows up in the subject, and the item's `of` is what puts it
// there:
//
//	events.news.headline                 every headline
//	events.markets.price.<company>       one subject per company
//
// Which is what lets a consumer subscribe to one company rather than filter the
// firehose, and what makes the latest value a fetch rather than a scan. One
// mechanism serves both: the entity is appended when the item declared one, and
// an item that declared none publishes to the subject it was given.
package nats

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	natsgo "github.com/nats-io/nats.go"

	"github.com/rangertaha/scour/internal/engine"
	"github.com/rangertaha/scour/internal/exporter"
	"github.com/rangertaha/scour/internal/record"
)

func init() {
	exporter.Register("nats", open)
}

// Config is what a nats exporter's block may set.
type Config struct {
	// Subject is the root every record of this item is published under.
	//
	// Required, and deliberately not defaulted to the item's name: a subject is
	// the contract with whatever is listening, and one this invented would be a
	// contract nobody had agreed to.
	Subject string `hcl:"subject"`

	// URL is a server to publish to. Empty publishes on the connection this
	// process already holds, which is what [Use] is for.
	URL string `hcl:"url,optional"`
}

// shared is the process's own connection, for exporters whose block names no
// server.
//
// A crawl node already holds a connection to the bus. Dialling a second one per
// exporter would multiply the connections a cluster carries by the number of
// items it writes, for no gain, so the node lends its own and only an exporter
// pointed at a different server opens anything.
var shared struct {
	mu   sync.RWMutex
	conn *natsgo.Conn
}

// Use lends this process's connection to every exporter that named no url.
//
// Called once by whatever already has one. Passing nil takes it back, which is
// what a caller shutting its connection down should do rather than leave
// exporters holding something closed.
func Use(conn *natsgo.Conn) {
	shared.mu.Lock()
	defer shared.mu.Unlock()
	shared.conn = conn
}

// stream publishes one item's records.
type stream struct {
	mu      sync.Mutex
	conn    *natsgo.Conn
	subject string
	shape   *engine.Item

	// owned says whether this dialled the connection, and so whether it may
	// close it. Closing a connection somebody else lent us would take the bus
	// away from the node that is still using it.
	owned  bool
	closed bool
}

func open(_ context.Context, cfg exporter.Config) (exporter.Exporter, error) {
	var c Config
	if err := cfg.Body.Decode(&c); err != nil {
		return nil, err
	}
	if strings.TrimSpace(c.Subject) == "" {
		return nil, errors.New("exporter/nats: no subject, and nothing would receive this")
	}

	conn, owned, err := connect(c.URL)
	if err != nil {
		return nil, err
	}
	return &stream{
		conn:    conn,
		subject: strings.TrimSpace(c.Subject),
		shape:   cfg.Shape,
		owned:   owned,
	}, nil
}

// connect resolves which connection this exporter publishes on.
//
// Refusing when there is neither a url nor a lent connection is the point: an
// exporter that quietly published nowhere would be the failure nobody notices
// until they go looking for the events.
func connect(url string) (*natsgo.Conn, bool, error) {
	if url == "" {
		shared.mu.RLock()
		defer shared.mu.RUnlock()

		if shared.conn == nil {
			return nil, false, errors.New(
				"exporter/nats: no url, and this process has no connection to lend")
		}
		return shared.conn, false, nil
	}

	conn, err := natsgo.Connect(url, natsgo.Name("scour-exporter"))
	if err != nil {
		return nil, false, fmt.Errorf("exporter/nats: %s: %w", url, err)
	}
	return conn, true, nil
}

// Write publishes each record as a measurement, in JSON.
//
// JSON rather than the line-protocol text a time-series store takes, because a
// subject is read by whatever somebody wrote this week and JSON is what every
// one of those can already parse. Rendering the line protocol is the job of the
// thing that writes to the store, and it has the measurement it needs.
func (s *stream) Write(_ context.Context, records ...*record.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return fmt.Errorf("exporter/nats: %s: already closed", s.subject)
	}

	for _, r := range records {
		measurement := r.Measure(s.shape)

		body, err := json.Marshal(measurement)
		if err != nil {
			return fmt.Errorf("exporter/nats: %s: %w", s.subject, err)
		}

		subject := subjectFor(s.subject, s.shape, measurement)
		if err := s.conn.Publish(subject, body); err != nil {
			return fmt.Errorf("exporter/nats: %s: %w", subject, err)
		}
	}
	return nil
}

// Close flushes, and then releases the connection if this opened it.
//
// The flush is the whole of why this method has to do anything. Publish buffers,
// so a crawl that ends immediately after its last record would exit with that
// record still in the client's buffer and publish nothing at all, which is
// exactly the run whose output somebody goes looking for.
func (s *stream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil
	}
	s.closed = true

	err := s.conn.Flush()
	if s.owned {
		s.conn.Close()
	}
	if err != nil {
		return fmt.Errorf("exporter/nats: %s: %w", s.subject, err)
	}
	return nil
}

// subjectFor is where one measurement goes.
//
// The shape says which kind of entity the item observes and the measurement
// says which one it is: `item "price" { of = "company" }` beside a property
// declaring `entity = "company"` gives `price,company=acme`, so the tag to read
// is the one named by the shape's `of`.
//
// This used to read a tag literally called "of", which no record has ever had:
// "of" is not a property, nothing extracts it, and [engine.Item.Tags] listed it
// as a name with no value behind it. So every price published to the root
// subject and the per-entity subject - the whole point of the feature - never
// fired. The test written for it hand-built a record with an "of" value, which
// no extraction path produces, so it stayed green.
//
// A record whose item shape is unknown is measured with no tags at all, since
// nothing has said which of its values are dimensions and guessing would invent
// series that cannot be un-invented, so it publishes to the root subject with
// everything as fields.
func subjectFor(root string, shape *engine.Item, m *record.Measurement) string {
	if shape == nil || shape.Of == "" {
		return root
	}
	entity := m.Tags[shape.Of]
	if entity == "" {
		return root
	}
	return root + "." + token(entity)
}

// token makes one subject token out of an entity's name.
//
// A dot inside it would add a level to the subject, so a consumer's
// `events.markets.price.*` would stop matching; a `*` or a `>` would be a
// wildcard where a name was meant; a space is not legal in a subject at all.
// None of those is worth dropping a record over, so they are replaced and the
// record is published.
func token(entity string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '.' || r == '*' || r == '>':
			return '_'
		case r <= ' ' || r == 0x7f:
			return '_'
		}
		return r
	}, entity)
}
