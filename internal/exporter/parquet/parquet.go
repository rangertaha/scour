// SPDX-License-Identifier: GPL-3.0-or-later

// Package parquet writes records to a Parquet file, one file per item.
//
// Import it for its side effect to make the format available:
//
//	import _ "github.com/rangertaha/scour/internal/exporter/parquet"
//
// # A table without a database
//
// Parquet on disk is a table nothing has to serve. DuckDB reads it where it
// lies, and so does anything else that speaks the format, so analytics is
// pointing a tool at a file rather than importing into something that then has
// to be kept in step. An archive that needs a running service to be readable is
// an archive with an expiry date, and that is the property this format is here
// for.
//
// It is columnar, so the columns nobody asked for are not read, and the values
// repeat, so they dictionary-encode to very little. A crawl's output is mostly
// repeated: the same site, the same section, the same handful of authors.
//
// # Every column is a string
//
// A record is names to values, all of them text, because that is what came off
// a page. The item's declared `type` says what a property is meant to be and
// the pipeline's validate step is what holds a record to it; rendering those
// types into the file's physical types here would make the export a second
// opinion about what a value is, and the two could differ.
//
// It would also have to decide what to do with a value that does not parse, and
// both answers are bad: write a null and the export quietly loses a number
// somebody can see in the CSV, or fail the write and a crawl that finished is
// reported as a crawl that did not. A reader casts what it wants when it wants
// it, DuckDB's `CAST(value AS DOUBLE)` costs nothing on a column scan, and the
// timestamps are written RFC 3339 so `CAST(fetched AS TIMESTAMP)` reads them.
//
// # The columns come from the shape
//
// The same rule as the CSV and SQLite exporters, and for the same reason: from
// the item the job declared rather than from whichever record arrived first, or
// two runs over one corpus would produce two files with different schemas and
// neither would be wrong. It is what makes an export with no records still a
// readable file: the schema is knowable when the data is not.
//
// A Parquet group orders its fields by name, so that is the order the columns
// are in. Order is not what the shape fixes, the set is, and every reader of
// this format names its columns rather than counting them.
//
// # The file has to be closed
//
// A Parquet file keeps its schema and the location of every column chunk in a
// footer, written last. A file whose writer never closed is not a short file,
// it is not a Parquet file at all, and no reader will open it. That is the
// trade for the format, the way the JSON array's closing bracket is the trade
// for that one, and jsonlines is what a run that may not survive should write
// beside this.
package parquet

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	parquetgo "github.com/parquet-go/parquet-go"
	"github.com/parquet-go/parquet-go/compress"

	"github.com/rangertaha/scour/internal/exporter"
	"github.com/rangertaha/scour/internal/record"
)

func init() {
	exporter.Register("parquet", open)
}

// RowGroup is how many rows are written before a row group is closed.
//
// A row group is the unit a reader skips: it carries the minimum and maximum of
// each of its columns, so a query that wants one site reads the groups that
// might hold it and none of the others. Groups too small make the footer large
// and the statistics useless, groups too large make the writer hold more of the
// crawl in memory than it needs to. Records are small, so ten thousand of them
// is a few megabytes of column chunks, which is where those two costs meet.
//
// This is not the SQLite exporter's batch, and it does not mean the same thing.
// Nothing here is readable until the footer is written, so a flushed row group
// bounds memory rather than loss.
const RowGroup = 10000

// Default is the compression a block gets when it does not name one.
//
// Snappy, because everything that reads Parquet reads Snappy: it is what the
// format's other writers have defaulted to for a decade, and an archive is
// worth less if a reader has to be new enough to open it. `zstd` is there for
// the archives where the disk matters more than that.
const Default = "snappy"

// Config is what a parquet exporter's block may set.
type Config struct {
	// Dir is where the file goes. Empty writes beside the working directory.
	Dir string `hcl:"dir,optional"`

	// File names it, for the jobs that want something other than the item's
	// name and the extension.
	File string `hcl:"file,optional"`

	// Compression is the codec for the column chunks, one of [Codecs].
	Compression string `hcl:"compression,optional"`
}

