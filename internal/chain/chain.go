// SPDX-License-Identifier: GPL-3.0-or-later

// Package chain runs a stage's middleware.
//
// Every stage in scour is a core that does the work, wrapped in links that see
// what goes in and what comes back. The downloader fetches, wrapped by robots,
// retry and cache. The spider extracts, wrapped by depth and offsite. The
// scheduler queues, wrapped by dupefilter and the ordering policy. One
// mechanism, one set of rules about order, one thing to learn.
//
// # A link wraps, it does not hook
//
// A link is given the next handler and returns a replacement. Whatever it does
// before calling next is the way out; whatever it does after is the way back:
//
//	func timing(next chain.Handler[*Request, *Response]) chain.Handler[*Request, *Response] {
//		return chain.Func(func(ctx context.Context, req *Request) (*Response, error) {
//			started := time.Now()          // on the way out
//			resp, err := next.Handle(ctx, req)
//			log.Println(time.Since(started)) // on the way back
//			return resp, err
//		})
//	}
//
// This is the shape [net/http] middleware has, for the reasons it has it.
// A pair of Request and Response methods would be the obvious alternative and
// is worse in three ways: it needs a convention for a link that wants to
// short-circuit, it makes a link that needs state across the two directions
// stash it somewhere, and it cannot express "run this on the way back even
// though the way out failed", which is what a timer and a stats counter both
// want.
//
// # Short-circuit and drop
//
// Both fall out of wrapping rather than needing anything added:
//
//   - **Short-circuit**: return a result without calling next. A cache hit is
//     this and nothing more. Links outside still see the result on the way back;
//     links inside never ran.
//   - **Drop**: return [ErrDrop]. robots.txt refusing a URL is this. It is a
//     sentinel rather than an ordinary error because a dropped request is a
//     normal outcome of a working crawl, and counting it as a failure would
//     make every politely-behaved crawl look broken.
//
// These two are in the contract from the start because they cannot be added to
// it later without changing every link ever written.
package chain

import (
	"context"
	"errors"
	"sort"
)

// ErrDrop reports that a link refused what it was given, on purpose.
//
// It is not a failure. A crawl that obeys robots.txt drops requests all day,
// and a caller distinguishes this from a real error with [errors.Is].
var ErrDrop = errors.New("dropped")

// Dropped reports whether an error means a link refused the work rather than
// failed at it.
func Dropped(err error) bool { return errors.Is(err, ErrDrop) }

// Handler does a stage's work: something in, something out.
type Handler[In, Out any] interface {
	Handle(ctx context.Context, in In) (Out, error)
}

// Func adapts an ordinary function to [Handler].
type Func[In, Out any] func(ctx context.Context, in In) (Out, error)

// Handle implements [Handler].
func (f Func[In, Out]) Handle(ctx context.Context, in In) (Out, error) { return f(ctx, in) }

// Wrapper replaces a handler with one that wraps it.
type Wrapper[In, Out any] func(next Handler[In, Out]) Handler[In, Out]

// Link is one middleware, with the name and position it was configured under.
type Link[In, Out any] struct {
	// Name is the plugin's name, for errors and for logs.
	Name string

	// Order is where it sits, lowest first. Lowest is outermost: it runs first
	// on the way out and last on the way back, so it is nearest whoever asked
	// and furthest from whatever does the work.
	Order int

	// Wrap builds the middleware around the rest of the chain.
	Wrap Wrapper[In, Out]
}

// Build returns core wrapped in links, ordered.
//
// The lowest order ends up outermost. Ties keep the order they were given, so
// a document that lists two links at the same position gets them in the order
// it listed them rather than in whichever order a map happened to produce.
//
// Building does not copy the links, and the result is safe for concurrent use
// as long as the links are.
func Build[In, Out any](core Handler[In, Out], links []Link[In, Out]) Handler[In, Out] {
	if core == nil {
		panic("chain: built without a core handler")
	}

	ordered := make([]Link[In, Out], len(links))
	copy(ordered, links)
	sort.SliceStable(ordered, func(a, b int) bool { return ordered[a].Order < ordered[b].Order })

	// Wrapped from the inside out, so the first link ends up outermost.
	handler := core
	for i := len(ordered) - 1; i >= 0; i-- {
		link := ordered[i]
		if link.Wrap == nil {
			continue
		}
		next := handler
		wrapped := link.Wrap(next)
		if wrapped == nil {
			// A link that returns nothing would panic on the next request with
			// no clue as to which one did it.
			panic("chain: middleware " + link.Name + " wrapped to nil")
		}
		handler = wrapped
	}
	return handler
}

// Names lists the links in the order they will run on the way out, which is
// what a plan or a log line wants to show.
func Names[In, Out any](links []Link[In, Out]) []string {
	ordered := make([]Link[In, Out], len(links))
	copy(ordered, links)
	sort.SliceStable(ordered, func(a, b int) bool { return ordered[a].Order < ordered[b].Order })

	out := make([]string, 0, len(ordered))
	for _, link := range ordered {
		out = append(out, link.Name)
	}
	return out
}
