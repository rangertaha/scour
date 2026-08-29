// SPDX-License-Identifier: GPL-3.0-or-later

package downloader

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sync"
	"time"

	"github.com/rangertaha/scour/internal/chain"
	"github.com/rangertaha/scour/internal/robots"
	"github.com/rangertaha/scour/internal/urls"
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
		asked, err := g.check(ctx, req.URL, g.agentFor(req))
		if err != nil {
			return nil, err
		}

		resp, err := next.Handle(ctx, req)
		if err != nil || resp == nil {
			return resp, err
		}

		// Reported on every response rather than once per host.
		//
		// Saying it once and remembering was tried and is wrong, because
		// nothing here can know whether a report survives: this wrapper is
		// inside the redirect follower, which throws away every response but
		// the last, so the one hop that carried the number was usually the one
		// discarded. A site redirecting http to https, which robots.txt's own
		// documentation calls entirely ordinary, lost its `Crawl-delay`
		// permanently, because the guard had already crossed the host off.
		//
		// So the number rides on every response and the scheduler writes it
		// once. Only the stage holding the frontier knows what it has already
		// recorded, which makes it the only stage that can decide not to.
		//
		// Appended rather than assigned: a redirect re-enters this wrapper, so
		// an inner hop may have put its own host on the response already.
		if host := urls.Host(req.URL); host != "" {
			resp.Delays = append(resp.Delays, CrawlDelay{Host: host, Delay: asked})
		}
		return resp, nil
	})
}

// check reports why this URL may not be fetched, or nil, and what the host's
// robots.txt asked for between requests.
//
// The delay comes back from here because this is the one place that already has
// the rules in hand. Reading the file a second time to ask it a second question
// would be a second fetch of somebody's robots.txt per page.
// agentFor is the user agent this request will actually be sent under.
//
// The job's, unless the request carries its own. A middleware may set one -
// that is a supported thing to do, and rotating them is why - and the guard was
// checking the job's agent against the rules while the wire carried another. A
// site that disallows `acmebot` and allows everybody else was read as allowing,
// and then asked for the disallowed path with `User-Agent: acmebot`: refused by
// name, fetched anyway.
//
// The parsed rules are agent-independent and cached per host, so asking about a
// different agent costs nothing and does not re-fetch anything.
func (g *guard) agentFor(req *Request) string {
	if req != nil && req.Header != nil {
		if sending := req.Header.Get("User-Agent"); sending != "" {
			return sending
		}
	}
	return g.agent
}

func (g *guard) check(ctx context.Context, rawURL, agent string) (time.Duration, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		// Not a drop: a URL that will not parse is a bug upstream, and the
		// fetch is about to say so with a better message.
		return 0, nil
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		// robots.txt is an HTTP protocol. Anything else is somebody else's
		// rules to enforce.
		return 0, nil
	}

	rules, err := g.rules(ctx, parsed.Scheme+"://"+parsed.Host)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", rawURL, errors.Join(ErrNoRobots, err))
	}
	if !rules.Allowed(agent, parsed.RequestURI()) {
		return 0, fmt.Errorf("%s: %w", rawURL, ErrDisallowed)
	}

	// A file with no `Crawl-delay` and a file asking for none are both reported
	// as zero, and both are worth telling the scheduler: what it must not do is
	// go on applying a delay a site has stopped asking for.
	delay, _ := rules.Delay(agent)
	return delay, nil
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
