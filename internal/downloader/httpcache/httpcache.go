// SPDX-License-Identifier: GPL-3.0-or-later

// Package httpcache is the `cache` middleware: it answers from the corpus
// instead of the network, and puts what the network said into the corpus.
//
// Import it for its side effect to make "cache" available to a job:
//
//	import _ "github.com/rangertaha/scour/internal/downloader/httpcache"
//
// # Where it sits
//
// At 900, the last thing before the network. A hit therefore short-circuits the
// fetch only after every other request middleware has had its say, so a request
// the offsite rule would drop is dropped whether or not it happens to be
// cached. This is where Scrapy puts HttpCacheMiddleware and the reasoning is
// the same.
//
// # What it stores, and why in two keys
//
// The body under the URL's key, byte for byte as the server sent it, and a
// small header block under the same key with ".meta" on the end.
//
// The sidecar exists because a body on its own is not re-readable. A page in
// windows-1251 that declared its encoding in the Content-Type header and
// nowhere else decodes correctly on the way in and into mojibake on the way
// back out, and nothing about the resulting text says it went wrong. Keeping
// the status, the final URL and the headers means a hit reconstructs the
// response that was actually received, rather than a body with the provenance
// filed off.
//
// It could have gone in the records database instead. It goes here because the
// notes call the cache the corpus, and a corpus that cannot be read without a
// second database that happens to still exist is not one. The [cache.Store]
// stays what it was, a key holding bytes; using two keys is this middleware's
// business and none of the store's.
//
// # A cache that fails does not fail the crawl
//
// A read that errors is a miss, and a write that errors is logged and dropped.
// Losing a page that was successfully fetched because the disk that was only
// ever an optimisation filled up is a bad trade, and the fetch has already been
// paid for by then.
package httpcache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/rangertaha/scour/internal/cache"
	"github.com/rangertaha/scour/internal/downloader"
	"github.com/rangertaha/scour/internal/plugin"
)

// Name is what this middleware registers as, and what a job writes in a
// `plugin` block.
const Name = "cache"

// metaSuffix names the sidecar. A dot is legal in a cache key and a hash is
// hex, so no body's key can collide with a sidecar's.
const metaSuffix = ".meta"

// DefaultStatuses is what gets cached when the job does not say.
//
// Only 200. A redirect is followed inside the client and never reaches here as
// a response of its own; a 404 says a URL is dead today and caching it would
// keep saying so after the page came back. A job that wants either can list it.
var DefaultStatuses = []int{http.StatusOK}

func init() {
	downloader.Register(Name, New)
}

// Config is what a `plugin "cache"` block may set.
//
// The backend fields are [cache.Config]'s, because the choice of local
// directory, S3 or GCS is the cache's and repeating its documentation here
// would only let the two drift apart.
type Config struct {
	Backend  string `hcl:"backend,optional"`
	Dir      string `hcl:"dir,optional"`
	Bucket   string `hcl:"bucket,optional"`
	Prefix   string `hcl:"prefix,optional"`
	Region   string `hcl:"region,optional"`
	Endpoint string `hcl:"endpoint,optional"`
	Profile  string `hcl:"profile,optional"`

	// TTL makes a hit older than this a miss. Empty never expires, which is
	// what an archive wants and what a monitor does not.
	TTL string `hcl:"ttl,optional"`

	// Statuses are the status codes worth keeping. Empty means
	// [DefaultStatuses].
	Statuses []int `hcl:"statuses,optional"`
}

// New builds the middleware. It is [downloader.Middleware].
func New(ctx context.Context, cfg plugin.Config) (downloader.Wrapper, error) {
	var c Config
	if err := cfg.Decode(&c); err != nil {
		return nil, err
	}

	ttl, err := parseTTL(c.TTL)
	if err != nil {
		return nil, err
	}

	store, err := cache.New(ctx, cache.Config{
		Backend:  c.Backend,
		Dir:      c.Dir,
		Bucket:   c.Bucket,
		Prefix:   c.Prefix,
		Region:   c.Region,
		Endpoint: c.Endpoint,
		Profile:  c.Profile,
	})
	if err != nil {
		return nil, err
	}
	// The bucket outlives this function and has to close when the job stops.
	cfg.Defer(store.Close)

	m := &middleware{
		store:    store,
		ttl:      ttl,
		statuses: statusSet(c.Statuses),
		log:      slog.Default().With("plugin", Name, "job", cfg.Job),
	}
	return m.wrap, nil
}

