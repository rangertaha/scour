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

// PushQueue appends a serialised request.
func (s *Store) PushQueue(ctx context.Context, entityID uint, score float64, data []byte) error {
	item := QueueItem{EntityID: entityID, Score: score, Data: data}
	if err := s.db.WithContext(ctx).Create(&item).Error; err != nil {
		return fmt.Errorf("push queue: %w", err)
	}
	return nil
}

// PopQueue removes and returns the next request, highest score first and
// oldest first within a score.
//
// The select and the delete run in one transaction, so two crawlers sharing a
// database cannot hand the same URL to both.
func (s *Store) PopQueue(ctx context.Context, entityID uint) ([]byte, error) {
	var data []byte
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var item QueueItem
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("entity_id = ?", entityID).
			Order("score DESC, id ASC").
			First(&item).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrQueueEmpty
		}
		if err != nil {
			return fmt.Errorf("read queue: %w", err)
		}
		if err := tx.Delete(&QueueItem{}, item.ID).Error; err != nil {
			return fmt.Errorf("pop queue: %w", err)
		}
		data = item.Data
		return nil
	})
	if err != nil {
		return nil, err
	}
	return data, nil
}

// QueueSize returns how many requests are waiting.
func (s *Store) QueueSize(ctx context.Context, entityID uint) (int, error) {
	var n int64
	err := s.db.WithContext(ctx).
		Model(&QueueItem{}).
		Where("entity_id = ?", entityID).
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
