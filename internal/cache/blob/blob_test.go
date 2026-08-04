// SPDX-License-Identifier: GPL-3.0-or-later

package blob_test

import (
	"context"
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
