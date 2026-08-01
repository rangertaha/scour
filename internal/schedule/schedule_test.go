// SPDX-License-Identifier: GPL-3.0-or-later

package schedule

import (
	"context"
	"errors"
	"testing"
)

// The default must be what scour did before there was a choice, or every
// existing crawl changes behaviour on upgrade.
func TestDefaultIsBestFirst(t *testing.T) {
	p, err := New("", Config{})
	if err != nil {
		t.Fatal(err)
	}
	if p.Name() != "best" {
		t.Errorf("default policy is %q, want best", p.Name())
	}
	if got := p.Order(State{}); got != ByScore {
		t.Errorf("default order is %v, want score", got)
	}
}

func TestShippedPoliciesAnswerAsNamed(t *testing.T) {
	for name, want := range map[string]Order{
		"best": ByScore, "breadth": Breadth, "depth": Depth, "random": Random,
	} {
		p, err := New(name, Config{})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := p.Order(State{}); got != want {
			t.Errorf("%s ordered %v, want %v", name, got, want)
		}
	}
}

// A policy is asked per lease, not per crawl, so it may change its mind. Until
// a model exists every score is equal and ordering by score is ordering by
// noise.
func TestWarmupCrawlsBroadlyUntilThereIsAModel(t *testing.T) {
	p, err := New("warmup", Config{Switch: 10})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		state State
		want  Order
	}{
		{"nothing trained yet", State{Fetched: 100}, Breadth},
		{"trained but early", State{Trained: true, Fetched: 3}, Breadth},
		{"trained and past the mark", State{Trained: true, Fetched: 10}, ByScore},
	} {
		if got := p.Order(tc.state); got != tc.want {
			t.Errorf("%s: order = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestOrderNamesMatchConfiguration(t *testing.T) {
	for o, want := range map[Order]string{
		ByScore: "score", Breadth: "breadth", Depth: "depth", Random: "random",
	} {
		if got := o.String(); got != want {
			t.Errorf("Order.String = %q, want %q", got, want)
		}
	}
}

// Ordering the frontier and deciding when a URL goes back into it are two
// questions, and they have two registries.
func TestRefreshIsItsOwnExtensionPoint(t *testing.T) {
	if Has("cron") {
		t.Error("cron is a refresh policy, not an ordering policy")
	}
	if !HasRefresh("cron") {
		t.Fatal("cron is not registered as a refresh policy")
	}
	if HasRefresh("best") {
		t.Error("best is an ordering policy, not a refresh policy")
	}

	c, err := NewRefresh("cron", RefreshConfig{Spec: "0 * * * *"})
	if err != nil {
		t.Fatal(err)
	}
	// Registered and unwritten is a different answer from unknown.
	out, err := c.Due(context.Background(), []Node{{URL: "http://example.com/"}})
	if !errors.Is(err, ErrNoSchedule) {
		t.Errorf("Due err = %v, want ErrNoSchedule", err)
	}
	if out != nil {
		t.Errorf("an unwritten schedule returned times: %v", out)
	}
	if _, err := NewRefresh("nonsense", RefreshConfig{}); err == nil {
		t.Error("an unregistered refresh policy must fail")
	} else if errors.Is(err, ErrNoSchedule) {
		t.Error("an unknown name must not look like a planned policy")
	}
}
