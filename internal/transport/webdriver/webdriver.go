// SPDX-License-Identifier: GPL-3.0-or-later

// Package webdriver fetches pages with a real browser.
//
// It is an [http.RoundTripper], not a second kind of fetcher: RoundTrip drives
// a browser tab and returns the rendered DOM as a synthetic response. colly
// cannot tell the difference, so a JavaScript-rendered page goes through the
// same callbacks, the same queue and the same metrics as any other.
package webdriver

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/rangertaha/scour/internal/transport"
)

func init() {
	transport.Register("webdriver", func(cfg transport.Config) (http.RoundTripper, error) {
		return New(Options{
			UserAgent: cfg.UserAgent,
			// A render is not a round trip: it includes every request the page
			// makes, so it gets its own timeout rather than the HTTP one.
			Timeout:  cfg.Browser.Timeout,
			Pool:     cfg.Browser.Pool,
			ExecPath: cfg.Browser.ExecPath,
		})
	})
}

// DefaultPool is how many tabs may render at once.
//
// A browser tab costs orders of magnitude more than a socket, in memory and in
// CPU, so this is deliberately far below the crawl's own concurrency. The
// bound is the point: without it a wide crawl of a JavaScript-heavy site would
// open a tab per worker and exhaust the machine.
const DefaultPool = 2

// Options configures the browser transport.
type Options struct {
	// UserAgent is what the browser reports.
	UserAgent string
	// Timeout bounds one page render, including waiting for the network to
	// settle.
	Timeout time.Duration
	// Pool caps how many tabs render at once.
	Pool int
	// ExecPath overrides the browser binary.
	ExecPath string
}

func (o Options) timeout() time.Duration {
	if o.Timeout <= 0 {
		return 45 * time.Second
	}
	return o.Timeout
}

// Transport renders pages in a browser.
type Transport struct {
	opts Options
	slot chan struct{} // the pool, as a counting semaphore

	mu     sync.Mutex
	alloc  context.Context
	cancel context.CancelFunc
}

// New returns a browser transport. The browser itself starts lazily on the
// first request, so building one costs nothing if no page ever needs it.
func New(opts Options) (*Transport, error) {
	size := opts.Pool
	if size <= 0 {
		size = DefaultPool
	}
	return &Transport{
		opts: opts,
		slot: make(chan struct{}, size),
	}, nil
}

// Close shuts the browser down.
func (t *Transport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cancel != nil {
		t.cancel()
		t.cancel = nil
		t.alloc = nil
	}
	return nil
}

// allocator starts the browser once and reuses it.
func (t *Transport) allocator() context.Context {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.alloc != nil {
		return t.alloc
	}

	opts := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	opts = append(opts,
		chromedp.DisableGPU,
		chromedp.NoSandbox,
		// A crawler wants the page, not the pictures.
		chromedp.Flag("blink-settings", "imagesEnabled=false"),
	)
	if t.opts.UserAgent != "" {
		opts = append(opts, chromedp.UserAgent(t.opts.UserAgent))
	}
	if t.opts.ExecPath != "" {
		opts = append(opts, chromedp.ExecPath(t.opts.ExecPath))
	}

	alloc, cancel := chromedp.NewExecAllocator(context.Background(), opts...)
	t.alloc = alloc
	t.cancel = cancel
	return alloc
}

// RoundTrip implements [http.RoundTripper].
//
// Only GET is rendered. Anything else falls through as an error rather than
// being quietly turned into a navigation, since a browser cannot honestly
// represent a POST as a page load.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodGet && req.Method != "" {
		return nil, fmt.Errorf("webdriver: %s is not a page load", req.Method)
	}

	select {
	case t.slot <- struct{}{}:
		defer func() { <-t.slot }()
	case <-req.Context().Done():
		return nil, req.Context().Err()
	}

	tab, closeTab := chromedp.NewContext(t.allocator())
	defer closeTab()

	// Bind the tab to the request's deadline, so a page that never settles is
	// abandoned rather than holding a pool slot forever.
	tab, cancelTab := context.WithTimeout(tab, t.opts.timeout())
	defer cancelTab()
	if deadline, ok := req.Context().Deadline(); ok {
		var cancelReq context.CancelFunc
		tab, cancelReq = context.WithDeadline(tab, deadline)
		defer cancelReq()
	}

	var body string
	err := chromedp.Run(tab,
		chromedp.Navigate(req.URL.String()),
		// Waiting for the body is the cheapest signal that rendering has
		// started; the page may still be filling in, which is what the
		// settle below is for.
		chromedp.WaitReady("body", chromedp.ByQuery),
		chromedp.Sleep(settle),
		chromedp.OuterHTML("html", &body, chromedp.ByQuery),
	)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("webdriver: %s did not render in %s", req.URL, t.opts.timeout())
		}
		return nil, fmt.Errorf("webdriver: render %s: %w", req.URL, err)
	}

	return synthesise(req, body), nil
}

// settle is how long to wait after the document is ready for scripts to fill
// the page in. It is a compromise: too short and the crawl reads a half-built
// page, too long and every render pays for the slowest site on the internet.
const settle = 300 * time.Millisecond

// synthesise wraps rendered HTML in the response colly expects.
//
// The status is 200 because the render succeeded; the browser has already
// followed whatever redirects and errors the network produced, and reporting
// those again would double-count them.
func synthesise(req *http.Request, body string) *http.Response {
	if !strings.HasPrefix(strings.TrimSpace(body), "<") {
		body = "<html>" + body + "</html>"
	}
	data := []byte(body)

	return &http.Response{
		Status:        "200 OK",
		StatusCode:    http.StatusOK,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
		Body:          io.NopCloser(bytes.NewReader(data)),
		ContentLength: int64(len(data)),
		Request:       req,
	}
}
