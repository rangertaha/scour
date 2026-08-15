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
//
// It has to return the format itself. See [Only], and the guard in [Run] that
// refuses anything else.
type Open func(t *testing.T, dir string) exporter.Exporter

// Only returns the one format a job document declared.
//
// Building through [exporter.New] is the right way to make a format, because it
// is the path that decodes the format's own block, and getting that wrong is
// what this suite is for. What it returns is a Set, and handing the Set back is
// the mistake [Run] refuses: three of the five checks below would then be
// answered by the Set and never reach the format.
func Only(t *testing.T, set *exporter.Set) exporter.Exporter {
	t.Helper()

	built := set.Exporters()
	if len(built) != 1 {
		t.Fatalf("the contract suite wants one exporter to test, and the document declared %d", len(built))
	}
	return built[0]
}

// Run puts an exporter through the contract.
func Run(t *testing.T, open Open) {
	t.Helper()

	open = itself(open)

	t.Run("CloseWithNothingWritten", func(t *testing.T) { testCloseEmpty(t, open) })
	t.Run("CloseIsIdempotent", func(t *testing.T) { testCloseTwice(t, open) })
	t.Run("WriteAfterCloseIsRefused", func(t *testing.T) { testWriteAfterClose(t, open) })
	t.Run("WriteWithNoRecordsIsFine", func(t *testing.T) { testWriteNothing(t, open) })
	t.Run("AnotherItemsRecordIsNotAnError", func(t *testing.T) { testOtherItem(t, open) })
}

// itself refuses an [exporter.Set] where a format was asked for.
//
// # Why this guard exists
//
// Because every one of the five wirings passed a Set, and the suite passed, and
// it was testing [exporter.Set] five times over. A Set satisfies
// [exporter.Exporter], so nothing complained: its Write refuses after close, its
// Close is idempotent, and its Write filters by item, which is precisely
// CloseIsIdempotent, WriteAfterCloseIsRefused and AnotherItemsRecordIsNotAnError.
// Deleting a format's own closed guard left this suite green.
//
// A comment saying "pass the format" would have been read by whoever wrote the
// sixth wiring and by nobody else. This is checked, so the next one fails.
func itself(open Open) Open {
	return func(t *testing.T, dir string) exporter.Exporter {
		t.Helper()

		built := open(t, dir)
		if set, ok := built.(*exporter.Set); ok {
			t.Fatalf("the contract suite was handed an *exporter.Set of %d exporter(s), not a format.\n"+
				"A Set answers this suite's questions itself and the format under it is never asked one.\n"+
				"Wrap the call in exportertest.Only(t, set).", len(set.Exporters()))
		}
		return built
	}
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

// testOtherItem: a record for another item is not an error.
//
// # What this does and does not promise
//
// It used to be called "ignored, not written", and no format keeps that
// promise: not one of the six looks at [record.Record.Item], so every one of
// them writes whatever it is handed. The check never noticed because it only
// asserts Write returns nil, and it was reading an [exporter.Set] at the time,
// which does filter.
//
// Routing is the Set's job and belongs in one place rather than in six, so the
// promise stays where it is and this suite states the weaker thing that is
// actually true: a format handed a record it was not built for must not fail
// the run over it. TestOneExporterPerItem in the exporter package is what holds
// the filter itself, by asserting the foreign value never reaches the output.
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