// codecs are the compressions this can write, by the name a block uses.
//
// Every one of them is in the format's own specification rather than an
// extension of it, so a file written with any of these is a file the ecosystem
// opens.
var codecs = map[string]compress.Codec{
	"uncompressed": &parquetgo.Uncompressed,
	"snappy":       &parquetgo.Snappy,
	"gzip":         &parquetgo.Gzip,
	"brotli":       &parquetgo.Brotli,
	"zstd":         &parquetgo.Zstd,
	"lz4raw":       &parquetgo.Lz4Raw,
}

// Codecs names the compressions this build can write, sorted, which is what an
// error tells somebody who asked for one that is not there.
func Codecs() []string {
	out := make([]string, 0, len(codecs))
	for name := range codecs {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// file writes one item's records.
type file struct {
	mu     sync.Mutex
	out    io.WriteCloser
	writer *parquetgo.Writer
	path   string

	// layout says what each column holds, so every format renders a record the
	// same way. See [exporter.Layout].
	layout *exporter.Layout

	// columns is the file's column order, taken from the schema rather than
	// assumed, because a row is written by column index.
	columns []string

	pending int
	closed  bool
}

func open(_ context.Context, cfg exporter.Config) (exporter.Exporter, error) {
	// Decoded even when the caller supplied the writer, so a typo in a block is
	// still refused with a line and a column when a run writes to stdout.
	var c Config
	if err := cfg.Body.Decode(&c); err != nil {
		return nil, err
	}

	// Both checked before anything is created, so a job that will never work
	// leaves no half-written file behind for somebody to find and wonder about.
	codec, err := codecNamed(c.Compression)
	if err != nil {
		return nil, err
	}
	layout, err := exporter.NewLayout("parquet", cfg.Shape, []string{"url", "fetched", "spec"})
	if err != nil {
		return nil, err
	}
	names := layout.Columns()
	schema := schemaOf(cfg.Item, names)

	out, path, err := destination(cfg, c)
	if err != nil {
		return nil, err
	}

	// NewWriter panics on a configuration it cannot use. The two options here
	// are a schema this built and a codec from the table above, so there is
	// nothing left for a document to make invalid.
	return &file{
		out:     out,
		writer:  parquetgo.NewWriter(out, schema, parquetgo.Compression(codec)),
		path:    path,
		layout:  layout,
		columns: leaves(schema),
	}, nil
}

// codecNamed resolves a block's compression.
//
// An unknown name is refused rather than quietly falling back to the default: a
// job that asked for zstd and got snappy produced a bigger archive than the
// person who wrote it will be expecting, and nothing would have said so.
func codecNamed(name string) (compress.Codec, error) {
	if name == "" {
		name = Default
	}
	codec, ok := codecs[strings.ToLower(name)]
	if !ok {
		return nil, fmt.Errorf("exporter/parquet: no compression named %q. This writes %s",
			name, strings.Join(Codecs(), ", "))
	}
	return codec, nil
}

// destination resolves where the file goes, and creates it.
//
// The caller may have supplied a writer, which is what a test and a run that
// writes to stdout both do. Nothing is created in that case, because a command
// piped into something else should not leave a file behind.
func destination(cfg exporter.Config, c Config) (io.WriteCloser, string, error) {
	if cfg.Out != nil {
		return cfg.Out, "(given)", nil
	}

	name := c.File
	if name == "" {
		name = cfg.Item + ".parquet"
	}
	path := filepath.Join(c.Dir, name)
	if err := cfg.Claim(path, cfg.Format+"."+cfg.Item); err != nil {
		return nil, "", err
	}

	// The directory is made first, so a missing parent reads as a missing
	// parent rather than as a file that could not be created.
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, "", fmt.Errorf("exporter/parquet: %w", err)
		}
	}

	created, err := os.Create(path)
	if err != nil {
		return nil, "", fmt.Errorf("exporter/parquet: %w", err)
	}
	return created, path, nil
}

