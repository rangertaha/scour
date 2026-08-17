// SPDX-License-Identifier: GPL-3.0-or-later

// Package files writes records to a file: JSON, JSON Lines or CSV.
//
// Import it for its side effect to make those formats available:
//
//	import _ "github.com/rangertaha/scour/internal/exporter/files"
//
// # Three formats because they answer three questions
//
// **jsonlines** is what a crawl that is still running should write: one record
// per line, appendable, and readable while it is being written. A run that dies
// halfway leaves a file with every record it had, which is the property that
// matters most when a crawl takes hours.
//
// **json** is one array, for whatever is going to load the lot at once. It has
// to be closed to be valid, so a run that dies halfway leaves a file that needs
// repairing, which is the trade for being loadable by everything.
//
// **csv** is for a spreadsheet, and it is the awkward one: a CSV has columns
// and records do not. The columns come from the shape the job declared rather
// than from whichever record happened to arrive first, or two runs over one
// corpus would produce two different headers.
package files

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/rangertaha/scour/internal/exporter"
	"github.com/rangertaha/scour/internal/record"
)

func init() {
	exporter.Register("json", newJSON)
	exporter.Register("jsonlines", newLines)
	exporter.Register("csv", newCSV)
}

// Config is what a file exporter's block may set.
type Config struct {
	// Dir is where the file goes. Empty writes beside the working directory.
	Dir string `hcl:"dir,optional"`

	// File names it, for the jobs that want something other than the item's
	// name and the format's extension.
	File string `hcl:"file,optional"`
}

// open resolves where a format writes, and creates it.
//
// The caller may have supplied a writer, which is what a test and a run that
// writes to stdout both do. Nothing is created in that case, because a command
// piped into something else should not leave a file behind.
func open(cfg exporter.Config, extension string) (io.WriteCloser, string, error) {
	if cfg.Out != nil {
		return cfg.Out, "(given)", nil
	}

	var c Config
	if err := cfg.Body.Decode(&c); err != nil {
		return nil, "", err
	}

	name := c.File
	if name == "" {
		name = cfg.Item + "." + extension
	}
	path := filepath.Join(c.Dir, name)
	if err := cfg.Claim(path, cfg.Format+"."+cfg.Item); err != nil {
		return nil, "", err
	}

	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, "", err
		}
	}

	file, err := os.Create(path)
	if err != nil {
		return nil, "", err
	}
	return file, path, nil
}

// lines writes one record per line.
type lines struct {
	mu     sync.Mutex
	out    io.WriteCloser
	enc    *json.Encoder
	path   string
	closed bool
}

func newLines(_ context.Context, cfg exporter.Config) (exporter.Exporter, error) {
	out, path, err := open(cfg, "jsonl")
	if err != nil {
		return nil, err
	}
	return &lines{out: out, enc: json.NewEncoder(out), path: path}, nil
}

// A write after Close is refused rather than swallowed. The file is finished,
// so the record cannot land in it, and returning nil would tell a run that it
// had: an export that looks complete and is short is the worst outcome
// available here. All three formats in this file were missing the guard, which
// the shared contract suite found the first time it was run against them.
func (l *lines) Write(_ context.Context, records ...*record.Record) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.closed {
		return fmt.Errorf("%s: already closed", l.path)
	}

	for _, r := range records {
		if err := l.enc.Encode(r); err != nil {
			return fmt.Errorf("%s: %w", l.path, err)
		}
	}
	return nil
}

func (l *lines) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.closed {
		return nil
	}
	l.closed = true
	return l.out.Close()
}

// array writes one JSON array.
type array struct {
	mu      sync.Mutex
	out     io.WriteCloser
	path    string
	written bool
	closed  bool
}

func newJSON(_ context.Context, cfg exporter.Config) (exporter.Exporter, error) {
	out, path, err := open(cfg, "json")
	if err != nil {
		return nil, err
	}
	if _, err := io.WriteString(out, "[\n"); err != nil {
		_ = out.Close()
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &array{out: out, path: path}, nil
}

func (a *array) Write(_ context.Context, records ...*record.Record) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.closed {
		return fmt.Errorf("%s: already closed", a.path)
	}

	for _, r := range records {
		encoded, err := json.Marshal(r)
		if err != nil {
			return fmt.Errorf("%s: %w", a.path, err)
		}
		separator := ""
		if a.written {
			separator = ",\n"
		}
		if _, err := io.WriteString(a.out, separator+"  "+string(encoded)); err != nil {
			return fmt.Errorf("%s: %w", a.path, err)
		}
		a.written = true
	}
	return nil
}

// Close finishes the array. Without this the file is not JSON, which is the
// price of the format and the reason jsonlines exists beside it.
func (a *array) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.closed {
		return nil
	}
	a.closed = true

	if _, err := io.WriteString(a.out, "\n]\n"); err != nil {
		_ = a.out.Close()
		return fmt.Errorf("%s: %w", a.path, err)
	}
	return a.out.Close()
}

// table writes CSV.
type table struct {
	mu      sync.Mutex
	out     io.WriteCloser
	writer  *csv.Writer
	path    string
	layout  *exporter.Layout
	columns []string
	closed  bool
}

func newCSV(_ context.Context, cfg exporter.Config) (exporter.Exporter, error) {
	// The layout is built before the file, so a shape this format cannot write
	// leaves no empty file behind for somebody to find and wonder about. It is
	// also where the collision check lives: this exporter did not have one, so
	// a property named `url` produced a header with two `url` columns, both the
	// page address, and the extracted value never reached the file.
	layout, err := exporter.NewLayout("csv", cfg.Shape, []string{"url", "fetched"})
	if err != nil {
		return nil, err
	}

	out, path, err := open(cfg, "csv")
	if err != nil {
		return nil, err
	}

	t := &table{
		out:     out,
		writer:  csv.NewWriter(out),
		path:    path,
		layout:  layout,
		columns: layout.Columns(),
	}

	// The header goes in at construction, not on the first row. A job whose
	// pipeline dropped everything left a zero-byte file, where the header is
	// the one thing still knowable: it comes from the shape, and the shape is
	// what makes "no rows" different from "wrong file". The json exporter
	// already writes its opening bracket here for the same reason.
	if err := t.writer.Write(t.columns); err != nil {
		_ = out.Close()
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	t.writer.Flush()
	if err := t.writer.Error(); err != nil {
		_ = out.Close()
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return t, nil
}

func (t *table) Write(_ context.Context, records ...*record.Record) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closed {
		return fmt.Errorf("%s: already closed", t.path)
	}

	for _, r := range records {
		row := make([]string, 0, len(t.columns))
		for _, name := range t.columns {
			// Newlines out, because a CSV cell holding one is legal and is read
			// back as two rows by half the things that open a CSV.
			row = append(row, strings.ReplaceAll(t.layout.Value(r, name), "\n", " "))
		}
		if err := t.writer.Write(row); err != nil {
			return fmt.Errorf("%s: %w", t.path, err)
		}
	}
	t.writer.Flush()
	return t.writer.Error()
}

func (t *table) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closed {
		return nil
	}
	t.closed = true

	t.writer.Flush()
	if err := t.writer.Error(); err != nil {
		_ = t.out.Close()
		return fmt.Errorf("%s: %w", t.path, err)
	}
	return t.out.Close()
}
