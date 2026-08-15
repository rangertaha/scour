// SPDX-License-Identifier: GPL-3.0-or-later

// Package cachetest is the contract every cache backend must satisfy.
//
// A pluggable backend is only pluggable if swapping it changes nothing a
// caller can observe, and that is a claim about behaviour rather than about
// method signatures: an interface makes them compile alike, and only a shared
// suite makes them behave alike. So the tests live here once, and each backend
// runs them against itself.
//
// A backend that cannot pass this is not a backend, it is a different thing
// with the same methods.
package cachetest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"

	"github.com/rangertaha/scour/internal/cache"
)

// Open returns a store to test, empty, and registers its cleanup with t.
type Open func(t *testing.T) cache.Store

// Run exercises the whole contract.
func Run(t *testing.T, open Open) {
	t.Helper()

	t.Run("RoundTrip", func(t *testing.T) { testRoundTrip(t, open) })
	t.Run("MissingIsNotFound", func(t *testing.T) { testMissing(t, open) })
	t.Run("Has", func(t *testing.T) { testHas(t, open) })
	t.Run("Delete", func(t *testing.T) { testDelete(t, open) })
	t.Run("DeleteMissingIsNotAnError", func(t *testing.T) { testDeleteMissing(t, open) })
	t.Run("Overwrite", func(t *testing.T) { testOverwrite(t, open) })
	t.Run("EmptyBody", func(t *testing.T) { testEmpty(t, open) })
	t.Run("LargeBody", func(t *testing.T) { testLarge(t, open) })
	t.Run("BadKeysRejected", func(t *testing.T) { testBadKeys(t, open) })
	t.Run("KeysCannotClimb", func(t *testing.T) { testKeysCannotClimb(t, open) })
	t.Run("AFailedPutKeepsThePrevious", func(t *testing.T) { testFailedPutKeepsPrevious(t, open) })
	t.Run("Keys", func(t *testing.T) { testKeys(t, open) })
	t.Run("Concurrent", func(t *testing.T) { testConcurrent(t, open) })
	t.Run("Close", func(t *testing.T) { testClose(t, open) })
	t.Run("CloseIsIdempotent", func(t *testing.T) { testCloseTwice(t, open) })
}

// testClose: a store that was opened can be closed.
//
// # Why this was missing, and why that mattered
//
// Close was the one method of [cache.Store] this suite never called. It is also
// the one with a regression already recorded against it: the object-storage
// backend closed the underlying bucket rather than the prefixed wrapper in front
// of it, which returned "Bucket has been closed" on a healthy store, leaked a
// bucket per job in a process opening one cache per job, and went on serving
// reads afterwards because the wrapper had never been told it was shut.
//
// That defect was caught by one backend's private tests, which is exactly the
// arrangement this package exists to replace: a promise checked for one
// implementation is a promise the others are free to break.
func testClose(t *testing.T, open Open) {
	if err := open(t).Close(); err != nil {
		t.Errorf("closing a store: %v", err)
	}
}

// testCloseTwice: Close is idempotent.
//
// Not hypothetical, and not tidiness. `scour run` closes its crawl explicitly so
// that a failure to flush is reported before the summary claims what was
// written, and it also closes it from a deferred call that covers the paths
// returning earlier. The successful path therefore closes twice, and the second
// error is discarded because a deferred Close has nowhere to report one.
//
// So a backend whose second Close fails does so silently, and the run says
// nothing. A backend that does something worse than fail the second time, such
// as releasing a handle another store has since been given, has nothing here to
// stop it.
func testCloseTwice(t *testing.T, open Open) {
	s := open(t)

	if err := s.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("second close: %v", err)
	}
}

func testRoundTrip(t *testing.T, open Open) {
	ctx := context.Background()
	s := open(t)

	key := cache.Key("https://example.com/a")
	body := []byte("<html><title>a</title></html>")

	if err := cache.PutBytes(ctx, s, key, body); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := cache.GetBytes(ctx, s, key)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("got %q, want %q", got, body)
	}
}

