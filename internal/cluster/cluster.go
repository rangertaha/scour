// SPDX-License-Identifier: GPL-3.0-or-later

// Package cluster is the node registry: who is running, what they are running,
// and how they are getting on.
//
// The command group is `scour node` because the commands act on one node at a
// time, this node joins and this node leaves, but the thing held here is the
// fleet, so the package is named for that instead.
//
// # Where a node lives
//
// In a NATS key-value bucket rather than in the database. Three reasons, in the
// order they mattered.
//
// A node is not durable state. The fleet is what is running now, and a row that
// outlives the process that wrote it is a lie every later reader has to be
// protected from. A KV entry carries a time to live, so a machine that is
// unplugged stops being listed without anybody writing a reaper, and a reaper
// that quietly stops running is exactly how a listing fills with ghosts.
//
// Only the store role touches the database. That is the invariant the service
// package exists to hold, and it is what lets a crawl role run on a machine
// with no database at all. A crawler registering itself in a table would have
// to break it. Every process already holds a bus connection, because that is
// how it is handed work in the first place.
//
// And a bucket costs nothing on a laptop. KV is built on JetStream, which
// [bus.Open] already sets up whether the broker is embedded or external, so
// single-process scour gets a registry with nothing installed and nothing
// configured.
package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/rangertaha/scour/internal/bus"
	"github.com/rangertaha/scour/internal/store"
)

// Bucket is where node entries are kept, one per running process.
const Bucket = "SCOUR_NODES"

// State is how a node is getting on.
//
// A node only ever writes [StateUp] or [StateDraining] about itself, because a
// process able to report that it is down would still be running. [StateDown] is
// derived by whoever is reading, from a heartbeat that stopped arriving.
type State string

// The states a node can be in.
const (
	// StateUp is a node beating normally and taking work.
	StateUp State = "up"
	// StateDraining is a node that has been asked to leave: it is finishing
	// what it holds and accepting nothing new.
	StateDraining State = "draining"
	// StateDown is a node whose heartbeat has aged out, which is not the same
	// as one that said goodbye. A goodbye removes the entry.
	StateDown State = "down"
)

// The registry's clock, when a caller does not set one.
const (
	// DefaultInterval is how often a member writes its heartbeat. Frequent
	// enough that `scour node ls` is worth refreshing, rare enough that a
	// hundred nodes are a trickle of small writes rather than a load.
	DefaultInterval = 5 * time.Second

	// DefaultForget is how long a node nobody has heard from stays listed at
	// all. Long enough that a restart, a reboot or a network blip does not
	// erase the evidence that the node existed, short enough that a listing is
	// about the fleet rather than about its history.
	DefaultForget = 5 * time.Minute
)

// Roles is what a node runs.
//
// It travels as one comma-joined string rather than as an array because the
// HTTP representation gives a node a single role field, and a scour node
// commonly has two: keeping the slice in Go and the string on the wire means
// one spelling of the fact in each place rather than two in either.
type Roles []string

// String is the wire form: the roles, comma separated, in the order given.
func (r Roles) String() string { return strings.Join(r, ",") }

// MarshalJSON implements [json.Marshaler].
func (r Roles) MarshalJSON() ([]byte, error) {
	body, err := json.Marshal(r.String())
	if err != nil {
		return nil, fmt.Errorf("encode roles: %w", err)
	}
	return body, nil
}

// UnmarshalJSON implements [json.Unmarshaler].
func (r *Roles) UnmarshalJSON(data []byte) error {
	var joined string
	if err := json.Unmarshal(data, &joined); err != nil {
		return fmt.Errorf("decode roles: %w", err)
	}
	*r = nil
	for _, name := range strings.Split(joined, ",") {
		if name = strings.TrimSpace(name); name != "" {
			*r = append(*r, name)
		}
	}
	return nil
}

