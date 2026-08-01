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
func URLHash(itemID uint, rawURL string) string {
	sum := sha256.Sum256(fmt.Appendf(nil, "%d\x00%s", itemID, rawURL))
	return hex.EncodeToString(sum[:])
}

// Discovered records a URL the crawler has seen but not yet fetched. An
// already-known URL keeps its existing row, so a link found twice does not
// reset its state.
func (s *Store) Discovered(ctx context.Context, itemID uint, rawURL, parentURL string, depth int, score float64) error {
	var parentID *uint
	if parentURL != "" && parentURL != rawURL {
		if id, err := s.urlID(ctx, itemID, parentURL); err == nil {
			parentID = &id
		}
	}

	u := URL{
		ItemID:   itemID,
		Hash:     URLHash(itemID, rawURL),
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
	// ItemID owns the url row: a fetched page joins the item's corpus, and is
	// there for the next job over the same site rather than fetched again.
	ItemID uint
	// RunID is the occasion this fetch happened on, which is what gives a run
	// a log. Zero for a fetch outside any run.
	RunID uint
	// JobID owns the frontier entry this fetch settles. A page can be reached
	// by two jobs of one item, and each has its own entry to release.
	JobID       uint
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
		if id, err := s.urlID(ctx, f.ItemID, f.ParentURL); err == nil {
			parentID = &id
		}
	}

	u := URL{
		ItemID:      f.ItemID,
		JobID:       f.JobID,
		RunID:       f.RunID,
		Hash:        URLHash(f.ItemID, f.URL),
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
	// Only when this fetch had a job. A crawler handed its work does not know
	// which job sent it, and writing its zero would erase an attribution an
	// earlier fetch had already made.
	if f.JobID != 0 {
		columns = append(columns, "job_id")
	}
	if f.RunID != 0 {
		columns = append(columns, "run_id")
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

	// The frontier item is done once its outcome is on the record, whether the
	// fetch succeeded or failed. Both paths arrive here, so this is the single
	// place a lease is released; anything that never arrives is returned by its
	// lease expiring instead.
	if err := s.ReleaseQueue(ctx, f.JobID, u.Hash); err != nil {
		return err
	}

	if f.CacheKey == "" {
		return nil
	}

	id, err := s.urlID(ctx, f.ItemID, f.URL)
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

func (s *Store) urlID(ctx context.Context, itemID uint, rawURL string) (uint, error) {
	var u URL
	err := s.db.WithContext(ctx).
		Select("id").
		Where("hash = ?", URLHash(itemID, rawURL)).
		First(&u).Error
	if err != nil {
		return 0, fmt.Errorf("look up url %s: %w", rawURL, err)
	}
	return u.ID, nil
}

// URLs returns an item's frontier rows, highest score first. A limit of zero
// returns everything.
func (s *Store) URLs(ctx context.Context, itemID uint, limit int) ([]URL, error) {
	q := s.db.WithContext(ctx).
		Where("item_id = ?", itemID).
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
func (s *Store) FetchedURLs(ctx context.Context, itemID uint) ([]URL, error) {
	var out []URL
	err := s.db.WithContext(ctx).
		Where("item_id = ? AND fetched_at IS NOT NULL", itemID).
		Order("score DESC, url ASC").
		Find(&out).Error
	if err != nil {
		return nil, fmt.Errorf("list fetched urls: %w", err)
	}
	return out, nil
}

// Status summarises an item, and is what `scour item ls` prints.
type Status struct {
	Targets    int64
	Properties int64
	Aliases    int64
	// Queued is how many URLs are waiting across the item's jobs. Not a url
	// status: see the note on the query.
	Queued     int64
	Visited    int64
	Failed     int64
	Skipped    int64
	Paused     bool
	Matches    int64
	Valid      int64
	Invalid    int64
	Unlabelled int64
	Rules      int64
	Formats    map[string]int64
	Roles      map[string]int64
	Model      *ModelMeta
}

// Status gathers the counters for one item.
func (s *Store) Status(ctx context.Context, itemID uint) (*Status, error) {
	db := s.db.WithContext(ctx)
	st := &Status{Formats: map[string]int64{}, Roles: map[string]int64{}}

	// Paused is durable and invisible everywhere else, so an item that is
	// simply not being worked on looks identical to one that has finished.
	paused, err := s.IsPaused(ctx, itemID)
	if err != nil {
		return nil, err
	}
	st.Paused = paused

	counts := []struct {
		dst   *int64
		model any
		where []any
	}{
		// Targets belong to the item's jobs, so counting them is a join rather
		// than a column: an item's target count is what all its jobs point at.
		{&st.Targets, &Target{}, []any{
			"job_id IN (SELECT id FROM jobs WHERE item_id = ?)", itemID}},
		{&st.Properties, &Property{}, []any{"item_id = ?", itemID}},
		{&st.Aliases, &Alias{}, []any{"item_id = ?", itemID}},
		// Queued is the work waiting, which lives in the frontier and not in
		// the url states. A url stays marked queued when its frontier entry
		// goes without a fetch being recorded, so counting urls reported work
		// that no crawl would ever be handed: 10,942 against a real frontier
		// of 6,580. Everything asking this means "is there anything to do",
		// so it is answered from the queue the dispatcher actually reads.
		{&st.Queued, &QueueItem{}, []any{
			"job_id IN (SELECT id FROM jobs WHERE item_id = ?)", itemID}},
		{&st.Visited, &URL{}, []any{"item_id = ? AND status = ?", itemID, URLFetched}},
		{&st.Failed, &URL{}, []any{"item_id = ? AND status = ?", itemID, URLFailed}},
		{&st.Skipped, &URL{}, []any{"item_id = ? AND status = ?", itemID, URLSkipped}},
		{&st.Matches, &Record{}, []any{"item_id = ?", itemID}},
		{&st.Valid, &Record{}, []any{"item_id = ? AND label = ?", itemID, Valid}},
		{&st.Invalid, &Record{}, []any{"item_id = ? AND label = ?", itemID, Invalid}},
		{&st.Unlabelled, &Record{}, []any{"item_id = ? AND label = ?", itemID, Unlabelled}},
		{&st.Rules, &Rule{}, []any{"item_id = ?", itemID}},
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
	err = db.Model(&URL{}).
		Select("content_type AS key, COUNT(*) AS n").
		Where("item_id = ? AND content_type != ''", itemID).
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
		Where("item_id = ? AND role != ''", itemID).
		Group("role").Scan(&grouped).Error
	if err != nil {
		return nil, fmt.Errorf("count roles: %w", err)
	}
	for _, g := range grouped {
		st.Roles[g.Key] = g.N
	}

	var meta ModelMeta
	if err := db.Where("item_id = ?", itemID).First(&meta).Error; err == nil {
		st.Model = &meta
	}
	return st, nil
}

// StopJob discards one job's frontier, leaving everything else alone.
//
// This is what `scour job stop` does, and the design says exactly what it may
// touch: the definition, the cached pages, the records and the model are all
// kept, and only the frontier goes. So it takes a job rather than an item. A
// reset scoped to the item would take the other jobs of that item with it, and
// their frontiers are hours of deciding what to fetch that nothing recomputes.
//
// The visited set stays. It is the item's record of what its corpus already
// holds, not this crawl's scratch space, and two jobs over one site are meant
// not to refetch each other's pages. A later start therefore re-seeds and goes
// on to find what is new rather than fetching the site again; that second
// thing is a recrawl, and has its own function.
func (s *Store) StopJob(ctx context.Context, jobID uint) error {
	err := s.db.WithContext(ctx).Where("job_id = ?", jobID).Delete(&QueueItem{}).Error
	if err != nil {
		return fmt.Errorf("discard the frontier of job %d: %w", jobID, err)
	}
	return nil
}

// RecrawlJob makes one job fetch its sites again from the seeds.
//
// The frontier of that job goes, and so does the item's visited set, because
// without the second half the first does nothing: colly would decline every
// re-seeded URL as one it had already seen, and a crawl asked to start over
// would fetch nothing at all.
//
// Everything else it clears is the item's, and so reaches every job of that
// item: the visited set, the urls and their responses all go, and a sibling
// will fetch pages it has already fetched. That is deliberate. Starting over
// means the item's record of what it has seen starts empty, or the counts
// afterwards describe two crawls added together and nothing can be measured.
// The cost is a refetch, which is a cost and not a loss.
//
// What a sibling does not lose is its frontier. That is the whole of the fix
// here: the queue is the one thing nothing recomputes, hours of deciding what
// to fetch on a large site, and it used to be cleared for every job of the
// item whenever one of them was reset.
//
// The cached bodies survive either way, so a re-crawl pays for the fetching
// rather than for the parsing.
func (s *Store) RecrawlJob(ctx context.Context, itemID, jobID uint) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("job_id = ?", jobID).Delete(&QueueItem{}).Error; err != nil {
			return fmt.Errorf("discard the frontier of job %d: %w", jobID, err)
		}

		var ids []uint
		if err := tx.Model(&URL{}).Where("item_id = ?", itemID).Pluck("id", &ids).Error; err != nil {
			return fmt.Errorf("collect urls: %w", err)
		}
		if len(ids) > 0 {
			if err := tx.Where("url_id IN ?", ids).Delete(&Response{}).Error; err != nil {
				return fmt.Errorf("delete responses: %w", err)
			}
		}
		if err := tx.Where("item_id = ?", itemID).Delete(&URL{}).Error; err != nil {
			return fmt.Errorf("delete urls: %w", err)
		}
		for _, model := range []any{&Visit{}, &Cookie{}} {
			if err := tx.Where("item_id = ?", itemID).Delete(model).Error; err != nil {
				return fmt.Errorf("clear %T: %w", model, err)
			}
		}
		return nil
	})
}

// SetHostTransport records that a host needs a particular transport, which is
// how an escalation to the browser survives the crawl that discovered it.
//
// Politeness and capability are owed to the server rather than to any one
// item, so this is shared across items like the rest of the host policy.
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

// HostRates returns the per-host rate overrides that have been recorded.
//
// Returned in one query rather than looked up per URL: the dispatcher consults
// them on every pass, and politeness settings are few and change rarely.
func (s *Store) HostRates(ctx context.Context) (map[string]time.Duration, error) {
	var rows []Host
	err := s.db.WithContext(ctx).
		Where("rate > 0").
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list host rates: %w", err)
	}
	out := make(map[string]time.Duration, len(rows))
	for _, r := range rows {
		out[r.Host] = r.Rate
	}
	return out, nil
}
