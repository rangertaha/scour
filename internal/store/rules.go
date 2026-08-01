// SPDX-License-Identifier: GPL-3.0-or-later

package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/rangertaha/scour/internal/query"
)

// ReplaceRules swaps an item's induced rules for a new set.
//
// Training produces the whole set at once, so this is a replace rather than a
// merge: a rule that induction no longer believes in should disappear, not
// linger as a stale locator that still matches something.
func (s *Store) ReplaceRules(ctx context.Context, itemID uint, rules []Rule) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("item_id = ?", itemID).Delete(&Rule{}).Error; err != nil {
			return fmt.Errorf("clear rules: %w", err)
		}
		for i := range rules {
			rules[i].ItemID = itemID
			// A child rule carries its parent's position in the slice, which
			// only becomes a database id once the parent is written.
			if p := rules[i].ParentID; p != nil {
				parent := rules[*p].ID
				rules[i].ParentID = &parent
			}
			if err := tx.Create(&rules[i]).Error; err != nil {
				return fmt.Errorf("store rule %q: %w", rules[i].Prop, err)
			}
		}
		return nil
	})
}

// Rules lists an item's induced rules, parents before their children.
func (s *Store) Rules(ctx context.Context, itemID uint) ([]Rule, error) {
	var out []Rule
	err := s.db.WithContext(ctx).
		Where("item_id = ?", itemID).
		Order("id ASC").
		Find(&out).Error
	if err != nil {
		return nil, fmt.Errorf("list rules: %w", err)
	}
	return out, nil
}