// Node is one process in the fleet.
type Node struct {
	// Name is what this node is addressed as, and is also its bucket key, so
	// the name a listing prints is the name `scour node show` takes.
	Name string `json:"name"`
	// Role is what this process was started to do.
	Role Roles `json:"role"`
	// State is up, draining or down. Down is never written, only derived.
	State State `json:"state"`
	// Queue is how many URLs this node has left to fetch.
	Queue int64 `json:"queue"`
	// Rate is pages a second, over the gap since the previous heartbeat.
	Rate float64 `json:"rate"`
	// Seen is when the last heartbeat was written, so a node that has gone
	// away is visible as a stale timestamp before it is gone altogether. A
	// partition and a clean shutdown look identical for one beat, and the
	// timestamp is the only thing that tells them apart.
	Seen time.Time `json:"seen"`
	// Host is the machine, which is not always recoverable from the name once
	// a second node on that machine has taken a suffix.
	Host string `json:"host,omitempty"`
	// Version is the binary, because a fleet mid-upgrade is running two.
	Version string `json:"version,omitempty"`
	// Joined is when this process registered, which is its uptime.
	Joined time.Time `json:"joined,omitempty"`
}

// Load is what one process is carrying, as its own components see it.
type Load struct {
	// Queue is how many URLs this process still has to fetch.
	Queue int64
	// Fetched is how many pages it has fetched since it started. It only ever
	// goes up: the registry turns it into a rate by differencing it.
	Fetched int64
}

// Health is asked for a process's load once per heartbeat.
type Health func(ctx context.Context) Load

// Options tunes the registry's clock. The zero value is the sensible one.
type Options struct {
	// Interval is how often a member writes a heartbeat.
	Interval time.Duration
	// Down is how long a node may go unheard before a reader calls it down.
	// Defaults to three missed beats, because one lost packet is not a dead
	// machine and one slow write is not a partition.
	Down time.Duration
	// Forget is how long an unheard node stays listed at all, and is also the
	// bucket's time to live.
	Forget time.Duration
}

func (o Options) withDefaults() Options {
	if o.Interval <= 0 {
		o.Interval = DefaultInterval
	}
	if o.Down <= 0 {
		o.Down = 3 * o.Interval
	}
	if o.Forget <= 0 {
		o.Forget = DefaultForget
	}
	// Forgetting a node before anyone has had the chance to see it go down
	// would hide the one transition an operator is watching for.
	if o.Forget < o.Down {
		o.Forget = o.Down
	}
	return o
}

// Registry is the fleet as this process can see it.
//
// Every method tolerates a nil receiver, and that is the whole degradation
// story. A process that could not reach the registry holds a nil one, every
// call on it does nothing, and it goes on crawling: a fleet listing is a
// convenience and fetching pages is the job.
type Registry struct {
	kv   jetstream.KeyValue
	opts Options
}

// Open binds this process to the node bucket, creating it if it is not there.
//
// The bucket is memory-backed like every other scour stream. The durable record
// is the database; a registry that survived a broker restart would come back
// full of nodes that did not.
func Open(ctx context.Context, b *bus.Bus, opts Options) (*Registry, error) {
	if b == nil || b.JetStream() == nil {
		return nil, errors.New("no bus to keep the node registry in")
	}
	opts = opts.withDefaults()

	kv, err := b.JetStream().CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket:      Bucket,
		Description: "scour nodes, one entry per running process",
		History:     1,
		// The time to live is the reaper. A node that is switched off writes
		// nothing more, its entry ages out, and no component had to remember
		// to tidy up after it.
		TTL:     opts.Forget,
		Storage: jetstream.MemoryStorage,
	})
	if err != nil {
		return nil, fmt.Errorf("open node registry: %w", err)
	}
	return &Registry{kv: kv, opts: opts}, nil
}

// Key is the bucket key a node name is kept under, and is idempotent.
//
// NATS gives meaning to dots, stars and angle brackets in a key, and a node
// name usually comes from a hostname, which may well contain a dot. Mapping
// them out stops one node reading as several tokens or matching a wildcard
// nobody wrote. A name is put through this before it is stored as well as
// before it is looked up, so what a listing prints is what a lookup takes.
func Key(name string) string {
	name = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == '-', r == '_':
			return r
		default:
			return '-'
		}
	}, strings.TrimSpace(name))
	if name == "" {
		return "node"
	}
	return name
}

// DefaultName is what this machine calls itself when nobody said otherwise.
//
// The hostname, because a node is a process on a machine and the machine's name
// is what an operator already calls it: `scour node leave` typed on that
// machine then addresses the right node with no argument at all.
func DefaultName() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		return "node"
	}
	return Key(host)
}

