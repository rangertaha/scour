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
)

// createStreams declares the streams components consume from.
//
// Both are work queues: a message is delivered to one consumer and removed
// once acknowledged, which is what makes a restarted component pick up where
// the last one stopped rather than replaying everything.
func (b *Bus) createStreams(ctx context.Context) error {
	streams := []jetstream.StreamConfig{
		{
			Name:      StreamCrawl,
			Subjects:  []string{AllEntities(SubjectFetched), AllEntities(SubjectDiscovered)},
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

	for _, cfg := range streams {
		if _, err := b.js.CreateOrUpdateStream(ctx, cfg); err != nil {
			return fmt.Errorf("declare stream %s: %w", cfg.Name, err)
		}
	}
	return nil
}
