// SPDX-License-Identifier: GPL-3.0-or-later

package tui

import (
	"context"
	"testing"
	"time"

	"github.com/rangertaha/scour/internal/store"
)

// fakeSource is a fleet without a database behind it.
type fakeSource struct {
	entities []store.EntitySummary
	byName   map[string]*store.Entity
	status   map[uint]*store.Status
	rates    map[uint]float64
}

func (f fakeSource) Entities(context.Context) ([]store.EntitySummary, error) {
	return f.entities, nil
}
func (f fakeSource) Entity(_ context.Context, name string) (*store.Entity, error) {
	return f.byName[name], nil
}
func (f fakeSource) Status(_ context.Context, id uint) (*store.Status, error) {
	return f.status[id], nil
}
func (f fakeSource) FetchRate(_ context.Context, id uint, _ time.Duration) (float64, error) {
	return f.rates[id], nil
}

// What an entity is doing is not the same question as whether it has work.
func TestStateOf(t *testing.T) {
	tests := []struct {
		name   string
		paused bool
		queued int64
		rate   float64
		want   State
	}{
		{"fetching", false, 100, 4.2, StateRunning},
		{"work but nothing fetching it", false, 100, 0, StateIdle},
		{"frontier empty", false, 0, 0, StateDone},
		{"paused", true, 100, 0, StatePaused},
		// An entity can be paused while requests already in flight are still
		// landing. It is still paused: that is a fact, not an inference.
		{"paused with pages still arriving", true, 100, 4.2, StatePaused},
		// Nothing queued but still fetching means the last few are in flight.
		{"draining", false, 0, 1.5, StateRunning},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stateOf(tt.paused, tt.queued, tt.rate); got != tt.want {
				t.Errorf("stateOf(%v, %d, %v) = %q, want %q",
					tt.paused, tt.queued, tt.rate, got, tt.want)
			}
		})
	}
}

func TestTakeBuildsSortedRows(t *testing.T) {
	src := fakeSource{
		entities: []store.EntitySummary{{Name: "zebra"}, {Name: "alpha"}},
		byName: map[string]*store.Entity{
			"zebra": {ID: 2, Name: "zebra", Paused: true},
			"alpha": {ID: 1, Name: "alpha"},
		},
		status: map[uint]*store.Status{
			1: {Targets: 19, Queued: 6197, Visited: 808, Matches: 713, Rules: 8},
			2: {Targets: 30, Queued: 11042, Visited: 1267, Matches: 867, Rules: 8},
		},
		rates: map[uint]float64{1: 4.2, 2: 0},
	}

	snap, err := Take(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(snap.Rows))
	}
	// Sorted, so a row does not move under the cursor between refreshes.
	if snap.Rows[0].Name != "alpha" || snap.Rows[1].Name != "zebra" {
		t.Errorf("rows are not sorted by name: %v %v", snap.Rows[0].Name, snap.Rows[1].Name)
	}
	if snap.Rows[0].State != StateRunning {
		t.Errorf("alpha state = %q, want running", snap.Rows[0].State)
	}
	if snap.Rows[1].State != StatePaused {
		t.Errorf("zebra state = %q, want paused", snap.Rows[1].State)
	}

	queued, visited, records, rate := snap.Totals()
	if queued != 17239 || visited != 2075 || records != 1580 || rate != 4.2 {
		t.Errorf("totals = %d %d %d %v", queued, visited, records, rate)
	}
}
