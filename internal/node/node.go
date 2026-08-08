// SPDX-License-Identifier: GPL-3.0-or-later

// Package node is one machine in a cluster.
//
// A node joins a bus, watches the jobs, and serves whatever stages it was told
// to serve for whatever jobs appear. That is the whole of it: no election, no
// coordinator, no assignment. Work is distributed by queue group, so two nodes
// serving one job's downloader share it because NATS hands each request to one
// of them, and a node that dies takes nothing with it but the request it was
// holding.
//
// # A node is not a scheduler
//
// One node per job drives the crawl: it owns the frontier, and the frontier
// cannot be shared, because two schedulers handing out the same host cannot
// honour a crawl delay between them. Every other node serves stages. That
// asymmetry is not an implementation detail to be fixed later; it is the
// politeness rule, and it is why `--worker` exists.
package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/nats-io/nats.go"

	"github.com/rangertaha/scour/internal/bus"
	"github.com/rangertaha/scour/internal/cache"
	"github.com/rangertaha/scour/internal/downloader"
	"github.com/rangertaha/scour/internal/engine"
	"github.com/rangertaha/scour/internal/spider"
)

// Stages a node may serve.
const (
	// StageDownload fetches pages for other nodes.
	StageDownload = "download"

	// StageRead extracts items for other nodes.
	StageRead = "read"
)

// Options are how a node is set up.
type Options struct {
	// Name identifies this node to the rest of the cluster.
	Name string

	// Serve lists the stages this node offers. Empty offers both, which is
	// what a machine added to a cluster to do more work means.
	Serve []string

	// Bodies is the cache the stages read and write. Required: it is how a
	// body gets from the node that fetched it to the node that reads it,
	// because a body never crosses the bus.
	Bodies cache.Store

	// Log is where a node says what it picked up.
	Log *slog.Logger

	// Eval resolves `secret()` in plugin configuration. Nil means a job whose
	// plugins reference a secret is refused here, naming the secret, which is
	// the right answer on a node with no way to read one.
	Eval *hcl.EvalContext
}

// Node is a member of a cluster.
type Node struct {
	conn  *bus.Conn
	opts  Options
	log   *slog.Logger
	jobs  *bus.Jobs
	nodes *bus.Nodes

	mu      sync.Mutex
	serving map[string]*served

	// closed is set by Close, so a serve that was building when it ran does not
	// put its subscriptions into the map afterwards. Without it the node kept
	// answering for a job after Close had returned, with nothing holding a
	// handle on the subscriptions.
	closed bool

	// leave stops the presence renewal. Held so Close can end it: a closed node
	// that kept renewing was listed in the registry with its stages while
	// answering nothing.
	leave func()
}

// served is what one node is running for one job.
type served struct {
	revision uint64
	subs     []*nats.Subscription
	closers  []func() error

	// stop ends the context the handlers run on, and done is closed when the
	// last of them has returned. They are separate from the context that says
	// when to stop serving, for the reason in [served.close].
	stop context.CancelFunc
	work *inFlight
}

// inFlight counts the handlers that have started and not finished.
//
// A NATS subscription's Drain is asynchronous: it marks the subscription and
// returns, and the pending messages are handled on another goroutine. Closing
// the stages straight after it therefore closed a cache and a chain out from
// under handlers that were still using them, which is a use after close: an
// error at best and a panic inside a callback goroutine at worst, taking the
// node with it.
type inFlight struct {
	mu    sync.Mutex
	count int
	idle  chan struct{}
}

func newInFlight() *inFlight {
	f := &inFlight{idle: make(chan struct{})}
	close(f.idle)
	return f
}

func (f *inFlight) Enter() {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.count == 0 {
		f.idle = make(chan struct{})
	}
	f.count++
}

func (f *inFlight) Leave() {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.count--
	if f.count == 0 {
		close(f.idle)
	}
}

// wait blocks until nothing is in flight, or until it has waited long enough
// that something is plainly stuck. A node that hung forever on shutdown would
// be worse than one that gave up on a request.
func (f *inFlight) wait(limit time.Duration) {
	f.mu.Lock()
	idle := f.idle
	f.mu.Unlock()

	select {
	case <-idle:
	case <-time.After(limit):
	}
}