// Fingerprint identifies a record by what it holds rather than where it was
// found, so re-extracting the same values is an update and not a duplicate.
func Fingerprint(itemID uint, values map[string]string) string {
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	h := sha256.New()
	fmt.Fprintf(h, "%d", itemID)
	for _, k := range keys {
		fmt.Fprintf(h, "\x00%s\x00%s", k, strings.TrimSpace(values[k]))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Extracted is one record as extraction found it.
type Extracted struct {
	URL        string
	Confidence float64
	Format     string
	Values     map[string]string
}

// SaveRecords reconciles an item's records with a freshly extracted set.
//
// Records are matched by fingerprint and updated in place, so a record that
// survives a retraining keeps both its id and its label. That matters twice
// over: labels are the expensive part, since a person produced them, and the
// ids are what a label is given, over the API or MCP. Deleting and recreating
// would renumber every row, so the ids someone just read off a listing would
// label the wrong records, or nothing at all.
//
// Records whose fingerprint no longer appears are removed, since the model
// stopped finding them.
func (s *Store) SaveRecords(ctx context.Context, itemID uint, records []Extracted) (int, error) {
	saved := 0
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		existing, err := existingRecords(tx, itemID)
		if err != nil {
			return err
		}

		keep := make(map[uint]bool, len(records))
		done := make(map[string]bool, len(records))

		for _, rec := range records {
			fp := Fingerprint(itemID, rec.Values)

			// Identical values are the same record by definition, however many
			// pages they were found on. Without this, a site that repeats a
			// listing across several pages fails the uniqueness constraint
			// instead of recording one record.
			if done[fp] {
				continue
			}
			done[fp] = true

			row, found := existing[fp]
			if !found {
				row = Record{ItemID: itemID, Fingerprint: fp, Label: Unlabelled}
			}
			row.Confidence = rec.Confidence
			row.Format = rec.Format
			if rec.URL != "" {
				var u URL
				if err := tx.Select("id").Where("hash = ?", URLHash(itemID, rec.URL)).First(&u).Error; err == nil {
					row.URLID = u.ID
				}
			}

			if err := tx.Save(&row).Error; err != nil {
				return fmt.Errorf("store record: %w", err)
			}
			keep[row.ID] = true

			if err := tx.Where("record_id = ?", row.ID).Delete(&Value{}).Error; err != nil {
				return fmt.Errorf("clear values: %w", err)
			}
			for prop, text := range rec.Values {
				if err := tx.Create(&Value{RecordID: row.ID, Prop: prop, Text: text}).Error; err != nil {
					return fmt.Errorf("store value %q: %w", prop, err)
				}
			}
			saved++
		}

		var stale []uint
		for _, row := range existing {
			if !keep[row.ID] {
				stale = append(stale, row.ID)
			}
		}
		if len(stale) > 0 {
			if err := tx.Where("record_id IN ?", stale).Delete(&Value{}).Error; err != nil {
				return fmt.Errorf("clear stale values: %w", err)
			}
			if err := tx.Where("id IN ?", stale).Delete(&Record{}).Error; err != nil {
				return fmt.Errorf("clear stale records: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return saved, nil
}

func existingRecords(tx *gorm.DB, itemID uint) (map[string]Record, error) {
	var rows []Record
	if err := tx.Where("item_id = ?", itemID).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("read records: %w", err)
	}
	out := make(map[string]Record, len(rows))
	for _, r := range rows {
		out[r.Fingerprint] = r
	}
	return out, nil
}

// RecordQuery filters a search over extracted records.
type RecordQuery struct {
	// Terms is the search. Every one must match, and their presence changes
	// the order from newest or best first to best answering the query, which
	// is the whole difference between record ls and record search.
	Terms []query.Term

	// IDs narrows to particular records, which is how showing one and
	// removing a named few reuse the same read as a listing.
	IDs []uint

	// JobID narrows to what one job produced, which is a fact about the page a
	// record was read out of rather than about the record: training runs over
	// the item's whole corpus, so the job that produced a record is the job
	// that paid to fetch its page.
	JobID uint

	MinConfidence float64
	Formats       []string
	ExcludeFormat []string
	Label         Label
	Limit         int

	// SinceID returns only records written after this one, in id order rather
	// than by confidence, which is what a follower wants: it has already seen
	// everything below the mark and cares when the next one lands, not how
	// good it is relative to the rest.
	SinceID uint
}

// RecordRow is one record with its values attached.
type RecordRow struct {
	Record
	Values map[string]string
	URL    string

	// Rank is how well this row answered the query, and Matched names where.
	// Both are zero for a listing, which has no query to answer.
	Rank    float64  `json:",omitempty"`
	Matched []string `json:",omitempty"`
}

// SearchRecords returns extracted records, highest confidence first.
func (s *Store) SearchRecords(ctx context.Context, itemID uint, q RecordQuery) ([]RecordRow, int64, error) {
	base := s.db.WithContext(ctx).Model(&Record{}).Where("item_id = ?", itemID)
	if q.MinConfidence > 0 {
		base = base.Where("confidence >= ?", q.MinConfidence)
	}
	if len(q.Formats) > 0 {
		base = base.Where("format IN ?", q.Formats)
	}
	if len(q.ExcludeFormat) > 0 {
		base = base.Where("format NOT IN ?", q.ExcludeFormat)
	}
	if q.Label != "" {
		base = base.Where("label = ?", q.Label)
	}
	if q.SinceID > 0 {
		base = base.Where("id > ?", q.SinceID)
	}
	if len(q.IDs) > 0 {
		base = base.Where("id IN ?", q.IDs)
	}
	if q.JobID > 0 {
		base = base.Where(
			"EXISTS (SELECT 1 FROM urls u WHERE u.id = records.url_id AND u.job_id = ?)", q.JobID)
	}
	for _, t := range q.Terms {
		cond, args := termWhere(t)
		base = base.Where(cond, args...)
	}

	var total int64
	if err := base.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count records: %w", err)
	}

	sel := base.Session(&gorm.Session{}).Order("confidence DESC, id ASC")
	if q.SinceID > 0 {
		sel = base.Session(&gorm.Session{}).Order("id ASC")
	}
	// A search is ordered by how well each row answers it, which is not
	// something the database can sort on, so the limit is applied after the
	// ranking rather than here. The set it runs over is what the terms already
	// matched, not the whole table.
	if q.Limit > 0 && len(q.Terms) == 0 {
		sel = sel.Limit(q.Limit)
	}
	var records []Record
	if err := sel.Find(&records).Error; err != nil {
		return nil, 0, fmt.Errorf("list records: %w", err)
	}
	if len(records) == 0 {
		return nil, total, nil
	}

	ids := make([]uint, 0, len(records))
	for _, r := range records {
		ids = append(ids, r.ID)
	}
	var values []Value
	if err := s.db.WithContext(ctx).Where("record_id IN ?", ids).Find(&values).Error; err != nil {
		return nil, 0, fmt.Errorf("read values: %w", err)
	}
	byRecord := make(map[uint]map[string]string, len(records))
	for _, v := range values {
		if byRecord[v.RecordID] == nil {
			byRecord[v.RecordID] = map[string]string{}
		}
		byRecord[v.RecordID][v.Prop] = v.Text
	}

	urls, err := s.urlsByID(ctx, records)
	if err != nil {
		return nil, 0, err
	}

	out := make([]RecordRow, 0, len(records))
	for _, r := range records {
		row := RecordRow{Record: r, Values: byRecord[r.ID], URL: urls[r.URLID]}
		if len(q.Terms) > 0 {
			// The terms already matched in SQL, so this repeats the decision
			// only to find out how well and where. A row that fails here is a
			// row the two disagree about, and dropping it keeps the printed
			// reason honest rather than showing a match it cannot explain.
			m, ok := (query.Query{Terms: q.Terms}).Match(row.Values, row.URL)
			if !ok {
				continue
			}
			row.Rank, row.Matched = m.Score, m.Fields
		}
		out = append(out, row)
	}

	// Ranked, unless a follower asked. A follower has already seen everything
	// below its mark and wants what landed after it in the order it landed;
	// re-sorting a poll by rank would print the stream out of sequence.
	if len(q.Terms) > 0 && q.SinceID == 0 {
		sort.SliceStable(out, func(i, j int) bool {
			if out[i].Rank != out[j].Rank {
				return out[i].Rank > out[j].Rank
			}
			if out[i].Confidence != out[j].Confidence {
				return out[i].Confidence > out[j].Confidence
			}
			return out[i].ID < out[j].ID
		})
		if q.Limit > 0 && len(out) > q.Limit {
			out = out[:q.Limit]
		}
	}
	return out, total, nil
}

// urlsByID reads the url of every record in one query rather than one each.
func (s *Store) urlsByID(ctx context.Context, records []Record) (map[uint]string, error) {
	ids := make([]uint, 0, len(records))
	seen := map[uint]bool{}
	for _, r := range records {
		if r.URLID != 0 && !seen[r.URLID] {
			seen[r.URLID] = true
			ids = append(ids, r.URLID)
		}
	}
	out := make(map[uint]string, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	var urls []URL
	if err := s.db.WithContext(ctx).Select("id", "url").Where("id IN ?", ids).Find(&urls).Error; err != nil {
		return nil, fmt.Errorf("read record urls: %w", err)
	}
	for _, u := range urls {
		out[u.ID] = u.URL
	}
	return out, nil
}

// termWhere is one term as a condition on the records table.
//
// Pushed into SQL rather than filtered in Go because the alternative is loading
// every record of the item to throw most of them away, and an item's records
// are the one table that grows without bound as a crawl runs. LIKE is what
// sqlite can answer here; the ranking that follows is the part it cannot.
func termWhere(t query.Term) (string, []any) {
	like := "%" + escapeLike(t.Text) + "%"
	const (
		inValues = `EXISTS (SELECT 1 FROM "values" v WHERE v.record_id = records.id AND v.text LIKE ? ESCAPE '\')`
		inURL    = `EXISTS (SELECT 1 FROM urls u WHERE u.id = records.url_id AND u.url LIKE ? ESCAPE '\')`
	)
	switch {
	case t.Any():
		return "(" + inValues + " OR " + inURL + ")", []any{like, like}
	case t.Field == query.URLField:
		return inURL, []any{like}
	default:
		return `EXISTS (SELECT 1 FROM "values" v
			WHERE v.record_id = records.id AND v.prop = ? AND v.text LIKE ? ESCAPE '\')`,
			[]any{t.Field, like}
	}
}

// escapeLike neutralises the wildcards in a search term, so looking for a
// literal percent sign finds one rather than everything.
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`)
	return r.Replace(s)
}

// LabelRecords marks records valid or invalid. Unknown ids are reported rather
// than ignored, since a mistyped id would otherwise look like success.
// MarkRecords is LabelRecords under the name the command line uses.
//
// A label is a tag here: the words a page might name a property with. A mark is
// the other thing, a person's verdict on a record already extracted. The column
// keeps its name because the API and MCP have always called it label and that
// is a published contract.
func (s *Store) MarkRecords(ctx context.Context, itemID uint, ids []uint, label Label) (int64, error) {
	return s.LabelRecords(ctx, itemID, ids, label)
}

// MarkedCount counts an item's records carrying one verdict.
//
// Worth its own query because "is anything marked valid" decides whether
// training fits the field order chain at all, and answering it by listing every
// record to count them reads the whole table to learn one number.
func (s *Store) MarkedCount(ctx context.Context, itemID uint, label Label) (int64, error) {
	var n int64
	err := s.db.WithContext(ctx).Model(&Record{}).
		Where("item_id = ? AND label = ?", itemID, label).Count(&n).Error
	if err != nil {
		return 0, fmt.Errorf("count %s records: %w", label, err)
	}
	return n, nil
}

func (s *Store) LabelRecords(ctx context.Context, itemID uint, ids []uint, label Label) (int64, error) {
	res := s.db.WithContext(ctx).
		Model(&Record{}).
		Where("item_id = ? AND id IN ?", itemID, ids).
		Updates(map[string]any{"label": label, "updated_at": time.Now().UTC()})
	if res.Error != nil {
		return 0, fmt.Errorf("label records: %w", res.Error)
	}
	return res.RowsAffected, nil
}

// SaveModelMeta records where an item's model lives and how it scored.
func (s *Store) SaveModelMeta(ctx context.Context, meta ModelMeta) error {
	err := s.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "item_id"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"path", "algorithm", "accuracy", "observations", "trained_at",
			}),
		}).
		Create(&meta).Error
	if err != nil {
		return fmt.Errorf("save model metadata: %w", err)
	}
	return nil
}

// SetURLMatches records how many records came out of each URL, which is the
// MATCHES column of the crawl summary.
func (s *Store) SetURLMatches(ctx context.Context, itemID uint, counts map[string]int) error {
	if len(counts) == 0 {
		return nil
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&URL{}).Where("item_id = ?", itemID).Update("matches", 0).Error; err != nil {
			return fmt.Errorf("reset matches: %w", err)
		}
		for rawURL, n := range counts {
			err := tx.Model(&URL{}).
				Where("hash = ?", URLHash(itemID, rawURL)).
				Update("matches", n).Error
			if err != nil {
				return fmt.Errorf("set matches for %s: %w", rawURL, err)
			}
		}
		return nil
	})
}

// DeleteModel forgets what was learned for an item, keeping the pages and the
// marks.
//
// The rules and the fitted chain go with it: they are what training produced,
// and leaving them would mean the next crawl scored against a model the meta
// says is not there. The records stay, because a record is what was found
// rather than what was learned, and so do the marks, which are the expensive
// part and were made by a person.
func (s *Store) DeleteModel(ctx context.Context, itemID uint) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, model := range []any{&Rule{}, &ModelMeta{}} {
			if err := tx.Where("item_id = ?", itemID).Delete(model).Error; err != nil {
				return fmt.Errorf("delete %T: %w", model, err)
			}
		}
		// The crawl chain is this item's; the extraction prior is shared and
		// has no item, so a null item_id is left alone.
		if err := tx.Where("item_id = ?", itemID).Delete(&Chain{}).Error; err != nil {
			return fmt.Errorf("delete chain: %w", err)
		}
		return nil
	})
}

// RecordByID reads one record in full, with its values and the url it came
// from. Scoped to the item so an id from another item's listing is not found
// rather than shown.
func (s *Store) RecordByID(ctx context.Context, itemID, id uint) (*RecordRow, error) {
	rows, _, err := s.SearchRecords(ctx, itemID, RecordQuery{IDs: []uint{id}})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("record %d: %w", id, ErrNotFound)
	}
	return &rows[0], nil
}

// DeleteRecords removes records and reports how many went.
//
// Scoped to the item, so an id belonging to another item is a miss rather than
// somebody else's row deleted. The values go with them: the association
// declares the cascade, and foreign keys are enforced once migrations are done.
//
// The pages are left alone. A record is what was read out of a page, and
// removing a bad reading is not a reason to refetch the site; the next train
// reads the same page again.
func (s *Store) DeleteRecords(ctx context.Context, itemID uint, ids []uint) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	res := s.db.WithContext(ctx).
		Where("item_id = ? AND id IN ?", itemID, ids).
		Delete(&Record{})
	if res.Error != nil {
		return 0, fmt.Errorf("delete records: %w", res.Error)
	}
	return res.RowsAffected, nil
}
