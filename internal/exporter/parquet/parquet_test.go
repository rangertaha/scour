// SPDX-License-Identifier: GPL-3.0-or-later

package parquet_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	parquetgo "github.com/parquet-go/parquet-go"

	"github.com/rangertaha/scour/internal/engine"
	"github.com/rangertaha/scour/internal/exporter"
	"github.com/rangertaha/scour/internal/exporter/exportertest"
	"github.com/rangertaha/scour/internal/record"

	exportparquet "github.com/rangertaha/scour/internal/exporter/parquet"
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

// build is the exporter a crawl would have, writing into dir.
func build(t *testing.T, dir string, blocks ...string) *exporter.Set {
	t.Helper()

	set, err := exporter.New(context.Background(), job(t, `
  exporter "parquet" "article" {
    dir = "`+dir+`"
`+strings.Join(blocks, "\n")+`
  }
`), nil)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	return set
}

// run is a whole crawl as far as an exporter is concerned: build, write, close.
func run(t *testing.T, dir string, records ...*record.Record) {
	t.Helper()

	set := build(t, dir)
	if err := set.Write(context.Background(), records...); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := set.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// reopen is how every claim here is checked: the real file, read by the library
// rather than by the exporter that wrote it. Nothing in this package tells the
// reader what to expect, which is the whole promise of the format.
func reopen(t *testing.T, dir string) *parquetgo.Reader {
	t.Helper()

	path := exportparquet.Path(dir, "", "article")
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	t.Cleanup(func() { f.Close() })

	reader := parquetgo.NewReader(f)
	t.Cleanup(func() { reader.Close() })
	return reader
}

// names lists a file's columns in the order the file holds them.
func names(schema *parquetgo.Schema) []string {
	paths := schema.Columns()
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		out = append(out, strings.Join(path, "."))
	}
	return out
}

// rows reads every row as a map of column name to text, which is the shape a
// record went in as.
func rows(t *testing.T, reader *parquetgo.Reader) []map[string]string {
	t.Helper()

	columns := names(reader.Schema())

	buf := make([]parquetgo.Row, reader.NumRows())
	if len(buf) == 0 {
		return nil
	}
	n, err := reader.ReadRows(buf)
	if err != nil && n != len(buf) {
		t.Fatalf("read rows: %v", err)
	}

	out := make([]map[string]string, 0, n)
	for _, row := range buf[:n] {
		values := map[string]string{}
		for i, value := range row {
			values[columns[i]] = value.String()
		}
		out = append(out, values)
	}
	return out
}

// TestWhatWasWrittenIsWhatComesBack, read out of the file by something that
// knows nothing about the exporter that wrote it. That is the property the
// format is chosen for: the archive outlives the tool that made it.
func TestWhatWasWrittenIsWhatComesBack(t *testing.T) {
	dir := t.TempDir()

	run(t, dir,
		rec("https://example.com/a", "One", "Alex"),
		rec("https://example.com/b", "Two", "Sam"))

	got := rows(t, reopen(t, dir))
	want := []map[string]string{
		{
			"url": "https://example.com/a", "fetched": "2026-08-05T12:00:00Z",
			"spec": "abc123", "author.name": "Alex", "title": "One",
		},
		{
			"url": "https://example.com/b", "fetched": "2026-08-05T12:00:00Z",
			"spec": "abc123", "author.name": "Sam", "title": "Two",
		},
	}

	if len(got) != len(want) {
		t.Fatalf("got %d rows, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		for name, value := range want[i] {
			if got[i][name] != value {
				t.Errorf("row %d, column %q = %q, want %q", i, name, got[i][name], value)
			}
		}
	}
}

