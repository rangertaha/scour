// SPDX-License-Identifier: GPL-3.0-or-later

// Package gcs stores page bodies in a Google Cloud Storage bucket.
//
// Import it for its side effect to make "gcs" available:
//
//	import _ "github.com/rangertaha/scour/internal/cache/gcs"
//
// # Where the credential comes from
//
// By default Google's application default chain: the environment, the gcloud
// login, or the service account a workload is running as. That is still the
// default, so nothing about an existing setup changes.
//
// A job may instead supply a service account key as
// `credentials = secret("name")`. It is resolved on the node building the
// plugin, and it never becomes part of a URL, for the reason set out in the S3
// backend: a URL carrying a credential puts it in the error message the moment
// the open fails.
package gcs

import (
	"context"
	"errors"
	"net/url"

	"gocloud.dev/blob/gcsblob"
	"gocloud.dev/gcp"
	"golang.org/x/oauth2/google"

	"github.com/rangertaha/scour/internal/cache"
	"github.com/rangertaha/scour/internal/cache/blob"
)

// cloudPlatformScope is what gocloud's DefaultCredentials asks for.
const cloudPlatformScope = "https://www.googleapis.com/auth/cloud-platform"

// Name is what this backend registers as.
//
// It is "gcs" rather than gocloud's "gs" because the backend is named for the
// service, as "s3" is, and the URL scheme is an implementation detail of how
// the bucket is opened.
const Name = "gcs"

func init() {
	cache.Register(Name, Open)
}

// Open builds the store for a config. It is a [cache.Factory].
func Open(ctx context.Context, cfg cache.Config) (cache.Store, error) {
	if cfg.Bucket == "" {
		return nil, errors.New("cache/gcs: no bucket configured")
	}
	if cfg.Secret() {
		return explicit(ctx, cfg)
	}

	u := url.URL{Scheme: "gs", Host: cfg.Bucket}
	return blob.Open(ctx, u.String(), cfg.Prefix)
}

// explicit opens a bucket with the service account key a job supplied.
func explicit(ctx context.Context, cfg cache.Config) (cache.Store, error) {
	if cfg.Credentials == "" {
		return nil, errors.New("cache/gcs: no credentials given")
	}

	// The scope gocloud's own default credentials use, so an explicit key and
	// an ambient one are granted the same thing and a job that switches
	// between them does not discover a permissions difference in production.
	creds, err := google.CredentialsFromJSON(ctx, []byte(cfg.Credentials), cloudPlatformScope)
	if err != nil {
		// Not wrapped: the message would carry the key material, and an error
		// is the most likely thing to be pasted into an issue.
		return nil, errors.New("cache/gcs: the credentials given are not a service account key")
	}

	client, err := gcp.NewHTTPClient(gcp.DefaultTransport(), gcp.CredentialsTokenSource(creds))
	if err != nil {
		return nil, errors.New("cache/gcs: could not build a client for the credentials given")
	}

	bucket, err := gcsblob.OpenBucket(ctx, client, cfg.Bucket, nil)
	if err != nil {
		return nil, errors.New("cache/gcs: could not open bucket " + cfg.Bucket + " with the credentials given")
	}
	return blob.Wrap(bucket, cfg.Prefix), nil
}
