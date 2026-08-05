// SPDX-License-Identifier: GPL-3.0-or-later

package downloader

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// ErrTooManyRedirects reports a request that was forwarded more times than the
// job allows. Not a drop: a site that redirects in a circle is broken, and a
// crawl that quietly counted it as politeness would hide that.
var ErrTooManyRedirects = errors.New("too many redirects")

// follower forwards a request to where the server says the thing actually is.
//
// # Why it is not a plugin
//
// For the same reason robots is not: there is one correct position. A redirect
// is a different URL, on a host that may have its own robots.txt, and a
// follower anywhere inside the robots check would fetch it without ever asking.
// So this wraps everything, robots included, and each hop re-enters the whole
// downloader from the top: checked against its own host's rules, passed through
// every middleware, cached under its own key.
//
// # Why the client does not do it
//
// net/http follows redirects itself, and a redirect it followed would be
// invisible to all of the above: the cache would hold the final body under the
// original URL, and the target host's robots.txt would never be read. So the
// client is told to hand 3xx back, and this decides what to do with it.
//
// # What this will become
//
// When the frontier exists, a redirect that leaves the host should become a
// frontier request rather than an inline hop, so dedup, scope and politeness
// each get a say. Following inline is right for the same-host case, which is
// nearly all of them, and is what this does today.
type follower struct{ max int }

func (f *follower) wrap(next Handler) Handler {
	return HandlerFunc(func(ctx context.Context, req *Request) (*Response, error) {
		asked := req
		trail := []string{req.URL}

		for hop := 0; ; hop++ {
			resp, err := next.Handle(ctx, req)
			if err != nil {
				return resp, err
			}

			target, ok := redirected(resp)
			if !ok {
				// The caller asked for the first URL and is owed a response
				// that says so. Where the body came from is [Response.URL],
				// and the two differing is what "this redirected" looks like.
				resp.Request = asked
				return resp, nil
			}

			if hop >= f.max {
				return nil, fmt.Errorf("downloader: %w: %s", ErrTooManyRedirects, strings.Join(trail, " -> "))
			}

			req = forward(req, resp, target)
			trail = append(trail, req.URL)
		}
	})
}

// redirected reports where a response says to go instead, if it does.
//
// A 3xx with no Location is not a redirect, whatever it says: there is nowhere
// to go, and the response is the only answer the caller is going to get.
func redirected(resp *Response) (*url.URL, bool) {
	switch resp.Status {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
	default:
		return nil, false
	}

	location := resp.Header.Get("Location")
	if location == "" {
		return nil, false
	}

	// Relative locations are legal and common, and are resolved against where
	// the body actually came from rather than what was asked for, which is what
	// makes a chain of relative redirects land in the right place.
	base, err := url.Parse(resp.URL)
	if err != nil {
		return nil, false
	}
	target, err := base.Parse(location)
	if err != nil {
		return nil, false
	}
	return target, true
}

// forward builds the next hop.
func forward(req *Request, resp *Response, target *url.URL) *Request {
	next := req.Clone()
	next.URL = target.String()

	// 303 says to fetch the other thing with GET, and 301 and 302 are treated
	// the same way for anything that had a body, which is what every client
	// does and therefore what every server expects. 307 and 308 exist because
	// they do not.
	switch resp.Status {
	case http.StatusSeeOther:
		next.Method, next.Body = http.MethodGet, nil
	case http.StatusMovedPermanently, http.StatusFound:
		if next.Method != http.MethodGet && next.Method != http.MethodHead {
			next.Method, next.Body = http.MethodGet, nil
		}
	}

	// Credentials were given to one host. A redirect to another is exactly how
	// they end up somewhere they were never meant to go, so they are dropped
	// rather than forwarded.
	if !sameHost(resp.URL, next.URL) {
		next.Header.Del("Authorization")
		next.Header.Del("Cookie")
	}
	return next
}

func sameHost(a, b string) bool {
	first, err := url.Parse(a)
	if err != nil {
		return false
	}
	second, err := url.Parse(b)
	if err != nil {
		return false
	}
	return strings.EqualFold(first.Host, second.Host)
}
