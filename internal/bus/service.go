// SPDX-License-Identifier: GPL-3.0-or-later

package bus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/rangertaha/scour/internal/classify/store"
	"github.com/rangertaha/scour/internal/event"
)

// The plumbing the services share.
//
// # Why a service and not a file each node opens
//
// The entity graph and the event store are shared: two jobs crawling different
// sites should agree about who Acme is, and a series is only a series if
// everything writing to it writes to the same one. Both are SQLite, which has
// one writer, so a cluster where each node opened the file would be a cluster
// where the answer depended on which node you asked and where two nodes writing
// at once failed. One process owns the store and answers for it.
//
// # Why one subject per operation
//
// Because a subject is what NATS routes, counts and lets you subscribe to, and
// an envelope with an operation field inside it makes all of that opaque: every
// consumer sees one subject called "entity" and has to open the payload to know
// what happened. The cost is a subscription each, which is what
// [serving] keeps from becoming fifteen error checks.
//
// The subjects are not per job, unlike the stages. A stage belongs to the crawl
// that configured it; these stores belong to everybody, which is the whole
// argument for them existing.

// call makes one request and decodes the reply.
//
// The reply carries an error string rather than the transport carrying it,
// because "the store said no" and "nothing answered" are different things and a
// caller has to be able to tell them apart: the first is an answer.
func call[Req, Res any](ctx context.Context, c *Conn, subject string, wait time.Duration, req Req) (Res, error) {
	var zero Res

	payload, err := json.Marshal(req)
	if err != nil {
		return zero, fmt.Errorf("bus: %s: %w", subject, err)
	}

	timed, cancel := withTimeout(ctx, wait)
	defer cancel()

	msg, err := c.RequestWithContext(timed, subject, payload)
	if err != nil {
		return zero, fmt.Errorf("bus: %s: %w", subject, noResponders(err))
	}

	var reply struct {
		Result   Res    `json:"result"`
		Error    string `json:"error,omitempty"`
		Sentinel string `json:"sentinel,omitempty"`
	}
	if err := json.Unmarshal(msg.Data, &reply); err != nil {
		return zero, fmt.Errorf("bus: %s: %w", subject, err)
	}
	if reply.Error != "" {
		return zero, remoteError(reply.Error, reply.Sentinel)
	}
	return reply.Result, nil
}

// Sentinels are the errors a caller is expected to test for with [errors.Is],
// and which therefore have to survive a trip over the bus.
//
// An error crosses as a string, so a client that rebuilt it with fmt.Errorf got
// something that read correctly and matched nothing: errors.Is(err,
// event.ErrNotFound) was true against a local store and false against the same
// store one hop away, so a client could not tell "not there" from "went wrong"
// and the conformance suites could not either. The name travels beside the
// message and is put back on the other side.
//
// A short list on purpose. Everything here is a decision a caller branches on;
// an error it only reports does not need to be here, and adding one that nobody
// tests for would be carrying a name for nothing.
var sentinels = map[string]error{
	"event.ErrNotFound":      event.ErrNotFound,
	"classify.ErrNotTrained": store.ErrNotTrained,
	"bus.ErrNoStage":         ErrNoStage,
}

// nameOf is the sentinel an error is, if it is one.
func nameOf(err error) string {
	for name, sentinel := range sentinels {
		if errors.Is(err, sentinel) {
			return name
		}
	}
	return ""
}

// remote is an error that came back from a service.
//
// It keeps the message the store wrote and unwraps to the sentinel the store
// meant, so errors.Is works the same either side of the bus and the text does
// not gain a second copy of itself.
type remote struct {
	msg string
	is  error
}

func (r *remote) Error() string { return r.msg }
func (r *remote) Unwrap() error { return r.is }

func remoteError(msg, sentinel string) error {
	return &remote{msg: msg, is: sentinels[sentinel]}
}

// Answered reports whether an error is one a service replied with, rather than
// a failure to reach one.
//
// The distinction the reply envelope exists to keep, made usable by a caller.
// "The service refused this" and "nothing is listening" are different things to
// do about, and a command line that conflated them would exit the same way for
// a job document the cluster rejected and for a cluster that is not running.
func Answered(err error) bool {
	var r *remote
	return errors.As(err, &r)
}

