// SPDX-License-Identifier: GPL-3.0-or-later

// Package store is scour's persistence layer. It owns the gorm models, the
// migrations, and every query; nothing else in the program talks to the
// database.
package store

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// ErrNotFound is returned when a named entity, target or property does not
// exist. Callers branch on it with errors.Is.
var ErrNotFound = errors.New("not found")

// ErrExists is returned when creating something that is already there.
var ErrExists = errors.New("already exists")

// Store is a handle on the database.
type Store struct {
	db *gorm.DB
}

// Open connects to the database at dsn and applies the migrations. The parent
// directory is created if it does not exist, so a first run needs no setup.
//
// The sqlite driver is the pure-Go one, so scour cross-compiles and installs
// without cgo.
func Open(dsn string) (*Store, error) {
	if dir := filepath.Dir(dsn); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, fmt.Errorf("create database directory %s: %w", dir, err)
		}
	}

	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger:                 logger.Default.LogMode(logger.Silent),
		SkipDefaultTransaction: true,
	})
	if err != nil {
		return nil, fmt.Errorf("open database %s: %w", dsn, err)
	}

	// Foreign keys are off by default in sqlite, which would let the cascade
	// constraints in the models silently do nothing.
	if err := db.Exec("PRAGMA foreign_keys = ON").Error; err != nil {
		return nil, fmt.Errorf("enable foreign keys: %w", err)
	}
	// A crawl writes from several goroutines; WAL is what makes that bearable.
	if err := db.Exec("PRAGMA journal_mode = WAL").Error; err != nil {
		return nil, fmt.Errorf("enable write-ahead logging: %w", err)
	}
	// Without this a writer that finds the lock held fails immediately rather
	// than waiting, so reading the status of a running crawl is a coin toss.
	// WAL lets readers through regardless; this is for the writes.
	if err := db.Exec("PRAGMA busy_timeout = 10000").Error; err != nil {
		return nil, fmt.Errorf("set busy timeout: %w", err)
	}

	if err := limitPool(db); err != nil {
		return nil, err
	}

	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	slog.Debug("store opened", "dsn", dsn)
	return s, nil
}

// limitPool pins the store to a single connection.
//
// SQLite takes one writer at a time regardless, so a pool buys no write
// throughput. It costs correctness: with several connections, a row written on
// one is not reliably visible to a read that starts on another, which showed
// up as the crawl queue reporting itself empty immediately after a URL was
// pushed onto it. Database work here is microseconds against network fetches
// of milliseconds, so serialising it is not a bottleneck.
func limitPool(db *gorm.DB) error {
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("configure connection pool: %w", err)
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	sqlDB.SetConnMaxLifetime(0)
	return nil
}

