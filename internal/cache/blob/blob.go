// SPDX-License-Identifier: GPL-3.0-or-later

// Package blob is the object storage backend the cloud caches share.
//
// S3 and GCS differ in how a bucket is addressed and in nothing else that this
// cache cares about, so they are one implementation and two ways of naming a
// bucket. Each cloud backend lives in its own package so that importing the
// one you use does not drag in the SDK of the one you do not.
//
// This package registers nothing itself. It has no backend name because it is
// not a choice a user makes.
package blob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"iter"
	"sync"

	gcblob "gocloud.dev/blob"
	"gocloud.dev/gcerrors"

	"github.com/rangertaha/scour/internal/cache"
)

// Store is a cache held in an object storage bucket.
type Store struct {
	// bucket is what every operation goes through, Close included, and it is
	// the prefixed wrapper when a prefix was given. The wrapper delegates its
	// own Close down to what it wraps, so this one handle is the whole story;
	// keeping a second handle to the unprefixed bucket and closing that instead
	// is what leaked one bucket per job.
	bucket *gcblob.Bucket

	// closed makes Close idempotent. Guarded because a store is shared by the
	// downloader and the spider, which are concurrent, and the contract's own
	// Concurrent case drives them that way.
	mu     sync.Mutex
	closed bool
}

// Open returns a store over the bucket named by a gocloud URL, such as
// s3://bucket?region=eu-west-1 or gs://bucket.
//
// A prefix confines the cache to a path within the bucket, so one bucket can
// hold several caches, or a cache alongside something else, without either
// listing the other's keys.
func Open(ctx context.Context, bucketURL, prefix string) (*Store, error) {
	if bucketURL == "" {
		return nil, errors.New("cache/blob: no bucket configured")
	}

	b, err := gcblob.OpenBucket(ctx, bucketURL)
	if err != nil {
		return nil, fmt.Errorf("cache/blob: open %s: %w", bucketURL, err)
	}
	return Wrap(b, prefix), nil
}

// Wrap returns a store over a bucket somebody else opened.
//
// It exists because a bucket opened with an explicit credential cannot be
// opened from a URL: putting a secret key in one would put it in the error
// message the moment the open failed, which is the one place a credential is
// most likely to be read by somebody who should not have it. So the cloud
// backends build their own client when a job supplies a credential, and hand
// the bucket here rather than reimplementing the prefixing and the closing.
func Wrap(bucket *gcblob.Bucket, prefix string) *Store {
	s := &Store{bucket: bucket}
	if prefix != "" {
		// A prefix that does not end in a separator would silently merge with
		// the first characters of every key.
		if prefix[len(prefix)-1] != '/' {
			prefix += "/"
		}
		s.bucket = gcblob.PrefixedBucket(bucket, prefix)
	}
	return s
}

// Put implements [cache.Store].
//
// A key becomes visible when the writer closes, and until then readers see
// whatever was there before, so a Put that fails partway leaves the previous
// body alone. That is the same guarantee the local backend buys with a rename,
// and it holds only because a failed copy cancels rather than closes: see
// below.
func (s *Store) Put(ctx context.Context, key string, r io.Reader) error {
	if err := cache.CheckKey(key); err != nil {
		return err
	}

	// A context of this write's own, so that a body which stops partway can be
	// abandoned rather than committed. gocloud is explicit that Close IS the
	// commit and that cancelling the context is the only way to abort, and
	// closing on error is what this used to do: a re-crawl whose connection
	// reset at 40 KB replaced a good 200 KB body with a truncated one that
	// parses and extracts silently. That is exactly what [cache.Store] forbids
	// and what the local backend buys with a rename.
	ctx, abort := context.WithCancel(ctx)
	defer abort()

	w, err := s.bucket.NewWriter(ctx, key, nil)
	if err != nil {
		return fmt.Errorf("cache/blob: write %q: %w", key, err)
	}
	if _, err := io.Copy(w, r); err != nil {
		abort()
		// Close after the cancel, which releases the writer without
		// committing. Its own error is the cancellation and is not the
		// interesting one.
		_ = w.Close()
		return fmt.Errorf("cache/blob: write %q: %w", key, err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("cache/blob: commit %q: %w", key, err)
	}
	return nil
}

// Get implements [cache.Store].
func (s *Store) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	if err := cache.CheckKey(key); err != nil {
		return nil, err
	}

	r, err := s.bucket.NewReader(ctx, key, nil)
	if gcerrors.Code(err) == gcerrors.NotFound {
		return nil, fmt.Errorf("%w: %s", cache.ErrNotFound, key)
	}
	if err != nil {
		return nil, fmt.Errorf("cache/blob: read %q: %w", key, err)
	}
	return r, nil
}

// Has implements [cache.Store].
func (s *Store) Has(ctx context.Context, key string) (bool, error) {
	if err := cache.CheckKey(key); err != nil {
		return false, err
	}

	ok, err := s.bucket.Exists(ctx, key)
	if gcerrors.Code(err) == gcerrors.NotFound {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("cache/blob: stat %q: %w", key, err)
	}
	return ok, nil
}

// Delete implements [cache.Store].
func (s *Store) Delete(ctx context.Context, key string) error {
	if err := cache.CheckKey(key); err != nil {
		return err
	}

	err := s.bucket.Delete(ctx, key)
	if err != nil && gcerrors.Code(err) != gcerrors.NotFound {
		return fmt.Errorf("cache/blob: delete %q: %w", key, err)
	}
	return nil
}

// Keys implements [cache.Store].
func (s *Store) Keys(ctx context.Context) iter.Seq2[string, error] {
	return func(yield func(string, error) bool) {
		it := s.bucket.List(nil)
		for {
			obj, err := it.Next(ctx)
			if errors.Is(err, io.EOF) {
				return
			}
			if err != nil {
				yield("", fmt.Errorf("cache/blob: list: %w", err))
				return
			}
			if obj.IsDir {
				continue
			}
			if !yield(obj.Key, nil) {
				return
			}
		}
	}
}

// Close implements [cache.Store], releasing the bucket.
//
// The prefixed wrapper is what gets closed when there is one, and the
// underlying bucket only when there is not. gocloud's PrefixedBucket marks the
// bucket it is handed as closed and delegates its own Close down to it, so
// closing the underlying one directly returned "Bucket has been closed" on a
// perfectly healthy store and left the driver open: a process opening one cache
// per job leaked a bucket per job, and the store went on serving reads after
// Close because the wrapper had never been told.
// Closing twice is not an error. `scour run` closes its crawl explicitly, so a
// failure to flush is reported before the summary says what was written, and
// again from a deferred call covering the paths that return earlier. The
// successful path therefore closes every store twice, and a deferred Close has
// nowhere to report an error, so the second one was discarded in silence: this
// returned "Bucket has been closed" on every completed crawl backed by S3 or
// GCS. The local backend returns nil however often it is called, which is why
// nothing showed up on a laptop.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil
	}
	s.closed = true

	if err := s.bucket.Close(); err != nil {
		return fmt.Errorf("cache/blob: close: %w", err)
	}
	return nil
}