// columns are the file's, from the shape the job declared.
//
// The same rule as the CSV header and the SQLite table, and the same reason:
// from the declaration rather than from whichever record arrived first. Nested
// properties are flattened the way records are.
//
// url, fetched and spec are here because they are what a query filters by:
// which page it came from, when that page was fetched, and which version of the
// shape read it.
// schemaOf builds the file's schema: one required string column per name.
//
// Required rather than optional, because the record model has no null. A
// property that found nothing is a name that is not there, and an export that
// invented the difference between "empty" and "absent" would be inventing
// information the crawl never had.
func schemaOf(item string, names []string) *parquetgo.Schema {
	group := make(parquetgo.Group, len(names))
	for _, name := range names {
		group[name] = parquetgo.String()
	}
	return parquetgo.NewSchema(item, group)
}

// leaves is the schema's columns in the file's own order.
//
// Asked of the schema rather than assumed from the names it was built with,
// because a row is written by column index and the group ordered those names
// itself. Every column here is a leaf directly under the root, so its path is
// its name.
func leaves(schema *parquetgo.Schema) []string {
	paths := schema.Columns()
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		out = append(out, strings.Join(path, "."))
	}
	return out
}

// Write adds records to the row group in progress, closing it when it is full.
func (f *file) Write(_ context.Context, records ...*record.Record) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.closed {
		return fmt.Errorf("exporter/parquet: %s: %w", f.path, exporter.ErrClosed)
	}
	if len(records) == 0 {
		return nil
	}

	rows := make([]parquetgo.Row, 0, len(records))
	for _, r := range records {
		rows = append(rows, f.row(r))
	}
	if _, err := f.writer.WriteRows(rows); err != nil {
		return fmt.Errorf("exporter/parquet: %s: %w", f.path, err)
	}

	f.pending += len(rows)
	if f.pending >= RowGroup {
		return f.flush()
	}
	return nil
}

// row is one record in the file's column order.
//
// Every value is at repetition and definition level zero, which is what a
// required column at the root of the schema means: one value per row, always
// there.
func (f *file) row(r *record.Record) parquetgo.Row {
	row := make(parquetgo.Row, 0, len(f.columns))
	for i, name := range f.columns {
		row = append(row, parquetgo.ByteArrayValue([]byte(f.layout.Value(r, name))).Level(0, 0, i))
	}
	return row
}

// flush closes the row group in progress. The caller holds the lock.
func (f *file) flush() error {
	if f.pending == 0 {
		return nil
	}
	f.pending = 0

	if err := f.writer.Flush(); err != nil {
		return fmt.Errorf("exporter/parquet: %s: %w", f.path, err)
	}
	return nil
}

// Close writes the footer and shuts the file.
//
// This is what makes the file readable at all: the schema and the offset of
// every column chunk live in the footer, and without it there is nothing for a
// reader to open. A failure still closes the file, because the alternative is a
// process that reports the failure and holds the handle anyway.
func (f *file) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.closed {
		return nil
	}
	f.closed = true

	// The writer's own Close flushes the rows still buffered, so the last row
	// group does not need closing here first.
	err := f.writer.Close()
	if err != nil {
		err = fmt.Errorf("exporter/parquet: %s: %w", f.path, err)
	}
	if closeErr := f.out.Close(); err == nil && closeErr != nil {
		err = fmt.Errorf("exporter/parquet: %s: %w", f.path, closeErr)
	}
	return err
}

// Path is where a block's file lives, which is what somebody asks before they
// go looking for it.
func Path(dir, file, item string) string {
	if file == "" {
		file = item + ".parquet"
	}
	return filepath.Join(dir, file)
}
