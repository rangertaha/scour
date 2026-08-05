// SPDX-License-Identifier: GPL-3.0-or-later

// Package cache stores fetched page bodies, keyed by URL, behind a backend
// that can be a local directory, S3 or Google Cloud Storage.
//
// The cache is deliberately dumb: a key maps to bytes, and nothing else. What
// a body was fetched from, when, and what came out of it belong to whatever
// records the crawl, not here. That is what lets the same interface be a
// directory on a laptop and a bucket shared by a fleet.
//
// # Why a cache at all
//
// Fetching is the expensive, rate-limited, impolite part of crawling, and
// understanding a page is neither. Keeping the bodies means extraction can be
// re-run, and re-run again after a change to how it works, without paying the
// network a second time or asking a site for the same page twice. The cache is
// therefore not an optimisation: it is the corpus, and it is what makes a
// change to inference measurable against fixed evidence.
package cache

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"iter"
	"strings"

	"github.com/rangertaha/scour/internal/registry"
)

// ErrNotFound is returned by [Store.Get] when a key holds nothing.
var ErrNotFound = errors.New("cache: key not found")

// ErrBadKey is returned when a key is not one [Key] could have produced.
var ErrBadKey = errors.New("cache: invalid key")

// Store holds page bodies.
//
// Implementations must be safe for concurrent use: a crawl writes from several
// goroutines, and on a fleet from several machines.
type Store interface {
	// Put writes a body, replacing whatever the key held before. A failed Put
	// must leave the previous value intact rather than a partial one, because
	// a truncated body is indistinguishable from a real one to every later
	// reader.
	Put(ctx context.Context, key string, r io.Reader) error

	// Get reads a body. It returns [ErrNotFound] if the key holds nothing.
	// The caller closes the reader.
	Get(ctx context.Context, key string) (io.ReadCloser, error)

	// Has reports whether a key holds anything, without reading it.
	Has(ctx context.Context, key string) (bool, error)

	// Delete removes a key. Deleting a key that holds nothing is not an
	// error, so a caller cleaning up does not have to look first.
	Delete(ctx context.Context, key string) error

	// Keys iterates every key held, in no guaranteed order. It is how a cache
	// is swept or counted; nothing in the crawl path needs it.
	Keys(ctx context.Context) iter.Seq2[string, error]

	// Close releases whatever the backend holds open.
	Close() error
}

// Config is what a backend is built from. A backend ignores the fields that do
// not apply to it.
type Config struct {
	// Backend names the implementation. Empty means [DefaultBackend].
	Backend string

	// Dir is the directory the local backend writes under.
	Dir string

	// Bucket is the bucket name for a cloud backend.
	Bucket string

	// Prefix is an optional path within the bucket, so one bucket can hold
	// more than one cache without them seeing each other's keys.
	Prefix string

	// Region is the S3 region. Empty leaves it to the environment.
	Region string

	// Endpoint overrides the S3 endpoint, which is what points the S3 backend
	// at MinIO or another S3-compatible service.
	Endpoint string

	// Profile names an AWS credentials profile. Empty leaves it to the
	// environment.
	Profile string

	// AccessKey, SecretKey and SessionToken are an explicit S3 credential.
	//
	// Empty leaves it to the SDK's own chain: environment, shared config,
	// instance role. That is what a laptop and a machine with a role both
	// want, and it stays the default.
	//
	// A job supplies these as `secret("name")`, which is an unevaluated call
	// everywhere the document travels and becomes a value only on the node
	// building the plugin. There is deliberately no way to write one as a
	// literal that is any easier than that: a credential in a config file is a
	// credential in a backup, in a bug report and in a repository.
	AccessKey    string
	SecretKey    string
	SessionToken string

	// Credentials is an explicit Google credential, as the JSON of a service
	// account key. Empty leaves it to the application default chain.
	Credentials string
}

// Secret reports whether this config carries an explicit credential, which is
// what decides whether a backend builds its own client or lets the SDK find
// one.
func (c Config) Secret() bool {
	return c.AccessKey != "" || c.SecretKey != "" || c.Credentials != ""
}

// String redacts the credentials.
//
// A config is exactly the sort of thing that ends up in a log line, an error
// message or a debugger's output, and the default formatting of a struct
// prints every field. Implementing String and GoString means the wrong thing
// is not merely discouraged, it is not what happens: `%v`, `%s` and `%#v` all
// go through here.
func (c Config) String() string {
	c.AccessKey, c.SecretKey, c.SessionToken, c.Credentials =
		redact(c.AccessKey), redact(c.SecretKey), redact(c.SessionToken), redact(c.Credentials)

	return fmt.Sprintf(
		"cache.Config{Backend:%q Dir:%q Bucket:%q Prefix:%q Region:%q Endpoint:%q Profile:%q "+
			"AccessKey:%s SecretKey:%s SessionToken:%s Credentials:%s}",
		c.Backend, c.Dir, c.Bucket, c.Prefix, c.Region, c.Endpoint, c.Profile,
		c.AccessKey, c.SecretKey, c.SessionToken, c.Credentials)
}