func testMissing(t *testing.T, open Open) {
	ctx := context.Background()
	s := open(t)

	_, err := s.Get(ctx, cache.Key("https://example.com/never-fetched"))
	if !errors.Is(err, cache.ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
}

func testHas(t *testing.T, open Open) {
	ctx := context.Background()
	s := open(t)
	key := cache.Key("https://example.com/b")

	switch has, err := s.Has(ctx, key); {
	case err != nil:
		t.Fatalf("has before put: %v", err)
	case has:
		t.Fatal("reported present before it was written")
	}

	if err := cache.PutBytes(ctx, s, key, []byte("b")); err != nil {
		t.Fatalf("put: %v", err)
	}

	switch has, err := s.Has(ctx, key); {
	case err != nil:
		t.Fatalf("has after put: %v", err)
	case !has:
		t.Fatal("reported absent after it was written")
	}
}

func testDelete(t *testing.T, open Open) {
	ctx := context.Background()
	s := open(t)
	key := cache.Key("https://example.com/c")

	if err := cache.PutBytes(ctx, s, key, []byte("c")); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := s.Delete(ctx, key); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.Get(ctx, key); !errors.Is(err, cache.ErrNotFound) {
		t.Fatalf("get after delete: got %v, want ErrNotFound", err)
	}
}

func testDeleteMissing(t *testing.T, open Open) {
	ctx := context.Background()
	s := open(t)

	if err := s.Delete(ctx, cache.Key("https://example.com/absent")); err != nil {
		t.Fatalf("deleting an absent key: %v", err)
	}
}

func testOverwrite(t *testing.T, open Open) {
	ctx := context.Background()
	s := open(t)
	key := cache.Key("https://example.com/d")

	if err := cache.PutBytes(ctx, s, key, []byte("first")); err != nil {
		t.Fatalf("first put: %v", err)
	}
	if err := cache.PutBytes(ctx, s, key, []byte("second")); err != nil {
		t.Fatalf("second put: %v", err)
	}

	got, err := cache.GetBytes(ctx, s, key)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if string(got) != "second" {
		t.Errorf("got %q, want the later write", got)
	}
}

// testEmpty covers a real case rather than a pedantic one: a 204, a HEAD, and
// a page that really is zero bytes all cache an empty body, and "present but
// empty" must stay distinguishable from "absent".
func testEmpty(t *testing.T, open Open) {
	ctx := context.Background()
	s := open(t)
	key := cache.Key("https://example.com/empty")

	if err := cache.PutBytes(ctx, s, key, nil); err != nil {
		t.Fatalf("put: %v", err)
	}
	switch has, err := s.Has(ctx, key); {
	case err != nil:
		t.Fatalf("has: %v", err)
	case !has:
		t.Fatal("an empty body reported as absent")
	}

	got, err := cache.GetBytes(ctx, s, key)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d bytes, want 0", len(got))
	}
}

func testLarge(t *testing.T, open Open) {
	ctx := context.Background()
	s := open(t)
	key := cache.Key("https://example.com/large.pdf")

	// Larger than any single write the backends make internally, so a body
	// that has to be streamed in pieces is covered.
	body := bytes.Repeat([]byte("0123456789abcdef"), 1<<18) // 4 MiB

	if err := s.Put(ctx, key, bytes.NewReader(body)); err != nil {
		t.Fatalf("put: %v", err)
	}
	r, err := s.Get(ctx, key)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer r.Close()

	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != len(body) {
		t.Fatalf("got %d bytes, want %d", len(got), len(body))
	}
	if !bytes.Equal(got, body) {
		t.Error("body came back changed")
	}
}

// testBadKeys is the security case. A key can arrive from a database row
// written by an older version, so a backend must not trust it: none of these
// may reach the filesystem or the bucket.
// testKeysCannotClimb is in the shared suite because the escape was not in the
// key checker, it was in what a backend built out of the key. The local backend
// shards on the first four characters, so "...." became the directories ".."
// and "..", and a body landed two levels above the cache root. Any backend that
// derives a path from a key can make the same mistake, so every backend is
// asked.
func testKeysCannotClimb(t *testing.T, open Open) {
	s := open(t)
	ctx := context.Background()

	for _, key := range []string{"....", "...", "..", ".", "..a", "ab..xyz", ".hidden", "-dash"} {
		if err := cache.CheckKey(key); err == nil {
			t.Errorf("CheckKey accepted %q, which a backend may turn into a path that climbs", key)
		}
		if err := cache.PutBytes(ctx, s, key, []byte("escaped")); err == nil {
			t.Errorf("a store accepted %q", key)
		}
	}
}

