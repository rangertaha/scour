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

// SaveChain stores a fitted chain. The extraction chain transfers between
// items and is stored with a null item, the crawl chain is per item.
func (s *Store) SaveChain(ctx context.Context, itemID *uint, kind ChainKind, transitions []byte, observations int) error {
	q := s.db.WithContext(ctx).Where("kind = ?", kind)
	if itemID == nil {
		q = q.Where("item_id IS NULL")
	} else {
		q = q.Where("item_id = ?", *itemID)
	}

	var existing Chain
	err := q.First(&existing).Error
	switch {
	case err == nil:
		existing.Transitions = string(transitions)
		existing.Observations = observations
		existing.FittedAt = time.Now().UTC()
		if err := s.db.WithContext(ctx).Save(&existing).Error; err != nil {
			return fmt.Errorf("update chain: %w", err)
		}
		return nil
	case errors.Is(err, gorm.ErrRecordNotFound):
		row := Chain{
			ItemID:       itemID,
			Kind:         kind,
			Transitions:  string(transitions),
			Observations: observations,
			FittedAt:     time.Now().UTC(),
		}
		if err := s.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&row).Error; err != nil {
			return fmt.Errorf("store chain: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("read chain: %w", err)
	}
}

// LoadChain returns a stored chain, or nil when none has been fitted.
func (s *Store) LoadChain(ctx context.Context, itemID *uint, kind ChainKind) ([]byte, error) {
	q := s.db.WithContext(ctx).Where("kind = ?", kind)
	if itemID == nil {
		q = q.Where("item_id IS NULL")
	} else {
		q = q.Where("item_id = ?", *itemID)
	}

	var row Chain
	err := q.First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read chain: %w", err)
	}
	return []byte(row.Transitions), nil
}

// Path is one URL and the outcome of fetching it, in crawl order from the seed
// down. It is what the crawl chain is decoded and fitted over.
type Path struct {
	URLs     []string
	Matches  []int
	Links    []int
	Statuses []int
}

// Paths reconstructs every root-to-leaf crawl path for an item, following
// the parent edges recorded during the crawl.
//
// Fitting runs over these rather than over the whole visited set, because a
// chain fitted to every page at once is dominated by boilerplate, which is the
// class it exists to discount.
func (s *Store) Paths(ctx context.Context, itemID uint) ([]Path, error) {
	var rows []URL
	err := s.db.WithContext(ctx).
		Where("item_id = ? AND fetched_at IS NOT NULL", itemID).
		Order("id ASC").
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list urls: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}

	byID := make(map[uint]URL, len(rows))
	hasChild := make(map[uint]bool, len(rows))
	for _, r := range rows {
		byID[r.ID] = r
		if r.ParentID != nil {
			hasChild[*r.ParentID] = true
		}
	}

	// Count outgoing links per page from the frontier: a page that put URLs
	// into the queue is a page that had links, whether or not they were
	// followed.
	var discovered []URL
	err = s.db.WithContext(ctx).
		Select("parent_id").
		Where("item_id = ? AND parent_id IS NOT NULL", itemID).
		Find(&discovered).Error
	if err != nil {
		return nil, fmt.Errorf("count links: %w", err)
	}
	links := make(map[uint]int, len(rows))
	for _, d := range discovered {
		links[*d.ParentID]++
	}

	var out []Path
	for _, leaf := range rows {
		if hasChild[leaf.ID] {
			continue // only whole paths, so each page is counted once
		}

		var chain []URL
		for cur, ok := leaf, true; ok; {
			chain = append(chain, cur)
			if cur.ParentID == nil {
				break
			}
			cur, ok = byID[*cur.ParentID]
			if len(chain) > 64 {
				break // a cycle in the recorded parents; take what we have
			}
		}

		p := Path{}
		for i := len(chain) - 1; i >= 0; i-- {
			p.URLs = append(p.URLs, chain[i].URL)
			p.Matches = append(p.Matches, chain[i].Matches)
			p.Links = append(p.Links, links[chain[i].ID])
			p.Statuses = append(p.Statuses, chain[i].StatusCode)
		}
		out = append(out, p)
	}
	return out, nil
}

// SetRoles records the role decoding gave each page, which is what `scour
// status` counts and what the next crawl scores links against.
func (s *Store) SetRoles(ctx context.Context, itemID uint, roles map[string]string) error {
	if len(roles) == 0 {
		return nil
	}
	now := time.Now().UTC()
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for rawURL, role := range roles {
			row := PageRole{
				ItemID:    itemID,
				Hash:      URLHash(itemID, rawURL),
				URL:       rawURL,
				Role:      role,
				DecodedAt: now,
			}
			err := tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "item_id"}, {Name: "hash"}},
				DoUpdates: clause.AssignmentColumns([]string{"role", "url", "decoded_at"}),
			}).Create(&row).Error
			if err != nil {
				return fmt.Errorf("set role for %s: %w", rawURL, err)
			}
		}
		return nil
	})
}

// Roles returns the decoded role of every page that has one.
func (s *Store) Roles(ctx context.Context, itemID uint) (map[string]string, error) {
	var rows []PageRole
	err := s.db.WithContext(ctx).
		Where("item_id = ?", itemID).
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("read roles: %w", err)
	}
	out := make(map[string]string, len(rows))
	for _, r := range rows {
		out[r.URL] = r.Role
	}
	return out, nil
}
