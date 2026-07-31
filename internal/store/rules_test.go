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

// A record that survives a retraining has to keep its id and its label.
//
// Labels are the expensive part, since a person produced them, and the ids are
// what a label is given over the API. Deleting and recreating on every training
// run would renumber every row, so an id read off one listing would label the
// wrong record on the next. This was covered through the CLI until the labelling
// commands were removed; the property is still here.
func TestRecordsKeepIDsAndLabelsAcrossRetraining(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	item, err := s.CreateItem(ctx, "vehicle")
	if err != nil {
		t.Fatal(err)
	}

	first := []Extracted{
		{Values: map[string]string{"make": "Ford"}, Confidence: 0.9, Format: "html"},
		{Values: map[string]string{"make": "Toyota"}, Confidence: 0.8, Format: "html"},
	}
	if _, err := s.SaveRecords(ctx, item.ID, first); err != nil {
		t.Fatal(err)
	}

	rows, _, err := s.SearchRecords(ctx, item.ID, RecordQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("saved %d records, want 2", len(rows))
	}
	ford := rows[0].ID
	if n, err := s.LabelRecords(ctx, item.ID, []uint{ford}, Invalid); err != nil || n != 1 {
		t.Fatalf("LabelRecords = %d, %v; want 1, nil", n, err)
	}

	// Train again over the same pages: the same values, so the same records.
	if _, err := s.SaveRecords(ctx, item.ID, first); err != nil {
		t.Fatal(err)
	}

	after, _, err := s.SearchRecords(ctx, item.ID, RecordQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 2 {
		t.Fatalf("retraining changed the record count to %d, want 2", len(after))
	}
	var seen bool
	for _, r := range after {
		if r.ID != ford {
			continue
		}
		seen = true
		if r.Label != Invalid {
			t.Errorf("the label was lost by retraining: %q", r.Label)
		}
	}
	if !seen {
		t.Errorf("record %d was renumbered by retraining", ford)
	}

	// Filtering by label is how a reviewer finds what they marked.
	only, total, err := s.SearchRecords(ctx, item.ID, RecordQuery{Label: Invalid})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(only) != 1 || only[0].ID != ford {
		t.Errorf("filtering by label found %d records, want just %d", total, ford)
	}
}

// Labelling ids that are not there must report that it did nothing, or a
// reviewer believes a verdict was recorded when it was not.
func TestLabelRecordsIgnoresUnknownIDs(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	item, err := s.CreateItem(ctx, "vehicle")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveRecords(ctx, item.ID, []Extracted{
		{Values: map[string]string{"make": "Ford"}, Confidence: 0.9, Format: "html"},
	}); err != nil {
		t.Fatal(err)
	}

	n, err := s.LabelRecords(ctx, item.ID, []uint{9999}, Invalid)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("labelled %d unknown records, want 0", n)
	}
}
