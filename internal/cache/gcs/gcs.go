// SPDX-License-Identifier: GPL-3.0-or-later

// Package gcs stores page bodies in a Google Cloud Storage bucket.
//
// Import it for its side effect to make "gcs" available:
//
//	import _ "github.com/rangertaha/scour/internal/cache/gcs"
//
// Credentials are Google's application default ones: the environment, the
// gcloud login, or the service account a workload is running as. Nothing is
// read from scour's configuration, for the same reason as S3.
package gcs

import (
	"context"
	"errors"
	"net/url"

	// Registers the gs scheme with gocloud's bucket opener.
	_ "gocloud.dev/blob/gcsblob"

	"github.com/rangertaha/scour/internal/cache"
	"github.com/rangertaha/scour/internal/cache/blob"
)

// Name is what this backend registers as.
//
// It is "gcs" rather than gocloud's "gs" because the backend is named for the
// service, as "s3" is, and the URL scheme is an implementation detail of how
// the bucket is opened.
const Name = "gcs"

func init() {
	cache.Register(Name, func(ctx context.Context, cfg cache.Config) (cache.Store, error) {
		if cfg.Bucket == "" {
			return nil, errors.New("cache/gcs: no bucket configured")
		}
		u := url.URL{Scheme: "gs", Host: cfg.Bucket}
		return blob.Open(ctx, u.String(), cfg.Prefix)
	})
}
