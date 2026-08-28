// SPDX-License-Identifier: GPL-3.0-or-later

package bus

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/rangertaha/scour/internal/engine"
)

// Buckets the cluster keeps its state in.
//
// KV rather than a database because it is the only store every node already
// has. A node joining a cluster needs an address and nothing else, and a job
// arriving on one node has to be visible on all of them without anybody
// installing Postgres to make that true.
const (
	// JobsBucket holds jobs as desired state: the document somebody submitted.
	JobsBucket = "SCOUR_JOBS"

	// NodesBucket holds who is here, with a TTL. Not durable state: a row
	// outliving its process is a lie, and a TTL is how it stops being one.
	NodesBucket = "SCOUR_NODES"
)

// NodeTTL is how long a node's entry survives without being renewed.
const NodeTTL = 30 * time.Second

// Jobs is the cluster's job store.
type Jobs struct {
	kv jetstream.KeyValue
}

// OpenJobs returns the job store, creating the bucket if it is not there.
//
// History is kept, because a job is desired state and a resubmission is a
// change to it: being able to see what a job was yesterday is what makes a
// mutation reviewable rather than merely applied.
func (c *Conn) OpenJobs(ctx context.Context) (*Jobs, error) {
	js, err := jetstream.New(c.Conn)
	if err != nil {
		return nil, fmt.Errorf("bus: jetstream: %w", err)
	}

	kv, err := js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket:      JobsBucket,
		Description: "scour jobs, as desired state",
		History:     10,
	})
	if err != nil {
		return nil, fmt.Errorf("bus: %s: %w", JobsBucket, err)
	}
	return &Jobs{kv: kv}, nil
}

// Put stores a job document under a name.
//
// The document rather than the parsed job, because what a client submitted is
// what a later reader should see: reformatting somebody's file on the way
// through would make a diff between submissions unreadable, and the diff is the
// whole of what a resubmission is reviewed by.
func (j *Jobs) Put(ctx context.Context, name string, document []byte) (uint64, error) {
	if err := checkName(name); err != nil {
		return 0, err
	}

	revision, err := j.kv.Put(ctx, name, document)
	if err != nil {
		return 0, fmt.Errorf("bus: store job %q: %w", name, err)
	}
	return revision, nil
}

// Update stores a job only if it has not changed since the revision given.
//
// Compare and swap rather than a plain write, because two clients submitting
// the same job name at once is exactly the case a crawler's control plane has
// to survive: one of them wins and the other is told, rather than one of them
// silently disappearing.
func (j *Jobs) Update(ctx context.Context, name string, document []byte, revision uint64) (uint64, error) {
	next, err := j.kv.Update(ctx, name, document, revision)
	if err != nil {
		return 0, fmt.Errorf("bus: update job %q: %w", name, err)
	}
	return next, nil
}

// ErrNoJob reports a job nobody has submitted.
var ErrNoJob = errors.New("bus: no such job")

// Get reads a job document and the revision it is at.
func (j *Jobs) Get(ctx context.Context, name string) ([]byte, uint64, error) {
	entry, err := j.kv.Get(ctx, name)
	switch {
	case errors.Is(err, jetstream.ErrKeyNotFound):
		return nil, 0, fmt.Errorf("%w: %q", ErrNoJob, name)
	case err != nil:
		return nil, 0, fmt.Errorf("bus: read job %q: %w", name, err)
	}
	return entry.Value(), entry.Revision(), nil
}

// Job reads a job document and parses it, which is what a node picking up work
// actually wants.
func (j *Jobs) Job(ctx context.Context, name string) (*engine.Job, uint64, error) {
	document, revision, err := j.Get(ctx, name)
	if err != nil {
		return nil, 0, err
	}

	doc, err := engine.Parse(document, name+".hcl")
	if err != nil {
		return nil, 0, fmt.Errorf("bus: job %q: %w", name, err)
	}
	if err := doc.Validate(); err != nil {
		return nil, 0, fmt.Errorf("bus: job %q: %w", name, err)
	}

	for _, job := range doc.Jobs {
		if job.Name == name {
			return job, revision, nil
		}
	}
	if len(doc.Jobs) == 1 {
		return doc.Jobs[0], revision, nil
	}
	return nil, 0, fmt.Errorf("bus: %q holds %d jobs and none of them is called that", name, len(doc.Jobs))
}

// Names lists the jobs the cluster knows about.
func (j *Jobs) Names(ctx context.Context) ([]string, error) {
	names, err := j.kv.Keys(ctx)
	switch {
	case errors.Is(err, jetstream.ErrNoKeysFound):
		return nil, nil
	case err != nil:
		return nil, fmt.Errorf("bus: list jobs: %w", err)
	}
	return names, nil
}

// Delete removes a job.
func (j *Jobs) Delete(ctx context.Context, name string) error {
	if err := j.kv.Delete(ctx, name); err != nil {
		return fmt.Errorf("bus: delete job %q: %w", name, err)
	}
	return nil
}

