// SPDX-License-Identifier: GPL-3.0-or-later

package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// URLHash is the frontier's identity for a URL. Re-discovering a URL is an
// upsert on this, which is what makes the crawl idempotent under the
// at-least-once delivery the bus will bring in M5.
func URLHash(entityID uint, rawURL string) string {
	sum := sha256.Sum256(fmt.Appendf(nil, "%d\x00%s", entityID, rawURL))
	return hex.EncodeToString(sum[:])
}

// Discovered records a URL the crawler has seen but not yet fetched. An
// already-known URL keeps its existing row, so a link found twice does not
// reset its state.
func (s *Store) Discovered(ctx context.Context, entityID uint, rawURL, parentURL string, depth int, score float64) error {
	var parentID *uint
	if parentURL != "" && parentURL != rawURL {
		if id, err := s.urlID(ctx, entityID, parentURL); err == nil {
			parentID = &id
		}
	}

	u := URL{
		EntityID: entityID,
		Hash:     URLHash(entityID, rawURL),
		URL:      rawURL,
		ParentID: parentID,
		Depth:    depth,
		Score:    score,
		Status:   URLQueued,
	}
	err := s.db.WithContext(ctx).
		Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "hash"}}, DoNothing: true}).
		Create(&u).Error
	if err != nil {
		return fmt.Errorf("record discovered url %s: %w", rawURL, err)
	}
	return nil
}

// Fetched is the outcome of one fetch, as the crawl callbacks see it.
type Fetched struct {
	EntityID    uint
	URL         string
	ParentURL   string
	Depth       int
	Score       float64
	Status      URLStatus
	StatusCode  int
	ContentType string
	Size        int64
	Latency     time.Duration
	CacheKey    string
}

// RecordFetch writes the result of a fetch, creating the frontier row if the
// URL was never queued. Fetching a URL twice updates it in place.
func (s *Store) RecordFetch(ctx context.Context, f Fetched) error {
	now := time.Now().UTC()

	var parentID *uint
	if f.ParentURL != "" {
		if id, err := s.urlID(ctx, f.EntityID, f.ParentURL); err == nil {
			parentID = &id
		}
	}

	u := URL{
		EntityID:    f.EntityID,
		Hash:        URLHash(f.EntityID, f.URL),
		URL:         f.URL,
		ParentID:    parentID,
		Depth:       f.Depth,
		Score:       f.Score,
		Status:      f.Status,
		StatusCode:  f.StatusCode,
		ContentType: f.ContentType,
		Size:        f.Size,
		Latency:     f.Latency,
	}
	// A skip never reached the wire far enough to have a body, so it is not a
	// fetch. Stamping it would make skipped URLs indistinguishable from
	// crawled ones in every later query.
	if f.Status == URLFetched || f.Status == URLFailed {
		u.FetchedAt = &now
	}

	// The row may already exist from discovery, which is why this is an
	// upsert. parent_id is only in the update list when we actually know the
	// parent, so a later fetch cannot null out a lineage already recorded.
	columns := []string{
		"status", "status_code", "content_type", "size",
		"latency", "fetched_at", "score", "depth", "updated_at",
	}
	if parentID != nil {
		columns = append(columns, "parent_id")
	}

	err := s.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "hash"}},
			DoUpdates: clause.AssignmentColumns(columns),
		}).
		Create(&u).Error
	if err != nil {
		return fmt.Errorf("record fetch %s: %w", f.URL, err)
	}

	if f.CacheKey == "" {
		return nil
	}

	id, err := s.urlID(ctx, f.EntityID, f.URL)
	if err != nil {
		return err
	}
	resp := Response{
		URLID:     id,
		Status:    f.StatusCode,
		CacheKey:  f.CacheKey,
		FetchedAt: now,
	}
	if err := s.db.WithContext(ctx).Create(&resp).Error; err != nil {
		return fmt.Errorf("record response for %s: %w", f.URL, err)
	}
	return nil
}

func (s *Store) urlID(ctx context.Context, entityID uint, rawURL string) (uint, error) {
	var u URL
	err := s.db.WithContext(ctx).
		Select("id").
		Where("hash = ?", URLHash(entityID, rawURL)).
		First(&u).Error
	if err != nil {
		return 0, fmt.Errorf("look up url %s: %w", rawURL, err)
	}
	return u.ID, nil
}

// URLs returns an entity's frontier rows, highest score first. A limit of zero
// returns everything.
func (s *Store) URLs(ctx context.Context, entityID uint, limit int) ([]URL, error) {
	q := s.db.WithContext(ctx).
		Where("entity_id = ?", entityID).
		Order("score DESC, url ASC")
	if limit > 0 {
		q = q.Limit(limit)
	}
	var out []URL
	if err := q.Find(&out).Error; err != nil {
		return nil, fmt.Errorf("list urls: %w", err)
	}
	return out, nil
}

// FetchedURLs returns only the rows a fetch was attempted for, which is what
// the crawl summary is built from.
func (s *Store) FetchedURLs(ctx context.Context, entityID uint) ([]URL, error) {
	var out []URL
	err := s.db.WithContext(ctx).
		Where("entity_id = ? AND fetched_at IS NOT NULL", entityID).
		Order("score DESC, url ASC").
		Find(&out).Error
	if err != nil {
		return nil, fmt.Errorf("list fetched urls: %w", err)
	}
	return out, nil
}

// Status summarises an entity, and is what `scour status` prints.
type Status struct {
	Targets    int64
	Properties int64
	Aliases    int64
	Queued     int64
	Visited    int64
	Failed     int64
	Skipped    int64
	Matches    int64
	Valid      int64
	Invalid    int64
	Unlabelled int64
	Rules      int64
	Formats    map[string]int64
	Roles      map[string]int64
	Model      *ModelMeta
}

