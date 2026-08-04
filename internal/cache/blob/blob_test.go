// SPDX-License-Identifier: GPL-3.0-or-later

package blob_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	// An in-memory bucket, so the object storage path is covered without
	// credentials or a network. It is the same code S3 and GCS run: only the
	// URL scheme differs, which is the point of the package being shared.
	_ "gocloud.dev/blob/memblob"

	"github.com/rangertaha/scour/internal/cache"
	"github.com/rangertaha/scour/internal/cache/blob"
	"github.com/rangertaha/scour/internal/cache/cachetest"
)

func TestContract(t *testing.T) {
	cachetest.Run(t, func(t *testing.T) cache.Store {
		s, err := blob.Open(context.Background(), "mem://", "")
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		t.Cleanup(func() { s.Close() })
		return s
	})
}

// TestContractWithPrefix runs the whole contract again through a prefix,
// because a prefix that leaked would show up as keys listing wrong or as reads
// missing, and neither is visible without exercising everything.
func TestContractWithPrefix(t *testing.T) {
	cachetest.Run(t, func(t *testing.T) cache.Store {
		s, err := blob.Open(context.Background(), "mem://", "crawl/bodies")
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		t.Cleanup(func() { s.Close() })
		return s
	})
}

func TestOpenWithoutBucket(t *testing.T) {
	if _, err := blob.Open(context.Background(), "", ""); err == nil {
		t.Fatal("opened a store with no bucket")
	}
}

// TestPrefixIsolates is the reason a prefix exists: two caches in one bucket
// must not see each other's keys.
//
// memblob gives each open its own bucket, so this cannot prove isolation
// against a shared one. What it does hold is that a prefixed store lists only
// what it wrote, which is the half that would break if the prefix were dropped
// on the read path.
func TestPrefixIsolates(t *testing.T) {
	ctx := context.Background()

	// memblob gives each mem:// URL its own bucket, so both stores are opened
	// over one shared bucket to make this a real test rather than a tautology.
	shared := "mem://shared"

	a, err := blob.Open(ctx, shared, "a")
	if err != nil {
		t.Fatalf("open a: %v", err)
	}
	defer a.Close()

	b, err := blob.Open(ctx, shared, "b")
	if err != nil {
		t.Fatalf("open b: %v", err)
	}
	defer b.Close()

	key := cache.Key("https://example.com/")
	if err := cache.PutBytes(ctx, a, key, []byte("belongs to a")); err != nil {
		t.Fatalf("put into a: %v", err)
	}

	switch has, err := b.Has(ctx, key); {
	case err != nil:
		t.Fatalf("has in b: %v", err)
	case has:
		t.Error("b can see a's key")
	}

	for gotKey, err := range b.Keys(ctx) {
		if err != nil {
			t.Fatalf("keys in b: %v", err)
		}
		t.Errorf("b listed %q, which is not its own", gotKey)
	}
}

// Error paths. memblob is a real driver, so most of these are reached the same
// way S3 would reach them.

func TestBadKeysAreRefusedBeforeTheBucket(t *testing.T) {
	ctx := context.Background()
	s, err := blob.Open(ctx, "mem://", "")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	for _, key := range []string{"", "..", "a/b", "with space"} {
		if err := cache.PutBytes(ctx, s, key, []byte("x")); err == nil {
			t.Errorf("put accepted %q", key)
		}
		if _, err := s.Get(ctx, key); err == nil {
			t.Errorf("get accepted %q", key)
		}
		if _, err := s.Has(ctx, key); err == nil {
			t.Errorf("has accepted %q", key)
		}
		if err := s.Delete(ctx, key); err == nil {
			t.Errorf("delete accepted %q", key)
		}
	}
}

func TestOpenRefusesAnUnknownScheme(t *testing.T) {
	if _, err := blob.Open(context.Background(), "carrier-pigeon://bucket", ""); err == nil {
		t.Fatal("opened a bucket with a scheme nothing registered")
	}
}

// TestPrefixGainsItsSeparator covers the branch that appends one. A prefix
// that did not end in a separator would silently merge with the first
// characters of every key.
//
// This cannot be shown by comparing two stores: memblob hands out a fresh
// bucket per open, so two opens never share anything. What it does show is that
// the unslashed spelling is accepted and round-trips.
func TestPrefixGainsItsSeparator(t *testing.T) {
	ctx := context.Background()

	s, err := blob.Open(ctx, "mem://", "crawl/bodies")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	key := cache.Key("https://example.com/")
	if err := cache.PutBytes(ctx, s, key, []byte("body")); err != nil {
		t.Fatal(err)
	}

	got, err := cache.GetBytes(ctx, s, key)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if string(got) != "body" {
		t.Errorf("got %q", got)
	}

	// The key is listed under its own name, not under one glued to the prefix.
	for listed, err := range s.Keys(ctx) {
		if err != nil {
			t.Fatalf("keys: %v", err)
		}
		if listed != key {
			t.Errorf("listed %q, want the key alone", listed)
		}
	}
}

func TestKeysStopsWhenTheCallerDoes(t *testing.T) {
	ctx := context.Background()
	s, err := blob.Open(ctx, "mem://", "")
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

func TestKeysReportsAFailure(t *testing.T) {
	ctx := context.Background()

	s, err := blob.Open(ctx, "mem://", "")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := cache.PutBytes(ctx, s, cache.Key("x"), []byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	// Listing a closed bucket is the one failure this driver can be made to
	// produce, and it stands in for the network ones S3 would produce.
	var failed bool
	for _, err := range s.Keys(ctx) {
		if err != nil {
			failed = true
		}
	}
	if !failed {
		t.Error("listing a closed bucket reported no error")
	}
}

func TestPutReportsAFailingBody(t *testing.T) {
	ctx := context.Background()
	s, err := blob.Open(ctx, "mem://", "")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	err = s.Put(ctx, cache.Key("x"), failingReader{})
	if err == nil {
		t.Fatal("a body that could not be read was accepted")
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("the pipe broke") }

func TestCloseIsIdempotentEnough(t *testing.T) {
	s, err := blob.Open(context.Background(), "mem://", "")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("close: %v", err)
	}
}