// testFailedPutKeepsPrevious is in the shared suite because a truncated body is
// indistinguishable from a real one to every later reader, which is what
// [cache.Store] promises and what only one backend was tested for.
func testFailedPutKeepsPrevious(t *testing.T, open Open) {
	s := open(t)
	ctx := context.Background()

	const key = "keeper"
	good := bytes.Repeat([]byte("a"), 4096)
	if err := cache.PutBytes(ctx, s, key, good); err != nil {
		t.Fatalf("put: %v", err)
	}

	// A body that stops partway, which is what a connection reset looks like
	// from here.
	if err := s.Put(ctx, key, io.MultiReader(
		bytes.NewReader(bytes.Repeat([]byte("b"), 512)),
		failingReader{},
	)); err == nil {
		t.Fatal("a body that stopped partway was accepted")
	}

	back, err := cache.GetBytes(ctx, s, key)
	if err != nil {
		t.Fatalf("the previous body is gone entirely: %v", err)
	}
	if !bytes.Equal(back, good) {
		t.Errorf("the previous body was replaced by %d bytes of a failed write", len(back))
	}
}

// failingReader stops with an error partway, like a connection that reset.
type failingReader struct{}

func (failingReader) Read([]byte) (int, error) {
	return 0, errors.New("the connection reset")
}

func testBadKeys(t *testing.T, open Open) {
	ctx := context.Background()
	s := open(t)

	bad := []string{
		"",
		".",
		"..",
		"../escape",
		"../../etc/passwd",
		"a/b",
		`a\b`,
		"with space",
		"semi;colon",
		"null\x00byte",
	}

	for _, key := range bad {
		t.Run(fmt.Sprintf("%q", key), func(t *testing.T) {
			if err := cache.PutBytes(ctx, s, key, []byte("x")); err == nil {
				t.Error("put accepted it")
			}
			if _, err := s.Get(ctx, key); err == nil {
				t.Error("get accepted it")
			}
			if _, err := s.Has(ctx, key); err == nil {
				t.Error("has accepted it")
			}
			if err := s.Delete(ctx, key); err == nil {
				t.Error("delete accepted it")
			}
		})
	}
}

func testKeys(t *testing.T, open Open) {
	ctx := context.Background()
	s := open(t)

	want := map[string]bool{}
	for _, u := range []string{
		"https://example.com/1",
		"https://example.com/2",
		"https://example.com/3",
	} {
		key := cache.Key(u)
		want[key] = true
		if err := cache.PutBytes(ctx, s, key, []byte(u)); err != nil {
			t.Fatalf("put %s: %v", u, err)
		}
	}

	got := map[string]bool{}
	for key, err := range s.Keys(ctx) {
		if err != nil {
			t.Fatalf("keys: %v", err)
		}
		got[key] = true
	}

	if len(got) != len(want) {
		t.Fatalf("got %d keys, want %d", len(got), len(want))
	}
	for key := range want {
		if !got[key] {
			t.Errorf("key %s was not listed", key)
		}
	}
}

// testConcurrent is not a race-detector formality: a crawl writes from several
// goroutines, and on a fleet from several machines, so a backend that is only
// correct one call at a time is not correct.
func testConcurrent(t *testing.T, open Open) {
	ctx := context.Background()
	s := open(t)

	const workers = 16
	var wg sync.WaitGroup
	errs := make(chan error, workers)

	for i := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			url := fmt.Sprintf("https://example.com/page/%d", i)
			key := cache.Key(url)
			if err := cache.PutBytes(ctx, s, key, []byte(url)); err != nil {
				errs <- fmt.Errorf("put %d: %w", i, err)
				return
			}
			got, err := cache.GetBytes(ctx, s, key)
			if err != nil {
				errs <- fmt.Errorf("get %d: %w", i, err)
				return
			}
			if string(got) != url {
				errs <- fmt.Errorf("page %d: got %q, want %q", i, got, url)
			}
		}()
	}

	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}
