// SPDX-License-Identifier: GPL-3.0-or-later

package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rangertaha/scour/internal/bus"
	"github.com/rangertaha/scour/internal/store"
)

// halt stops a member's heartbeat without removing its entry, which is what a
// killed process leaves behind: the last beat it managed, and then silence.
func (m *Membership) halt() {
	m.cancel()
	m.wg.Wait()
}

// quick is the registry's clock wound down so a test can watch a node go stale
// without waiting the minutes a real fleet is tuned for.
var quick = Options{
	Interval: 50 * time.Millisecond,
	Down:     200 * time.Millisecond,
	Forget:   2 * time.Second,
}

func openRegistry(t *testing.T, opts Options) *Registry {
	t.Helper()

	b, err := bus.Open(context.Background(), bus.Options{Name: "cluster-test", Timeout: 15 * time.Second})
	if err != nil {
		t.Fatalf("bus.Open: %v", err)
	}
	t.Cleanup(func() { b.Close() })

	reg, err := Open(context.Background(), b, opts)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return reg
}

func names(nodes []Node) string {
	out := make([]string, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, n.Name+"="+string(n.State))
	}
	return strings.Join(out, " ")
}

func find(t *testing.T, reg *Registry, name string) (Node, bool) {
	t.Helper()
	nodes, err := reg.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, n := range nodes {
		if n.Name == name {
			return n, true
		}
	}
	return Node{}, false
}

func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// The whole point of a heartbeat: a node that dies has to stop being listed.
//
// It goes in two stages, and both matter. First it reads as down with a stale
// timestamp, because a partition and a clean shutdown look identical for one
// beat and the timestamp is what tells them apart. Then it is gone, because a
// listing that accumulated every machine that ever ran scour would answer a
// question nobody asked.
func TestANodeThatStopsHeartbeatingLeavesTheListing(t *testing.T) {
	reg := openRegistry(t, quick)
	ctx := context.Background()

	alive, err := reg.Join(ctx, Node{Name: "alive", Role: Roles{"crawl"}}, nil)
	if err != nil {
		t.Fatalf("Join alive: %v", err)
	}
	defer alive.Close(ctx)

	ghost, err := reg.Join(ctx, Node{Name: "ghost", Role: Roles{"crawl"}}, nil)
	if err != nil {
		t.Fatalf("Join ghost: %v", err)
	}

	waitFor(t, func() bool {
		_, ok := find(t, reg, "ghost")
		return ok
	}, "the ghost to register")

	// Killed rather than asked to leave: no goodbye, no removal, just a
	// heartbeat that stops.
	ghost.halt()

	waitFor(t, func() bool {
		n, ok := find(t, reg, "ghost")
		return ok && n.State == StateDown
	}, "the ghost to read as down")

	// Down is derived, never written, so a node nobody has heard from cannot
	// still be reporting a fetch rate.
	if n, _ := find(t, reg, "ghost"); n.Rate != 0 {
		t.Errorf("a down node reports rate %v, want nothing", n.Rate)
	}

	waitFor(t, func() bool {
		_, ok := find(t, reg, "ghost")
		return !ok
	}, "the ghost to leave the listing")

	// And the node that kept beating is untouched by any of it, which is the
	// half that says the listing thinned out rather than broke.
	n, ok := find(t, reg, "alive")
	if !ok || n.State != StateUp {
		nodes, _ := reg.List(ctx)
		t.Fatalf("the live node reads as %q, listing was: %s", n.State, names(nodes))
	}

	if _, err := reg.Get(ctx, "ghost"); !isNotFound(err) {
		t.Errorf("Get on the forgotten node = %v, want not found", err)
	}
}

// isNotFound is the sentinel the CLI maps to exit 3, so a missing node reads
// the same way to a script as a missing item or run.
func isNotFound(err error) bool { return errors.Is(err, store.ErrNotFound) }

// Registration is what makes a listing possible at all, so it has to carry
// enough to act on: which machine, running what, at which version.
func TestJoinRecordsWhatTheNodeIs(t *testing.T) {
	reg := openRegistry(t, quick)
	ctx := context.Background()

	member, err := reg.Join(ctx, Node{
		Name:    "worker-3",
		Role:    Roles{"store", "crawl"},
		Host:    "box.example.com",
		Version: "v1.2.3",
	}, nil)
	if err != nil {
		t.Fatalf("Join: %v", err)
	}
	defer member.Close(ctx)

	node, err := reg.Get(ctx, "worker-3")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if node.Role.String() != "store,crawl" {
		t.Errorf("role = %q", node.Role.String())
	}
	if node.Host != "box.example.com" || node.Version != "v1.2.3" {
		t.Errorf("host %q version %q", node.Host, node.Version)
	}
	if node.State != StateUp {
		t.Errorf("state = %q, want up", node.State)
	}
	if node.Joined.IsZero() || node.Seen.IsZero() {
		t.Error("a node registered without a clock on it")
	}
}

// A name a live node already holds is not free. Two nodes sharing one would
// each overwrite the other's heartbeat and each look intermittently down.
func TestTwoNodesDoNotShareAName(t *testing.T) {
	reg := openRegistry(t, quick)
	ctx := context.Background()

	first, err := reg.Join(ctx, Node{Name: "box", Role: Roles{"crawl"}}, nil)
	if err != nil {
		t.Fatalf("Join: %v", err)
	}
	defer first.Close(ctx)

	second, err := reg.Join(ctx, Node{Name: "box", Role: Roles{"store"}}, nil)
	if err != nil {
		t.Fatalf("Join again: %v", err)
	}
	defer second.Close(ctx)

	if first.Name() == second.Name() {
		t.Fatalf("both nodes registered as %q", first.Name())
	}
	nodes, err := reg.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(nodes) != 2 {
		t.Errorf("listing has %d nodes, want both: %s", len(nodes), names(nodes))
	}
}

