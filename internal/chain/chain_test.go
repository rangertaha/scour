// SPDX-License-Identifier: GPL-3.0-or-later

package chain_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/rangertaha/scour/internal/chain"
)

// A chain of doubles that records the order it was walked. Everything here is
// about order and control flow, which is all the package does.

type trace struct {
	mu    sync.Mutex
	steps []string
}

func (t *trace) add(step string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.steps = append(t.steps, step)
}

func (t *trace) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.Join(t.steps, " ")
}

// recorder is a link that notes when it is entered and left.
func recorder(tr *trace, name string) chain.Wrapper[string, string] {
	return func(next chain.Handler[string, string]) chain.Handler[string, string] {
		return chain.Func[string, string](func(ctx context.Context, in string) (string, error) {
			tr.add(name + ">")
			out, err := next.Handle(ctx, in)
			tr.add(name + "<")
			return out, err
		})
	}
}

func core(tr *trace) chain.Handler[string, string] {
	return chain.Func[string, string](func(_ context.Context, in string) (string, error) {
		tr.add("core")
		return in + "!", nil
	})
}

// TestWalkedOutAndBack is the whole contract in one assertion: low order is
// outermost, and every link sees the way back in the opposite order.
func TestWalkedOutAndBack(t *testing.T) {
	tr := &trace{}

	h := chain.Build(core(tr), []chain.Link[string, string]{
		{Name: "cache", Order: 900, Wrap: recorder(tr, "cache")},
		{Name: "robots", Order: 100, Wrap: recorder(tr, "robots")},
		{Name: "retry", Order: 550, Wrap: recorder(tr, "retry")},
	})

	got, err := h.Handle(context.Background(), "page")
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	if got != "page!" {
		t.Errorf("got %q", got)
	}

	want := "robots> retry> cache> core cache< retry< robots<"
	if tr.String() != want {
		t.Errorf("walked\n  %s\nwant\n  %s", tr, want)
	}
}

// TestShortCircuit is a cache hit: the links outside still see a result, and
// the ones inside never ran.
func TestShortCircuit(t *testing.T) {
	tr := &trace{}

	hit := func(next chain.Handler[string, string]) chain.Handler[string, string] {
		return chain.Func[string, string](func(_ context.Context, in string) (string, error) {
			tr.add("hit")
			return "cached", nil // next is never called
		})
	}

	h := chain.Build(core(tr), []chain.Link[string, string]{
		{Name: "outer", Order: 100, Wrap: recorder(tr, "outer")},
		{Name: "cache", Order: 500, Wrap: hit},
		{Name: "inner", Order: 900, Wrap: recorder(tr, "inner")},
	})

	got, err := h.Handle(context.Background(), "page")
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	if got != "cached" {
		t.Errorf("got %q, want the short-circuited result", got)
	}

	want := "outer> hit outer<"
	if tr.String() != want {
		t.Errorf("walked\n  %s\nwant\n  %s", tr, want)
	}
	if strings.Contains(tr.String(), "core") {
		t.Error("the core ran despite a short-circuit")
	}
}

// TestDrop is robots.txt refusing a URL: a normal outcome, not a failure.
func TestDrop(t *testing.T) {
	tr := &trace{}

	refuse := func(next chain.Handler[string, string]) chain.Handler[string, string] {
		return chain.Func[string, string](func(_ context.Context, in string) (string, error) {
			tr.add("refuse")
			return "", fmt.Errorf("robots.txt disallows %s: %w", in, chain.ErrDrop)
		})
	}

	h := chain.Build(core(tr), []chain.Link[string, string]{
		{Name: "outer", Order: 100, Wrap: recorder(tr, "outer")},
		{Name: "robots", Order: 200, Wrap: refuse},
	})

	_, err := h.Handle(context.Background(), "/private")
	if err == nil {
		t.Fatal("a refused request came back without an error")
	}
	if !chain.Dropped(err) {
		t.Errorf("error is not a drop: %v", err)
	}
	// The reason survives the sentinel, or a log line says only "dropped".
	if !strings.Contains(err.Error(), "/private") {
		t.Errorf("the drop lost its reason: %v", err)
	}
	if strings.Contains(tr.String(), "core") {
		t.Error("the core ran despite a drop")
	}
}

// TestDropIsDistinctFromFailure is why ErrDrop is a sentinel: a crawl that
// obeys robots drops all day, and that must not read as broken.
func TestDropIsDistinctFromFailure(t *testing.T) {
	broken := errors.New("connection refused")

	if chain.Dropped(broken) {
		t.Error("an ordinary error was read as a drop")
	}
	if !chain.Dropped(fmt.Errorf("wrapped: %w", chain.ErrDrop)) {
		t.Error("a wrapped drop was not recognised")
	}
}

