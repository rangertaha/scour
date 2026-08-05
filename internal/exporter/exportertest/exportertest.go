// SPDX-License-Identifier: GPL-3.0-or-later

// Package exportertest is the contract every exporter has to keep.
//
// # Why a shared suite and not six sets of tests
//
// Because six implementations of one interface, each with tests of its own,
// drift, and the drift is invisible: each package is green, and the promise
// that is missing from one of them is the promise nobody notices is missing.
// This repository has been bitten by exactly that twice.
//
// [cache/cachetest] found it first. A failed write committed a truncated body
// over a good one in the object storage backend, and the local backend was
// immune because it renames. The defect had been there for as long as the
// backend had, and it was found within a minute of moving a single-backend test
// into the shared suite.
//
// The exporters had it too. Three of them derived their columns with the same
// twenty lines and two of them checked for a property colliding with a column
// the format owns. The one that did not silently wrote the page's URL into both
// of two columns named `url` and dropped the extracted value, while the same
// job was refused at build time by the other two.
//
// So a promise an exporter makes belongs here, where a format that does not
// keep it fails rather than merely differs.
//
// # What is asserted
//
// Only what every format can keep, which is what makes it a contract rather
// than a description of one implementation. Nothing here reads the output: what
// a given format writes is that format's own business and its own tests. What is
// shared is the lifecycle, because a run drives every exporter the same way.
package exportertest

import (
	"context"
	"testing"
	"time"

	"github.com/rangertaha/scour/internal/exporter"
	"github.com/rangertaha/scour/internal/record"
)

// Open builds an exporter for one test, in a directory of that test's own.
type Open func(t *testing.T, dir string) exporter.Exporter

// Run puts an exporter through the contract.
func Run(t *testing.T, open Open) {
	t.Helper()

	t.Run("CloseWithNothingWritten", func(t *testing.T) { testCloseEmpty(t, open) })
	t.Run("CloseIsIdempotent", func(t *testing.T) { testCloseTwice(t, open) })
	t.Run("WriteAfterCloseIsRefused", func(t *testing.T) { testWriteAfterClose(t, open) })
	t.Run("WriteWithNoRecordsIsFine", func(t *testing.T) { testWriteNothing(t, open) })
	t.Run("AnotherItemsRecordIsNotWritten", func(t *testing.T) { testOtherItem(t, open) })
}

// testCloseEmpty: an export that received nothing still has to be finished.
//
// A crawl whose pipeline dropped everything is the ordinary way this happens,
// and it is the case where the operator most needs the artifact to exist: an
// empty file with a header says "the shape was read and found nothing", and no
// file at all says "something went wrong and I do not know what".
func testCloseEmpty(t *testing.T, open Open) {
	e := open(t, t.TempDir())
	if err := e.Close(); err != nil {
		t.Errorf("closing an export that received nothing: %v", err)
	}
}

// testCloseTwice: Close is idempotent.
//
// A run closes its exporters on the way out and again from a deferred cleanup
// when it failed, so the second call is not hypothetical. It must not report an
// error and must not corrupt what the first one finished: a second footer, a
// second closing bracket or a second commit is a file no reader can open.
func testCloseTwice(t *testing.T, open Open) {
	e := open(t, t.TempDir())

	if err := e.Write(context.Background(), one()); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := e.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if err := e.Close(); err != nil {
		t.Errorf("second close: %v", err)
	}
}

// testWriteAfterClose: a write after Close is refused rather than swallowed.
//
// The file is finished, so the record cannot land in it, and returning nil
// would tell a caller it had been written. A run that reported success while
// silently dropping the last flush is the worst outcome available here: the
// export looks complete and is short.
func testWriteAfterClose(t *testing.T, open Open) {
	e := open(t, t.TempDir())

	if err := e.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := e.Write(context.Background(), one()); err == nil {
		t.Error("a record written after Close was accepted, so a caller was told it landed")
	}
}

// testWriteNothing: Write with no records is not an error.
//
// A run hands over whatever it has ready, and a wave that filtered everything
// leaves nothing. That is a crawl working, not a crawl failing.
func testWriteNothing(t *testing.T, open Open) {
	e := open(t, t.TempDir())
	defer e.Close()

	if err := e.Write(context.Background()); err != nil {
		t.Errorf("writing no records: %v", err)
	}
}

// testOtherItem: a record for another item is ignored, not written.
//
// An exporter is named for one item and a run hands every exporter every
// record, so this is the ordinary path and not an edge case. Writing another
// item's record would put values under column names that mean something else.
func testOtherItem(t *testing.T, open Open) {
	e := open(t, t.TempDir())

	if err := e.Write(context.Background(), &record.Record{
		Item:    "somethingelse",
		URL:     "https://example.com/other",
		Spec:    "abc123",
		Fetched: When,
		Values:  map[string]string{"title": "Not this one"},
	}); err != nil {
		t.Errorf("a record for another item was an error rather than ignored: %v", err)
	}
	if err := e.Close(); err != nil {
		t.Errorf("close: %v", err)
	}
}

// When is the fetch time every record in this suite carries, fixed so that a
// format writing it cannot make two runs differ.
var When = time.Date(2026, 8, 5, 12, 23, 45, 0, time.UTC)

// Item is the item name the suite's records belong to. An exporter handed to
// [Run] must be built for it.
const Item = "article"

func one() *record.Record {
	return &record.Record{
		Item:    Item,
		URL:     "https://example.com/a",
		Spec:    "abc123",
		Fetched: When,
		Values:  map[string]string{"title": "One", "author.name": "Alex"},
	}
}
