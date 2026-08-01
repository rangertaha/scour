// SPDX-License-Identifier: GPL-3.0-or-later

package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/rangertaha/scour/internal/schedule"
)

// ErrQueueEmpty is returned by [Store.PopQueue] when there is nothing left to
// crawl. colly's queue treats any error from its storage as "empty", but a
// distinct value lets scour tell an exhausted queue from a broken database.
var ErrQueueEmpty = errors.New("queue is empty")

// MarkVisited records that colly has visited a request.
//
// See [Visit] for why colly's uint64 hash is stored as an int64.
func (s *Store) MarkVisited(ctx context.Context, itemID uint, requestID uint64) error {
	v := Visit{ItemID: itemID, RequestID: int64(requestID), VisitedAt: time.Now().UTC()}
	err := s.db.WithContext(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).
		Create(&v).Error
	if err != nil {
		return fmt.Errorf("mark visited: %w", err)
	}
	return nil
}

// IsVisited reports whether colly has already visited a request.
func (s *Store) IsVisited(ctx context.Context, itemID uint, requestID uint64) (bool, error) {
	var n int64
	err := s.db.WithContext(ctx).
		Model(&Visit{}).
		Where("item_id = ? AND request_id = ?", itemID, int64(requestID)).
		Count(&n).Error
	if err != nil {
		return false, fmt.Errorf("check visited: %w", err)
	}
	return n > 0, nil
}

// VisitCount returns how many requests have been visited.
func (s *Store) VisitCount(ctx context.Context, itemID uint) (int64, error) {
	var n int64
	err := s.db.WithContext(ctx).Model(&Visit{}).Where("item_id = ?", itemID).Count(&n).Error
	if err != nil {
		return 0, fmt.Errorf("count visits: %w", err)
	}
	return n, nil
}

// SetCookies stores the cookie header for a host.
func (s *Store) SetCookies(ctx context.Context, itemID uint, host, cookies string) error {
	c := Cookie{ItemID: itemID, Host: host, Value: cookies, UpdatedAt: time.Now().UTC()}
	err := s.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "item_id"}, {Name: "host"}},
			DoUpdates: clause.AssignmentColumns([]string{"value", "updated_at"}),
		}).
		Create(&c).Error
	if err != nil {
		return fmt.Errorf("store cookies for %s: %w", host, err)
	}
	return nil
}

