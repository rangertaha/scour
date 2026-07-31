// SPDX-License-Identifier: GPL-3.0-or-later

package store

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Judgement returns a cached model verdict, and reports whether there was one.
//
// A hit bumps the use counter, so the saving a cache produced can be measured
// rather than claimed.
func (s *Store) Judgement(ctx context.Context, key string) (float64, bool, error) {
	var row Judgement
	err := s.db.WithContext(ctx).Where("key = ?", key).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("read judgement: %w", err)
	}

	if err := s.db.WithContext(ctx).Model(&Judgement{}).
		Where("id = ?", row.ID).
		UpdateColumn("uses", gorm.Expr("uses + 1")).Error; err != nil {
		// The answer is already in hand; failing to count its reuse is not a
		// reason to go and ask a model again.
		return row.Score, true, nil
	}
	return row.Score, true, nil
}

// RememberJudgement stores a verdict.
func (s *Store) RememberJudgement(ctx context.Context, key, model string, score float64) error {
	row := Judgement{Key: key, Model: model, Score: score}
	err := s.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "key"}},
			DoUpdates: clause.AssignmentColumns([]string{"score", "model", "updated_at"}),
		}).
		Create(&row).Error
	if err != nil {
		return fmt.Errorf("record judgement: %w", err)
	}
	return nil
}

// JudgementStats reports what the cache holds and how much reuse it has seen.
func (s *Store) JudgementStats(ctx context.Context) (cached, reused int, err error) {
	var row struct {
		Count int64
		Uses  int64
	}
	err = s.db.WithContext(ctx).
		Model(&Judgement{}).
		Select("count(*) as count, coalesce(sum(uses), 0) as uses").
		Scan(&row).Error
	if err != nil {
		return 0, 0, fmt.Errorf("judgement stats: %w", err)
	}
	return int(row.Count), int(row.Uses), nil
}

// ForgetJudgements empties the cache, for when a prompt or a model changes and
// old verdicts are answers to a different question.
func (s *Store) ForgetJudgements(ctx context.Context) error {
	if err := s.db.WithContext(ctx).Where("1 = 1").Delete(&Judgement{}).Error; err != nil {
		return fmt.Errorf("forget judgements: %w", err)
	}
	return nil
}

// Verdict returns a cached page classification.
//
// It shares the judgement table with the matcher's scores because both are the
// same thing: one model's answer to one question, keyed by a hash of the
// question. The hash includes the model and the prompt, so a score and a
// verdict can never collide.
func (s *Store) Verdict(ctx context.Context, key string) (string, bool, error) {
	var row Judgement
	err := s.db.WithContext(ctx).Where("key = ?", key).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read verdict: %w", err)
	}
	if row.Verdict == "" {
		return "", false, nil
	}

	if err := s.db.WithContext(ctx).Model(&Judgement{}).
		Where("id = ?", row.ID).
		UpdateColumn("uses", gorm.Expr("uses + 1")).Error; err != nil {
		return row.Verdict, true, nil
	}
	return row.Verdict, true, nil
}

// RememberVerdict stores a page classification.
func (s *Store) RememberVerdict(ctx context.Context, key, model, verdict string) error {
	row := Judgement{Key: key, Model: model, Verdict: verdict}
	err := s.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "key"}},
			DoUpdates: clause.AssignmentColumns([]string{"verdict", "model", "updated_at"}),
		}).
		Create(&row).Error
	if err != nil {
		return fmt.Errorf("record verdict: %w", err)
	}
	return nil
}
