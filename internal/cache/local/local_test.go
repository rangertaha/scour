// SPDX-License-Identifier: GPL-3.0-or-later

package local_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/rangertaha/scour/internal/cache"
	"github.com/rangertaha/scour/internal/cache/cachetest"
	"github.com/rangertaha/scour/internal/cache/local"
)

func TestContract(t *testing.T) {
	cachetest.Run(t, func(t *testing.T) cache.Store {
		s, err := local.Open(t.TempDir())
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		t.Cleanup(func() { s.Close() })
		return s
	})
}

func TestRegistered(t *testing.T) {
	dir := t.TempDir()
	s, err := cache.New(context.Background(), cache.Config{Backend: local.Name, Dir: dir})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer s.Close()

	if err := cache.PutBytes(context.Background(), s, cache.Key("https://example.com/"), []byte("x")); err != nil {
		t.Fatalf("put: %v", err)
	}
}

// TestDefaultBackend covers the laptop case: nothing configured but a
// directory, and the local backend is what you get.
func TestDefaultBackend(t *testing.T) {
	s, err := cache.New(context.Background(), cache.Config{Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer s.Close()
}

func TestOpenWithoutDirectory(t *testing.T) {
	if _, err := local.Open(""); err == nil {
		t.Fatal("opened a store with no directory")
	}
}

func TestOpenCreatesDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "does", "not", "exist", "yet")
	s, err := local.Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("directory was not created: %v", err)
	}
}

// TestSharded pins the layout, because it is what stops a million-page crawl
// from putting a million entries in one directory.
func TestSharded(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	s, err := local.Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	key := cache.Key("https://example.com/shard")
	if err := cache.PutBytes(ctx, s, key, []byte("x")); err != nil {
		t.Fatalf("put: %v", err)
	}

	want := filepath.Join(dir, key[0:2], key[2:4], key)
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("body is not at %s: %v", want, err)
	}
}

// TestNoTemporaryFilesLeftBehind guards the staging directory: a Put that
// succeeded must leave nothing in it, or a long crawl fills the disk with
// bodies it already committed.
func TestNoTemporaryFilesLeftBehind(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	s, err := local.Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	for i := range 10 {
		key := cache.Key(string(rune('a' + i)))
		if err := cache.PutBytes(ctx, s, key, []byte("x")); err != nil {
			t.Fatalf("put: %v", err)
		}
	}

	entries, err := os.ReadDir(filepath.Join(dir, "tmp"))
	if err != nil {
		t.Fatalf("read staging directory: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("%d temporary files left behind", len(entries))
	}
}

// TestFailedPutKeepsPrevious is the reason Put stages and renames. A body that
// fails halfway must not replace a good one with a truncated one.
func TestFailedPutKeepsPrevious(t *testing.T) {
	ctx := context.Background()
	s, err := local.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	key := cache.Key("https://example.com/interrupted")
	if err := cache.PutBytes(ctx, s, key, []byte("the good body")); err != nil {
		t.Fatalf("put: %v", err)
	}

	if err := s.Put(ctx, key, failingReader{}); err == nil {
		t.Fatal("a failing body was accepted")
	}

	got, err := cache.GetBytes(ctx, s, key)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if string(got) != "the good body" {
		t.Errorf("got %q, want the previous body intact", got)
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, os.ErrClosed }