// TestTheSchemaComesFromTheShape, not from whichever record arrived first, or
// two runs over one corpus would produce two files with different schemas and
// nothing could read both.
func TestTheSchemaComesFromTheShape(t *testing.T) {
	dir := t.TempDir()

	// A record missing a value the shape declares, and holding one it does not.
	run(t, dir, &record.Record{
		Item: "article", URL: "https://example.com/a", Spec: "abc123", Fetched: fetched,
		Values: map[string]string{"title": "One", "extra": "ignored"},
	})

	reader := reopen(t, dir)
	if got := strings.Join(names(reader.Schema()), ","); got != "author.name,fetched,spec,title,url" {
		t.Errorf("columns = %q", got)
	}

	got := rows(t, reader)
	if len(got) != 1 {
		t.Fatalf("got %d rows, want 1", len(got))
	}
	if _, ok := got[0]["extra"]; ok {
		t.Errorf("a value the shape did not declare became a column: %v", got[0])
	}
	if got[0]["author.name"] != "" {
		t.Errorf("a value the record did not have was invented: %q", got[0]["author.name"])
	}
}

// TestAnEmptyExportIsStillAReadableFileWithItsSchema.
//
// A job whose pipeline drops everything must not leave a file nothing can open.
// The schema is knowable when the data is not, because it comes from the shape,
// and it is what tells a consumer "no rows" from "wrong file". The CSV
// exporter's header is the same claim.
func TestAnEmptyExportIsStillAReadableFileWithItsSchema(t *testing.T) {
	dir := t.TempDir()

	run(t, dir)

	reader := reopen(t, dir)
	if n := reader.NumRows(); n != 0 {
		t.Errorf("an empty export holds %d rows", n)
	}
	if got := strings.Join(names(reader.Schema()), ","); got != "author.name,fetched,spec,title,url" {
		t.Errorf("columns = %q, want the shape's", got)
	}
}

// TestCloseFinishesTheFile, and nothing before it is readable.
//
// The schema and the offset of every column chunk are in a footer written last,
// so a run that died halfway leaves bytes no reader will open. That is the
// trade for the format and the reason jsonlines exists beside it, and it is
// only honest if Close really does write the footer.
func TestCloseFinishesTheFile(t *testing.T) {
	dir := t.TempDir()

	set := build(t, dir)
	if err := set.Write(context.Background(), rec("https://example.com/a", "One", "Alex")); err != nil {
		t.Fatal(err)
	}

	// As though the process had stopped here.
	path := exportparquet.Path(dir, "", "article")
	unfinished, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	info, err := unfinished.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parquetgo.OpenFile(unfinished, info.Size()); err == nil {
		t.Error("a file that was never closed read as Parquet, so Close proves nothing")
	}
	unfinished.Close()

	if err := set.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	got := rows(t, reopen(t, dir))
	if len(got) != 1 || got[0]["title"] != "One" {
		t.Errorf("close left %v", got)
	}
}

// TestARowGroupIsClosedBeforeTheRunEnds, or a crawl of millions of pages would
// hold every row it had written in one group and grow with the crawl.
func TestARowGroupIsClosedBeforeTheRunEnds(t *testing.T) {
	dir := t.TempDir()

	set := build(t, dir)

	records := make([]*record.Record, 0, exportparquet.RowGroup)
	for i := range cap(records) {
		records = append(records, rec("https://example.com/"+strings.Repeat("a", i+1), "One", "Alex"))
	}
	if err := set.Write(context.Background(), records...); err != nil {
		t.Fatal(err)
	}

	// One more after the group was full, so what Close finishes is a second
	// group rather than the only one.
	records = append(records, rec("https://example.com/last", "Two", "Sam"))
	if err := set.Write(context.Background(), records[len(records)-1]); err != nil {
		t.Fatal(err)
	}
	if err := set.Close(); err != nil {
		t.Fatal(err)
	}

	path := exportparquet.Path(dir, "", "article")
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	file, err := parquetgo.OpenFile(f, info.Size())
	if err != nil {
		t.Fatal(err)
	}

	if groups := len(file.RowGroups()); groups < 2 {
		t.Errorf("%d rows left %d row group(s), want the full one closed before the rest",
			len(records), groups)
	}
	if n := file.NumRows(); n != int64(len(records)) {
		t.Errorf("the file holds %d rows, want %d", n, len(records))
	}
}