// TestErrorOnTheWayOutStillUnwinds: a link's way-back code runs even when the
// way out failed, which is what a timer and a stats counter both need.
func TestErrorOnTheWayOutStillUnwinds(t *testing.T) {
	tr := &trace{}
	failing := errors.New("network is down")

	fail := func(next chain.Handler[string, string]) chain.Handler[string, string] {
		return chain.Func[string, string](func(_ context.Context, in string) (string, error) {
			return "", failing
		})
	}

	h := chain.Build(core(tr), []chain.Link[string, string]{
		{Name: "stats", Order: 100, Wrap: recorder(tr, "stats")},
		{Name: "network", Order: 900, Wrap: fail},
	})

	if _, err := h.Handle(context.Background(), "page"); !errors.Is(err, failing) {
		t.Fatalf("got %v, want the failure through", err)
	}
	if tr.String() != "stats> stats<" {
		t.Errorf("walked %s, want the outer link to see the way back", tr)
	}
}

func TestEmptyChainIsTheCore(t *testing.T) {
	tr := &trace{}

	h := chain.Build(core(tr), nil)
	got, err := h.Handle(context.Background(), "page")
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	if got != "page!" || tr.String() != "core" {
		t.Errorf("got %q after %s", got, tr)
	}
}

// TestTiesKeepDocumentOrder: two links at one position come out in the order
// they were written, not in whatever order a map produced.
func TestTiesKeepDocumentOrder(t *testing.T) {
	tr := &trace{}

	h := chain.Build(core(tr), []chain.Link[string, string]{
		{Name: "first", Order: 500, Wrap: recorder(tr, "first")},
		{Name: "second", Order: 500, Wrap: recorder(tr, "second")},
		{Name: "third", Order: 500, Wrap: recorder(tr, "third")},
	})

	if _, err := h.Handle(context.Background(), "page"); err != nil {
		t.Fatalf("handle: %v", err)
	}
	want := "first> second> third> core third< second< first<"
	if tr.String() != want {
		t.Errorf("walked\n  %s\nwant\n  %s", tr, want)
	}
}

func TestNamesAreInRunOrder(t *testing.T) {
	got := chain.Names([]chain.Link[string, string]{
		{Name: "cache", Order: 900},
		{Name: "robots", Order: 100},
		{Name: "retry", Order: 550},
	})
	want := []string{"robots", "retry", "cache"}

	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("got %v, want %v", got, want)
	}
}

// TestContextReachesTheCore keeps cancellation working through the wrapping.
func TestContextReachesTheCore(t *testing.T) {
	type key struct{}

	h := chain.Build(
		chain.Func[string, string](func(ctx context.Context, _ string) (string, error) {
			v, _ := ctx.Value(key{}).(string)
			return v, nil
		}),
		[]chain.Link[string, string]{{
			Name:  "adds",
			Order: 1,
			Wrap: func(next chain.Handler[string, string]) chain.Handler[string, string] {
				return chain.Func[string, string](func(ctx context.Context, in string) (string, error) {
					return next.Handle(context.WithValue(ctx, key{}, "carried"), in)
				})
			},
		}},
	)

	got, err := h.Handle(context.Background(), "")
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	if got != "carried" {
		t.Errorf("got %q, want the context through the chain", got)
	}
}

func TestBuildWithoutACoreIsAMistake(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("building without a core did not panic")
		}
	}()
	chain.Build[string, string](nil, nil)
}

func TestLinkThatWrapsToNilIsNamed(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("a link that wrapped to nil did not panic")
		}
		if !strings.Contains(fmt.Sprint(r), "broken") {
			t.Errorf("panic does not name the link: %v", r)
		}
	}()

	chain.Build(
		chain.Func[string, string](func(context.Context, string) (string, error) { return "", nil }),
		[]chain.Link[string, string]{{
			Name:  "broken",
			Order: 1,
			Wrap:  func(chain.Handler[string, string]) chain.Handler[string, string] { return nil },
		}},
	)
}

// TestLinkWithoutAWrapperIsSkipped: a Link is a struct, so one can be built
// with no Wrap. Skipping it keeps a partly-built chain usable rather than
// panicking on the first request.
func TestLinkWithoutAWrapperIsSkipped(t *testing.T) {
	tr := &trace{}

	h := chain.Build(core(tr), []chain.Link[string, string]{
		{Name: "empty", Order: 100},
		{Name: "real", Order: 200, Wrap: recorder(tr, "real")},
	})

	if _, err := h.Handle(context.Background(), "page"); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if tr.String() != "real> core real<" {
		t.Errorf("walked %s", tr)
	}
}