// serve subscribes one operation, in a queue group so that starting a second
// service does not double every write.
func serve[Req, Res any](c *Conn, subject, queue string, wait time.Duration, handle func(context.Context, Req) (Res, error)) (*nats.Subscription, error) {
	sub, err := c.QueueSubscribe(subject, queue, func(msg *nats.Msg) {
		var req Req
		var reply struct {
			Result   Res    `json:"result"`
			Error    string `json:"error,omitempty"`
			Sentinel string `json:"sentinel,omitempty"`
		}

		if err := json.Unmarshal(msg.Data, &req); err != nil {
			reply.Error = fmt.Sprintf("bus: %s: %v", subject, err)
		} else {
			// A context of the handler's own. The request that arrived carries
			// no deadline, and a store call with none is one that can hold a
			// connection open for as long as the store is unwell.
			//
			// How long is the service document's to say, which is why it
			// arrives here rather than being read from the package constant:
			// `timeout` was documented as how long one request may take and
			// then never reached the request. Zero still means [Timeout].
			ctx, cancel := withTimeout(context.Background(), wait)
			result, err := handle(ctx, req)
			cancel()

			if err != nil {
				reply.Error = err.Error()
				reply.Sentinel = nameOf(err)
			} else {
				reply.Result = result
			}
		}

		out, err := json.Marshal(reply)
		if err != nil {
			// The reply cannot be encoded, so say that rather than leaving the
			// caller to time out: a caller waiting on nothing is the one
			// failure mode a request/reply service must not have.
			out = []byte(`{"error":"bus: ` + subject + `: the reply could not be encoded"}`)
		}
		// Deliberately dropped, and it is the one ignored error here worth
		// explaining. The lines above encode an error reply precisely so a
		// caller is never left waiting, so failing to send it is the same
		// failure arriving by another route.
		//
		// There is nothing to do about it from inside a handler: the send fails
		// when the connection is already gone, there is no error path out of a
		// nats.MsgHandler, and this function has no logger. The caller's own
		// timeout is the backstop. Giving the service a logger so this could be
		// reported is worth doing and is not a lint fix.
		_ = msg.Respond(out)
	})
	if err != nil {
		return nil, fmt.Errorf("bus: serve %s: %w", subject, err)
	}
	return sub, nil
}

// none is the reply of an operation that returns nothing but an error.
//
// A named type rather than struct{}, so the generic reply above has something
// to decode into and the wire shape of "it worked" is the same as everything
// else.
type none struct{}

// Service is a set of subscriptions answering for one store.
type Service struct {
	subs []*nats.Subscription
	err  error

	// wait bounds one request, for every operation this service registers.
	//
	// Held here rather than passed to each serving call because a service
	// whose operations disagreed about how long they may take is a service
	// nobody can reason about, and because it is one place for the next
	// ServeX to find. Zero means [Timeout].
	wait time.Duration
}

// serving registers one operation, remembering the first failure rather than
// returning it.
//
// Collected rather than returned because a service is a list of subscriptions
// and checking each one at its call site would bury what the service actually
// does under error handling. The caller checks once, at the end, and a partial
// service is closed rather than returned.
func serving[Req, Res any](c *Conn, s *Service, subject, queue string, handle func(context.Context, Req) (Res, error)) {
	if s.err != nil {
		return
	}
	sub, err := serve(c, subject, queue, s.wait, handle)
	if err != nil {
		s.err = err
		return
	}
	s.subs = append(s.subs, sub)
}

// ready reports the service usable, once every subscription above is live on
// the server.
//
// Subscribing is asynchronous: the call returns as soon as the client has
// queued it, and until the server has processed it a request on that subject
// finds nothing serving. A service that returned before flushing therefore
// worked whenever the caller was slow and failed whenever it was not, which is
// the shape that reads as an unreliable network rather than as a missing flush.
// It surfaced as a test asking a topic service for a model microseconds after
// starting it, under load.
//
// Called by every ServeX before it returns, so no service can be handed back
// half-registered.
func (s *Service) ready(c *Conn) error {
	if s.err != nil {
		// Closed, not merely reported. A registration that failed partway
		// leaves every subscription that already succeeded in the queue group,
		// so a caller told "nothing is serving" would in fact have a
		// half-registered member competing for the operations that did
		// register, answering them from a store the caller believed it had
		// abandoned. serving's own comment promised this and it was not
		// happening.
		_ = s.Close()
		return s.err
	}
	if err := c.Flush(); err != nil {
		_ = s.Close()
		return fmt.Errorf("bus: %w", err)
	}
	return nil
}

// Close stops taking work and waits for the requests already taken.
//
// Drained rather than unsubscribed, and this stopped being a detail the moment
// anything depended on it. Unsubscribing marks the subscription closed, and
// NATS then abandons every message already sitting in it without replying:
// core NATS does not redeliver, so a caller that had been routed to this member
// waits out its whole timeout for an answer nobody was ever going to give. Two
// minutes, by default, for a write the cluster had already accepted.
//
// It also returned while a handler was still running, which is worse, because
// `scour server` closes the store immediately afterwards: the handler finished
// against a closed database and reported a failure for work that had been
// accepted.
//
// See [Drain] for why this is shared with the node rather than written twice.
func (s *Service) Close() error {
	err := Drain(s.subs, DrainFor)
	s.subs = nil
	return err
}
