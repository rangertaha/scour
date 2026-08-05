// SPDX-License-Identifier: GPL-3.0-or-later

// Package s3 stores page bodies in an S3 bucket.
//
// Import it for its side effect to make "s3" available:
//
//	import _ "github.com/rangertaha/scour/internal/cache/s3"
//
// # Where the credential comes from
//
// By default the AWS SDK's own chain: environment, shared config, instance
// role. That is what a laptop and a machine with a role both want, and it is
// still the default, so nothing about an existing setup changes.
//
// A job may instead supply one explicitly, as `access_key = secret("name")`.
// The value is an unevaluated call everywhere the document travels and becomes
// a credential only on the node building the plugin. What it must not become
// is part of a URL: gocloud opens a bucket from one, and a URL with a secret
// key in it is a secret key in the error message the moment the open fails,
// which is exactly where somebody reads it. So an explicit credential builds
// the client directly and hands the bucket over, and the URL path is used only
// when there is nothing to hide.
package s3

import (
	"context"
	"errors"
	"net/url"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"gocloud.dev/blob/s3blob"

	"github.com/rangertaha/scour/internal/cache"
	"github.com/rangertaha/scour/internal/cache/blob"
)

// Name is what this backend registers as.
const Name = "s3"

func init() {
	cache.Register(Name, Open)
}

// Open builds the store for a config. It is a [cache.Factory].
func Open(ctx context.Context, cfg cache.Config) (cache.Store, error) {
	if cfg.Bucket == "" {
		return nil, errors.New("cache/s3: no bucket configured")
	}
	if cfg.Secret() {
		return explicit(ctx, cfg)
	}
	return blob.Open(ctx, bucketURL(cfg), cfg.Prefix)
}

// explicit opens a bucket with the credential a job supplied.
//
// Both halves are required. A key without a secret is a misconfiguration that
// would otherwise fall through to the ambient chain and succeed or fail for
// reasons that have nothing to do with what the job asked for, which is the
// worst kind of failure to debug.
func explicit(ctx context.Context, cfg cache.Config) (cache.Store, error) {
	if cfg.AccessKey == "" || cfg.SecretKey == "" {
		return nil, errors.New("cache/s3: an explicit credential needs both an access key and a secret key")
	}

	loaded, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(cfg.Region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			cfg.AccessKey, cfg.SecretKey, cfg.SessionToken)))
	if err != nil {
		// Deliberately not wrapping the config: it holds the credential, and
		// an error is the most likely thing to be pasted into an issue.
		return nil, errors.New("cache/s3: could not build a client for the credential given")
	}

	client := awss3.NewFromConfig(loaded, func(o *awss3.Options) {
		if cfg.Endpoint != "" {
			// An S3-compatible service such as MinIO addresses buckets by path
			// rather than by hostname, and gets this wrong silently otherwise:
			// it looks up bucket.minio.example and fails to resolve it.
			o.BaseEndpoint = aws.String(cfg.Endpoint)
			o.UsePathStyle = true
		}
	})

	bucket, err := s3blob.OpenBucketV2(ctx, client, cfg.Bucket, nil)
	if err != nil {
		return nil, errors.New("cache/s3: could not open bucket " + cfg.Bucket + " with the credential given")
	}
	return blob.Wrap(bucket, cfg.Prefix), nil
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