// Leaving is a request made from another process, so the node has to hear it
// and act on it rather than be removed behind its own back.
func TestLeaveAsksTheNodeToDrain(t *testing.T) {
	reg := openRegistry(t, quick)
	ctx := context.Background()

	member, err := reg.Join(ctx, Node{Name: "worker", Role: Roles{"crawl"}}, nil)
	if err != nil {
		t.Fatalf("Join: %v", err)
	}
	defer member.Close(ctx)

	node, err := reg.Leave(ctx, "worker")
	if err != nil {
		t.Fatalf("Leave: %v", err)
	}
	if node.State != StateDraining {
		t.Errorf("Leave left the node %q, want draining", node.State)
	}

	select {
	case <-member.Leaving():
	case <-time.After(15 * time.Second):
		t.Fatal("the node was never told to leave")
	}
	if !member.Draining() {
		t.Error("the node is not draining after being asked to")
	}

	// Still listed while it drains, because it is still fetching. Vanishing
	// here is what would lose the pages it is holding.
	if n, ok := find(t, reg, "worker"); !ok || n.State != StateDraining {
		t.Errorf("draining node reads as %q, listed %v", n.State, ok)
	}

	// And gone when it goes.
	member.Close(ctx)
	if _, ok := find(t, reg, "worker"); ok {
		t.Error("a node that left is still listed")
	}
}

// Asking a node that is already down to leave has nothing to drain, so the
// entry goes rather than sitting in the listing waiting for a reply.
func TestLeavingADownNodeRemovesIt(t *testing.T) {
	reg := openRegistry(t, quick)
	ctx := context.Background()

	member, err := reg.Join(ctx, Node{Name: "gone", Role: Roles{"crawl"}}, nil)
	if err != nil {
		t.Fatalf("Join: %v", err)
	}
	member.halt()

	waitFor(t, func() bool {
		n, ok := find(t, reg, "gone")
		return ok && n.State == StateDown
	}, "the node to read as down")

	if _, err := reg.Leave(ctx, "gone"); err != nil {
		t.Fatalf("Leave: %v", err)
	}
	if _, ok := find(t, reg, "gone"); ok {
		t.Error("a down node asked to leave is still listed")
	}
}

// The heartbeat is what carries the health columns, and the rate is worked out
// from a counter rather than reported, so it has to come out of a counter that
// only goes up.
func TestHeartbeatCarriesQueueAndThroughput(t *testing.T) {
	reg := openRegistry(t, quick)
	ctx := context.Background()

	var fetched atomic.Int64
	health := func(context.Context) Load {
		return Load{Queue: 42, Fetched: fetched.Add(10)}
	}

	member, err := reg.Join(ctx, Node{Name: "busy", Role: Roles{"crawl"}}, health)
	if err != nil {
		t.Fatalf("Join: %v", err)
	}
	defer member.Close(ctx)

	waitFor(t, func() bool {
		n, ok := find(t, reg, "busy")
		return ok && n.Queue == 42 && n.Rate > 0
	}, "a heartbeat carrying both numbers")
}

// The HTTP representation gives a node one role field and a scour node commonly
// has two, so the slice has to arrive as one string rather than as an array.
func TestRolesTravelAsOneField(t *testing.T) {
	body, err := json.Marshal(Node{Name: "worker-3", Role: Roles{"store", "crawl"}, State: StateUp})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(body), `"role":"store,crawl"`) {
		t.Errorf("encoded as %s", body)
	}

	var back Node
	if err := json.Unmarshal(body, &back); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(back.Role) != 2 || back.Role[0] != "store" || back.Role[1] != "crawl" {
		t.Errorf("decoded roles = %v", back.Role)
	}
}

// A hostname is a node name, and NATS gives meaning to the dots in one.
func TestKeysCannotWidenASubscription(t *testing.T) {
	for _, name := range []string{"box.example.com", "box>1", "box*", "box 1"} {
		key := Key(name)
		if strings.ContainsAny(key, ".*> ") {
			t.Errorf("Key(%q) = %q, want the special characters neutralised", name, key)
		}
		if Key(key) != key {
			t.Errorf("Key is not idempotent: Key(%q) = %q", key, Key(key))
		}
	}
	if Key("") != "node" {
		t.Errorf("Key(\"\") = %q, want a usable key", Key(""))
	}
}

// The degradation the whole package is built around: a process that could not
// reach the registry holds a nil one, and nothing it does with it can stop it
// crawling.
func TestANodeWithNoRegistryStillWorks(t *testing.T) {
	ctx := context.Background()
	var reg *Registry

	nodes, err := reg.List(ctx)
	if err != nil || len(nodes) != 0 {
		t.Errorf("List on no registry = %v, %v", nodes, err)
	}
	if _, err := reg.Get(ctx, "anything"); !isNotFound(err) {
		t.Errorf("Get on no registry = %v, want not found", err)
	}
	if _, err := reg.Leave(ctx, "anything"); !isNotFound(err) {
		t.Errorf("Leave on no registry = %v, want not found", err)
	}

	member, err := reg.Join(ctx, Node{Name: "orphan"}, nil)
	if err != nil {
		t.Fatalf("Join on no registry = %v, want a working nil membership", err)
	}
	if member.Name() != "" || member.Draining() {
		t.Errorf("a nil membership claims to be %q, draining %v", member.Name(), member.Draining())
	}
	if err := member.Drain(ctx); err != nil {
		t.Errorf("Drain on no membership = %v", err)
	}
	select {
	case <-member.Leaving():
		t.Error("a node with no registry was asked to leave by nobody")
	default:
	}
	member.Close(ctx)
}
