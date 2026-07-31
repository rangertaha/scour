// SPDX-License-Identifier: GPL-3.0-or-later

package cache

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPutThenGet(t *testing.T) {
	c := New(t.TempDir())
	const url = "http://www.example.com/cars/one/"

	if c.Has(url) {
		t.Fatal("a fresh cache should be empty")
	}
	if _, err := c.Put(url, []byte("hello")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if !c.Has(url) {
		t.Error("Has should report the stored page")
	}

	body, err := c.Get(url)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(body) != "hello" {
		t.Errorf("body = %q, want hello", body)
	}
}

func TestGetMissingReportsNotExist(t *testing.T) {
	c := New(t.TempDir())
	_, err := c.Get("http://example.com/never-fetched")
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err = %v, want fs.ErrNotExist so callers can tell it apart", err)
	}
}

func TestURLsWithSameHostShareADirectory(t *testing.T) {
	root := t.TempDir()
	c := New(root)

	for _, u := range []string{
		"http://www.example.com/a",
		"http://www.example.com/b",
		"http://other.test/c",
	} {
		if _, err := c.Put(u, []byte(u)); err != nil {
			t.Fatal(err)
		}
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	hosts := make([]string, 0, len(entries))
	for _, e := range entries {
		hosts = append(hosts, e.Name())
	}
	if len(hosts) != 2 {
		t.Errorf("host directories = %v, want two", hosts)
	}
}

func TestQueryStringsAreDistinctEntries(t *testing.T) {
	c := New(t.TempDir())
	if _, err := c.Put("http://example.com/search?q=a", []byte("a")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Put("http://example.com/search?q=b", []byte("b")); err != nil {
		t.Fatal(err)
	}

	got, err := c.Get("http://example.com/search?q=a")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "a" {
		t.Errorf("body = %q, want the entry for q=a", got)
	}
}

func TestPutIsAtomic(t *testing.T) {
	root := t.TempDir()
	c := New(root)
	if _, err := c.Put("http://example.com/x", []byte("body")); err != nil {
		t.Fatal(err)
	}

	// No temporary files are left behind for a later pass to trip over.
	err := filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasPrefix(d.Name(), ".tmp-") {
			t.Errorf("temporary file left behind: %s", d.Name())
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestOverwriteReplacesTheBody(t *testing.T) {
	c := New(t.TempDir())
	const url = "http://example.com/x"

	if _, err := c.Put(url, []byte("first")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Put(url, []byte("second")); err != nil {
		t.Fatal(err)
	}

	body, err := c.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "second" {
		t.Errorf("body = %q, want the newer one", body)
	}
}

func TestStats(t *testing.T) {
	c := New(t.TempDir())
	for _, u := range []string{"http://example.com/a", "http://example.com/b"} {
		if _, err := c.Put(u, []byte("12345")); err != nil {
			t.Fatal(err)
		}
	}

	s, err := c.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if s.Pages != 2 {
		t.Errorf("pages = %d, want 2", s.Pages)
	}
	if s.Bytes != 10 {
		t.Errorf("bytes = %d, want 10", s.Bytes)
	}
}

func TestStatsOnAnAbsentCache(t *testing.T) {
	c := New(filepath.Join(t.TempDir(), "never-created"))
	s, err := c.Stats()
	if err != nil {
		t.Fatalf("a cache that was never written to is not an error: %v", err)
	}
	if s.Pages != 0 {
		t.Errorf("pages = %d, want 0", s.Pages)
	}
}
