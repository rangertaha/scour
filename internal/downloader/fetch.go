// SPDX-License-Identifier: GPL-3.0-or-later

package downloader

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// ErrTooLarge reports a body over the job's limit.
//
// A sentinel because it is a normal outcome of crawling the open web, where a
// link to a video is indistinguishable from a link to an article until the
// headers arrive, and counting it as a failure would make a working crawl look
// broken.
var ErrTooLarge = errors.New("body over the limit")

// Fetcher is the core of the downloader: one request, one response, no
// middleware.
//
// It is what the chain wraps, and on its own it is the whole downloader for a
// job that configured no plugins.
type Fetcher struct {
	// Client does the request. Required.
	Client *http.Client

	// Agent is the User-Agent to send when a request does not set one.
	Agent string

	// MaxBody refuses a body larger than this. Zero means no limit, which is
	// not what any job should run with and is what a test that does not care
	// gets.
	MaxBody int64

	// Timeout bounds one request, including reading the body. Zero leaves it
	// to the client and the context.
	Timeout time.Duration

	// Truncate keeps the first MaxBody bytes instead of refusing a body that
	// is larger.
	//
	// One caller: robots.txt. RFC 9309 §2.5 says to parse the first 500 KiB
	// and ignore the rest, and refusing instead meant a site whose robots.txt
	// was larger than that had every URL on it dropped, forever, at the cost
	// of re-downloading the file for each one. For a page, refusing is right:
	// half an article is worse than no article.
	Truncate bool
}

// Handle implements [Handler].
func (f *Fetcher) Handle(ctx context.Context, req *Request) (*Response, error) {
	if req == nil || req.URL == "" {
		return nil, errors.New("downloader: no URL to fetch")
	}

	// The timeout covers reading the body, not just the headers, because a
	// server that answers instantly and then dribbles a body for an hour is
	// the case a header-only timeout does not catch.
	if f.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, f.Timeout)
		defer cancel()
	}

	req = req.normalise(f.Agent)

	var body io.Reader
	if len(req.Body) > 0 {
		body = bytes.NewReader(req.Body)
	}

	httpReq, err := http.NewRequestWithContext(ctx, req.Method, req.URL, body)
	if err != nil {
		return nil, fmt.Errorf("downloader: %s %s: %w", req.Method, req.URL, err)
	}
	httpReq.Header = req.Header

	started := time.Now()
	httpResp, err := f.Client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("downloader: %s %s: %w", req.Method, req.URL, err)
	}
	defer httpResp.Body.Close()

	// Refused on the declared length first, so a four gigabyte file costs the
	// headers rather than four gigabytes. A server that lies or says nothing
	// is caught by the read below.
	if f.MaxBody > 0 && !f.Truncate && httpResp.ContentLength > f.MaxBody {
		return nil, fmt.Errorf("downloader: %s: %w: %d bytes declared, limit %d",
			req.URL, ErrTooLarge, httpResp.ContentLength, f.MaxBody)
	}

	read, err := readBody(httpResp.Body, f.MaxBody, f.Truncate)
	if err != nil {
		return nil, fmt.Errorf("downloader: %s: %w", req.URL, err)
	}

	final := req.URL
	if httpResp.Request != nil && httpResp.Request.URL != nil {
		final = httpResp.Request.URL.String()
	}

	return &Response{
		Request: req,
		URL:     final,
		Status:  httpResp.StatusCode,
		Header:  httpResp.Header,
		Body:    read,
		Fetched: started,
	}, nil
}

// readBody reads at most limit bytes, and either reports a body that wanted
// more or keeps what it got.
//
// One byte over the limit is read on purpose: without it a body of exactly the
// limit and a body of a gigabyte are the same read, and the only way to tell
// them apart is to have read the gigabyte.
func readBody(r io.Reader, limit int64, truncate bool) ([]byte, error) {
	if limit <= 0 {
		return io.ReadAll(r)
	}

	read, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(read)) > limit {
		if truncate {
			return read[:limit], nil
		}
		return nil, fmt.Errorf("%w: over %d bytes", ErrTooLarge, limit)
	}
	return read, nil
}
