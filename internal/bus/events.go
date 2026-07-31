// SPDX-License-Identifier: GPL-3.0-or-later

package bus

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// Fetched is published when a page has been fetched, or deliberately not.
//
// It carries the metadata rather than the body: the body goes to the page
// cache, and the cache key is how a consumer finds it. Putting megabytes of
// HTML through the broker would buy nothing, since every component that wants
// it can read the cache.
type Fetched struct {
	Entity      string        `json:"entity"`
	EntityID    uint          `json:"entity_id"`
	URL         string        `json:"url"`
	ParentURL   string        `json:"parent_url,omitempty"`
	Depth       int           `json:"depth"`
	Score       float64       `json:"score"`
	Status      string        `json:"status"`
	StatusCode  int           `json:"status_code,omitempty"`
	ContentType string        `json:"content_type,omitempty"`
	Size        int64         `json:"size,omitempty"`
	Latency     time.Duration `json:"latency,omitempty"`
	CacheKey    string        `json:"cache_key,omitempty"`
}

// Discovered is published when a link is found and scored.
type Discovered struct {
	Entity    string  `json:"entity"`
	EntityID  uint    `json:"entity_id"`
	URL       string  `json:"url"`
	ParentURL string  `json:"parent_url,omitempty"`
	Depth     int     `json:"depth"`
	Score     float64 `json:"score"`
}

// Work is published when the store hands a URL to a crawler.
//
// It carries the whole serialised request rather than only the URL, because the
// crawler feeds it back to colly, which needs the depth and the context the
// scorer and the lineage were recorded in.
type Work struct {
	Entity   string `json:"entity"`
	EntityID uint   `json:"entity_id"`
	URL      string `json:"url"`
	Request  []byte `json:"request"`
}

// Metric is one measurement taken while the pipeline ran.
//
// Deliberately generic. A typed event per measurement would need a new type,
// a new subject and a new consumer every time someone wanted to watch something
// else; a name and a value need none of that, and the labels carry whatever
// distinguishes one reading from another.
type Metric struct {
	Entity   string            `json:"entity"`
	EntityID uint              `json:"entity_id,omitempty"`
	Name     string            `json:"name"`
	Value    float64           `json:"value"`
	Unit     string            `json:"unit,omitempty"`
	At       time.Time         `json:"at"`
	Labels   map[string]string `json:"labels,omitempty"`
}

// Metric names, so a publisher and a consumer agree on the spelling.
const (
	MetricFetchLatency = "fetch.latency"
	MetricFetchBytes   = "fetch.bytes"
	MetricFetchStatus  = "fetch.status"
	MetricQueueDepth   = "queue.depth"
	MetricQueueFlight  = "queue.in_flight"
	MetricRecords      = "extract.records"
	MetricRules        = "extract.rules"
)

// Emit publishes a measurement and does not care whether it arrived.
//
// Observability must not be able to break the thing it observes. A metric that
// cannot be published is logged at debug and forgotten: no error is returned,
// nothing is retried, and no caller has to decide what to do about a number
// that went missing. This is why it is not Publish, which deduplicates and
// reports failure because a page really must be written exactly once.
func (b *Bus) Emit(ctx context.Context, entity string, m Metric) {
	m.Entity = entity
	if m.At.IsZero() {
		m.At = time.Now().UTC()
	}
	body, err := json.Marshal(m)
	if err != nil {
		slog.Debug("metric not encoded", "name", m.Name, "err", err)
		return
	}
	if _, err := b.js.PublishAsync(Subject(entity, SubjectMetric), body); err != nil {
		slog.Debug("metric not published", "name", m.Name, "err", err)
	}
}

// Publish sends a message, deduplicated on id.
//
// The id is what makes at-least-once delivery safe to combine with a broker
// that may itself redeliver: two publishes of the same URL within the
// duplicate window collapse to one message.
func (b *Bus) Publish(ctx context.Context, subject, id string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode %s: %w", subject, err)
	}

	msg := &nats.Msg{Subject: subject, Data: body}
	if id != "" {
		msg.Header = nats.Header{jetstream.MsgIDHeader: []string{id}}
	}

	if _, err := b.js.PublishMsg(ctx, msg); err != nil {
		return fmt.Errorf("publish %s: %w", subject, err)
	}
	return nil
}

// Handler processes one message. Returning an error leaves the message
// unacknowledged, so it is redelivered.
type Handler func(ctx context.Context, data []byte) error

// Consume subscribes durably to a subject and runs handler for each message.
//
// The returned stop function drains in-flight work before returning, which is
// what lets a command wait for the pipeline to catch up before printing
// results gathered by another component.
func (b *Bus) Consume(ctx context.Context, stream, durable, subject string, handler Handler) (func(), error) {
	s, err := b.js.Stream(ctx, stream)
	if err != nil {
		return nil, fmt.Errorf("open stream %s: %w", stream, err)
	}

	cons, err := s.CreateOrUpdateConsumer(ctx, jetstream.ConsumerConfig{
		Durable:       durable,
		FilterSubject: subject,
		AckPolicy:     jetstream.AckExplicitPolicy,
		MaxDeliver:    5,
		AckWait:       30 * time.Second,
	})
	if err != nil {
		return nil, fmt.Errorf("create consumer %s: %w", durable, err)
	}

	sub, err := cons.Consume(func(msg jetstream.Msg) {
		if err := handler(ctx, msg.Data()); err != nil {
			// Leaving it unacknowledged is the retry: the broker redelivers
			// until MaxDeliver, then drops it rather than blocking the queue.
			if nakErr := msg.Nak(); nakErr != nil {
				slog.Warn("could not nak message", "subject", msg.Subject(), "err", nakErr)
			}
			slog.Error("handler failed", "subject", msg.Subject(), "err", err)
			return
		}
		if err := msg.Ack(); err != nil {
			slog.Warn("could not ack message", "subject", msg.Subject(), "err", err)
		}
	})
	if err != nil {
		return nil, fmt.Errorf("consume %s: %w", subject, err)
	}

	return func() { sub.Drain() }, nil
}

// Pending reports how many messages are waiting on a stream. A command uses it
// to wait for the pipeline to settle.
func (b *Bus) Pending(ctx context.Context, stream string) (uint64, error) {
	s, err := b.js.Stream(ctx, stream)
	if err != nil {
		return 0, fmt.Errorf("open stream %s: %w", stream, err)
	}
	info, err := s.Info(ctx)
	if err != nil {
		return 0, fmt.Errorf("stream info %s: %w", stream, err)
	}
	return info.State.Msgs, nil
}

// Drain waits until every stream is empty, or the deadline passes.
//
// A component that publishes and a component that writes are not the same
// goroutine, so a command that prints what was written has to wait for the
// writer. Without this the first crawl through the bus reports an empty
// frontier it is about to fill.
func (b *Bus) Drain(ctx context.Context, streams ...string) error {
	if len(streams) == 0 {
		streams = []string{StreamCrawl, StreamRecords}
	}

	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		var total uint64
		for _, name := range streams {
			n, err := b.Pending(ctx, name)
			if err != nil {
				return err
			}
			total += n
		}
		if total == 0 {
			return nil
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for %d pending messages: %w", total, ctx.Err())
		case <-ticker.C:
		}
	}
}
