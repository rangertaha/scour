// SPDX-License-Identifier: GPL-3.0-or-later

//go:build cloud

// The object stores are behind a build tag because they are not free. Linking
// the AWS and Google SDKs takes the binary from 64MB to 105MB, and most crawls
// keep their pages in a directory. Pluggable should mean you do not pay for a
// driver you are not using:
//
//	go build -tags cloud ./cmd/scour
//
// A build without the tag knows the names and says so, rather than failing with
// "unknown driver" as though the name were a typo.

package cache

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"strings"

	"gocloud.dev/blob"
	_ "gocloud.dev/blob/gcsblob" // registers gs://
	_ "gocloud.dev/blob/s3blob"  // registers s3://

	"gocloud.dev/gcerrors"
)

func init() {
	Register(driverS3, openBlob)
	Register(driverGCS, openBlob)
}

// Blob keeps bodies in an object store.
//
// One implementation for both clouds, because the difference between them is a
// URL scheme and a credential, not a way of storing bytes. Credentials are the
// provider's own: AWS_* and the shared config for S3, application default
// credentials for Google. scour does not take keys in its configuration, so a
// crawler needs no secrets in a file that also says what to crawl.
type Blob struct {
	bucket *blob.Bucket
	prefix string
}

// openBlob builds a store from a bucket URL: s3://bucket/prefix or
// gs://bucket/prefix. Options are appended as query parameters, which is how
// the drivers take a region or an endpoint.
func openBlob(cfg Config) (Store, error) {
	raw := cfg.URL
	if raw == "" {
		return nil, errors.New("cache: an object store needs a bucket url")
	}

	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("cache: parse bucket url %q: %w", raw, err)
	}
	// The path is a prefix within the bucket rather than part of the bucket
	// name, and the drivers want it as a query parameter.
	prefix := strings.Trim(u.Path, "/")
	u.Path = ""

	q := u.Query()
	for k, v := range cfg.Options {
		q.Set(k, v)
	}
	u.RawQuery = q.Encode()

	// Opened with a background context: the bucket outlives any one request,
	// and the per-call contexts are the ones that matter.
	bucket, err := blob.OpenBucket(context.Background(), u.String())
	if err != nil {
		return nil, fmt.Errorf("cache: open %s: %w", raw, err)
	}
	return &Blob{bucket: bucket, prefix: prefix}, nil
}

// key is the object a URL's body is stored at, laid out like the local cache:
// <prefix>/<host>/<ab>/<key>, so a single site's pages can be listed or dropped
// on their own with a prefix query.
func (b *Blob) key(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("cache key for %q: %w", rawURL, err)
	}
	host := u.Hostname()
	if host == "" {
		host = "unknown"
	}
	sum := Key(rawURL)
	parts := []string{host, sum[:2], sum}
	if b.prefix != "" {
		parts = append([]string{b.prefix}, parts...)
	}
	return strings.Join(parts, "/"), nil
}

// Put implements [Store].
func (b *Blob) Put(ctx context.Context, rawURL string, body []byte) (string, error) {
	k, err := b.key(rawURL)
	if err != nil {
		return "", err
	}
	if err := b.bucket.WriteAll(ctx, k, body, nil); err != nil {
		return "", fmt.Errorf("cache write %s: %w", k, err)
	}
	return Key(rawURL), nil
}

// Get implements [Store].
func (b *Blob) Get(ctx context.Context, rawURL string) ([]byte, error) {
	k, err := b.key(rawURL)
	if err != nil {
		return nil, err
	}
	data, err := b.bucket.ReadAll(ctx, k)
	if err != nil {
		if gcerrors.Code(err) == gcerrors.NotFound {
			// The same signal the local driver gives, so a caller deciding
			// whether a page is simply absent does not have to know which
			// driver it is talking to.
			return nil, fmt.Errorf("cache read %s: %w", k, fs.ErrNotExist)
		}
		return nil, fmt.Errorf("cache read %s: %w", k, err)
	}
	return data, nil
}

// Has implements [Store].
func (b *Blob) Has(ctx context.Context, rawURL string) bool {
	k, err := b.key(rawURL)
	if err != nil {
		return false
	}
	ok, err := b.bucket.Exists(ctx, k)
	return err == nil && ok
}

// Stats implements [Store].
//
// Counting means listing the bucket, which is a request per page of results
// rather than a directory walk. It is called by `scour status`, not on any
// crawl path.
func (b *Blob) Stats(ctx context.Context) (Stats, error) {
	var s Stats
	it := b.bucket.List(&blob.ListOptions{Prefix: b.prefix})
	for {
		obj, err := it.Next(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return s, fmt.Errorf("cache stats: %w", err)
		}
		if obj.IsDir {
			continue
		}
		s.Pages++
		s.Bytes += obj.Size
	}
	return s, nil
}

// Close releases the bucket.
func (b *Blob) Close() error { return b.bucket.Close() }
