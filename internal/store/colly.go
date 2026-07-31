// SPDX-License-Identifier: GPL-3.0-or-later

package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrQueueEmpty is returned by [Store.PopQueue] when there is nothing left to
// crawl. colly's queue treats any error from its storage as "empty", but a
// distinct value lets scour tell an exhausted queue from a broken database.
var ErrQueueEmpty = errors.New("queue is empty")

// MarkVisited records that colly has visited a request.
//
// See [Visit] for why colly's uint64 hash is stored as an int64.
func (s *Store) MarkVisited(ctx context.Context, entityID uint, requestID uint64) error {
	v := Visit{EntityID: entityID, RequestID: int64(requestID), VisitedAt: time.Now().UTC()}
	err := s.db.WithContext(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).
		Create(&v).Error
	if err != nil {
		return fmt.Errorf("mark visited: %w", err)
	}
	return nil
}

// IsVisited reports whether colly has already visited a request.
func (s *Store) IsVisited(ctx context.Context, entityID uint, requestID uint64) (bool, error) {
	var n int64
	err := s.db.WithContext(ctx).
		Model(&Visit{}).
		Where("entity_id = ? AND request_id = ?", entityID, int64(requestID)).
		Count(&n).Error
	if err != nil {
		return false, fmt.Errorf("check visited: %w", err)
	}
	return n > 0, nil
}

// VisitCount returns how many requests have been visited.
func (s *Store) VisitCount(ctx context.Context, entityID uint) (int64, error) {
	var n int64
	err := s.db.WithContext(ctx).Model(&Visit{}).Where("entity_id = ?", entityID).Count(&n).Error
	if err != nil {
		return 0, fmt.Errorf("count visits: %w", err)
	}
	return n, nil
}

// SetCookies stores the cookie header for a host.
func (s *Store) SetCookies(ctx context.Context, entityID uint, host, cookies string) error {
	c := Cookie{EntityID: entityID, Host: host, Value: cookies, UpdatedAt: time.Now().UTC()}
	err := s.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "entity_id"}, {Name: "host"}},
			DoUpdates: clause.AssignmentColumns([]string{"value", "updated_at"}),
		}).
		Create(&c).Error
	if err != nil {
		return fmt.Errorf("store cookies for %s: %w", host, err)
	}
	return nil
}

// Cookies returns the stored cookie header for a host, or an empty string.
func (s *Store) Cookies(ctx context.Context, entityID uint, host string) (string, error) {
	var c Cookie
	err := s.db.WithContext(ctx).
		Where("entity_id = ? AND host = ?", entityID, host).
		First(&c).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read cookies for %s: %w", host, err)
	}
	return c.Value, nil
}

// PushQueue appends a serialised request. hash identifies the URL so the item
// can be released when the fetch is recorded.
func (s *Store) PushQueue(ctx context.Context, entityID uint, score float64, hash string, data []byte) error {
	item := QueueItem{EntityID: entityID, Score: score, Hash: hash, Data: data}
	if err := s.db.WithContext(ctx).Create(&item).Error; err != nil {
		return fmt.Errorf("push queue: %w", err)
	}
	return nil
}

// DefaultLease is how long a handed-out request may go unreported before it
// returns to the queue.
//
// Comfortably longer than any fetch, including one that escalates to a browser,
// because returning a URL that is merely slow would fetch it twice.
const DefaultLease = 10 * time.Minute

// MaxAttempts is how many times an item may be handed out before it is
// abandoned.
//
// A hand-out does not always end in a fetch: colly declines a request it has
// already visited or that is past the depth limit, and a declined request
// reports no outcome, so nothing releases it. It would otherwise return on
// every lease expiry and be declined again for as long as the crawl ran.
const MaxAttempts = 3

// LeaseQueue hands out the next request, highest score first and oldest first
// within a score, and marks it in flight rather than removing it.
//
// The select and the update run in one transaction, so two crawlers sharing a
// database cannot be handed the same URL. The item is removed when the fetch is
// recorded; if nothing reports back before the lease expires it is handed out
// again, which is what makes a crawler dying mid-page cost a retry rather than
// a silently missing page.
func (s *Store) LeaseQueue(ctx context.Context, entityID uint, lease time.Duration) ([]byte, error) {
	if lease <= 0 {
		lease = DefaultLease
	}
	now := time.Now().UTC()
	until := now.Add(lease)

	var data []byte
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var item QueueItem
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("entity_id = ? AND (leased_until IS NULL OR leased_until < ?)", entityID, now).
			Order("score DESC, id ASC").
			First(&item).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrQueueEmpty
		}
		if err != nil {
			return fmt.Errorf("read queue: %w", err)
		}
		if item.Attempts+1 >= MaxAttempts {
			// Handed out as many times as it is going to be. Give it out once
			// more and drop it, so a request nothing will ever report cannot
			// cycle for the length of the crawl.
			if err := tx.Delete(&QueueItem{}, item.ID).Error; err != nil {
				return fmt.Errorf("abandon queue item: %w", err)
			}
			data = item.Data
			return nil
		}
		if err := tx.Model(&QueueItem{}).Where("id = ?", item.ID).
			Updates(map[string]any{
				"leased_until": until,
				"attempts":     item.Attempts + 1,
			}).Error; err != nil {
			return fmt.Errorf("lease queue item: %w", err)
		}
		data = item.Data
		return nil
	})
	if err != nil {
		return nil, err
	}
	return data, nil
}

// ReleaseQueue removes a leased item once its fetch has been reported.
func (s *Store) ReleaseQueue(ctx context.Context, entityID uint, hash string) error {
	if hash == "" {
		return nil
	}
	err := s.db.WithContext(ctx).
		Where("entity_id = ? AND hash = ?", entityID, hash).
		Delete(&QueueItem{}).Error
	if err != nil {
		return fmt.Errorf("release queue item: %w", err)
	}
	return nil
}

// QueueSize returns how many requests are waiting to be handed out. Items
// already in flight are not waiting, so they are not counted: colly ends its
// loop when the queue reports empty, and counting in-flight work would keep it
// spinning on requests it has already been given.
func (s *Store) QueueSize(ctx context.Context, entityID uint) (int, error) {
	var n int64
	err := s.db.WithContext(ctx).
		Model(&QueueItem{}).
		Where("entity_id = ? AND (leased_until IS NULL OR leased_until < ?)", entityID, time.Now().UTC()).
		Count(&n).Error
	if err != nil {
		return 0, fmt.Errorf("queue size: %w", err)
	}
	return int(n), nil
}

// ClearCrawlState drops the visited set, the queue and the cookies for an
// entity, which is what makes a re-crawl start over rather than resume.
func (s *Store) ClearCrawlState(ctx context.Context, entityID uint) error {
	db := s.db.WithContext(ctx)
	for _, model := range []any{&Visit{}, &QueueItem{}, &Cookie{}} {
		if err := db.Where("entity_id = ?", entityID).Delete(model).Error; err != nil {
			return fmt.Errorf("clear %T: %w", model, err)
		}
	}
	return nil
}

// QueuedEntities lists the entities with work waiting in the frontier, so a
// dispatcher can find what to hand out without being told which entities are
// being crawled.
func (s *Store) QueuedEntities(ctx context.Context) ([]uint, error) {
	var ids []uint
	err := s.db.WithContext(ctx).
		Model(&QueueItem{}).
		Distinct("entity_id").
		Where("leased_until IS NULL OR leased_until < ?", time.Now().UTC()).
		Pluck("entity_id", &ids).Error
	if err != nil {
		return nil, fmt.Errorf("entities with queued work: %w", err)
	}
	return ids, nil
}
