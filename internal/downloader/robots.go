// SPDX-License-Identifier: GPL-3.0-or-later

package downloader

import (
	"context"
	"fmt"
	"net/url"
	"sync"

	"github.com/rangertaha/scour/internal/chain"
	"github.com/rangertaha/scour/internal/robots"
)

// Errors a robots.txt decision produces. Both wrap [chain.ErrDrop], because a
// crawl that obeys robots.txt drops requests all day and none of it is a
// failure, and both are distinguishable with [errors.Is] so a log can say which
// happened.
var (
	// ErrDisallowed reports that the site's robots.txt refused this path.
	ErrDisallowed = fmt.Errorf("robots.txt disallows it: %w", chain.ErrDrop)

	// ErrNoRobots reports that the site's robots.txt could not be read, so
	// nothing about it is known and nothing is permitted.
	ErrNoRobots = fmt.Errorf("robots.txt could not be read: %w", chain.ErrDrop)
)

// guard refuses what a site's robots.txt refuses.
//
// # Why it is not a plugin
//
// Because there is exactly one correct place for it, and a position that can be
// configured is a position that can be configured wrongly. It wraps the entire
// chain, so a disallowed URL is refused before the cache is consulted, before a
// retry is scheduled and before anything else pays for it. Making it a
// `downloader` attribute rather than a `plugin` block means a job can turn it
// off, which is sometimes legitimate, but cannot quietly move it behind the
// cache, which never is.
//
// # Why it fetches through the core
//
// robots.txt is fetched with the bare fetcher rather than the chain. Through the
// chain it would be checked against robots.txt, which is a loop, and it would
// land in the page cache, where it would be served back long after the site
// changed its mind. What a site permits has to be current.
type guard struct {
	fetch Handler
	agent string

	mu    sync.Mutex
	known map[string]*answer
}

// answer is one host's robots.txt, or the fetch of it that is still in flight.
type answer struct {
	ready chan struct{}
	rules *robots.Rules
	err   error
}

func newGuard(fetch Handler, agent string) *guard {
	return &guard{fetch: fetch, agent: agent, known: map[string]*answer{}}
}

func (g *guard) wrap(next Handler) Handler {
	return HandlerFunc(func(ctx context.Context, req *Request) (*Response, error) {
		if err := g.check(ctx, req.URL); err != nil {
			return nil, err
		}
		return next.Handle(ctx, req)
	})
}

// check reports why this URL may not be fetched, or nil.
func (g *guard) check(ctx context.Context, rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		// Not a drop: a URL that will not parse is a bug upstream, and the
		// fetch is about to say so with a better message.
		return nil
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		// robots.txt is an HTTP protocol. Anything else is somebody else's
		// rules to enforce.
		return nil
	}

	rules, err := g.rules(ctx, parsed.Scheme+"://"+parsed.Host)
	if err != nil {
		return fmt.Errorf("%s: %w: %v", rawURL, ErrNoRobots, err)
	}
	if !rules.Allowed(g.agent, parsed.RequestURI()) {
		return fmt.Errorf("%s: %w", rawURL, ErrDisallowed)
	}
	return nil
}

// rules returns one host's robots.txt, fetching it at most once.
//
// A success is kept for the life of the stage, which is the life of the job on
// this node. A failure is not kept at all: a robots.txt that could not be
// fetched is a question that has not been answered rather than an answer, so
// the next request asks again and a network blip costs one request rather than
// a host.
func (g *guard) rules(ctx context.Context, origin string) (*robots.Rules, error) {
	g.mu.Lock()
	if found, ok := g.known[origin]; ok {
		g.mu.Unlock()
		<-found.ready
		return found.rules, found.err
	}
	pending := &answer{ready: make(chan struct{})}
	g.known[origin] = pending
	g.mu.Unlock()

	pending.rules, pending.err = g.load(ctx, origin)
	close(pending.ready)

	if pending.err != nil {
		g.mu.Lock()
		if g.known[origin] == pending {
			delete(g.known, origin)
		}
		g.mu.Unlock()
	}
	return pending.rules, pending.err
}

// robotsRedirects is how many hops a robots.txt fetch follows.
//
// RFC 9309 §2.3.1.2 asks for at least five. It has to follow at all: a site
// that moved, or that serves http and redirects to https, or that keeps one
// robots.txt for several hostnames, is entirely ordinary, and treating the
// redirect as an unreadable file would refuse the whole host.
const robotsRedirects = 5

// load fetches and reads one robots.txt.
//
// What each answer means is RFC 9309 §2.3.1: a 2xx is the rules, a 4xx is a
// site with nothing to say, and anything else is a site that could not tell us,
// which is not the same as a site that said yes.
func (g *guard) load(ctx context.Context, origin string) (*robots.Rules, error) {
	// Not the caller's context: this fetch is shared by every request to the
	// host, and one requester giving up must not answer for the rest. The
	// fetcher's own timeout still bounds it.
	ctx = context.WithoutCancel(ctx)

	target := origin + "/robots.txt"
	for hop := 0; ; hop++ {
		resp, err := g.fetch.Handle(ctx, &Request{URL: target})
		if err != nil {
			return nil, err
		}

		switch {
		case resp.OK():
			return robots.Parse(resp.Body), nil

		case resp.Status >= 400 && resp.Status < 500:
			// Nothing to obey, which is the overwhelmingly common case: most
			// sites have no robots.txt at all.
			return &robots.Rules{}, nil
		}

		// Followed here rather than by the client, which is told not to, and
		// not by the redirect follower, which wraps this and would be a loop.
		next, ok := redirected(resp)
		if !ok || hop >= robotsRedirects {
			return nil, fmt.Errorf("%s: status %d", target, resp.Status)
		}
		target = next.String()
	}
}