// Join connects a node to a cluster and registers it.
func Join(ctx context.Context, conn *bus.Conn, opts Options) (*Node, error) {
	if conn == nil {
		return nil, errors.New("node: no connection")
	}
	if opts.Bodies == nil {
		return nil, errors.New("node: no cache, and a body never crosses the bus")
	}
	if opts.Name == "" {
		return nil, errors.New("node: a node needs a name")
	}
	if opts.Log == nil {
		opts.Log = slog.New(slog.DiscardHandler)
	}
	if len(opts.Serve) == 0 {
		opts.Serve = []string{StageDownload, StageRead}
	}

	jobs, err := conn.OpenJobs(ctx)
	if err != nil {
		return nil, err
	}
	nodes, err := conn.OpenNodes(ctx)
	if err != nil {
		return nil, err
	}

	n := &Node{
		conn:    conn,
		opts:    opts,
		log:     opts.Log.With("node", opts.Name),
		jobs:    jobs,
		nodes:   nodes,
		serving: map[string]*served{},
	}

	announcement, err := json.Marshal(struct {
		Stages []string `json:"stages"`
		Bus    string   `json:"bus"`
	}{Stages: opts.Serve, Bus: conn.Address()})
	if err != nil {
		return nil, fmt.Errorf("node: %w", err)
	}
	leave, err := nodes.Announce(ctx, opts.Name, announcement)
	if err != nil {
		return nil, err
	}
	n.leave = leave
	return n, nil
}

// Watch serves jobs as they appear and stops serving them as they go away.
//
// It returns when the context ends, having torn down everything it was serving,
// so a node leaving a cluster leaves nothing behind but the request it was
// holding.
func (n *Node) Watch(ctx context.Context) error {
	changes, stop, err := n.jobs.Watch(ctx)
	if err != nil {
		return err
	}
	defer stop()

	for {
		select {
		case <-ctx.Done():
			n.stopAll()
			return nil

		case change, open := <-changes:
			if !open {
				n.stopAll()

				// The channel closing while the context is still live is not
				// this node being asked to leave: jetstream closes it when the
				// connection drops or the consumer fails, so the node has
				// stopped noticing jobs and nothing said so. Returning nil made
				// `scour serve` print "has left" and exit zero, so a supervisor
				// set to restart on failure never restarted it and the machine
				// sat there serving whatever it happened to have.
				if err := ctx.Err(); err == nil {
					n.log.ErrorContext(ctx, "the job watch ended, so this node has stopped noticing jobs")
					return errors.New("node: the job watch ended, so this node has stopped noticing jobs")
				}
				return nil
			}
			if change.Replayed {
				n.log.InfoContext(ctx, "caught up", "serving", n.Serving())
				continue
			}
			if change.Deleted {
				n.stop(change.Name)
				n.log.InfoContext(ctx, "job went away", "job", change.Name)
				continue
			}
			if err := n.serve(ctx, change); err != nil {
				// A job this node cannot serve is not a node that should stop:
				// the rest of the cluster may be able to, and the next
				// submission may fix it.
				n.log.WarnContext(ctx, "cannot serve job", "job", change.Name, "error", err)
			}
		}
	}
}

