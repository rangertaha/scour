// SPDX-License-Identifier: GPL-3.0-or-later

package downloader

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/rangertaha/scour/internal/chain"
	"github.com/rangertaha/scour/internal/scope"
	"github.com/rangertaha/scour/internal/urls"
)

// ErrTooManyRedirects reports a request that was forwarded more times than the
// job allows. Not a drop: a site that redirects in a circle is broken, and a
// crawl that quietly counted it as politeness would hide that.
var ErrTooManyRedirects = errors.New("too many redirects")

// ErrRedirectOutOfScope reports a redirect pointing somewhere the job said it
// would not go.
//
// A drop, because it is the ordinary outcome for a URL outside the scope and
// that is what the scheduler calls the same thing. Sites redirect off their own
// hosts all day, to a login, a consent wall, a CDN or somebody who bought the
// domain, and none of that is a crawl going wrong.
var ErrRedirectOutOfScope = fmt.Errorf("the redirect leaves the job's scope: %w", chain.ErrDrop)

// allowed reports why a redirect target may not be followed, or nil.
//
// The URL is normalised first, because that is what the scope was built to
// compare against: the scheduler normalises before it checks, and a check
// against the raw form would answer a different question about the same page.
func (f *follower) allowed(target *url.URL) error {
	if f.bounds == nil {
		return nil
	}

	normalised, err := urls.Normalise(target.String(), urls.Options{})
	if err != nil {
		// Not somewhere this job may go, because it is not anywhere: a target
		// that will not normalise is not a URL the crawl can hold.
		return fmt.Errorf("%s: %w", target, ErrRedirectOutOfScope)
	}
	if !f.bounds.Allows(normalised) {
		return fmt.Errorf("%s: %w", normalised, ErrRedirectOutOfScope)
	}
	return nil
}

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
// # A hop is checked against the job's scope
//
// Because the stage that normally does that has already run. The scheduler
// drops an out-of-scope URL before it is queued, and a redirect happens after
// queueing, inside the fetch: so a site could hand a crawl any URL in the world
// and it would be fetched. It was not theoretical, and it was not subtle
// either. A job naming one host followed a redirect to another and fetched it,
// robots.txt and all, having been told to stay put.
//
// That is worse than an ordinary scope leak. Every other URL a crawl considers
// came from a page the job chose to read; this one is chosen by whoever
// controls a page the crawl was already fetching, which makes it the one place
// a third party picks the next request.
//
// An out-of-scope hop is a drop rather than a failure, which is how the
// scheduler treats the same URL discovered as a link. The page is not fetched
// and the crawl carries on.
//
// # What this will still become
//
// A redirect that stays inside the scope but moves to another host is still
// followed inline, so that host is fetched without being paced. Making the hop
// a frontier request instead would give dedup and politeness a say too, and is
// the shape this wants in the end.
type follower struct {
	max int

	// bounds is the job's scope, or nil for a caller that has none: a job with
	// no domains, included or excluded allows everything, which is what `scour
	// try` on a single URL means.
	bounds *scope.Scope
}

func (f *follower) wrap(next Handler) Handler {
	return HandlerFunc(func(ctx context.Context, req *Request) (*Response, error) {
		asked := req
		trail := []string{req.URL}

		// What the hops asked for between requests. Every response but the last
		// is thrown away here, and each one carries the `Crawl-delay` of the
		// host that served it, so anything learnt on the way has to be brought
		// along or it is lost: a chain crossing two hosts would leave the first
		// paced at the job's own rate for the rest of the crawl, with its file
		// read and understood and dropped one frame up.
		var learnt []CrawlDelay

		for hop := 0; ; hop++ {
			resp, err := next.Handle(ctx, req)
			if err != nil {
				return resp, err
			}
			learnt = append(learnt, resp.Delays...)

			target, ok := redirected(resp)
			if !ok {
				// The caller asked for the first URL and is owed a response
				// that says so. Where the body came from is [Response.URL],
				// and the two differing is what "this redirected" looks like.
				resp.Request = asked
				resp.Delays = learnt
				return resp, nil
			}

			if hop >= f.max {
				return nil, fmt.Errorf("downloader: %w: %s", ErrTooManyRedirects, strings.Join(trail, " -> "))
			}

			// Checked before the hop is taken, so a URL outside the job's scope
			// is not fetched and its host's robots.txt is not fetched either.
			// Checking after would already have made both requests.
			if err := f.allowed(target); err != nil {
				return nil, err
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