// Status gathers the counters for one entity.
func (s *Store) Status(ctx context.Context, entityID uint) (*Status, error) {
	db := s.db.WithContext(ctx)
	st := &Status{Formats: map[string]int64{}, Roles: map[string]int64{}}

	counts := []struct {
		dst   *int64
		model any
		where []any
	}{
		{&st.Targets, &Target{}, []any{"entity_id = ?", entityID}},
		{&st.Properties, &Property{}, []any{"entity_id = ?", entityID}},
		{&st.Aliases, &Alias{}, []any{"entity_id = ?", entityID}},
		{&st.Queued, &URL{}, []any{"entity_id = ? AND status = ?", entityID, URLQueued}},
		{&st.Visited, &URL{}, []any{"entity_id = ? AND status = ?", entityID, URLFetched}},
		{&st.Failed, &URL{}, []any{"entity_id = ? AND status = ?", entityID, URLFailed}},
		{&st.Skipped, &URL{}, []any{"entity_id = ? AND status = ?", entityID, URLSkipped}},
		{&st.Matches, &Record{}, []any{"entity_id = ?", entityID}},
		{&st.Valid, &Record{}, []any{"entity_id = ? AND label = ?", entityID, Valid}},
		{&st.Invalid, &Record{}, []any{"entity_id = ? AND label = ?", entityID, Invalid}},
		{&st.Unlabelled, &Record{}, []any{"entity_id = ? AND label = ?", entityID, Unlabelled}},
		{&st.Rules, &Rule{}, []any{"entity_id = ?", entityID}},
	}
	for _, c := range counts {
		if err := db.Model(c.model).Where(c.where[0], c.where[1:]...).Count(c.dst).Error; err != nil {
			return nil, fmt.Errorf("count %T: %w", c.model, err)
		}
	}

	var grouped []struct {
		Key string
		N   int64
	}
	err := db.Model(&URL{}).
		Select("content_type AS key, COUNT(*) AS n").
		Where("entity_id = ? AND content_type != ''", entityID).
		Group("content_type").Scan(&grouped).Error
	if err != nil {
		return nil, fmt.Errorf("count formats: %w", err)
	}
	for _, g := range grouped {
		st.Formats[g.Key] = g.N
	}

	grouped = grouped[:0]
	err = db.Model(&PageRole{}).
		Select("role AS key, COUNT(*) AS n").
		Where("entity_id = ? AND role != ''", entityID).
		Group("role").Scan(&grouped).Error
	if err != nil {
		return nil, fmt.Errorf("count roles: %w", err)
	}
	for _, g := range grouped {
		st.Roles[g.Key] = g.N
	}

	var meta ModelMeta
	if err := db.Where("entity_id = ?", entityID).First(&meta).Error; err == nil {
		st.Model = &meta
	}
	return st, nil
}

// ResetFrontier clears an entity's crawl state, leaving its definition intact.
// The cached bodies survive, so a re-crawl is cheap.
//
// This includes the visited set. Clearing the frontier without it would leave
// colly believing it had already seen every URL, so the re-crawl would fetch
// nothing at all, which is the opposite of starting over.
func (s *Store) ResetFrontier(ctx context.Context, entityID uint) error {
	db := s.db.WithContext(ctx)
	var ids []uint
	if err := db.Model(&URL{}).Where("entity_id = ?", entityID).Pluck("id", &ids).Error; err != nil {
		return fmt.Errorf("collect urls: %w", err)
	}
	if len(ids) > 0 {
		if err := db.Where("url_id IN ?", ids).Delete(&Response{}).Error; err != nil {
			return fmt.Errorf("delete responses: %w", err)
		}
	}
	if err := db.Where("entity_id = ?", entityID).Delete(&URL{}).Error; err != nil {
		return fmt.Errorf("delete urls: %w", err)
	}
	return s.ClearCrawlState(ctx, entityID)
}

// SetHostTransport records that a host needs a particular transport, which is
// how an escalation to the browser survives the crawl that discovered it.
//
// Politeness and capability are owed to the server rather than to any one
// entity, so this is shared across entities like the rest of the host policy.
func (s *Store) SetHostTransport(ctx context.Context, host, transport string) error {
	row := Host{Host: host, Transport: transport}
	err := s.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "host"}},
			DoUpdates: clause.AssignmentColumns([]string{"transport"}),
		}).
		Create(&row).Error
	if err != nil {
		return fmt.Errorf("record transport for %s: %w", host, err)
	}
	return nil
}

// HostsByTransport lists the hosts recorded as needing a given transport.
//
// This is what makes an escalation worth recording: a later crawl can start
// already knowing which hosts need a browser, instead of rediscovering it one
// wasted request at a time.
func (s *Store) HostsByTransport(ctx context.Context, transport string) ([]string, error) {
	var hosts []string
	err := s.db.WithContext(ctx).
		Model(&Host{}).
		Where("transport = ?", transport).
		Pluck("host", &hosts).Error
	if err != nil {
		return nil, fmt.Errorf("list hosts using %s: %w", transport, err)
	}
	return hosts, nil
}

// HostTransport returns the transport recorded for a host, if any.
func (s *Store) HostTransport(ctx context.Context, host string) (string, error) {
	var row Host
	err := s.db.WithContext(ctx).Select("transport").Where("host = ?", host).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read transport for %s: %w", host, err)
	}
	return row.Transport, nil
}
