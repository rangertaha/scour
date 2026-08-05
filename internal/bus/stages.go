// SPDX-License-Identifier: GPL-3.0-or-later

package bus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/rangertaha/scour/internal/cache"
	"github.com/rangertaha/scour/internal/chain"
	"github.com/rangertaha/scour/internal/downloader"
	"github.com/rangertaha/scour/internal/extract"
	"github.com/rangertaha/scour/internal/frontier"
	"github.com/rangertaha/scour/internal/spider"
)

// The wire format.
//
// JSON rather than anything faster, because the first thing anybody does when a
// cluster misbehaves is watch a subject with the NATS command line, and a
// message they can read is worth more than one that is a few microseconds
// quicker to parse. The messages are small: a body never travels.

// fetchRequest is what a scheduler asks a downloader for.
type fetchRequest struct {
	URL    string      `json:"url"`
	Method string      `json:"method,omitempty"`
	Header http.Header `json:"header,omitempty"`
	Job    string      `json:"job"`
	Depth  int         `json:"depth"`
}

// fetchReply is what comes back. There is no body in it, and that is the point:
// the body is in the cache under Key, and whoever wants it fetches it there.
type fetchReply struct {
	URL     string      `json:"url"`
	Status  int         `json:"status"`
	Header  http.Header `json:"header,omitempty"`
	Key     string      `json:"key"`
	Bytes   int         `json:"bytes"`
	Fetched time.Time   `json:"fetched"`
	Cached  bool        `json:"cached"`

	// Dropped says a stage refused this on purpose. Carried as a field rather
	// than as an error string, because a drop is an ordinary outcome and a
	// caller has to be able to tell it from a stage that broke.
	Dropped bool   `json:"dropped,omitempty"`
	Error   string `json:"error,omitempty"`
}

// readRequest is what a downloader hands a spider: everything but the body.
type readRequest struct {
	URL     string      `json:"url"`
	Status  int         `json:"status"`
	Header  http.Header `json:"header,omitempty"`
	Key     string      `json:"key"`
	Fetched time.Time   `json:"fetched"`
	Depth   int         `json:"depth"`
	Job     string      `json:"job"`
}

// readReply is what a spider found.
type readReply struct {
	URL   string              `json:"url"`
	Depth int                 `json:"depth"`
	Spec  string              `json:"spec"`
	Items []*extract.Item     `json:"items,omitempty"`
	Links []*frontier.Request `json:"links,omitempty"`

	Dropped bool   `json:"dropped,omitempty"`
	Error   string `json:"error,omitempty"`
}

// ServeDownloader answers fetch requests for one job.
//
// The subscription is a queue group, so two nodes serving one job's downloader
// share the work without coordinating: NATS hands each request to one of them.
//
// It takes a handler rather than the stage, because what is behind it is not
// this package's business: a downloader, one wrapped in something that counts,
// or a stand-in are all the same thing from here.
func (c *Conn) ServeDownloader(ctx context.Context, job string, stage downloader.Handler, bodies cache.Store) (*nats.Subscription, error) {
	return c.QueueSubscribe(Subject(job, DownloadSubject), Queue(job, DownloadSubject),
		func(msg *nats.Msg) {
			var req fetchRequest
			if err := json.Unmarshal(msg.Data, &req); err != nil {
				reply(msg, fetchReply{Error: "bus: unreadable request: " + err.Error()})
				return
			}

			resp, err := stage.Handle(ctx, &downloader.Request{
				URL:    req.URL,
				Method: req.Method,
				Header: req.Header,
				Job:    req.Job,
				Depth:  req.Depth,
			})
			switch {
			case chain.Dropped(err):
				reply(msg, fetchReply{URL: req.URL, Dropped: true, Error: err.Error()})
				return
			case err != nil:
				reply(msg, fetchReply{URL: req.URL, Error: err.Error()})
				return
			}

			// The body goes to the cache under the request's own key, and only
			// the key travels. A downloader with a cache plugin has already
			// written it; one without has not, and this is what makes the
			// arrangement work either way.
			key := cache.Key(req.URL)
			if bodies != nil {
				if err := cache.PutBytes(ctx, bodies, key, resp.Body); err != nil {
					reply(msg, fetchReply{URL: req.URL, Error: "bus: could not store the body: " + err.Error()})
					return
				}
			}

			reply(msg, fetchReply{
				URL:     resp.URL,
				Status:  resp.Status,
				Header:  resp.Header,
				Key:     key,
				Bytes:   len(resp.Body),
				Fetched: resp.Fetched,
				Cached:  resp.Cached,
			})
		})
}

// Downloader is a downloader that is somewhere else.
//
// It satisfies the same interface the local one does, which is what lets the
// run loop be told nothing about where its stages are.
type Downloader struct {
	conn   *Conn
	job    string
	bodies cache.Store
}

// NewDownloader returns a client for one job's downloader.
func (c *Conn) NewDownloader(job string, bodies cache.Store) *Downloader {
	return &Downloader{conn: c, job: job, bodies: bodies}
}

