// SPDX-License-Identifier: GPL-3.0-or-later

// Package s3 stores page bodies in an S3 bucket.
//
// Import it for its side effect to make "s3" available:
//
//	import _ "github.com/rangertaha/scour/internal/cache/s3"
//
// Credentials are the AWS SDK's own: environment, shared config, instance
// role. Nothing is read from scour's configuration, because a secret in a
// config file is a secret in a backup, in a bug report and in a repository.
package s3

import (
	"context"
	"errors"
	"net/url"

	// Registers the s3 scheme with gocloud's bucket opener.
	_ "gocloud.dev/blob/s3blob"

	"github.com/rangertaha/scour/internal/cache"
	"github.com/rangertaha/scour/internal/cache/blob"
)

// Name is what this backend registers as.
const Name = "s3"

func init() {
	cache.Register(Name, func(ctx context.Context, cfg cache.Config) (cache.Store, error) {
		if cfg.Bucket == "" {
			return nil, errors.New("cache/s3: no bucket configured")
		}
		return blob.Open(ctx, bucketURL(cfg), cfg.Prefix)
	})
}

// bucketURL builds the gocloud URL for a config.
//
// Region, endpoint and profile are query parameters gocloud already
// understands, so an S3-compatible service such as MinIO needs no code here,
// only an endpoint.
func bucketURL(cfg cache.Config) string {
	u := url.URL{Scheme: "s3", Host: cfg.Bucket}

	q := url.Values{}
	if cfg.Region != "" {
		q.Set("region", cfg.Region)
	}
	if cfg.Endpoint != "" {
		q.Set("endpoint", cfg.Endpoint)
	}
	if cfg.Profile != "" {
		q.Set("profile", cfg.Profile)
	}
	u.RawQuery = q.Encode()

	return u.String()
}
