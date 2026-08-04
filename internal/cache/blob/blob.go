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

	gcblob "gocloud.dev/blob"
	"gocloud.dev/gcerrors"

	"github.com/rangertaha/scour/internal/cache"
)

// Store is a cache held in an object storage bucket.
type Store struct {
	// bucket is what operations go through, prefixed when a prefix was given.
	bucket *gcblob.Bucket
	// underlying is what must actually be closed. Closing a prefixed wrapper
	// does not close what it wraps, so a store that only kept the wrapper
	// would leak the connection it opened.
	underlying *gcblob.Bucket
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

	s := &Store{bucket: b, underlying: b}
	if prefix != "" {
		// A prefix that does not end in a separator would silently merge with
		// the first characters of every key.
		if prefix[len(prefix)-1] != '/' {
			prefix += "/"
		}
		s.bucket = gcblob.PrefixedBucket(b, prefix)
	}
	return s, nil
}

// Put implements [cache.Store].
//
// Object storage has no partial write to worry about: a key becomes visible
// when the writer closes, and until then readers see whatever was there
// before. That is the same guarantee the local backend buys with a rename.
func (s *Store) Put(ctx context.Context, key string, r io.Reader) error {
	if err := cache.CheckKey(key); err != nil {
		return err
	}

	w, err := s.bucket.NewWriter(ctx, key, nil)
	if err != nil {
		return fmt.Errorf("cache/blob: write %q: %w", key, err)
	}
	if _, err := io.Copy(w, r); err != nil {
		// Close is still required to release the writer, and its own error is
		// not the interesting one here.
		w.Close()
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

// Close implements [cache.Store].
func (s *Store) Close() error {
	if err := s.underlying.Close(); err != nil {
		return fmt.Errorf("cache/blob: close: %w", err)
	}
	return nil
}
