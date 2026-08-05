// SPDX-License-Identifier: GPL-3.0-or-later

package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sync"
)

// A file is one database, shared by every exporter writing into it.
//
// # Why this is shared and not one handle each
//
// Because one file for a job's items rather than one per item is what this
// format is for, so `exporter "sqlite" "article"` and `exporter "sqlite"
// "price"` in one job is the ordinary configuration, not an unusual one. Each
// used to open a handle of its own, and each handle holds a write transaction
// open across Write calls, because a run hands over whatever it has ready and
// one record is not a transaction's worth of work.
//
// Two write transactions on one SQLite file do not both proceed. The article
// writer left its transaction open, the price writer waited out busy_timeout
// and failed with SQLITE_BUSY, the run reported failure, and the price table
// was left empty: a five second stall per flush followed by a lost export.
//
// Sharing the handle alone would only change the symptom, because one
// connection with a transaction held open blocks the next Begin just as surely.
// So the batch belongs to the file rather than to the table. Every table on one
// file appends to one transaction and it commits when it is full or when the
// last table closes, which is also the more honest unit: the file is what a
// reader opens, and a batch that spanned only one of its tables would commit a
// half-consistent view of one flush.
type file struct {
	mu      sync.Mutex
	db      *sql.DB
	path    string
	tx      *sql.Tx
	pending int
	refs    int
}

var (
	filesMu sync.Mutex
	files   = map[string]*file{}
)

// acquire returns the shared handle for a path, opening it the first time.
//
// Keyed by the absolute path, so two exporters that name one database in two
// ways still get one handle. A path that cannot be made absolute is used as
// given, which is what the old behaviour was.
func acquire(path string) (*file, error) {
	key, err := filepath.Abs(path)
	if err != nil {
		key = path
	}

	filesMu.Lock()
	defer filesMu.Unlock()

	if f, ok := files[key]; ok {
		f.refs++
		return f, nil
	}

	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, fmt.Errorf("exporter/sqlite: %s: %w", path, err)
	}

	// One connection, following the frontier. Every statement here is a write,
	// so pooling buys nothing and costs the whole class of "database is locked"
	// failures that concurrent writers produce. WAL is what still lets somebody
	// query the file while the crawl is running.
	db.SetMaxOpenConns(1)

	f := &file{db: db, path: path, refs: 1}
	files[key] = f
	return f, nil
}

// exec runs one statement inside the file's batch, starting one if there is
// none and committing it when it is full.
func (f *file) exec(ctx context.Context, statement string, args ...any) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.tx == nil {
		tx, err := f.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("exporter/sqlite: %s: %w", f.path, err)
		}
		f.tx, f.pending = tx, 0
	}

	if _, err := f.tx.ExecContext(ctx, statement, args...); err != nil {
		return fmt.Errorf("exporter/sqlite: %s: %w", f.path, err)
	}

	f.pending++
	if f.pending >= Batch {
		return f.commitLocked()
	}
	return nil
}

// schema runs a CREATE outside the batch, because a table two exporters are
// about to write must exist before either of them starts one.
func (f *file) schema(ctx context.Context, statement string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if err := f.commitLocked(); err != nil {
		return err
	}
	if _, err := f.db.ExecContext(ctx, statement); err != nil {
		return fmt.Errorf("exporter/sqlite: %s: %w", f.path, err)
	}
	return nil
}

// commit ends the batch in progress.
func (f *file) commit() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.commitLocked()
}

func (f *file) commitLocked() error {
	if f.tx == nil {
		return nil
	}

	tx := f.tx
	f.tx, f.pending = nil, 0

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("exporter/sqlite: %s: %w", f.path, err)
	}
	return nil
}

// release gives back one exporter's share, committing and closing when the last
// one lets go.
//
// Reference counted rather than closed by whoever finishes first, because the
// other tables on this file are still writing: closing under them is the same
// lost export by a different route.
func (f *file) release() error {
	filesMu.Lock()
	defer filesMu.Unlock()

	f.refs--
	if f.refs > 0 {
		// Commit what is pending anyway, so an exporter that has closed has
		// had its records written even though the file stays open.
		return f.commit()
	}

	key, err := filepath.Abs(f.path)
	if err != nil {
		key = f.path
	}
	delete(files, key)

	closeErr := f.commit()
	if err := f.db.Close(); err != nil && closeErr == nil {
		closeErr = fmt.Errorf("exporter/sqlite: %s: %w", f.path, err)
	}
	return closeErr
}
