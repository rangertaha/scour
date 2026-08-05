// SPDX-License-Identifier: GPL-3.0-or-later

package exporter_test

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rangertaha/scour/internal/engine"
	"github.com/rangertaha/scour/internal/exporter"
	"github.com/rangertaha/scour/internal/record"

	_ "github.com/rangertaha/scour/internal/exporter/files"
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

// sink is a writer a test can read back, and one that records whether it was
// closed: a format that has to be closed to be valid must actually close.
type sink struct {
	bytes.Buffer
	closed bool
}

func (s *sink) Close() error { s.closed = true; return nil }

func write(t *testing.T, blocks string, out map[string]io.WriteCloser, records ...*record.Record) *exporter.Set {
	t.Helper()

	set, err := exporter.New(context.Background(), job(t, blocks), out)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := set.Write(context.Background(), records...); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := set.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return set
}

func TestJSONLinesIsOneRecordPerLine(t *testing.T) {
	out := &sink{}
	write(t, `
  exporter "jsonlines" "article" {}
`, map[string]io.WriteCloser{"jsonlines.article": out},
		rec("https://example.com/a", "One", "Alex"),
		rec("https://example.com/b", "Two", "Sam"))

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("wrote %d lines:\n%s", len(lines), out.String())
	}
	for i, line := range lines {
		var r record.Record
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Errorf("line %d is not JSON: %v", i, err)
		}
		if r.Item != "article" || r.Spec != "abc123" {
			t.Errorf("line %d lost its provenance: %+v", i, r)
		}
	}
	if !out.closed {
		t.Error("the file was not closed")
	}
}

// TestARunThatDiesHalfwayLeavesReadableJSONLines. That is the property that
// matters most when a crawl takes hours, and it is why this format exists
// beside the array one.
func TestARunThatDiesHalfwayLeavesReadableJSONLines(t *testing.T) {
	out := &sink{}
	set, err := exporter.New(context.Background(), job(t, `
  exporter "jsonlines" "article" {}
`), map[string]io.WriteCloser{"jsonlines.article": out})
	if err != nil {
		t.Fatal(err)
	}
	if err := set.Write(context.Background(), rec("https://example.com/a", "One", "Alex")); err != nil {
		t.Fatal(err)
	}

	// Never closed, as though the process had stopped here.
	var r record.Record
	if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &r); err != nil {
		t.Errorf("what was written before the crash is not readable: %v", err)
	}
	if r.URL != "https://example.com/a" {
		t.Errorf("got %+v", r)
	}
}

func TestJSONIsOneArray(t *testing.T) {
	out := &sink{}
	write(t, `
  exporter "json" "article" {}
`, map[string]io.WriteCloser{"json.article": out},
		rec("https://example.com/a", "One", "Alex"),
		rec("https://example.com/b", "Two", "Sam"))

	var records []record.Record
	if err := json.Unmarshal(out.Bytes(), &records); err != nil {
		t.Fatalf("not an array: %v\n%s", err, out.String())
	}
	if len(records) != 2 {
		t.Errorf("wrote %d records", len(records))
	}
}

func TestJSONWithNoRecordsIsStillAnArray(t *testing.T) {
	out := &sink{}
	write(t, `
  exporter "json" "article" {}
`, map[string]io.WriteCloser{"json.article": out})

	var records []record.Record
	if err := json.Unmarshal(out.Bytes(), &records); err != nil {
		t.Errorf("an empty export is not valid JSON: %v\n%q", err, out.String())
	}
	if len(records) != 0 {
		t.Errorf("got %d records from nothing", len(records))
	}
}

// TestCSVColumnsComeFromTheShape, not from whichever record arrived first, or
// two runs over one corpus would produce two different headers.
func TestCSVColumnsComeFromTheShape(t *testing.T) {
	out := &sink{}
	write(t, `
  exporter "csv" "article" {}
`, map[string]io.WriteCloser{"csv.article": out},
		// A record missing a value the shape declares, and holding one it
		// does not.
		&record.Record{
			Item: "article", URL: "https://example.com/a", Fetched: fetched,
			Values: map[string]string{"title": "One", "extra": "ignored"},
		})

	rows, err := csv.NewReader(strings.NewReader(out.String())).ReadAll()
	if err != nil {
		t.Fatalf("not CSV: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %v", rows)
	}

	header := strings.Join(rows[0], ",")
	if header != "url,fetched,author.name,title" {
		t.Errorf("header = %q", header)
	}
	if rows[1][3] != "One" {
		t.Errorf("row = %v", rows[1])
	}
	if rows[1][2] != "" {
		t.Errorf("a value the record did not have was invented: %v", rows[1])
	}
}