// Handle implements [downloader.Handler].
func (d *Downloader) Handle(ctx context.Context, req *downloader.Request) (*downloader.Response, error) {
	payload, err := json.Marshal(fetchRequest{
		URL:    req.URL,
		Method: req.Method,
		Header: req.Header,
		Job:    req.Job,
		Depth:  req.Depth,
	})
	if err != nil {
		return nil, fmt.Errorf("bus: %w", err)
	}

	timed, cancel := withTimeout(ctx)
	defer cancel()

	msg, err := d.conn.RequestWithContext(timed, Subject(d.job, DownloadSubject), payload)
	if err != nil {
		return nil, fmt.Errorf("bus: fetch %s: %w", req.URL, noResponders(err))
	}

	var got fetchReply
	if err := json.Unmarshal(msg.Data, &got); err != nil {
		return nil, fmt.Errorf("bus: unreadable reply: %w", err)
	}
	switch {
	case got.Dropped:
		return nil, fmt.Errorf("%s: %w", got.Error, chain.ErrDrop)
	case got.Error != "":
		return nil, errors.New(got.Error)
	}

	// The body was never in the message. It is fetched from the cache, which
	// is the arrangement that keeps a megabyte of HTML off the bus.
	body, err := cache.GetBytes(ctx, d.bodies, got.Key)
	if err != nil {
		return nil, fmt.Errorf("bus: %s: the body is not in the cache: %w", got.URL, err)
	}

	return &downloader.Response{
		Request: req,
		URL:     got.URL,
		Status:  got.Status,
		Header:  got.Header,
		Body:    body,
		Fetched: got.Fetched,
		Cached:  got.Cached,
	}, nil
}

// ServeSpider answers read requests for one job.
func (c *Conn) ServeSpider(ctx context.Context, job string, stage spider.Handler, bodies cache.Store) (*nats.Subscription, error) {
	return c.QueueSubscribe(Subject(job, ReadSubject), Queue(job, ReadSubject),
		func(msg *nats.Msg) {
			var req readRequest
			if err := json.Unmarshal(msg.Data, &req); err != nil {
				reply(msg, readReply{Error: "bus: unreadable request: " + err.Error()})
				return
			}

			body, err := cache.GetBytes(ctx, bodies, req.Key)
			if err != nil {
				reply(msg, readReply{URL: req.URL, Error: "bus: the body is not in the cache: " + err.Error()})
				return
			}

			out, err := stage.Handle(ctx, &downloader.Response{
				Request: &downloader.Request{URL: req.URL, Job: req.Job, Depth: req.Depth},
				URL:     req.URL,
				Status:  req.Status,
				Header:  req.Header,
				Body:    body,
				Fetched: req.Fetched,
			})
			switch {
			case chain.Dropped(err):
				reply(msg, readReply{URL: req.URL, Dropped: true, Error: err.Error()})
				return
			case err != nil:
				reply(msg, readReply{URL: req.URL, Error: err.Error()})
				return
			}

			reply(msg, readReply{
				URL:   out.URL,
				Depth: out.Depth,
				Spec:  out.Spec,
				Items: out.Items,
				Links: out.Links,
			})
		})
}

// Spider is a spider that is somewhere else.
type Spider struct {
	conn   *Conn
	job    string
	bodies cache.Store
}

// NewSpider returns a client for one job's spider.
func (c *Conn) NewSpider(job string, bodies cache.Store) *Spider {
	return &Spider{conn: c, job: job, bodies: bodies}
}

// Handle implements [spider.Handler].
func (s *Spider) Handle(ctx context.Context, resp *downloader.Response) (*spider.Output, error) {
	if resp == nil {
		return nil, errors.New("bus: nothing to read")
	}

	// The body goes into the cache and the key travels, which is the same rule
	// in the other direction: a spider elsewhere reads it from there.
	key := cache.Key(resp.URL)
	if s.bodies != nil {
		if err := cache.PutBytes(ctx, s.bodies, key, resp.Body); err != nil {
			return nil, fmt.Errorf("bus: could not store the body: %w", err)
		}
	}

	depth := 0
	job := ""
	if resp.Request != nil {
		depth, job = resp.Request.Depth, resp.Request.Job
	}

	payload, err := json.Marshal(readRequest{
		URL:     resp.URL,
		Status:  resp.Status,
		Header:  resp.Header,
		Key:     key,
		Fetched: resp.Fetched,
		Depth:   depth,
		Job:     job,
	})
	if err != nil {
		return nil, fmt.Errorf("bus: %w", err)
	}

	timed, cancel := withTimeout(ctx)
	defer cancel()

	msg, err := s.conn.RequestWithContext(timed, Subject(s.job, ReadSubject), payload)
	if err != nil {
		return nil, fmt.Errorf("bus: read %s: %w", resp.URL, noResponders(err))
	}

	var got readReply
	if err := json.Unmarshal(msg.Data, &got); err != nil {
		return nil, fmt.Errorf("bus: unreadable reply: %w", err)
	}
	switch {
	case got.Dropped:
		return nil, fmt.Errorf("%s: %w", got.Error, chain.ErrDrop)
	case got.Error != "":
		return nil, errors.New(got.Error)
	}

	return &spider.Output{
		URL:   got.URL,
		Depth: got.Depth,
		Spec:  got.Spec,
		Items: got.Items,
		Links: got.Links,
	}, nil
}

// reply answers a request, and says nothing if it cannot: a reply that failed
// to send leaves the caller waiting for its timeout, and there is nowhere
// better to report it from than the caller's own error.
func reply[T any](msg *nats.Msg, payload T) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		encoded = []byte(`{"error":"bus: the reply could not be encoded"}`)
	}
	_ = msg.Respond(encoded)
}

// withTimeout bounds one request, unless the caller already bounded it.
//
// The cancel comes back rather than being dropped: a request that returns early
// leaves a timer running otherwise, and a crawl makes one of these per page.
func withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, has := ctx.Deadline(); has {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, Timeout)
}
