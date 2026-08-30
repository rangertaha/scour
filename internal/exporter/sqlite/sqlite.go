// SPDX-License-Identifier: GPL-3.0-or-later

// Package sqlite writes records to a SQLite database, one table per item.
//
// Import it for its side effect to make the format available:
//
//	import _ "github.com/rangertaha/scour/internal/exporter/sqlite"
//
// # A file somebody can query
//
// The other file formats answer "give me everything" and leave the querying to
// whatever loads them. This one is for the crawl whose output gets asked
// questions: a single file, no server, and every tool that speaks SQL can open
// it. It is still a copy rather than the record of truth, so it can be deleted
// and produced again.
//
// # One table per item, with the shape's columns
//
// A table needs columns and records do not have them, which is the same problem
// the CSV exporter has and it is answered the same way: the columns come from
// the item the job declared rather than from whichever record arrived first, so
// two runs over one corpus produce the same table and a record missing a value
// leaves a hole rather than shifting the schema.
//
// # Following the frontier
//
// The DSN pragmas, the single connection and the immediate transaction lock are
// taken from internal/frontier/sqlite rather than derived again here. They were
// argued once, they are the same argument (one writer, a reader that wants to
// watch progress, and no interest in the "database is locked" class of
// failure), and two copies of that reasoning would be two things to keep in
// step.
package sqlite

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	_ "modernc.org/sqlite" // the pure-Go driver, so this cross-compiles

	"github.com/rangertaha/scour/internal/exporter"
	"github.com/rangertaha/scour/internal/record"
)

func init() {
	exporter.Register("sqlite", open)
}

// File is what the database is called when a block does not say.
//
// One file for a job's items rather than one per item, because the question
// this format exists to answer is usually about more than one of them and a
// join across two files is not a join.
const File = "records.db"

// Batch is how many rows are written before the transaction is committed.
//
// A transaction per record would fsync per record and make a long crawl crawl.
// One transaction for the whole run would be faster still and is what this
// deliberately does not do: nothing an open transaction holds is visible to
// anybody reading the file, and a run that dies four hours in would have
// written nothing at all. Committing in batches bounds both the loss and how
// stale the reader's view is.
const Batch = 500

// Config is what a sqlite exporter's block may set.
type Config struct {
	// Dir is where the database goes. Empty writes beside the working
	// directory.
	Dir string `hcl:"dir,optional"`

	// File names it, for the jobs that want something other than [File].
	File string `hcl:"file,optional"`
}

// table writes one item's records.
type table struct {
	mu sync.Mutex

	// file is the database, shared with every other exporter writing into it,
	// and it owns the batch. See [file].
	file *file

	item string
	path string
	// layout says what each column holds, so every format renders a record the
	// same way. See [exporter.Layout].
	layout  *exporter.Layout
	columns []string
	insert  string
	closed  bool
}

func open(ctx context.Context, cfg exporter.Config) (exporter.Exporter, error) {
	var c Config
	if err := cfg.Body.Decode(&c); err != nil {
		return nil, err
	}

	// Checked before anything is created, so a job that will never work leaves
	// no half-made database behind for somebody to find later and wonder about.
	layout, err := exporter.NewLayout("sqlite", cfg.Shape, []string{"url", "fetched", "spec"}, "key")
	if err != nil {
		return nil, err
	}
	cols := layout.Columns()

	name := c.File
	if name == "" {
		name = File
	}
	path := filepath.Join(c.Dir, name)

	// The directory is made before the driver is asked for a file inside it, so
	// a missing parent reads as a missing parent rather than as SQLite failing
	// to open something.
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, fmt.Errorf("exporter/sqlite: %w", err)
		}
	}

	f, err := acquire(path)
	if err != nil {
		return nil, err
	}

	t := &table{
		file:    f,
		item:    cfg.Item,
		path:    path,
		layout:  layout,
		columns: cols,
	}
	t.insert = insert(t.item, t.columns)

	if err := t.schema(ctx); err != nil {
		_ = f.release()
		return nil, err
	}
	return t, nil
}

// dsn is the frontier's, for the reasons given there.
//
// The one difference is that there is no in-memory case: an export that did not
// survive the process is not an export.
func dsn(path string) string {
	return "file:" + path +
		"?_pragma=journal_mode(wal)" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=synchronous(normal)" +
		"&_txlock=immediate"
}