// Cookies returns the stored cookie header for a host, or an empty string.
func (s *Store) Cookies(ctx context.Context, itemID uint, host string) (string, error) {
	var c Cookie
	err := s.db.WithContext(ctx).
		Where("item_id = ? AND host = ?", itemID, host).
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
//
// A URL already waiting is not queued again. The same link is found on many
// pages of a site, and in a distributed crawl it can arrive from two directions
// at once: the crawler that found it queues it, and the store queues it again
// when the discovery is reported. Either way, fetching it twice is not what
// anyone wanted.
func (s *Store) PushQueue(ctx context.Context, jobID uint, score float64, hash string, data []byte) error {
	if hash != "" {
		var n int64
		err := s.db.WithContext(ctx).
			Model(&QueueItem{}).
			Where("job_id = ? AND hash = ?", jobID, hash).
			Count(&n).Error
		if err != nil {
			return fmt.Errorf("check queue for %s: %w", hash, err)
		}
		if n > 0 {
			return nil
		}
	}

	item := QueueItem{
		JobID: jobID, Score: score, Hash: hash,
		Host: hostOfRequest(data), Data: data,
	}
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

// hostOfRequest reads the host a serialised request will be sent to.
func hostOfRequest(data []byte) string {
	var req struct {
		URL string `json:"URL"`
	}
	if err := json.Unmarshal(data, &req); err != nil {
		return ""
	}
	u, err := url.Parse(req.URL)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// LeaseQueue hands out the next request, highest score first and oldest first
// within a score, and marks it in flight rather than removing it.
//
// The select and the update run in one transaction, so two crawlers sharing a
// database cannot be handed the same URL. The item is removed when the fetch is
// recorded; if nothing reports back before the lease expires it is handed out
// again, which is what makes a crawler dying mid-page cost a retry rather than
// a silently missing page.
func (s *Store) LeaseQueue(ctx context.Context, jobID uint, lease time.Duration) ([]byte, error) {
	return s.LeaseQueueSkipping(ctx, jobID, lease, nil)
}

// LeaseQueueSkipping is LeaseQueue, ignoring items for hosts that have been
// asked for something too recently.
//
// This is where politeness survives being distributed. A rate limit inside a
// crawler bounds what that process does; with several crawlers the site sees
// the sum, and no crawler can see the others. The dispatcher can: it is the one
// component handing out every URL, so pacing here bounds what a site actually
// receives however many crawlers there are.
func (s *Store) LeaseQueueSkipping(ctx context.Context, jobID uint, lease time.Duration, cooling []string) ([]byte, error) {
	return s.LeaseQueueOrdered(ctx, jobID, lease, cooling, schedule.ByScore)
}

// orderBy turns a scheduling order into the clause that implements it.
//
// A closed set rather than a string from the caller: the frontier is a table
// with a hundred thousand rows in it, so the choice has to be made by the
// database, and taking SQL from a policy would be taking an injection point and
// a dependency on the schema at once.
func orderBy(o schedule.Order) string {
	switch o {
	case schedule.Breadth:
		return "id ASC"
	case schedule.Depth:
		return "id DESC"
	case schedule.Random:
		return "RANDOM()"
	default:
		// Highest score first, ties by insertion order, which is what a
		// focused crawl means.
		return "score DESC, id ASC"
	}
}

// LeaseQueueOrdered is LeaseQueueSkipping with the order chosen by a scheduling
// policy rather than fixed.
func (s *Store) LeaseQueueOrdered(ctx context.Context, jobID uint, lease time.Duration, cooling []string, order schedule.Order) ([]byte, error) {
	if lease <= 0 {
		lease = DefaultLease
	}
	now := time.Now().UTC()
	until := now.Add(lease)

	var data []byte
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var item QueueItem
		q := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("job_id = ? AND (leased_until IS NULL OR leased_until < ?)", jobID, now)
		if len(cooling) > 0 {
			// An item with no host recorded predates the column and is never
			// skipped: it would otherwise be stuck for the life of the queue.
			q = q.Where("host = '' OR host NOT IN ?", cooling)
		}
		err := q.
			Order(orderBy(order)).
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
func (s *Store) ReleaseQueue(ctx context.Context, jobID uint, hash string) error {
	if hash == "" {
		return nil
	}
	err := s.db.WithContext(ctx).
		Where("job_id = ? AND hash = ?", jobID, hash).
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
func (s *Store) QueueSize(ctx context.Context, jobID uint) (int, error) {
	var n int64
	err := s.db.WithContext(ctx).
		Model(&QueueItem{}).
		Where("job_id = ? AND (leased_until IS NULL OR leased_until < ?)", jobID, time.Now().UTC()).
		Count(&n).Error
	if err != nil {
		return 0, fmt.Errorf("queue size: %w", err)
	}
	return int(n), nil
}

// ClearCrawlState drops the visited set, the queue and the cookies for an item,
// which is what makes a re-crawl start over rather than resume.
//
// It takes an item because the visited set and the cookie jar are the item's:
// two jobs over one site should not refetch each other's pages, and a session
// is owed to the site. The queue is each job's, so it is cleared for all of
// them.
func (s *Store) ClearCrawlState(ctx context.Context, itemID uint) error {
	db := s.db.WithContext(ctx)
	for _, model := range []any{&Visit{}, &Cookie{}} {
		if err := db.Where("item_id = ?", itemID).Delete(model).Error; err != nil {
			return fmt.Errorf("clear %T: %w", model, err)
		}
	}
	err := db.Where("job_id IN (SELECT id FROM jobs WHERE item_id = ?)", itemID).
		Delete(&QueueItem{}).Error
	if err != nil {
		return fmt.Errorf("clear queue: %w", err)
	}
	return nil
}

// QueuedJobs lists the jobs with work waiting, so a dispatcher can find what to
// hand out without being told which crawls are running.
//
// A paused one is not among them, which is the whole of what pausing does to a
// dispatcher: the job keeps its frontier, its order and its leases, and simply
// stops being asked about. Both pauses count, the job's own state and the
// item's flag, because either is a statement that this work should wait.
func (s *Store) QueuedJobs(ctx context.Context) ([]uint, error) {
	var ids []uint
	err := s.db.WithContext(ctx).
		Model(&QueueItem{}).
		Distinct("queue_items.job_id").
		Joins("JOIN jobs ON jobs.id = queue_items.job_id AND jobs.state != ?", JobPaused).
		Joins("JOIN items ON items.id = jobs.item_id AND NOT items.paused").
		Where("leased_until IS NULL OR leased_until < ?", time.Now().UTC()).
		Pluck("queue_items.job_id", &ids).Error
	if err != nil {
		return nil, fmt.Errorf("jobs with queued work: %w", err)
	}
	return ids, nil
}

// ReturnLeases puts every in-flight item of an item back in the queue.
//
// A lease expiring is the net under a crawler that died. A crawler that stops
// on purpose should not make the frontier wait for it: everything it had taken
// and not fetched is available again immediately, which is what makes a crawl
// stopped on its budget resumable rather than resumable in ten minutes.
func (s *Store) ReturnLeases(ctx context.Context, jobID uint) error {
	err := s.db.WithContext(ctx).
		Model(&QueueItem{}).
		Where("job_id = ? AND leased_until IS NOT NULL", jobID).
		Update("leased_until", nil).Error
	if err != nil {
		return fmt.Errorf("return leases: %w", err)
	}
	return nil
}

// InFlight counts an item's handed-out items that have not reported back.
//
// This is what bounds dispatch. Counting unacknowledged broker messages instead
// measures the wrong thing: a crawler acknowledges work when it queues it, not
// when it fetches it, so the broker looks idle while the crawler holds
// thousands of URLs it has not got to. Left unbounded the frontier drains into
// a crawler's memory, which is the failure lazy seeding was introduced to cure,
// one layer further along.
func (s *Store) InFlight(ctx context.Context, jobID uint) (int, error) {
	var n int64
	err := s.db.WithContext(ctx).
		Model(&QueueItem{}).
		Where("job_id = ? AND leased_until IS NOT NULL AND leased_until >= ?",
			jobID, time.Now().UTC()).
		Count(&n).Error
	if err != nil {
		return 0, fmt.Errorf("in-flight count: %w", err)
	}
	return int(n), nil
}