// OpenMemory returns an isolated in-memory store, for tests.
func OpenMemory() (*Store, error) {
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared&_pragma=foreign_keys(1)"), &gorm.Config{
		Logger:                 logger.Default.LogMode(logger.Silent),
		SkipDefaultTransaction: true,
	})
	if err != nil {
		return nil, fmt.Errorf("open in-memory database: %w", err)
	}
	if err := limitPool(db); err != nil {
		return nil, err
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	// AutoMigrate adds a column but will not rebuild a unique index whose
	// definition changed. A database created before `domain` joined
	// (entity_id, name) therefore keeps the old two-column index, and every
	// property upsert fails with "ON CONFLICT clause does not match any
	// PRIMARY KEY or UNIQUE constraint" because the conflict target no longer
	// exists. Dropping the stale one first lets AutoMigrate rebuild it.
	if err := s.dropStaleIndex("properties", "idx_prop_entity_name", "domain"); err != nil {
		return err
	}
	// Before AutoMigrate, because it repairs rows the new constraints would
	// otherwise trip over.
	if err := s.settleDomains(); err != nil {
		return err
	}
	if err := s.db.AutoMigrate(tables()...); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

// settleDomains repairs property rows written before Domain existed.
//
// A column added to an existing table is NULL for every row already in it,
// while every new write stores the empty string for "no domain". In sqlite
// those are different values, and the damage is in two directions: the upsert
// looks for domain = ” and misses the NULL row, inserting a duplicate that
// makes the schema ambiguous, and PropertiesFor asks for domain = ” and cannot
// see the original at all. On this machine that turned into
// `wom: duplicate prop "link"` and a silently empty schema.
//
// Duplicates are merged before the backfill, because collapsing NULL to ” is
// exactly what makes them collide.
func (s *Store) settleDomains() error {
	// A database that has never had the column, or has never had the table,
	// has nothing to repair.
	if !s.db.Migrator().HasTable(&Property{}) || !s.db.Migrator().HasColumn(&Property{}, "domain") {
		return nil
	}

	// Whether there is anything to repair is a read, and almost always the
	// answer is no. Opening a write transaction to find that out made every
	// command take the write lock, so `scour status` during a crawl failed with
	// SQLITE_BUSY: a repair that runs once was costing every open afterwards.
	var pending int64
	err := s.db.Raw(`
		SELECT COUNT(*) FROM properties WHERE domain IS NULL
		   OR id IN (
		      SELECT MAX(id) FROM properties
		      GROUP BY entity_id, name, COALESCE(domain, '')
		      HAVING COUNT(*) > 1)`).Scan(&pending).Error
	if err != nil {
		return fmt.Errorf("check for property domains to settle: %w", err)
	}
	if pending == 0 {
		return nil
	}

	return s.db.Transaction(func(tx *gorm.DB) error {
		type dup struct {
			Keeper uint
			Extra  uint
			Name   string
		}
		var dups []dup
		err := tx.Raw(`
			SELECT MIN(id) AS keeper, MAX(id) AS extra, name
			FROM properties
			GROUP BY entity_id, name, COALESCE(domain, '')
			HAVING COUNT(*) > 1`).Scan(&dups).Error
		if err != nil {
			return fmt.Errorf("find duplicate properties: %w", err)
		}

		for _, d := range dups {
			// Aliases are the expensive part, so they move rather than die.
			// OR IGNORE because the survivor may already carry the same word.
			if err := tx.Exec(
				"UPDATE OR IGNORE property_aliases SET property_id = ? WHERE property_id = ?",
				d.Keeper, d.Extra).Error; err != nil {
				return fmt.Errorf("move aliases of %q: %w", d.Name, err)
			}
			if err := tx.Exec("DELETE FROM property_aliases WHERE property_id = ?", d.Extra).Error; err != nil {
				return fmt.Errorf("clear aliases of %q: %w", d.Name, err)
			}
			if err := tx.Exec("DELETE FROM properties WHERE id = ?", d.Extra).Error; err != nil {
				return fmt.Errorf("drop duplicate property %q: %w", d.Name, err)
			}
			slog.Info("merged a property duplicated by the domain column", "property", d.Name)
		}

		if err := tx.Exec("UPDATE properties SET domain = '' WHERE domain IS NULL").Error; err != nil {
			return fmt.Errorf("backfill property domains: %w", err)
		}
		return nil
	})
}

// dropStaleIndex removes an index whose definition predates a column it should
// now cover. It reads sqlite's own record of the index rather than guessing,
// so an index that is already correct is left alone.
func (s *Store) dropStaleIndex(table, index, mustCover string) error {
	var ddl string
	err := s.db.Raw(
		"SELECT sql FROM sqlite_master WHERE type = 'index' AND tbl_name = ? AND name = ?",
		table, index,
	).Scan(&ddl).Error
	if err != nil {
		return fmt.Errorf("read index %s: %w", index, err)
	}
	if ddl == "" || strings.Contains(ddl, mustCover) {
		return nil
	}
	if err := s.db.Exec("DROP INDEX IF EXISTS " + index).Error; err != nil {
		return fmt.Errorf("drop stale index %s: %w", index, err)
	}
	slog.Info("rebuilt index for a changed definition", "index", index, "now covers", mustCover)
	return nil
}

// Close releases the underlying connection.
func (s *Store) Close() error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return fmt.Errorf("close: %w", err)
	}
	if err := sqlDB.Close(); err != nil {
		return fmt.Errorf("close: %w", err)
	}
	return nil
}

// DB exposes the gorm handle for the packages that build on the store. It is
// deliberately not used outside internal/store's own siblings.
func (s *Store) DB() *gorm.DB { return s.db }