// Watch reports jobs as they are submitted and changed.
//
// This is how a node picks up work without being told: it joins, watches, and
// serves whatever appears. A cluster with no scheduler for a job is a cluster
// where nothing happens, not one that fails.
func (j *Jobs) Watch(ctx context.Context) (<-chan Change, func() error, error) {
	watcher, err := j.kv.WatchAll(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("bus: watch jobs: %w", err)
	}

	out := make(chan Change)
	go func() {
		defer close(out)
		for entry := range watcher.Updates() {
			if entry == nil {
				// The end of the replay: everything already stored has been
				// delivered, and what follows is new.
				select {
				case out <- Change{Replayed: true}:
				case <-ctx.Done():
					return
				}
				continue
			}

			change := Change{
				Name:     entry.Key(),
				Revision: entry.Revision(),
				Document: entry.Value(),
				Deleted:  entry.Operation() != jetstream.KeyValuePut,
			}
			select {
			case out <- change:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, watcher.Stop, nil
}

// Change is one job appearing, changing or going away.
type Change struct {
	Name     string
	Revision uint64
	Document []byte
	Deleted  bool

	// Replayed marks the moment everything already stored has been delivered,
	// so a node can tell "the cluster has no jobs" from "I have not been told
	// yet", which are different things to act on.
	Replayed bool
}

// checkName refuses a job name that would not be a key.
//
// KV keys may not contain spaces, dots or wildcards, and a job called `news.uk`
// would silently become a nested key with surprising watch behaviour. Refusing
// it at the door is better than explaining it later.
func checkName(name string) error {
	switch {
	case name == "":
		return errors.New("bus: a job needs a name")
	case strings.ContainsAny(name, " \t.*>/\\"):
		return fmt.Errorf("bus: %q cannot be a job name: no spaces, dots, slashes or wildcards", name)
	}
	return nil
}

// Nodes is who is in the cluster.
type Nodes struct {
	kv jetstream.KeyValue
}

// OpenNodes returns the node registry, creating the bucket if it is not there.
//
// With a TTL, and no history: this is not durable state. A node that stopped
// should stop being listed, and a row that outlives its process is a lie that
// an operator will act on.
func (c *Conn) OpenNodes(ctx context.Context) (*Nodes, error) {
	js, err := jetstream.New(c.Conn)
	if err != nil {
		return nil, fmt.Errorf("bus: jetstream: %w", err)
	}

	kv, err := js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket:      NodesBucket,
		Description: "scour nodes, with a ttl",
		History:     1,
		TTL:         NodeTTL,
	})
	if err != nil {
		return nil, fmt.Errorf("bus: %s: %w", NodesBucket, err)
	}
	return &Nodes{kv: kv}, nil
}

// Announce says this node is here, and keeps saying it until the returned stop
// is called or the context ends.
func (n *Nodes) Announce(ctx context.Context, name string, what []byte) (stop func(), err error) {
	if err := checkName(name); err != nil {
		return nil, err
	}
	if _, err := n.kv.Put(ctx, name, what); err != nil {
		return nil, fmt.Errorf("bus: announce %q: %w", name, err)
	}

	// Its own cancellation, so a caller that stops serving can stop saying it
	// is here.
	//
	// The renewal used to end only with the context the caller happened to pass
	// in, which for a node is the one its whole run uses: a node that had been
	// closed, with its stages torn down and answering nothing, went on
	// rewriting its row every ten seconds for as long as that context lived, so
	// the registry listed a node with its stages that nobody could get an
	// answer from.
	ctx, leave := context.WithCancel(ctx)

	go func() {
		ticker := time.NewTicker(NodeTTL / 3)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				// Gone deliberately rather than left to expire, so `scour
				// nodes` is right immediately rather than in half a minute.
				_ = n.kv.Delete(context.WithoutCancel(ctx), name)
				return
			case <-ticker.C:
				// A failure is not the end of the loop. The broker restarting
				// or a client reconnecting makes one Put fail, and returning
				// meant the node went on serving work while its entry expired
				// and was never rewritten: `scour cluster list` showed an empty
				// cluster that was busy, and nothing said so anywhere.
				_, _ = n.kv.Put(ctx, name, what)
			}
		}
	}()
	return leave, nil
}

// Here lists the nodes that have announced themselves recently.
func (n *Nodes) Here(ctx context.Context) (map[string][]byte, error) {
	names, err := n.kv.Keys(ctx)
	switch {
	case errors.Is(err, jetstream.ErrNoKeysFound):
		return map[string][]byte{}, nil
	case err != nil:
		return nil, fmt.Errorf("bus: list nodes: %w", err)
	}

	out := make(map[string][]byte, len(names))
	for _, name := range names {
		entry, err := n.kv.Get(ctx, name)
		if errors.Is(err, jetstream.ErrKeyNotFound) {
			// Expired between listing and reading, which is exactly what a TTL
			// is for and not worth reporting.
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("bus: read node %q: %w", name, err)
		}
		out[name] = entry.Value()
	}
	return out, nil
}
