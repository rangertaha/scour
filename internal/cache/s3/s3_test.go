// SPDX-License-Identifier: GPL-3.0-or-later

package s3

import (
	"context"
	"strings"
	"testing"

	"github.com/rangertaha/scour/internal/cache"
)

// The bucket URL is the whole of this package's logic: region, endpoint and
// profile are query parameters gocloud already understands, so an
// S3-compatible service such as MinIO needs no code here, only an endpoint.
func TestBucketURL(t *testing.T) {
	for name, tc := range map[string]struct {
		cfg  cache.Config
		want string
	}{
		"bucket only": {
			cache.Config{Bucket: "pages"},
			"s3://pages",
		},
		"region": {
			cache.Config{Bucket: "pages", Region: "eu-west-1"},
			"s3://pages?region=eu-west-1",
		},
		"minio": {
			cache.Config{Bucket: "pages", Endpoint: "http://localhost:9000", Region: "us-east-1"},
			"s3://pages?endpoint=http%3A%2F%2Flocalhost%3A9000&region=us-east-1",
		},
		"profile": {
			cache.Config{Bucket: "pages", Profile: "crawler"},
			"s3://pages?profile=crawler",
		},
	} {
		t.Run(name, func(t *testing.T) {
			if got := bucketURL(tc.cfg); got != tc.want {
				t.Errorf("got  %s\nwant %s", got, tc.want)
			}
		})
	}
}

func TestRegistered(t *testing.T) {
	if !cache.Has(Name) {
		t.Fatalf("importing this package did not register %q", Name)
	}
}

// TestNoBucketIsRefused: credentials come from the environment, but a bucket
// cannot, so it is the one thing this backend insists on.
func TestNoBucketIsRefused(t *testing.T) {
	_, err := cache.New(context.Background(), cache.Config{Backend: Name})
	if err == nil {
		t.Fatal("opened an S3 cache with no bucket")
	}
	if !strings.Contains(err.Error(), "bucket") {
		t.Errorf("error does not say what is missing: %v", err)
	}
}

// TestWithABucketReachesTheOpener covers the success path as far as it goes
// without credentials. See the GCS test for why it tolerates a failure.
func TestWithABucketReachesTheOpener(t *testing.T) {
	store, err := cache.New(context.Background(), cache.Config{
		Backend:  Name,
		Bucket:   "pages",
		Prefix:   "news",
		Region:   "eu-west-1",
		Endpoint: "http://127.0.0.1:1",
	})
	if err != nil {
		if strings.Contains(err.Error(), "bucket") {
			t.Fatalf("the factory refused a configured bucket: %v", err)
		}
		return
	}
	t.Cleanup(func() { store.Close() })
}

// TestHalfACredentialIsRefused.
//
// An access key with no secret key would otherwise fall through to the ambient
// chain and succeed or fail for reasons that have nothing to do with what the
// job asked for, which is the worst kind of failure to debug: it works on the
// developer's laptop, where a shared config exists, and not in production.
func TestHalfACredentialIsRefused(t *testing.T) {
	for name, cfg := range map[string]cache.Config{
		"no secret key": {Bucket: "pages", AccessKey: "PLACEHOLDER-ACCESS-KEY"},
		"no access key": {Bucket: "pages", SecretKey: "PLACEHOLDER-SECRET-KEY"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Open(context.Background(), cfg)
			if err == nil {
				t.Fatal("half a credential was accepted")
			}
			if !strings.Contains(err.Error(), "access key") {
				t.Errorf("the error does not say what is missing: %v", err)
			}
		})
	}
}

// TestAnErrorNeverCarriesTheCredential.
//
// An error is the most likely thing to be pasted into an issue, and the config
// that produced it holds a secret key. Wrapping it would be the natural thing
// to write and exactly the wrong one.
func TestAnErrorNeverCarriesTheCredential(t *testing.T) {
	_, err := Open(context.Background(), cache.Config{
		Bucket:    "pages",
		AccessKey: "PLACEHOLDER-ACCESS-KEY",
	})
	if err == nil {
		t.Fatal("expected a failure to inspect")
	}
	if strings.Contains(err.Error(), "PLACEHOLDER-ACCESS-KEY") {
		t.Errorf("the error carries the credential: %v", err)
	}
}
