// SPDX-License-Identifier: GPL-3.0-or-later

package store

import (
	"context"
	"fmt"
	"testing"
)

// SinceID is what a follower asks with: it has already seen everything below
// the mark and wants what landed after it, in the order it landed rather than
// by confidence.
func TestSearchRecordsSinceID(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	item, err := s.CreateItem(ctx, "news")
	if err != nil {
		t.Fatal(err)
	}

	var ids []uint
	for i := range 5 {
		rec := Record{ItemID: item.ID, Fingerprint: fmt.Sprintf("fp-%d", i), Format: "html",
			Confidence: float64(5-i) / 10, Label: Unlabelled}
		if err := s.db.Create(&rec).Error; err != nil {
			t.Fatal(err)
		}
		ids = append(ids, rec.ID)
	}

	// Without a mark, highest confidence first.
	rows, total, err := s.SearchRecords(ctx, item.ID, RecordQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if total != 5 || len(rows) != 5 {
		t.Fatalf("got %d rows of %d, want 5 of 5", len(rows), total)
	}
	if rows[0].Confidence < rows[len(rows)-1].Confidence {
		t.Error("an unmarked query should come back highest confidence first")
	}

	// With a mark, only what is newer, oldest first, whatever its confidence.
	rows, total, err = s.SearchRecords(ctx, item.ID, RecordQuery{SinceID: ids[2]})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || total != 2 {
		t.Fatalf("got %d rows of %d after the mark, want 2 of 2", len(rows), total)
	}
	if rows[0].ID != ids[3] || rows[1].ID != ids[4] {
		t.Errorf("ids = %d, %d; want %d, %d in that order", rows[0].ID, rows[1].ID, ids[3], ids[4])
	}

	// The newest mark returns nothing, which is what a quiet follower sees.
	rows, _, err = s.SearchRecords(ctx, item.ID, RecordQuery{SinceID: ids[4]})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Errorf("got %d rows past the last id, want none", len(rows))
	}
}