// serve builds this node's stages for one job.
func (n *Node) serve(ctx context.Context, change bus.Change) error {
	doc, err := engine.Parse(change.Document, change.Name+".hcl")
	if err != nil {
		return err
	}
	if err := doc.Validate(); err != nil {
		return err
	}

	var job *engine.Job
	for _, candidate := range doc.Jobs {
		if candidate.Name == change.Name {
			job = candidate
		}
	}
	if job == nil {
		return fmt.Errorf("node: %q holds no job of that name", change.Name)
	}

	// A job that changed is torn down and built again, rather than patched.
	// The stages are built from the document, and half of a stage built from
	// one revision and half from another is a thing nobody can debug.
	n.stop(change.Name)

	// The handlers run on a context of this job's own, not on the one that
	// says when to stop serving. Sharing them meant a SIGTERM aborted the
	// fetch a handler was in the middle of and it answered "context
	// canceled", so the drain that was supposed to guarantee an answer
	// guaranteed a failure instead.
	work, stop := context.WithCancel(context.WithoutCancel(ctx))
	running := &served{revision: change.Revision, stop: stop, work: newInFlight()}

	for _, stage := range n.opts.Serve {
		switch stage {
		case StageDownload:
			built, err := downloader.New(ctx, job, downloader.Options{Eval: n.opts.Eval})
			if err != nil {
				running.close()
				return err
			}
			sub, err := n.conn.ServeDownloader(work, job.Name, built, n.opts.Bodies, running.work)
			if err != nil {
				built.Close()
				running.close()
				return err
			}
			running.subs = append(running.subs, sub)
			running.closers = append(running.closers, built.Close)

		case StageRead:
			built, err := spider.New(ctx, job, spider.Options{Eval: n.opts.Eval})
			if err != nil {
				running.close()
				return err
			}
			sub, err := n.conn.ServeSpider(work, job.Name, built, n.opts.Bodies, running.work)
			if err != nil {
				built.Close()
				running.close()
				return err
			}
			running.subs = append(running.subs, sub)
			running.closers = append(running.closers, built.Close)

		default:
			running.close()
			return fmt.Errorf("node: nothing here serves a %q stage", stage)
		}
	}

	// Checked again under the lock, because serve releases it to build the
	// stages and subscribe, which takes as long as opening a cache and a
	// database. A Close in that window swapped in an empty map and returned,
	// having stopped nothing for this job, and then this line put the new
	// subscriptions into the fresh map where nothing would ever close them: the
	// node went on answering for a job after Close had returned, and a second
	// Close could not help because it had no handle on them.
	n.mu.Lock()
	if n.closed {
		n.mu.Unlock()
		running.close()
		return nil
	}
	n.serving[job.Name] = running
	n.mu.Unlock()

	n.log.InfoContext(ctx, "serving", "job", job.Name, "stages", n.opts.Serve, "revision", change.Revision)
	return nil
}

// Serving lists the jobs this node is serving.
func (n *Node) Serving() []string {
	n.mu.Lock()
	defer n.mu.Unlock()

	out := make([]string, 0, len(n.serving))
	for name := range n.serving {
		out = append(out, name)
	}
	return out
}

// Revision is which version of a job this node is serving, which is what tells
// an operator whether a resubmission has reached everybody.
func (n *Node) Revision(job string) (uint64, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()

	if running, ok := n.serving[job]; ok {
		return running.revision, true
	}
	return 0, false
}

func (n *Node) stop(job string) {
	n.mu.Lock()
	running, ok := n.serving[job]
	delete(n.serving, job)
	n.mu.Unlock()

	if ok {
		running.close()
	}
}

func (n *Node) stopAll() {
	n.mu.Lock()
	running := n.serving
	n.serving = map[string]*served{}
	n.mu.Unlock()

	for _, one := range running {
		one.close()
	}
}

// Close stops everything this node was serving.
func (n *Node) Close() error {
	n.mu.Lock()
	n.closed = true
	leave := n.leave
	n.mu.Unlock()

	// Stop saying this node is here before tearing down what it was here for,
	// so the registry never lists a node that has stopped answering.
	if leave != nil {
		leave()
	}

	n.stopAll()
	return nil
}

// Drain is how long a node waits for the requests it had already taken.
const Drain = 30 * time.Second

// close stops taking work, waits for what is in flight, and only then releases
// what the stages hold.
//
// Three steps and all three are needed. Draining a subscription is
// asynchronous, so closing straight after it closed a cache and a chain out
// from under handlers still using them. Waiting is what makes the promise in
// the package documentation true rather than aspirational. And the handlers'
// own context is cancelled last, so a request that was in flight is answered
// rather than aborted with "context canceled".
func (s *served) close() {
	// Drained, not unsubscribed, and waited for: unsubscribing discards the
	// requests NATS has already handed this member, and core NATS does not
	// redeliver them, so whoever asked would wait out its timeout for an answer
	// nobody was ever going to give.
	//
	// [bus.Drain] rather than a loop here, because the services had the same
	// obligation and did not know it: this was the only place that got it
	// right, and being right in one package is how the other one was written
	// wrong. It also waits for the drain itself to finish rather than for the
	// pending count to reach zero, which is a stronger signal than the settle
	// this used to do: a count of zero is also true in the moment between the
	// last message leaving the queue and its handler being entered.
	_ = bus.Drain(s.subs, Drain)

	if s.work != nil {
		s.work.wait(Drain)
	}
	if s.stop != nil {
		s.stop()
	}
	for _, closer := range s.closers {
		_ = closer()
	}
	s.subs, s.closers = nil, nil
}
