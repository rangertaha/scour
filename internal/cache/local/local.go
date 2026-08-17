// SPDX-License-Identifier: GPL-3.0-or-later

// Package local stores page bodies in a directory.
//
// It is the default backend and the only one with no dependencies, which is
// what lets scour run on a laptop with nothing installed and nothing
// configured. Import it for its side effect to make "local" available:
//
//	import _ "github.com/rangertaha/scour/internal/cache/local"
package local

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"iter"
	"os"
	"path/filepath"

	"github.com/rangertaha/scour/internal/cache"
)

// Name is what this backend registers as.
const Name = "local"

// Permissions. A cache is not secret, but it is not everybody's either, and a
// crawl of an authenticated site puts real pages in here.
const (
	dirPerm  fs.FileMode = 0o750
	filePerm fs.FileMode = 0o640
)

// tmpDir holds part-written bodies. It is a sibling of the shards rather than
// the system temp directory, because a rename is only atomic within one
// filesystem and /tmp is routinely on another.
const tmpDir = "tmp"

func init() {
	cache.Register(Name, func(_ context.Context, cfg cache.Config) (cache.Store, error) {
		return Open(cfg.Dir)
	})
}

// Store is a directory of page bodies.
//
// It holds no state beyond its root, so it is safe for concurrent use by
// definition: every operation is one filesystem call on one path.
type Store struct{ root string }

// Open returns a store rooted at dir, creating it if it is not there.
func Open(dir string) (*Store, error) {
	if dir == "" {
		return nil, errors.New("cache/local: no directory configured")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("cache/local: resolve %q: %w", dir, err)
	}
	if err := os.MkdirAll(abs, dirPerm); err != nil {
		return nil, fmt.Errorf("cache/local: create %q: %w", abs, err)
	}
	return &Store{root: abs}, nil
}

// Root is the directory this store writes under.
func (s *Store) Root() string { return s.root }

// path is where a key lives.
//
// Bodies are sharded two levels deep on the first four characters of the key,
// so a million-page crawl leaves a few hundred entries per directory rather
// than a million in one. Filesystems cope with wide directories far worse than
// with deep ones, and some tools refuse them outright.
func (s *Store) path(key string) (string, error) {
	if err := cache.CheckKey(key); err != nil {
		return "", err
	}
	// Short keys are legal, so pad the shard rather than slicing out of range.
	shard := key + "0000"
	return filepath.Join(s.root, shard[0:2], shard[2:4], key), nil
}

// Put implements [cache.Store].
//
// The body is written to a temporary file and renamed into place, so a reader
// sees either the previous body or the whole new one. Writing in place would
// leave a truncated page behind a crash or a full disk, and a truncated page
// is worse than a missing one: it parses, it extracts, and it quietly reports
// less than the page held.
func (s *Store) Put(ctx context.Context, key string, r io.Reader) error {
	final, err := s.path(key)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(final), dirPerm); err != nil {
		return fmt.Errorf("cache/local: create shard for %q: %w", key, err)
	}

	staging := filepath.Join(s.root, tmpDir)
	if err := os.MkdirAll(staging, dirPerm); err != nil {
		return fmt.Errorf("cache/local: create staging directory: %w", err)
	}
	tmp, err := os.CreateTemp(staging, "put-*")
	if err != nil {
		return fmt.Errorf("cache/local: create temporary file for %q: %w", key, err)
	}
	tmpName := tmp.Name()

	// From here every failure removes the temporary file, so a failed write
	// does not accumulate.
	if _, err := io.Copy(tmp, r); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("cache/local: write %q: %w", key, err)
	}
	if err := tmp.Chmod(filePerm); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("cache/local: set permissions on %q: %w", key, err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("cache/local: close %q: %w", key, err)
	}
	if err := os.Rename(tmpName, final); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("cache/local: commit %q: %w", key, err)
	}
	return nil
}

// Get implements [cache.Store].
func (s *Store) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	path, err := s.path(key)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", cache.ErrNotFound, key)
	}
	if err != nil {
		return nil, fmt.Errorf("cache/local: open %q: %w", key, err)
	}
	return f, nil
}

// Has implements [cache.Store].
func (s *Store) Has(ctx context.Context, key string) (bool, error) {
	path, err := s.path(key)
	if err != nil {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	switch _, err := os.Stat(path); {
	case err == nil:
		return true, nil
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	default:
		return false, fmt.Errorf("cache/local: stat %q: %w", key, err)
	}
}

// Delete implements [cache.Store].
func (s *Store) Delete(ctx context.Context, key string) error {
	path, err := s.path(key)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("cache/local: delete %q: %w", key, err)
	}
	return nil
}

// Keys implements [cache.Store].
//
// The staging directory is skipped, so a body being written is never handed
// out as a key that exists.
func (s *Store) Keys(ctx context.Context) iter.Seq2[string, error] {
	return func(yield func(string, error) bool) {
		staging := filepath.Join(s.root, tmpDir)

		err := filepath.WalkDir(s.root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if path == staging {
					return fs.SkipDir
				}
				return nil
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if !yield(d.Name(), nil) {
				return fs.SkipAll
			}
			return nil
		})
		if err != nil && !errors.Is(err, fs.SkipAll) {
			yield("", fmt.Errorf("cache/local: walk %q: %w", s.root, err))
		}
	}
}

// Close implements [cache.Store]. A directory holds nothing open.
func (s *Store) Close() error { return nil }
