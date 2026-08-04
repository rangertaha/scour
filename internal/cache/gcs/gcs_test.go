// SPDX-License-Identifier: GPL-3.0-or-later

package gcs

import (
	"context"
	"strings"
	"testing"

	"github.com/rangertaha/scour/internal/cache"
)

func TestRegistered(t *testing.T) {
	if !cache.Has(Name) {
		t.Fatalf("importing this package did not register %q", Name)
	}
	// Named for the service, as s3 is. The gs:// scheme is an implementation
	// detail of how the bucket is opened.
	if Name != "gcs" {
		t.Errorf("Name = %q", Name)
	}
}

func TestNoBucketIsRefused(t *testing.T) {
	_, err := cache.New(context.Background(), cache.Config{Backend: Name})
	if err == nil {
		t.Fatal("opened a GCS cache with no bucket")
	}
	if !strings.Contains(err.Error(), "bucket") {
		t.Errorf("error does not say what is missing: %v", err)
	}
}

// TestWithABucketReachesTheOpener covers the success path as far as it goes
// without credentials.
//
// Opening a GCS bucket needs application default credentials, which a test
// machine does not have, so this asserts only that the factory got as far as
// asking for them: whatever comes back, the configuration was accepted and the
// bucket URL was built.
func TestWithABucketReachesTheOpener(t *testing.T) {
	store, err := cache.New(context.Background(), cache.Config{
		Backend: Name,
		Bucket:  "pages",
		Prefix:  "news",
	})
	if err != nil {
		if strings.Contains(err.Error(), "bucket") {
			t.Fatalf("the factory refused a configured bucket: %v", err)
		}
		return // credentials, which this machine is not expected to have
	}
	t.Cleanup(func() { store.Close() })
}
