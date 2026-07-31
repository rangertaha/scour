// SPDX-License-Identifier: GPL-3.0-or-later

package service

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rangertaha/scour/internal/bus"
	"github.com/rangertaha/scour/internal/store"
)

func TestParseRoles(t *testing.T) {
	tests := []struct {
		spec string
		want []Role
	}{
		{"", AllRoles},
		{"all", AllRoles},
		{"store", []Role{RoleStore}},
		{"store,crawl", []Role{RoleStore, RoleCrawl}},
		{" Store , CRAWL ", []Role{RoleStore, RoleCrawl}},
		{"store,store", []Role{RoleStore}},
	}
	for _, tt := range tests {
		got, err := ParseRoles(tt.spec)
		if err != nil {
			t.Errorf("ParseRoles(%q): %v", tt.spec, err)
			continue
		}
		if !slices.Equal(got, tt.want) {
			t.Errorf("ParseRoles(%q) = %v, want %v", tt.spec, got, tt.want)
		}
	}
}

// Starting fewer components than asked for is worse than not starting: the
// pipeline would look healthy and quietly drop everything that stage handled.
func TestParseRolesRejectsUnknown(t *testing.T) {
	if _, err := ParseRoles("store,nonsense"); err == nil {
		t.Error("an unknown role must be an error")
	}
	if _, err := ParseRoles(" , "); err == nil {
		t.Error("a list with no roles in it must be an error")
	}
}

type fake struct {
	role    Role
	started atomic.Bool
	fail    error
}

func (f *fake) Role() Role { return f.role }

func (f *fake) Start(ctx context.Context) error {
	f.started.Store(true)
	if f.fail != nil {
		return f.fail
	}
	<-ctx.Done()
	return ctx.Err()
}

func TestSupervisorRunsEverythingAndStopsTogether(t *testing.T) {
	a := &fake{role: RoleStore}
	b := &fake{role: RoleCrawl}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- New(a, b).Run(ctx) }()

	waitFor(t, func() bool { return a.started.Load() && b.started.Load() }, "both services to start")
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned %v, want a clean stop on cancellation", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
}

// A pipeline missing a stage is not a pipeline: carrying on with one component
// down produces results that look complete and are not.
func TestOneFailureStopsTheRest(t *testing.T) {
	boom := errors.New("boom")
	failing := &fake{role: RoleStore, fail: boom}
	healthy := &fake{role: RoleCrawl}

	err := New(failing, healthy).Run(context.Background())
	if !errors.Is(err, boom) {
		t.Errorf("Run returned %v, want the failure", err)
	}
	if !healthy.started.Load() {
		t.Error("the other service never started")
	}
}

func TestSupervisorNeedsServices(t *testing.T) {
	if err := New().Run(context.Background()); err == nil {
		t.Error("running no services must be an error")
	}
}

func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// The single-process crawler checks the scope before queueing, but the bus path
// never did: a link to anywhere at all was recorded as discovered for the
// entity. Deciding it here is also what lets a crawler stay stateless, since a
// scope built from a million targets cannot be handed to one.
func TestTheStoreOnlyRecordsDiscoveriesInsideTheEntity(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "scour.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()

	e, err := db.CreateEntity(ctx, "news")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AddTarget(ctx, e.ID, store.TargetDomain, "example.com", false, 0); err != nil {
		t.Fatal(err)
	}

	// handleDiscovered never touches the broker, so the store service can be
	// exercised without one.
	svc := NewStore(nil, db)
	inside := bus.Discovered{EntityID: e.ID, URL: "http://example.com/a", Depth: 1, Score: 1}
	outside := bus.Discovered{EntityID: e.ID, URL: "http://elsewhere.test/a", Depth: 1, Score: 1}

	for _, ev := range []bus.Discovered{inside, outside} {
		body, err := json.Marshal(ev)
		if err != nil {
			t.Fatal(err)
		}
		if err := svc.handleDiscovered(ctx, body); err != nil {
			t.Fatalf("handleDiscovered(%s): %v", ev.URL, err)
		}
	}

	var urls []store.URL
	if err := db.DB().Where("entity_id = ?", e.ID).Find(&urls).Error; err != nil {
		t.Fatal(err)
	}
	if len(urls) != 1 {
		t.Fatalf("recorded %d urls, want only the one inside the entity: %+v", len(urls), urls)
	}
	if urls[0].URL != inside.URL {
		t.Errorf("recorded %q, want %q", urls[0].URL, inside.URL)
	}
}
