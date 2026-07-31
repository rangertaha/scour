// SPDX-License-Identifier: GPL-3.0-or-later

// Package cache stores fetched page bodies on disk so a re-crawl, and every
// later pass that needs the bytes again, does not re-download them.
package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// Cache is a content-addressed store of response bodies, laid out by host so a
// single site's pages can be found, counted or dropped on their own.
type Cache struct {
	root string
}

// New returns a cache rooted at dir. The directory is created on first write
// rather than here, so opening a cache never fails.
func New(dir string) *Cache { return &Cache{root: dir} }

// Root returns the directory the cache lives in.
func (c *Cache) Root() string { return c.root }

// Key derives the cache key for a URL. It is the digest of the whole URL, so
// two pages that differ only in query string are stored apart.
func Key(rawURL string) string {
	sum := sha256.Sum256([]byte(rawURL))
	return hex.EncodeToString(sum[:])
}

// path returns the file a URL's body is stored at: <root>/<host>/<ab>/<key>,
// with the two-character shard keeping directories from growing unbounded on
// large sites.
func (c *Cache) path(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("cache path for %q: %w", rawURL, err)
	}
	host := u.Hostname()
	if host == "" {
		host = "unknown"
	}
	// Hostnames come from the network, so keep them to what is safe in a path.
	host = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '-':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		default:
			return '_'
		}
	}, host)

	key := Key(rawURL)
	return filepath.Join(c.root, host, key[:2], key), nil
}

// Put stores a body. Writes go to a temporary file first and are renamed into
// place, so an interrupted crawl cannot leave a half-written page behind for
// the next pass to parse.
func (c *Cache) Put(rawURL string, body []byte) (string, error) {
	dst, err := c.path(rawURL)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return "", fmt.Errorf("create cache directory: %w", err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(dst), ".tmp-*")
	if err != nil {
		return "", fmt.Errorf("create cache file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename has succeeded

	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return "", fmt.Errorf("write cache file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("close cache file: %w", err)
	}
	if err := os.Rename(tmpName, dst); err != nil {
		return "", fmt.Errorf("commit cache file: %w", err)
	}
	return Key(rawURL), nil
}

// Get returns a stored body. A missing entry reports fs.ErrNotExist, so
// callers can tell "not cached" from "cache broken" with errors.Is.
func (c *Cache) Get(rawURL string) ([]byte, error) {
	src, err := c.path(rawURL)
	if err != nil {
		return nil, err
	}
	body, err := os.ReadFile(src) //nolint:gosec // path is derived from a digest
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		return nil, fmt.Errorf("read cache file: %w", err)
	}
	return body, nil
}

// Has reports whether a URL's body is already stored.
func (c *Cache) Has(rawURL string) bool {
	src, err := c.path(rawURL)
	if err != nil {
		return false
	}
	_, err = os.Stat(src)
	return err == nil
}

// Stats counts what is in the cache.
type Stats struct {
	Pages int
	Bytes int64
}

// Stats walks the cache. It is used by `scour status`, so it counts rather
// than reads.
func (c *Cache) Stats() (Stats, error) {
	var s Stats
	err := filepath.WalkDir(c.root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.IsDir() || strings.HasPrefix(d.Name(), ".tmp-") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		s.Pages++
		s.Bytes += info.Size()
		return nil
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return s, fmt.Errorf("walk cache: %w", err)
	}
	return s, nil
}