// GoString is String, so that %#v does not print what %v was careful to hide.
func (c Config) GoString() string { return c.String() }

func redact(value string) string {
	if value == "" {
		return `""`
	}
	return "[redacted]"
}

// DefaultBackend is what an empty [Config.Backend] means. It is the local
// directory, so a laptop needs nothing configured and nothing installed.
const DefaultBackend = "local"

// Key returns the cache key for a URL.
//
// A digest rather than the URL itself, because a URL is not a filename: it
// carries slashes, query strings, characters a filesystem will not take and
// lengths it will not accept. Hashing gives a fixed, safe, evenly distributed
// name, at the cost of a cache nobody can read by eye. That trade is worth it
// because the database already says which URL a key came from.
func Key(url string) string {
	sum := sha256.Sum256([]byte(url))
	return hex.EncodeToString(sum[:])
}

// CheckKey reports whether a key is usable.
//
// Keys are restricted to letters, digits, dot, dash and underscore. That is
// wider than [Key] produces on purpose, so a caller may use a readable key in
// a test, and narrow enough that a key can never climb out of a directory or
// mean something to a shell. A backend must reject anything else rather than
// trusting its caller, because a key can arrive from a database row written by
// an older version.
func CheckKey(key string) error {
	if key == "" {
		return fmt.Errorf("%w: empty", ErrBadKey)
	}
	if len(key) > 512 {
		return fmt.Errorf("%w: longer than 512 bytes", ErrBadKey)
	}
	for _, r := range key {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '.', r == '-', r == '_':
		default:
			return fmt.Errorf("%w: %q contains %q", ErrBadKey, key, r)
		}
	}

	// Every legal character is checked above and none of them is a separator,
	// which is not enough, because a backend may build a path out of the key's
	// own characters. The local one shards on the first four, so the key
	// "...." produced the directories ".." and "..", and a body was written
	// two levels above the cache root. Refusing "." and ".." was checking the
	// whole key for something that only had to appear in a slice of it.
	//
	// So: no dot pair anywhere, and the first character is a letter or a
	// digit. Together those make every two-character slice of a key a name
	// that cannot climb, whatever a backend does with it. A digest is hex and
	// a readable key in a test is a word, so nothing legitimate is lost.
	if strings.Contains(key, "..") {
		return fmt.Errorf("%w: %q contains %q, which can climb out of a directory", ErrBadKey, key, "..")
	}
	if first := key[0]; !(first >= 'a' && first <= 'z' || first >= 'A' && first <= 'Z' || first >= '0' && first <= '9') {
		return fmt.Errorf("%w: %q starts with %q, and a key starts with a letter or a digit",
			ErrBadKey, key, string(first))
	}
	return nil
}

// PutBytes writes a body already in memory.
func PutBytes(ctx context.Context, s Store, key string, body []byte) error {
	return s.Put(ctx, key, bytes.NewReader(body))
}

// GetBytes reads a whole body into memory. It returns [ErrNotFound] if the key
// holds nothing.
func GetBytes(ctx context.Context, s Store, key string) ([]byte, error) {
	r, err := s.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}

// Factory builds one backend from its configuration.
type Factory = registry.Factory[Config, Store]

// reg holds the implementations. See [registry] for the shape every extension
// point in scour shares, and for how to add one.
var reg = registry.New[Config, Store]("cache backend").Default(DefaultBackend)

// Register adds a backend, from an init function in the backend's own package.
//
// A backend is chosen by importing its package, which is what keeps a build
// that never wanted S3 from carrying its SDK.
func Register(name string, f Factory) { reg.Register(name, f) }

// Unregister removes a backend, and exists for tests. See
// [registry.Registry.Unregister].
func Unregister(name string) { reg.Unregister(name) }

// New builds the backend named by the config. An empty backend means
// [DefaultBackend].
func New(ctx context.Context, cfg Config) (Store, error) {
	return reg.New(ctx, cfg.Backend, cfg)
}

// Has reports whether a backend is registered.
func Has(name string) bool { return reg.Has(name) }

// Backends lists what has registered, sorted.
func Backends() []string { return reg.Names() }
