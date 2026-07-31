// SPDX-License-Identifier: GPL-3.0-or-later

// Package tui holds what a live view shows, apart from how it is drawn.
//
// The split is so the interesting part can be tested. Rendering a table into a
// terminal is hard to assert anything useful about; deciding what belongs in
// the table, and what a crawl's numbers mean, is not.
package tui

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/rangertaha/scour/internal/store"
)

// Window is how far back a rate looks.
//
// Long enough that a crawl fetching a page a second does not read as zero
// whenever a second passes without one, short enough that the number follows
// what is happening rather than what happened a minute ago.
const Window = 15 * time.Second

// Row is one entity as the fleet table shows it.
type Row struct {
	Name     string
	Targets  int64
	Queued   int64
	Visited  int64
	Records  int64
	Rules    int64
	Rate     float64
	State    State
	EntityID uint
}

// State is what an entity is doing, which is not the same question as whether
// it has work.
type State string

// The states an entity can be in.
const (
	// StateRunning is fetching now.
	StateRunning State = "running"
	// StatePaused is stopped on purpose, keeping its frontier.
	StatePaused State = "paused"
	// StateIdle has work and nothing fetching it: no crawl is running, or its
	// budget was spent.
	StateIdle State = "idle"
	// StateDone has nothing left in the frontier.
	StateDone State = "done"
)

// Snapshot is the whole view at one moment.
type Snapshot struct {
	Rows  []Row
	Taken time.Time
}

// Totals sums what the header shows.
func (s Snapshot) Totals() (queued, visited, records int64, rate float64) {
	for _, r := range s.Rows {
		queued += r.Queued
		visited += r.Visited
		records += r.Records
		rate += r.Rate
	}
	return queued, visited, records, rate
}

// Source is what a snapshot is built from. It is an interface so the model can
// be tested without a database.
type Source interface {
	Entities(ctx context.Context) ([]store.EntitySummary, error)
	Entity(ctx context.Context, name string) (*store.Entity, error)
	Status(ctx context.Context, entityID uint) (*store.Status, error)
	FetchRate(ctx context.Context, entityID uint, window time.Duration) (float64, error)
}

// Take builds a snapshot of every entity.
func Take(ctx context.Context, src Source) (Snapshot, error) {
	names, err := src.Entities(ctx)
	if err != nil {
		return Snapshot{}, fmt.Errorf("list entities: %w", err)
	}

	snap := Snapshot{Taken: time.Now(), Rows: make([]Row, 0, len(names))}
	for _, n := range names {
		e, err := src.Entity(ctx, n.Name)
		if err != nil {
			return Snapshot{}, err
		}
		st, err := src.Status(ctx, e.ID)
		if err != nil {
			return Snapshot{}, err
		}
		rate, err := src.FetchRate(ctx, e.ID, Window)
		if err != nil {
			return Snapshot{}, err
		}
		snap.Rows = append(snap.Rows, Row{
			Name: e.Name, EntityID: e.ID,
			Targets: st.Targets, Queued: st.Queued, Visited: st.Visited,
			Records: st.Matches, Rules: st.Rules,
			Rate:  rate,
			State: stateOf(e.Paused, st.Queued, rate),
		})
	}
	sort.Slice(snap.Rows, func(i, j int) bool { return snap.Rows[i].Name < snap.Rows[j].Name })
	return snap, nil
}

// stateOf decides what an entity is doing.
//
// Paused first, because it is a fact rather than an inference: an entity can be
// paused with pages still arriving from requests that were already in flight,
// and it is still paused. Then fetching, then whether there is anything left to
// fetch.
func stateOf(paused bool, queued int64, rate float64) State {
	switch {
	case paused:
		return StatePaused
	case rate > 0:
		return StateRunning
	case queued > 0:
		return StateIdle
	default:
		return StateDone
	}
}
