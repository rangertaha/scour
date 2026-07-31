// SPDX-License-Identifier: GPL-3.0-or-later

package storage

import (
	"context"
	"math"
	"net/url"
	"path/filepath"
	"testing"

	"github.com/rangertaha/scour/internal/store"
)

func harness(t *testing.T) (*Storage, *store.Store, uint) {
	t.Helper()

	s, err := store.Open(filepath.Join(t.TempDir(), "scour.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	e, err := s.CreateEntity(context.Background(), "vehicle")
	if err != nil {
		t.Fatal(err)
	}
	st := New(context.Background(), s, e.ID)
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	return st, s, e.ID
}

func TestVisitedRoundTrip(t *testing.T) {
	st, _, _ := harness(t)

	const id = uint64(42)
	visited, err := st.IsVisited(id)
	if err != nil {
		t.Fatal(err)
	}
	if visited {
		t.Fatal("a fresh storage should have visited nothing")
	}

	if err := st.Visited(id); err != nil {
		t.Fatalf("Visited: %v", err)
	}
	visited, err = st.IsVisited(id)
	if err != nil {
		t.Fatal(err)
	}
	if !visited {
		t.Error("the request should now be marked visited")
	}
}

// colly hashes URLs with FNV-64, so about half of all request IDs have the
// high bit set. database/sql cannot bind those as uint64, and colly silently
// drops any request whose visited check errors, so this is the difference
// between crawling a site and crawling half of it.
func TestHighBitRequestIDs(t *testing.T) {
	st, _, _ := harness(t)

	ids := []uint64{
		math.MaxUint64,
		math.MaxUint64 - 1,
		1 << 63,
		(1 << 63) + 12345,
		15152129137574095888, // an FNV-64 hash observed in a real crawl
	}

	for _, id := range ids {
		if err := st.Visited(id); err != nil {
			t.Fatalf("Visited(%d): %v", id, err)
		}
		visited, err := st.IsVisited(id)
		if err != nil {
			t.Fatalf("IsVisited(%d): %v", id, err)
		}
		if !visited {
			t.Errorf("IsVisited(%d) = false, want true", id)
		}
	}

	// Distinct high-bit values must not collide with each other.
	if visited, err := st.IsVisited(1<<63 + 999); err != nil || visited {
		t.Errorf("IsVisited of an unvisited high-bit id = %v, %v", visited, err)
	}
}

func TestVisitedIsIdempotent(t *testing.T) {
	st, _, _ := harness(t)

	for range 3 {
		if err := st.Visited(7); err != nil {
			t.Fatalf("Visited: %v", err)
		}
	}
	// A second visit is not an error, and the count stays at one.
	if visited, _ := st.IsVisited(7); !visited {
		t.Error("still expected to be visited")
	}
}

func TestCookiesRoundTrip(t *testing.T) {
	st, _, _ := harness(t)
	u, err := url.Parse("http://www.example.com/cars/")
	if err != nil {
		t.Fatal(err)
	}

	if got := st.Cookies(u); got != "" {
		t.Errorf("cookies = %q, want empty for a host never seen", got)
	}

	st.SetCookies(u, "session=abc")
	if got := st.Cookies(u); got != "session=abc" {
		t.Errorf("cookies = %q, want the stored value", got)
	}

	st.SetCookies(u, "session=def")
	if got := st.Cookies(u); got != "session=def" {
		t.Errorf("cookies = %q, want the updated value", got)
	}

	other, err := url.Parse("http://other.test/")
	if err != nil {
		t.Fatal(err)
	}
	if got := st.Cookies(other); got != "" {
		t.Errorf("cookies = %q, want cookies to be per host", got)
	}
}

func TestVisitedSurvivesReopening(t *testing.T) {
	dir := t.TempDir()
	dsn := filepath.Join(dir, "scour.db")
	ctx := context.Background()

	s, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	e, err := s.CreateEntity(ctx, "vehicle")
	if err != nil {
		t.Fatal(err)
	}
	if err := New(ctx, s, e.ID).Visited(99); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()

	visited, err := New(ctx, s2, e.ID).IsVisited(99)
	if err != nil {
		t.Fatal(err)
	}
	if !visited {
		t.Error("the visited set must survive a restart, or a resumed crawl refetches everything")
	}
}

func TestEntitiesHaveSeparateVisitedSets(t *testing.T) {
	st, s, _ := harness(t)
	ctx := context.Background()

	other, err := s.CreateEntity(ctx, "article")
	if err != nil {
		t.Fatal(err)
	}
	otherSt := New(ctx, s, other.ID)

	if err := st.Visited(5); err != nil {
		t.Fatal(err)
	}
	visited, err := otherSt.IsVisited(5)
	if err != nil {
		t.Fatal(err)
	}
	if visited {
		t.Error("one entity's crawl must not mask another's")
	}
}