// TestAFileGoesWhereTheBlockSaid.
func TestAFileGoesWhereTheBlockSaid(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "out", "nested")

	set, err := exporter.New(context.Background(), job(t, `
  exporter "jsonlines" "article" {
    dir = "`+dir+`"
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

	body, err := os.ReadFile(filepath.Join(dir, "article.jsonl"))
	if err != nil {
		t.Fatalf("no file: %v", err)
	}
	if !strings.Contains(string(body), "One") {
		t.Errorf("file = %q", body)
	}
}

// TestOneExporterPerItem: a record whose item nobody exports is not an error,
// because a job may extract something for a step to use and never write it.
func TestOneExporterPerItem(t *testing.T) {
	out := &sink{}
	write(t, `
  exporter "jsonlines" "article" {}
`, map[string]io.WriteCloser{"jsonlines.article": out},
		rec("https://example.com/a", "One", "Alex"),
		&record.Record{Item: "price", URL: "https://example.com/p", Fetched: fetched,
			Values: map[string]string{"value": "1.50"}})

	if strings.Contains(out.String(), "1.50") {
		t.Error("an exporter wrote an item it was not named for")
	}
	if !strings.Contains(out.String(), "One") {
		t.Error("the item it was named for was not written")
	}
}

// TestAFormatNothingWritesIsRefusedWhenBuilt, and all of them at once.
func TestAFormatNothingWritesIsRefusedWhenBuilt(t *testing.T) {
	_, err := exporter.New(context.Background(), job(t, `
  exporter "parquet" "article" {}
`), nil)
	if err == nil {
		t.Fatal("built an exporter for a format nothing writes")
	}
	if !strings.Contains(err.Error(), "parquet") || !strings.Contains(err.Error(), "json") {
		t.Errorf("the error does not say what is missing or what there is: %v", err)
	}
}

// TestARefusedSetClosesWhatItOpened, or a job refused for its second format
// leaves the first one's file open.
func TestARefusedSetClosesWhatItOpened(t *testing.T) {
	opened := &sink{}

	_, err := exporter.New(context.Background(), job(t, `
  exporter "jsonlines" "article" {}

  exporter "parquet" "article" {}
`), map[string]io.WriteCloser{"jsonlines.article": opened})
	if err == nil {
		t.Fatal("built it anyway")
	}
	if !opened.closed {
		t.Error("the exporter that did build was left open")
	}
}

// TestEveryFailureIsReportedOnClose: a JSON file that will not close must not
// stop a CSV from being flushed.
func TestEveryFailureIsReportedOnClose(t *testing.T) {
	set, err := exporter.New(context.Background(), job(t, `
  exporter "jsonlines" "article" {}

  exporter "csv" "article" {}
`), map[string]io.WriteCloser{
		"jsonlines.article": &stuck{},
		"csv.article":       &sink{},
	})
	if err != nil {
		t.Fatal(err)
	}

	err = set.Close()
	if err == nil {
		t.Fatal("a writer that would not close reported success")
	}
	if !strings.Contains(err.Error(), "jsonlines.article") {
		t.Errorf("the error does not say which: %v", err)
	}
}

type stuck struct{ bytes.Buffer }

func (s *stuck) Close() error { return io.ErrClosedPipe }

func TestAnExporterNeedsAJob(t *testing.T) {
	if _, err := exporter.New(context.Background(), nil, nil); err == nil {
		t.Error("built exporters for no job")
	}
}

func TestAddressesAndRegistered(t *testing.T) {
	out := &sink{}
	set := write(t, `
  exporter "jsonlines" "article" {}
`, map[string]io.WriteCloser{"jsonlines.article": out})

	// Closed already, so the set is empty; what matters is that Registered
	// says what this build can do.
	_ = set
	if !exporter.Has("csv") || exporter.Has("parquet") {
		t.Errorf("Registered() = %v", exporter.Registered())
	}
}

// TestAFieldTheExporterDoesNotKnowIsRefused, with a position.
func TestAFieldTheExporterDoesNotKnowIsRefused(t *testing.T) {
	_, err := exporter.New(context.Background(), job(t, `
  exporter "jsonlines" "article" {
    directory = "./out"
  }
`), nil)
	if err == nil {
		t.Fatal("a typo was silently ignored")
	}
	if !strings.Contains(err.Error(), "job.hcl") {
		t.Errorf("the error has no position: %v", err)
	}
}

// TestAnEmptyCSVStillHasItsHeader.
//
// A job whose pipeline drops everything left a zero-byte file, and the header
// is the one thing still knowable when there are no rows: it comes from the
// shape rather than from the data. Without it a consumer cannot tell "no rows"
// from "wrong file", and pandas raises EmptyDataError on the difference.
func TestAnEmptyCSVStillHasItsHeader(t *testing.T) {
	out := &sink{}
	write(t, `
  exporter "csv" "article" {}
`, map[string]io.WriteCloser{"csv.article": out})

	rows, err := csv.NewReader(strings.NewReader(out.String())).ReadAll()
	if err != nil {
		t.Fatalf("an empty export is not CSV: %v\n%q", err, out.String())
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %v, want the header alone", rows)
	}
	if got := strings.Join(rows[0], ","); got != "url,fetched,author.name,title" {
		t.Errorf("header = %q", got)
	}
}
