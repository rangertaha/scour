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
)

// ReplaceRules swaps an entity's induced rules for a new set.
//
// Training produces the whole set at once, so this is a replace rather than a
// merge: a rule that induction no longer believes in should disappear, not
// linger as a stale locator that still matches something.
func (s *Store) ReplaceRules(ctx context.Context, entityID uint, rules []Rule) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("entity_id = ?", entityID).Delete(&Rule{}).Error; err != nil {
			return fmt.Errorf("clear rules: %w", err)
		}
		for i := range rules {
			rules[i].EntityID = entityID
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

// Rules lists an entity's induced rules, parents before their children.
func (s *Store) Rules(ctx context.Context, entityID uint) ([]Rule, error) {
	var out []Rule
	err := s.db.WithContext(ctx).
		Where("entity_id = ?", entityID).
		Order("id ASC").
		Find(&out).Error
	if err != nil {
		return nil, fmt.Errorf("list rules: %w", err)
	}
	return out, nil
}

// Fingerprint identifies a record by what it holds rather than where it was
// found, so re-extracting the same values is an update and not a duplicate.
func Fingerprint(entityID uint, values map[string]string) string {
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	h := sha256.New()
	fmt.Fprintf(h, "%d", entityID)
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

// SaveRecords reconciles an entity's records with a freshly extracted set.
//
// Records are matched by fingerprint and updated in place, so a record that
// survives a retraining keeps both its id and its label. That matters twice
// over: labels are the expensive part, since a person produced them, and the
// ids are what `scour valid` and `scour invalid` are given. Deleting and
// recreating would renumber every row, so the ids a user just read off a
// search would label the wrong records, or nothing at all.
//
// Records whose fingerprint no longer appears are removed, since the model
// stopped finding them.
func (s *Store) SaveRecords(ctx context.Context, entityID uint, records []Extracted) (int, error) {
	saved := 0
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		existing, err := existingRecords(tx, entityID)
		if err != nil {
			return err
		}

		keep := make(map[uint]bool, len(records))
		done := make(map[string]bool, len(records))

		for _, rec := range records {
			fp := Fingerprint(entityID, rec.Values)

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
				row = Record{EntityID: entityID, Fingerprint: fp, Label: Unlabelled}
			}
			row.Confidence = rec.Confidence
			row.Format = rec.Format
			if rec.URL != "" {
				var u URL
				if err := tx.Select("id").Where("hash = ?", URLHash(entityID, rec.URL)).First(&u).Error; err == nil {
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

func existingRecords(tx *gorm.DB, entityID uint) (map[string]Record, error) {
	var rows []Record
	if err := tx.Where("entity_id = ?", entityID).Find(&rows).Error; err != nil {
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
	MinConfidence float64
	Formats       []string
	ExcludeFormat []string
	Label         Label
	Limit         int
}

// RecordRow is one record with its values attached.
type RecordRow struct {
	Record
	Values map[string]string
	URL    string
}

// SearchRecords returns extracted records, highest confidence first.
func (s *Store) SearchRecords(ctx context.Context, entityID uint, q RecordQuery) ([]RecordRow, int64, error) {
	base := s.db.WithContext(ctx).Model(&Record{}).Where("entity_id = ?", entityID)
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

	var total int64
	if err := base.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count records: %w", err)
	}

	query := base.Session(&gorm.Session{}).Order("confidence DESC, id ASC")
	if q.Limit > 0 {
		query = query.Limit(q.Limit)
	}
	var records []Record
	if err := query.Find(&records).Error; err != nil {
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

	out := make([]RecordRow, 0, len(records))
	for _, r := range records {
		row := RecordRow{Record: r, Values: byRecord[r.ID]}
		if r.URLID != 0 {
			var u URL
			if err := s.db.WithContext(ctx).Select("url").First(&u, r.URLID).Error; err == nil {
				row.URL = u.URL
			}
		}
		out = append(out, row)
	}
	return out, total, nil
}

// LabelRecords marks records valid or invalid. Unknown ids are reported rather
// than ignored, since a mistyped id would otherwise look like success.
func (s *Store) LabelRecords(ctx context.Context, entityID uint, ids []uint, label Label) (int64, error) {
	res := s.db.WithContext(ctx).
		Model(&Record{}).
		Where("entity_id = ? AND id IN ?", entityID, ids).
		Updates(map[string]any{"label": label, "updated_at": time.Now().UTC()})
	if res.Error != nil {
		return 0, fmt.Errorf("label records: %w", res.Error)
	}
	return res.RowsAffected, nil
}

// SaveModelMeta records where an entity's model lives and how it scored.
func (s *Store) SaveModelMeta(ctx context.Context, meta ModelMeta) error {
	err := s.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "entity_id"}},
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
func (s *Store) SetURLMatches(ctx context.Context, entityID uint, counts map[string]int) error {
	if len(counts) == 0 {
		return nil
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&URL{}).Where("entity_id = ?", entityID).Update("matches", 0).Error; err != nil {
			return fmt.Errorf("reset matches: %w", err)
		}
		for rawURL, n := range counts {
			err := tx.Model(&URL{}).
				Where("hash = ?", URLHash(entityID, rawURL)).
				Update("matches", n).Error
			if err != nil {
				return fmt.Errorf("set matches for %s: %w", rawURL, err)
			}
		}
		return nil
	})
}
