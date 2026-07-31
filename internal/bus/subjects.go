// SPDX-License-Identifier: GPL-3.0-or-later

package bus

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// Subjects are named scour.<entity>.<stage>, so a subscriber can wildcard one
// entity or all of them.
const (
	// SubjectFetched carries the outcome of one fetch.
	SubjectFetched = "fetched"
	// SubjectDiscovered carries a URL found on a page but not yet fetched.
	SubjectDiscovered = "discovered"
	// SubjectRecord carries one extracted record.
	SubjectRecord = "record"
	// SubjectWork carries one URL handed to a crawler to fetch.
	SubjectWork = "work"
	// SubjectMetric carries one measurement taken while the pipeline ran.
	SubjectMetric = "metric"
)

// Subject builds the subject for one entity and stage.
func Subject(entity, stage string) string {
	return "scour." + sanitise(entity) + "." + stage
}

// AllEntities is the wildcard form, for a component serving every entity.
func AllEntities(stage string) string { return "scour.*." + stage }

// sanitise removes the characters NATS gives meaning to, so an entity named
// with a dot or a star cannot widen a subscription or split a subject.
func sanitise(entity string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '.', '*', '>', ' ':
			return '_'
		default:
			return r
		}
	}, entity)
}

// Stream names.
const (
	StreamCrawl   = "SCOUR_CRAWL"
	StreamRecords = "SCOUR_RECORDS"
	StreamMetrics = "SCOUR_METRICS"
)

// createStreams declares the streams components consume from.
//
// Both are work queues: a message is delivered to one consumer and removed
// once acknowledged, which is what makes a restarted component pick up where
// the last one stopped rather than replaying everything.
func (b *Bus) createStreams(ctx context.Context) error {
	streams := []jetstream.StreamConfig{
		{
			Name: StreamCrawl,
			Subjects: []string{
				AllEntities(SubjectFetched),
				AllEntities(SubjectDiscovered),
				AllEntities(SubjectWork),
			},
			Retention: jetstream.WorkQueuePolicy,
			Storage:   jetstream.MemoryStorage,
			Discard:   jetstream.DiscardOld,
			MaxAge:    time.Hour,
			// Duplicate suppression keyed on the message id, which is the URL
			// hash: at-least-once delivery plus a re-published URL both
			// collapse to one write.
			Duplicates: 5 * time.Minute,
		},
		{
			Name:       StreamRecords,
			Subjects:   []string{AllEntities(SubjectRecord)},
			Retention:  jetstream.WorkQueuePolicy,
			Storage:    jetstream.MemoryStorage,
			Discard:    jetstream.DiscardOld,
			MaxAge:     time.Hour,
			Duplicates: 5 * time.Minute,
		},
	}

	// Measurements are not work. The other two streams are work queues, where a
	// message is delivered once and removed, which is right for a page that
	// must be written exactly once and wrong for a number several things may
	// want to watch: one dashboard consuming a metric would take it from every
	// other. This one keeps its messages for anyone who asks, drops the oldest
	// when full, and forgets them quickly, so nothing observing the pipeline
	// can slow it down or fill a disk.
	streams = append(streams, jetstream.StreamConfig{
		Name:      StreamMetrics,
		Subjects:  []string{AllEntities(SubjectMetric)},
		Retention: jetstream.LimitsPolicy,
		Storage:   jetstream.MemoryStorage,
		Discard:   jetstream.DiscardOld,
		MaxAge:    15 * time.Minute,
		MaxMsgs:   100_000,
	})

	for _, cfg := range streams {
		if _, err := b.js.CreateOrUpdateStream(ctx, cfg); err != nil {
			return fmt.Errorf("declare stream %s: %w", cfg.Name, err)
		}
	}
	return nil
}