// TestTheFileGoesWhereTheBlockSaid, under whatever name it asked for.
func TestTheFileGoesWhereTheBlockSaid(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "out", "nested")

	set := build(t, dir, `    file = "articles.pq"`)
	if err := set.Write(context.Background(), rec("https://example.com/a", "One", "Alex")); err != nil {
		t.Fatal(err)
	}
	if err := set.Close(); err != nil {
		t.Fatal(err)
	}

	path := exportparquet.Path(dir, "articles.pq", "article")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("no file at %s: %v", path, err)
	}
}

// TestEveryCompressionThisWritesIsReadBack, because a codec named in a block
// and not supported by the reader would be a file that opens and then fails on
// the first column.
func TestEveryCompressionThisWritesIsReadBack(t *testing.T) {
	for _, codec := range exportparquet.Codecs() {
		t.Run(codec, func(t *testing.T) {
			dir := t.TempDir()

			set := build(t, dir, `    compression = "`+codec+`"`)
			if err := set.Write(context.Background(), rec("https://example.com/a", "One", "Alex")); err != nil {
				t.Fatal(err)
			}
			if err := set.Close(); err != nil {
				t.Fatal(err)
			}

			got := rows(t, reopen(t, dir))
			if len(got) != 1 || got[0]["title"] != "One" || got[0]["author.name"] != "Alex" {
				t.Errorf("%s round-tripped to %v", codec, got)
			}
		})
	}
}

// TestACompressionNobodyWritesIsRefused, rather than quietly falling back: a
// job that asked for zstd and got snappy produced a bigger archive than the
// person who wrote it expects, and nothing would have said so.
func TestACompressionNobodyWritesIsRefused(t *testing.T) {
	dir := t.TempDir()

	_, err := exporter.New(context.Background(), job(t, `
  exporter "parquet" "article" {
    dir         = "`+dir+`"
    compression = "lzo"
  }
`), nil)
	if err == nil {
		t.Fatal("built an exporter with a compression this cannot write")
	}
	if !strings.Contains(err.Error(), "lzo") || !strings.Contains(err.Error(), "snappy") {
		t.Errorf("the error does not say what was asked for or what there is: %v", err)
	}

	// And nothing was created, so there is no half-written file to find later.
	if _, err := os.Stat(exportparquet.Path(dir, "", "article")); err == nil {
		t.Error("a refused exporter left a file behind")
	}
}

// TestAPropertyThatCollidesWithAColumnIsRefused. A schema is a set of names, so
// two columns of one name are one column, and the file would be missing a
// property without anything having failed.
func TestAPropertyThatCollidesWithAColumnIsRefused(t *testing.T) {
	doc, err := engine.Parse([]byte(`
job "news" {
  start = ["https://example.com/"]

  item "article" {
    property "url" {
      type = str
    }
  }

  exporter "parquet" "article" {
    dir = "`+t.TempDir()+`"
  }
}
`), "job.hcl")
	if err != nil {
		t.Fatal(err)
	}

	if _, err = exporter.New(context.Background(), doc.Jobs[0], nil); err == nil {
		t.Fatal("built a file with two columns of one name")
	} else if !strings.Contains(err.Error(), "url") {
		t.Errorf("the error does not say which property: %v", err)
	}
}

// TestContract holds this format to what every exporter promises. See
// [exportertest].
func TestContract(t *testing.T) {
	exportertest.Run(t, func(t *testing.T, dir string) exporter.Exporter {
		set, err := exporter.New(context.Background(), job(t, `
  exporter "parquet" "article" {
    dir = "`+dir+`"
  }
`), nil)
		if err != nil {
			t.Fatalf("new: %v", err)
		}
		return exportertest.Only(t, set)
	})
}