// List is every node the registry still holds, in name order.
func (r *Registry) List(ctx context.Context) ([]Node, error) {
	if r == nil {
		return nil, nil
	}
	keys, err := r.kv.Keys(ctx)
	if err != nil {
		if errors.Is(err, jetstream.ErrNoKeysFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("list nodes: %w", err)
	}

	now := time.Now().UTC()
	nodes := make([]Node, 0, len(keys))
	for _, key := range keys {
		node, err := r.read(ctx, key)
		if err != nil {
			// One entry written by a version that spelled things differently
			// is not a reason to answer nothing. The rest of the fleet is
			// still the answer to the question that was asked.
			continue
		}
		if r.forgotten(node, now) {
			continue
		}
		nodes = append(nodes, r.aged(node, now))
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].Name < nodes[j].Name })
	return nodes, nil
}

// Get is one node by name. A name nobody is registered under is
// [store.ErrNotFound], which is what exits 3.
func (r *Registry) Get(ctx context.Context, name string) (Node, error) {
	if r == nil {
		return Node{}, missing(name)
	}
	node, err := r.read(ctx, Key(name))
	if err != nil {
		return Node{}, err
	}
	now := time.Now().UTC()
	if r.forgotten(node, now) {
		return Node{}, missing(name)
	}
	return r.aged(node, now), nil
}

// Leave asks a node to drain: stop taking new work, finish what it is holding,
// then exit and take its entry with it.
//
// A request rather than a removal, because the node is usually another process
// and often on another machine, and it is the only one that knows what it still
// has in flight. Deleting the entry here would take it off the listing while it
// was still fetching, which is the exact lie this package exists to avoid.
//
// The write is the message. A KV bucket is a stream underneath, so the node
// watching its own key sees the change as it lands rather than at its next
// heartbeat, and a separate subject carrying the same fact would be a second
// thing to keep in step with the first.
//
// A node that is already down has nothing left to finish and nothing left
// listening, so its entry is removed outright.
func (r *Registry) Leave(ctx context.Context, name string) (Node, error) {
	if r == nil {
		return Node{}, missing(name)
	}
	node, err := r.Get(ctx, name)
	if err != nil {
		return Node{}, err
	}
	if node.State == StateDown {
		if err := r.kv.Purge(ctx, Key(name)); err != nil {
			return Node{}, fmt.Errorf("remove node %s: %w", name, err)
		}
		return node, nil
	}
	node.State = StateDraining
	if err := r.put(ctx, node); err != nil {
		return Node{}, err
	}
	return node, nil
}

// read loads one entry without judging how old it is.
func (r *Registry) read(ctx context.Context, key string) (Node, error) {
	entry, err := r.kv.Get(ctx, key)
	if err != nil {
		if errors.Is(err, jetstream.ErrKeyNotFound) {
			return Node{}, missing(key)
		}
		return Node{}, fmt.Errorf("read node %s: %w", key, err)
	}
	var node Node
	if err := json.Unmarshal(entry.Value(), &node); err != nil {
		return Node{}, fmt.Errorf("decode node %s: %w", key, err)
	}
	return node, nil
}

// put writes an entry, which also resets its time to live.
func (r *Registry) put(ctx context.Context, node Node) error {
	body, err := json.Marshal(node)
	if err != nil {
		return fmt.Errorf("encode node %s: %w", node.Name, err)
	}
	if _, err := r.kv.Put(ctx, Key(node.Name), body); err != nil {
		return fmt.Errorf("write node %s: %w", node.Name, err)
	}
	return nil
}

// aged is the state a reader sees, which is not always the state the node
// wrote. Nothing writes down about itself, so this is where down comes from.
func (r *Registry) aged(node Node, now time.Time) Node {
	if now.Sub(node.Seen) > r.opts.Down {
		node.State = StateDown
		// A node nobody has heard from is not fetching, whatever the last
		// heartbeat to arrive happened to say it was doing.
		node.Rate = 0
	}
	return node
}

// forgotten reports whether an entry has outlived its welcome.
//
// The bucket's time to live already removes these, so this is belt and braces,
// and deliberately so: the broker expires an entry when it gets round to it,
// while a listing has to be right at the moment it is asked. Filtering on the
// way out also means a bucket created by a process configured to remember
// nodes for longer cannot make this process's answer wrong.
func (r *Registry) forgotten(node Node, now time.Time) bool {
	return now.Sub(node.Seen) > r.opts.Forget
}

// missing is the not-found error, wrapping the sentinel the CLI maps to exit 3
// so that a missing node reads the same way as a missing item or run.
func missing(name string) error {
	return fmt.Errorf("no node named %s: %w", name, store.ErrNotFound)
}
