// SPDX-License-Identifier: GPL-3.0-or-later

package local_test

import (
	"context"
	"fmt"
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

// The error paths. Most of a cache's value is in what it does when something
// goes wrong, so the failures are worth as much test as the successes.

func TestRoot(t *testing.T) {
	dir := t.TempDir()
	s, err := local.Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	if s.Root() != dir {
		t.Errorf("root = %q, want %q", s.Root(), dir)
	}
}

func TestOpenUnderAFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := local.Open(filepath.Join(file, "cache")); err == nil {
		t.Fatal("opened a cache inside a regular file")
	}
}

func TestCancelledContextIsRefused(t *testing.T) {
	s, err := local.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	key := cache.Key("https://example.com/")
	if err := cache.PutBytes(ctx, s, key, []byte("x")); err == nil {
		t.Error("put accepted a cancelled context")
	}
	if _, err := s.Get(ctx, key); err == nil {
		t.Error("get accepted a cancelled context")
	}
	if _, err := s.Has(ctx, key); err == nil {
		t.Error("has accepted a cancelled context")
	}
	if err := s.Delete(ctx, key); err == nil {
		t.Error("delete accepted a cancelled context")
	}
}

// TestKeysStopsWhenTheCallerDoes covers the iterator's early return, which is
// what a caller looking for one key relies on.
func TestKeysStopsWhenTheCallerDoes(t *testing.T) {
	ctx := context.Background()
	s, err := local.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	for i := range 5 {
		if err := cache.PutBytes(ctx, s, cache.Key(fmt.Sprint(i)), []byte("x")); err != nil {
			t.Fatal(err)
		}
	}

	seen := 0
	for _, err := range s.Keys(ctx) {
		if err != nil {
			t.Fatalf("keys: %v", err)
		}
		seen++
		break
	}
	if seen != 1 {
		t.Errorf("saw %d keys after breaking, want 1", seen)
	}
}

func TestKeysUnderACancelledContext(t *testing.T) {
	s, err := local.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	if err := cache.PutBytes(context.Background(), s, cache.Key("x"), []byte("x")); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var failed bool
	for _, err := range s.Keys(ctx) {
		if err != nil {
			failed = true
		}
	}
	if !failed {
		t.Error("iterating under a cancelled context reported no error")
	}
}

// TestUnreadableShardIsReported: a cache whose directory has been made
// unreadable must say so rather than reporting an empty cache, because an
// empty cache and an unreadable one lead to very different decisions.
func TestUnreadableShardIsReported(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads everything")
	}

	ctx := context.Background()
	dir := t.TempDir()

	s, err := local.Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	key := cache.Key("https://example.com/")
	if err := cache.PutBytes(ctx, s, key, []byte("x")); err != nil {
		t.Fatal(err)
	}

	shard := filepath.Join(dir, key[0:2])
	if err := os.Chmod(shard, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(shard, 0o750) })

	var failed bool
	for _, err := range s.Keys(ctx) {
		if err != nil {
			failed = true
		}
	}
	if !failed {
		t.Error("an unreadable shard was reported as an empty cache")
	}

	if _, err := s.Get(ctx, key); err == nil {
		t.Error("read from an unreadable shard")
	}
	if _, err := s.Has(ctx, key); err == nil {
		t.Error("stat succeeded in an unreadable shard")
	}
	if err := s.Delete(ctx, key); err == nil {
		t.Error("deleted from an unreadable shard")
	}
}

func TestPutIntoAnUnwritableCache(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes everywhere")
	}

	dir := t.TempDir()
	s, err := local.Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o750) })

	err = cache.PutBytes(context.Background(), s, cache.Key("https://example.com/"), []byte("x"))
	if err == nil {
		t.Fatal("wrote into a cache that cannot be written to")
	}
}

// TestShortKeysArePadded covers the shard padding: a key shorter than four
// characters is legal, and slicing it would panic.
func TestShortKeysArePadded(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	s, err := local.Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	for _, key := range []string{"a", "ab", "abc", "abcd"} {
		if err := cache.PutBytes(ctx, s, key, []byte(key)); err != nil {
			t.Fatalf("put %q: %v", key, err)
		}
		got, err := cache.GetBytes(ctx, s, key)
		if err != nil {
			t.Fatalf("get %q: %v", key, err)
		}
		if string(got) != key {
			t.Errorf("got %q, want %q", got, key)
		}
	}
}

// TestStagingBlocked: the staging directory is a sibling of the shards, so a
// file sitting where it should be stops writes rather than corrupting them.
func TestStagingBlocked(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "tmp"), []byte("in the way"), 0o600); err != nil {
		t.Fatal(err)
	}

	s, err := local.Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	if err := cache.PutBytes(context.Background(), s, cache.Key("x"), []byte("x")); err == nil {
		t.Fatal("wrote a body with the staging directory blocked")
	}
}

// TestCommitBlocked: a directory where the body should go makes the rename
// fail, which must be reported rather than leaving a stray temporary file.
func TestCommitBlocked(t *testing.T) {
	dir := t.TempDir()
	s, err := local.Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	key := cache.Key("https://example.com/blocked")
	final := filepath.Join(dir, key[0:2], key[2:4], key)
	if err := os.MkdirAll(final, 0o750); err != nil {
		t.Fatal(err)
	}

	if err := cache.PutBytes(context.Background(), s, key, []byte("x")); err == nil {
		t.Fatal("committed a body over a directory")
	}

	entries, err := os.ReadDir(filepath.Join(dir, "tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("a failed commit left %d temporary files behind", len(entries))
	}
}