// columns are the table's, from the shape the job declared.
//
// The same rule as the CSV exporter's header, and for the same reason: from the
// declaration rather than from whichever record arrived first, or two runs over
// one corpus would produce two different tables and neither would be wrong.
// Nested properties are flattened the way records are.
//
// url, fetched and spec come first because they are what a query filters by:
// which page it came from, when that page was fetched, and which version of the
// shape read it.
// schema creates the table if it is not already there.
//
// IF NOT EXISTS rather than a drop, because re-running a crawl into an existing
// database is the case this format is shaped around, and dropping would make
// every re-run a fresh start whether or not that was wanted.
func (t *table) schema(ctx context.Context) error {
	var b strings.Builder
	fmt.Fprintf(&b, "CREATE TABLE IF NOT EXISTS %s (\n\tkey TEXT PRIMARY KEY", ident(t.item))
	for _, name := range t.columns {
		fmt.Fprintf(&b, ",\n\t%s TEXT NOT NULL DEFAULT ''", ident(name))
	}
	b.WriteString("\n)")

	if err := t.file.schema(ctx, b.String()); err != nil {
		return err
	}

	// And any column the table has not got yet.
	//
	// CREATE TABLE IF NOT EXISTS is a no-op against a table that is already
	// there, whatever shape it is in, and the insert names every column the job
	// declares now. So a job that gained a property could no longer write to
	// its own database at all: SQLite answered "table article has no column
	// named author" on the first batch and the crawl exported nothing for that
	// item - not a degraded export, a total one. Re-running a crawl into an
	// existing database is the case this format is shaped around, and a job
	// gaining a property is the ordinary way that database gets out of date.
	//
	// Added rather than recreated, because the rows already there are the point
	// of keeping the file. An old row simply has the empty default in the new
	// column, which is what "not extracted" already looks like everywhere else
	// in this table.
	have, err := t.file.existing(ctx, t.item)
	if err != nil {
		return err
	}
	if len(have) == 0 {
		// Freshly created above, so it is exactly what was asked for.
		return nil
	}

	for _, name := range t.columns {
		if have[name] {
			continue
		}
		add := fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s TEXT NOT NULL DEFAULT ''",
			ident(t.item), ident(name))
		if err := t.file.schema(ctx, add); err != nil {
			return err
		}
	}
	return nil
}

// insert is the upsert, built once.
//
// Only fetched and spec are updated on conflict. Everything else is inside the
// key by construction, so a row that matched an existing key cannot differ in
// any of it, and writing the columns back would be writing them the values they
// already hold.
func insert(item string, cols []string) string {
	names := make([]string, 0, len(cols)+1)
	names = append(names, "key")
	for _, name := range cols {
		names = append(names, ident(name))
	}

	holders := strings.TrimSuffix(strings.Repeat("?, ", len(names)), ", ")

	return fmt.Sprintf(
		"INSERT INTO %s (%s) VALUES (%s)\n"+
			"ON CONFLICT(key) DO UPDATE SET fetched = excluded.fetched, spec = excluded.spec",
		ident(item), strings.Join(names, ", "), holders)
}

// ident quotes a name for SQL.
//
// Table and column names cannot be parameters, so they are interpolated, and
// interpolating anything a document supplied without quoting it is an injection
// point. Doubling the quote is SQLite's own escape, and quoting is what lets a
// flattened column keep the dotted name records use rather than having one name
// in the file and another in the table.
func ident(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// Write adds records to the batch in progress, committing when it is full.
func (t *table) Write(ctx context.Context, records ...*record.Record) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closed {
		return fmt.Errorf("exporter/sqlite: %s: already closed", t.path)
	}

	for _, r := range records {
		values := t.row(r)
		args := make([]any, 0, len(values)+1)
		args = append(args, key(t.item, r.URL, t.columns, values))
		for _, v := range values {
			args = append(args, v)
		}

		if err := t.file.exec(ctx, t.insert, args...); err != nil {
			return err
		}
	}
	return nil
}

// row is one record in column order.
//
// A value the record does not have becomes empty rather than NULL. The record
// model has no null: a property that found nothing is a name that is not there,
// and inventing a distinction in the export that the records do not make would
// be inventing information.
func (t *table) row(r *record.Record) []string {
	out := make([]string, 0, len(t.columns))
	for _, name := range t.columns {
		out = append(out, t.layout.Value(r, name))
	}
	return out
}

// key is a row's identity: the item, the page it came from, and every value the
// table stores for it.
//
// Re-running a crawl must not double the table, and a record carries no
// identifier of its own. The URL alone will not do: one page can yield several
// of the same item, and a table keyed by URL would keep the last price on the
// page and lose the rest. What is stable across runs is the content, because
// the same page read under the same shape produces the same values, so their
// digest is the same key today and next month while two prices on one page are
// two keys.
//
// The digest covers exactly what is stored and nothing else, so two rows that
// read as identical cannot have different keys. Fetched and spec are outside
// it, which is what leaves the upsert something to do: a re-crawl that finds
// the same row again moves its fetch time and its shape version forward rather
// than inserting a second copy.
//
// The cost is honest and worth stating: a page whose value genuinely changed is
// a new key and so a new row, keeping the old one beside it. For a series that
// is what was wanted, and for a correction the previous row is what the fetch
// times tell apart.
func key(item, url string, cols, values []string) string {
	parts := make([]string, 0, len(cols)*2+2)
	parts = append(parts, item, url)
	for i, name := range cols {
		if name == "fetched" || name == "spec" {
			continue
		}
		parts = append(parts, name, values[i])
	}

	// Separated by a byte that cannot appear in extracted text, so that two
	// different rows cannot be joined into the same string.
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:16])
}

// commit ends the batch in progress. The caller holds the lock.

// Close commits what is left and shuts the database.
//
// A failed commit still closes the database, because the alternative is a
// process that reports the failure and holds the file open anyway.
func (t *table) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closed {
		return nil
	}
	t.closed = true

	return t.file.release()
}

// Path is where a block's database lives, which is what somebody asks before
// they go looking for it.
func Path(dir, file string) string {
	if file == "" {
		file = File
	}
	return filepath.Join(dir, file)
}
