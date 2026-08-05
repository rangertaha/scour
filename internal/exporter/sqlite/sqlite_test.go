// SPDX-License-Identifier: GPL-3.0-or-later

package sqlite_test

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/rangertaha/scour/internal/engine"
	"github.com/rangertaha/scour/internal/exporter"
	"github.com/rangertaha/scour/internal/exporter/exportertest"
	"github.com/rangertaha/scour/internal/record"

	exportsqlite "github.com/rangertaha/scour/internal/exporter/sqlite"
)

var fetched = time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)

func job(t *testing.T, blocks string) *engine.Job {
	t.Helper()

	src := `
job "news" {
  start = ["https://example.com/"]

  item "article" {
    property "title" {
      type = str
    }

    property "author" {
      type = object

      property "name" {
        type = str
      }
    }
  }
` + blocks + `
}
`
	doc, err := engine.Parse([]byte(src), "job.hcl")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := doc.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	return doc.Jobs[0]
}

func rec(url, title, author string) *record.Record {
	return &record.Record{
		Item: "article", URL: url, Spec: "abc123", Fetched: fetched,
		Values: map[string]string{"title": title, "author.name": author},
	}
}

// run is a whole crawl as far as an exporter is concerned: build, write, close.
func run(t *testing.T, dir string, records ...*record.Record) {
	t.Helper()

	set, err := exporter.New(context.Background(), job(t, `
  exporter "sqlite" "article" {
    dir = "`+dir+`"
  }
`), nil)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := set.Write(context.Background(), records...); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := set.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// reopen is how every claim here is checked: a second handle on the real file,
// because what matters is what landed rather than what the exporter believes it
// wrote.
func reopen(t *testing.T, dir string) *sql.DB {
	t.Helper()

	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, exportsqlite.File))
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func count(t *testing.T, db *sql.DB) int {
	t.Helper()

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM article`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

// TestWhatWasWrittenIsWhatComesBack, read out of the file by something that
// knows nothing about the exporter that wrote it.
func TestWhatWasWrittenIsWhatComesBack(t *testing.T) {
	dir := t.TempDir()

	run(t, dir,
		rec("https://example.com/a", "One", "Alex"),
		rec("https://example.com/b", "Two", "Sam"))

	db := reopen(t, dir)

	rows, err := db.Query(`SELECT url, fetched, spec, "author.name", title FROM article ORDER BY url`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()

	type row struct{ url, fetched, spec, author, title string }
	var got []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.url, &r.fetched, &r.spec, &r.author, &r.title); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	want := []row{
		{"https://example.com/a", "2026-08-05T12:00:00Z", "abc123", "Alex", "One"},
		{"https://example.com/b", "2026-08-05T12:00:00Z", "abc123", "Sam", "Two"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d rows, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestColumnsComeFromTheShape, not from whichever record arrived first, or two
// runs over one corpus would produce two different tables.
func TestColumnsComeFromTheShape(t *testing.T) {
	dir := t.TempDir()

	// A record missing a value the shape declares, and holding one it does not.
	run(t, dir, &record.Record{
		Item: "article", URL: "https://example.com/a", Spec: "abc123", Fetched: fetched,
		Values: map[string]string{"title": "One", "extra": "ignored"},
	})

	db := reopen(t, dir)

	rows, err := db.Query(`SELECT name FROM pragma_table_info('article')`)
	if err != nil {
		t.Fatalf("table_info: %v", err)
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	if got := strings.Join(names, ","); got != "key,url,fetched,spec,author.name,title" {
		t.Errorf("columns = %q", got)
	}

	// The value the shape did not declare is not a column, and the value the
	// record did not have is a hole rather than an invention.
	var author string
	if err := db.QueryRow(`SELECT "author.name" FROM article`).Scan(&author); err != nil {
		t.Fatal(err)
	}
	if author != "" {
		t.Errorf("a value the record did not have was invented: %q", author)
	}
}

// TestRunningTheCrawlAgainDoesNotDuplicateRows: an export is a copy that can be
// produced again, so producing it again over one corpus has to leave one row
// per record rather than two.
func TestRunningTheCrawlAgainDoesNotDuplicateRows(t *testing.T) {
	dir := t.TempDir()

	records := []*record.Record{
		rec("https://example.com/a", "One", "Alex"),
		rec("https://example.com/b", "Two", "Sam"),
	}

	run(t, dir, records...)
	run(t, dir, records...)

	db := reopen(t, dir)
	if n := count(t, db); n != 2 {
		t.Errorf("two runs over one corpus left %d rows, want 2", n)
	}

	// The second run's fetch time is what the row holds, because that is the
	// part of a repeated row that genuinely moved.
	later := fetched.Add(24 * time.Hour)
	again := rec("https://example.com/a", "One", "Alex")
	again.Fetched = later
	run(t, dir, again)

	var when string
	err := db.QueryRow(`SELECT fetched FROM article WHERE url = ?`, "https://example.com/a").Scan(&when)
	if err != nil {
		t.Fatal(err)
	}
	if when != later.Format("2006-01-02T15:04:05Z") {
		t.Errorf("fetched = %q, want the latest run's", when)
	}
	if n := count(t, db); n != 2 {
		t.Errorf("re-fetching one page left %d rows, want 2", n)
	}
}

// TestOnePageCanYieldSeveralOfOneItem, which is why the key is not the URL: a
// table keyed by URL would keep the last price on the page and lose the rest.
func TestOnePageCanYieldSeveralOfOneItem(t *testing.T) {
	dir := t.TempDir()

	run(t, dir,
		rec("https://example.com/a", "One", "Alex"),
		rec("https://example.com/a", "Two", "Sam"))

	db := reopen(t, dir)
	if n := count(t, db); n != 2 {
		t.Errorf("two items from one page left %d rows, want 2", n)
	}

	// And running it again still does not double them.
	run(t, dir,
		rec("https://example.com/a", "One", "Alex"),
		rec("https://example.com/a", "Two", "Sam"))
	if n := count(t, db); n != 2 {
		t.Errorf("the second run left %d rows, want 2", n)
	}
}

// TestCloseCommitsAndARunThatDiesHalfwayIsStillReadable. Both halves of one
// claim: nothing an open transaction holds is visible, and the file is a
// database the whole time regardless.
func TestCloseCommitsAndARunThatDiesHalfwayIsStillReadable(t *testing.T) {
	dir := t.TempDir()

	set, err := exporter.New(context.Background(), job(t, `
  exporter "sqlite" "article" {
    dir = "`+dir+`"
  }
`), nil)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := set.Write(context.Background(), rec("https://example.com/a", "One", "Alex")); err != nil {
		t.Fatal(err)
	}

	// As though the process were still running. The table is there and the file
	// answers questions, which is what somebody watching a long crawl needs.
	db := reopen(t, dir)
	if n := count(t, db); n != 0 {
		t.Errorf("an uncommitted batch was visible: %d rows", n)
	}

	if err := set.Close(); err != nil {
		t.Fatal(err)
	}
	if n := count(t, db); n != 1 {
		t.Errorf("close left %d rows, want the batch committed", n)
	}
}

// TestABatchIsCommittedBeforeTheRunEnds, or a crawl of a million pages would
// hold one transaction for hours and show a reader nothing until it ended.
func TestABatchIsCommittedBeforeTheRunEnds(t *testing.T) {
	dir := t.TempDir()

	set, err := exporter.New(context.Background(), job(t, `
  exporter "sqlite" "article" {
    dir = "`+dir+`"
  }
`), nil)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	t.Cleanup(func() { set.Close() })

	records := make([]*record.Record, 0, exportsqlite.Batch+1)
	for i := range cap(records) {
		records = append(records, rec("https://example.com/"+strings.Repeat("a", i+1), "One", "Alex"))
	}
	if err := set.Write(context.Background(), records...); err != nil {
		t.Fatal(err)
	}

	// Never closed. A full batch has been committed, so a run that died here
	// would have left that batch behind rather than nothing at all.
	db := reopen(t, dir)
	if n := count(t, db); n != exportsqlite.Batch {
		t.Errorf("%d rows survived, want the %d of the committed batch", n, exportsqlite.Batch)
	}
}

// TestTheFileGoesWhereTheBlockSaid, under whatever name it asked for.
func TestTheFileGoesWhereTheBlockSaid(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "out", "nested")

	set, err := exporter.New(context.Background(), job(t, `
  exporter "sqlite" "article" {
    dir  = "`+dir+`"
    file = "articles.sqlite"
  }
`), nil)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := set.Write(context.Background(), rec("https://example.com/a", "One", "Alex")); err != nil {
		t.Fatal(err)
	}
	if err := set.Close(); err != nil {
		t.Fatal(err)
	}

	path := exportsqlite.Path(dir, "articles.sqlite")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if n := count(t, db); n != 1 {
		t.Errorf("%s holds %d rows", path, n)
	}
}

// TestAPropertyThatCollidesWithAColumnIsRefused, rather than one of the two
// silently winning and the table reading as though nothing had gone wrong.
func TestAPropertyThatCollidesWithAColumnIsRefused(t *testing.T) {
	dir := t.TempDir()

	doc, err := engine.Parse([]byte(`
job "news" {
  start = ["https://example.com/"]

  item "article" {
    property "url" {
      type = str
    }
  }

  exporter "sqlite" "article" {
    dir = "`+dir+`"
  }
}
`), "job.hcl")
	if err != nil {
		t.Fatal(err)
	}

	_, err = exporter.New(context.Background(), doc.Jobs[0], nil)
	if err == nil {
		t.Fatal("built a table with two columns of one name")
	}
	if !strings.Contains(err.Error(), "url") {
		t.Errorf("the error does not say which property: %v", err)
	}
}

// TestTwoExportersShareOneFile.
//
// One file for a job's items rather than one per item is what this format is
// for, so two exporters on one database is the ordinary configuration and not
// an unusual one. Each used to open a handle of its own, and each handle holds a
// write transaction open across Write calls, because a run hands over whatever
// it has ready and one record is not a transaction's worth of work.
//
// Two write transactions on one SQLite file do not both proceed. The first
// writer left its transaction open, the second waited out busy_timeout and
// failed with SQLITE_BUSY, the run reported failure, and the second item's
// table was left empty: five seconds of stall per flush and then a lost export.
func TestTwoExportersShareOneFile(t *testing.T) {
	dir := t.TempDir()

	set, err := exporter.New(context.Background(), job(t, `
  item "price" {
    property "value" {
      type = str
    }
  }

  exporter "sqlite" "article" {
    dir = "`+dir+`"
  }

  exporter "sqlite" "price" {
    dir = "`+dir+`"
  }
`), nil)
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	var records []*record.Record
	for i := range 40 {
		records = append(records,
			rec(fmt.Sprintf("https://example.com/a/%d", i), "One", "Alex"),
			&record.Record{
				Item: "price", URL: fmt.Sprintf("https://example.com/p/%d", i),
				Spec: "abc123", Fetched: fetched,
				Values: map[string]string{"value": "9.99"},
			})
	}

	if err := set.Write(context.Background(), records...); err != nil {
		t.Fatalf("two exporters on one file could not both write: %v", err)
	}
	if err := set.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	db := reopen(t, dir)
	if got := count(t, db); got != 40 {
		t.Errorf("article rows = %d, want 40", got)
	}

	var prices int
	if err := db.QueryRow(`SELECT COUNT(*) FROM price`).Scan(&prices); err != nil {
		t.Fatalf("counting prices: %v", err)
	}
	if prices != 40 {
		t.Errorf("price rows = %d, want 40: the second exporter's table was lost", prices)
	}
}

// TestContract holds this format to what every exporter promises. See
// [exportertest].
func TestContract(t *testing.T) {
	exportertest.Run(t, func(t *testing.T, dir string) exporter.Exporter {
		set, err := exporter.New(context.Background(), job(t, `
  exporter "sqlite" "article" {
    dir = "`+dir+`"
  }
`), nil)
		if err != nil {
			t.Fatalf("new: %v", err)
		}
		return set
	})
}
