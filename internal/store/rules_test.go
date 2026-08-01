// SPDX-License-Identifier: GPL-3.0-or-later

package store

import (
	"context"
	"fmt"
	"testing"

	"github.com/rangertaha/scour/internal/query"
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

// seedRecords writes records with values and urls, which is what a search runs
// over. Built through the store rather than by hand so the test exercises the
// same rows the extractor writes.
func seedRecords(t *testing.T, s *Store, item *Item) {
	t.Helper()
	ctx := context.Background()
	rows := []struct {
		url    string
		conf   float64
		values map[string]string
	}{
		{"https://example.com/trucks/f-150", 0.9, map[string]string{"make": "Ford", "model": "F-150 crew cab", "year": "2026"}},
		{"https://example.com/cars/focus", 0.8, map[string]string{"make": "Ford", "model": "Focus", "year": "2019"}},
		{"https://other.com/trucks/tundra", 0.7, map[string]string{"make": "Toyota", "model": "Tundra", "year": "2026"}},
		{"https://other.com/blog/ford-history", 0.6, map[string]string{"make": "Various", "model": "A history of Ford", "year": "2020"}},
	}
	for i, r := range rows {
		if err := s.Discovered(ctx, item.ID, r.url, "", 0, 1); err != nil {
			t.Fatal(err)
		}
		u, err := s.urlID(ctx, item.ID, r.url)
		if err != nil {
			t.Fatal(err)
		}
		rec := Record{
			ItemID: item.ID, URLID: u, Confidence: r.conf,
			Fingerprint: fmt.Sprintf("fp-%d", i), Format: "html",
		}
		for p, v := range r.values {
			rec.Values = append(rec.Values, Value{Prop: p, Text: v})
		}
		if err := s.DB().Create(&rec).Error; err != nil {
			t.Fatal(err)
		}
	}
}

// A search is not a listing: it takes a query, every term narrows, and the
// order is how well each row answers it.
func TestSearchRecordsByQuery(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	item, err := s.CreateItem(ctx, "vehicle")
	if err != nil {
		t.Fatal(err)
	}
	seedRecords(t, s, item)
	props := []string{"make", "model", "year"}

	for _, tc := range []struct {
		name string
		args []string
		want int
	}{
		{"a bare word reaches every field", []string{"Ford"}, 3},
		{"a field term looks only there", []string{"make:Ford"}, 2},
		{"terms narrow", []string{"make:Ford", "year:2026"}, 1},
		{"the url is searchable", []string{"url:other.com"}, 2},
		{"a bare word reaches the url", []string{"trucks"}, 2},
		{"no match is not an error", []string{"make:Honda"}, 0},
		{"matching is case insensitive", []string{"make:ford"}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q, err := query.Parse(tc.args, props)
			if err != nil {
				t.Fatal(err)
			}
			rows, _, err := s.SearchRecords(ctx, item.ID, RecordQuery{Terms: q.Terms})
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != tc.want {
				got := make([]string, 0, len(rows))
				for _, r := range rows {
					got = append(got, r.Values["make"]+"/"+r.Values["model"])
				}
				t.Errorf("%v matched %d rows %v, want %d", tc.args, len(rows), got, tc.want)
			}
		})
	}
}

// The ordering is what makes search worth having: the record whose field is the
// word comes above the one that merely mentions it, whatever their confidence.
func TestSearchRanksTheDirectAnswerFirst(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	item, err := s.CreateItem(ctx, "vehicle")
	if err != nil {
		t.Fatal(err)
	}
	seedRecords(t, s, item)

	q, err := query.Parse([]string{"Ford"}, []string{"make", "model", "year"})
	if err != nil {
		t.Fatal(err)
	}
	rows, _, err := s.SearchRecords(ctx, item.ID, RecordQuery{Terms: q.Terms})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) < 3 {
		t.Fatalf("got %d rows, want the three mentioning Ford", len(rows))
	}
	if rows[0].Values["make"] != "Ford" {
		t.Errorf("first row is %q, want one whose make is exactly Ford", rows[0].Values["make"])
	}
	// The blog post has Ford inside a longer value, so it comes last however
	// its confidence compares.
	last := rows[len(rows)-1]
	if last.Values["model"] != "A history of Ford" {
		t.Errorf("last row is %q, want the one that only mentions Ford", last.Values["model"])
	}
	if len(rows[0].Matched) == 0 || rows[0].Matched[0] != "make" {
		t.Errorf("matched on %v, want make named first", rows[0].Matched)
	}
	if rows[0].Rank <= last.Rank {
		t.Errorf("ranks are %v and %v, want the direct answer above", rows[0].Rank, last.Rank)
	}
}

// The limit has to come after the ranking, or a search returns an arbitrary
// slice of the matches and calls it the best.
func TestSearchLimitsAfterRanking(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	item, err := s.CreateItem(ctx, "vehicle")
	if err != nil {
		t.Fatal(err)
	}
	seedRecords(t, s, item)

	q, err := query.Parse([]string{"Ford"}, []string{"make", "model", "year"})
	if err != nil {
		t.Fatal(err)
	}
	rows, total, err := s.SearchRecords(ctx, item.ID, RecordQuery{Terms: q.Terms, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if total != 3 {
		t.Errorf("total = %d, want the 3 that matched rather than the page size", total)
	}
	if rows[0].Values["make"] != "Ford" {
		t.Errorf("the one row kept is %q, want the best match", rows[0].Values["make"])
	}
}

// A wildcard in a term is a character to look for, not a pattern. Without
// escaping, searching for a percent sign returns everything.
func TestSearchTreatsWildcardsAsText(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	item, err := s.CreateItem(ctx, "vehicle")
	if err != nil {
		t.Fatal(err)
	}
	seedRecords(t, s, item)

	for _, term := range []string{"%", "_"} {
		q, err := query.Parse([]string{term}, []string{"make"})
		if err != nil {
			t.Fatal(err)
		}
		rows, _, err := s.SearchRecords(ctx, item.ID, RecordQuery{Terms: q.Terms})
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 0 {
			t.Errorf("%q matched %d rows, want 0: it is a character, not a wildcard", term, len(rows))
		}
	}
}

// A follower wants what landed after its mark, in the order it landed. Ranking
// a poll would print the stream out of sequence.
func TestSearchFollowKeepsArrivalOrder(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	item, err := s.CreateItem(ctx, "vehicle")
	if err != nil {
		t.Fatal(err)
	}
	seedRecords(t, s, item)

	q, err := query.Parse([]string{"Ford"}, []string{"make", "model", "year"})
	if err != nil {
		t.Fatal(err)
	}
	rows, _, err := s.SearchRecords(ctx, item.ID, RecordQuery{Terms: q.Terms, SinceID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) < 2 {
		t.Fatalf("got %d rows after the mark, want at least 2", len(rows))
	}
	for i := 1; i < len(rows); i++ {
		if rows[i].ID <= rows[i-1].ID {
			t.Fatalf("ids came back %d then %d, want ascending", rows[i-1].ID, rows[i].ID)
		}
	}
	// And the query still narrows: the Toyota must not appear.
	for _, r := range rows {
		if r.Values["make"] == "Toyota" {
			t.Error("a follower was given a record the query excludes")
		}
	}
}
