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
}

// served is what one node is running for one job.
type served struct {
	revision uint64
	subs     []*nats.Subscription
	closers  []func() error
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
	if err := nodes.Announce(ctx, opts.Name, announcement); err != nil {
		return nil, err
	}
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

	running := &served{revision: change.Revision}

	for _, stage := range n.opts.Serve {
		switch stage {
		case StageDownload:
			built, err := downloader.New(ctx, job, downloader.Options{})
			if err != nil {
				running.close()
				return err
			}
			sub, err := n.conn.ServeDownloader(ctx, job.Name, built, n.opts.Bodies)
			if err != nil {
				built.Close()
				running.close()
				return err
			}
			running.subs = append(running.subs, sub)
			running.closers = append(running.closers, built.Close)

		case StageRead:
			built, err := spider.New(ctx, job, spider.Options{})
			if err != nil {
				running.close()
				return err
			}
			sub, err := n.conn.ServeSpider(ctx, job.Name, built, n.opts.Bodies)
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

	n.mu.Lock()
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
	n.stopAll()
	return nil
}

// close drains the subscriptions before closing the stages, so a request this
// node had already taken is answered rather than dropped.
func (s *served) close() {
	for _, sub := range s.subs {
		_ = sub.Drain()
	}
	for _, closer := range s.closers {
		_ = closer()
	}
	s.subs, s.closers = nil, nil
}