// middleware is one job's cache.
type middleware struct {
	store    cache.Store
	ttl      time.Duration
	statuses map[int]bool
	log      *slog.Logger
}

func (m *middleware) wrap(next downloader.Handler) downloader.Handler {
	return downloader.HandlerFunc(func(ctx context.Context, req *downloader.Request) (*downloader.Response, error) {
		key := cache.Key(req.URL)

		if resp := m.hit(ctx, key, req); resp != nil {
			return resp, nil
		}

		resp, err := next.Handle(ctx, req)
		if err != nil || resp == nil {
			return resp, err
		}
		m.keep(ctx, key, resp)
		return resp, nil
	})
}

// hit returns the cached response, or nil for a miss.
//
// The sidecar is read first: it is small, and a body with no sidecar is a
// half-written entry that has to be refetched rather than half-believed.
func (m *middleware) hit(ctx context.Context, key string, req *downloader.Request) *downloader.Response {
	raw, err := cache.GetBytes(ctx, m.store, key+metaSuffix)
	if err != nil {
		if !errors.Is(err, cache.ErrNotFound) {
			m.log.WarnContext(ctx, "cache read failed, refetching", "url", req.URL, "error", err)
		}
		return nil
	}

	var meta entry
	if err := json.Unmarshal(raw, &meta); err != nil {
		m.log.WarnContext(ctx, "cache holds an unreadable entry, refetching", "url", req.URL, "error", err)
		return nil
	}

	if m.ttl > 0 && time.Since(meta.Fetched) > m.ttl {
		return nil
	}

	body, err := cache.GetBytes(ctx, m.store, key)
	if err != nil {
		// A sidecar with no body: the write was interrupted between the two.
		// Refetching rewrites both.
		if !errors.Is(err, cache.ErrNotFound) {
			m.log.WarnContext(ctx, "cache read failed, refetching", "url", req.URL, "error", err)
		}
		return nil
	}

	return &downloader.Response{
		Request: req,
		URL:     meta.URL,
		Status:  meta.Status,
		Header:  meta.Header,
		Body:    body,
		Fetched: meta.Fetched,
		Cached:  true,
	}
}

// keep writes a response, if it is one worth keeping.
func (m *middleware) keep(ctx context.Context, key string, resp *downloader.Response) {
	if resp.Cached || !m.statuses[resp.Status] {
		return
	}

	raw, err := json.Marshal(entry{
		URL:     resp.URL,
		Status:  resp.Status,
		Header:  resp.Header,
		Fetched: resp.Fetched,
	})
	if err != nil {
		// Unreachable with the fields this struct has, and left in because the
		// day somebody adds one that is not marshalable it should be a line in
		// a log rather than a corpus quietly missing its sidecars.
		m.log.WarnContext(ctx, "cache write failed", "url", resp.URL, "error", err)
		return
	}

	// Body first: a hit needs both and reads the sidecar to decide, so an
	// interrupted write leaves a clean miss rather than a sidecar promising a
	// body that is not there.
	if m.put(ctx, key, resp.Body, resp.URL) {
		m.put(ctx, key+metaSuffix, raw, resp.URL)
	}
}

// put writes one key, and reports what it could not write rather than failing
// the crawl. The fetch has already been paid for by the time anything gets
// here, and losing the page to a full disk would be a bad trade.
func (m *middleware) put(ctx context.Context, key string, content []byte, url string) bool {
	if err := cache.PutBytes(ctx, m.store, key, content); err != nil {
		m.log.WarnContext(ctx, "cache write failed", "url", url, "error", err)
		return false
	}
	return true
}

// entry is the sidecar: what the response was, other than its body.
type entry struct {
	URL     string      `json:"url"`
	Status  int         `json:"status"`
	Header  http.Header `json:"header,omitempty"`
	Fetched time.Time   `json:"fetched"`
}

func parseTTL(s string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("cache.ttl: %w", err)
	}
	if d < 0 {
		return 0, fmt.Errorf("cache.ttl: %q is negative", s)
	}
	return d, nil
}

func statusSet(statuses []int) map[int]bool {
	if len(statuses) == 0 {
		statuses = DefaultStatuses
	}
	out := make(map[int]bool, len(statuses))
	for _, s := range statuses {
		out[s] = true
	}
	return out
}
