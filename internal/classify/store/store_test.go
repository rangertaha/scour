// SPDX-License-Identifier: GPL-3.0-or-later

package store_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/rangertaha/scour/internal/classify"
	"github.com/rangertaha/scour/internal/classify/store"

	_ "github.com/rangertaha/scour/internal/classify/terms"
)

func open(t *testing.T) *store.Store {
	t.Helper()

	s, err := store.Open(filepath.Join(t.TempDir(), "topics"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return s
}

func TestATrainedClassifierComesBack(t *testing.T) {
	s := open(t)

	if err := s.Put("terms", classify.Config{
		Name: "climate", Version: 7, Terms: []string{"climate", "emissions"},
	}); err != nil {
		t.Fatalf("put: %v", err)
	}

	got, err := s.Get(context.Background(), classify.Ref{Name: "climate", Version: 7})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Name() != "climate" || got.Version() != 7 {
		t.Errorf("got %s@%d", got.Name(), got.Version())
	}

	score, err := got.Score(context.Background(), "climate and emissions and climate again")
	if err != nil {
		t.Fatal(err)
	}
	if score <= 0 {
		t.Errorf("score = %v for text that is plainly about it", score)
	}
}

// TestAVersionIsNeverReplaced. What a version means has to be fixed, or a job
// that pinned one would silently start behaving differently.
func TestAVersionIsNeverReplaced(t *testing.T) {
	s := open(t)
	cfg := classify.Config{Name: "climate", Version: 7, Terms: []string{"climate"}}

	if err := s.Put("terms", cfg); err != nil {
		t.Fatal(err)
	}
	err := s.Put("terms", classify.Config{Name: "climate", Version: 7, Terms: []string{"something else"}})
	if err == nil {
		t.Fatal("a version was replaced")
	}
	if !strings.Contains(err.Error(), "new version") {
		t.Errorf("the error does not say what to do instead: %v", err)
	}
}

func TestOneClassifierServesEveryCaller(t *testing.T) {
	s := open(t)
	if err := s.Put("terms", classify.Config{Name: "climate", Version: 1, Terms: []string{"climate"}}); err != nil {
		t.Fatal(err)
	}

	ref := classify.Ref{Name: "climate", Version: 1}
	first, err := s.Get(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Get(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Error("a second caller got a second classifier, and every page would read the file again")
	}
}

func TestNamesAndLatest(t *testing.T) {
	s := open(t)

	for _, version := range []int{1, 2, 7} {
		if err := s.Put("terms", classify.Config{
			Name: "climate", Version: version, Terms: []string{"climate"},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Put("terms", classify.Config{Name: "insolvency", Version: 1, Terms: []string{"administration"}}); err != nil {
		t.Fatal(err)
	}

	names, err := s.Names()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(names, " "); got != "climate@1 climate@2 climate@7 insolvency@1" {
		t.Errorf("names = %q", got)
	}

	if latest, _ := s.Latest("climate"); latest != 7 {
		t.Errorf("latest = %d", latest)
	}
	if latest, _ := s.Latest("nothing"); latest != 0 {
		t.Errorf("latest for an untrained subject = %d", latest)
	}
}

func TestSomethingNobodyTrained(t *testing.T) {
	_, err := open(t).Get(context.Background(), classify.Ref{Name: "climate", Version: 1})
	if !errors.Is(err, store.ErrNotTrained) {
		t.Errorf("err = %v, want ErrNotTrained", err)
	}
	if !strings.Contains(err.Error(), "topic train") {
		t.Errorf("the error does not say how to fix it: %v", err)
	}
}

func TestWhatIsNotAClassifier(t *testing.T) {
	s := open(t)

	for name, cfg := range map[string]classify.Config{
		"no name":    {Version: 1},
		"no version": {Name: "climate"},
		"a path":     {Name: "../escape", Version: 1},
		"an at":      {Name: "climate@8", Version: 1},
	} {
		t.Run(name, func(t *testing.T) {
			if err := s.Put("terms", cfg); err == nil {
				t.Error("accepted it")
			}
		})
	}

	if _, err := store.Open(""); err == nil {
		t.Error("opened a store with no directory")
	}
}

// TestOnlyOneConcurrentPutOfAVersionWins.
//
// Put used to Stat and then write, which is a race with a name: two
// `scour topic train` runs starting together both read the same Latest, both
// computed the same next version, both saw no file, and both wrote it. The
// second won silently, and a job that had already pinned that version scored
// pages with a model nobody chose — which is what versions exist to prevent and
// what this package's documentation promises cannot happen.
//
// Both runs exiting zero is the part that made it invisible: each printed
// "trained, use it with climate@2" and one of them was lying.
func TestOnlyOneConcurrentPutOfAVersionWins(t *testing.T) {
	topics, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	const writers = 8

	var start sync.WaitGroup
	var done sync.WaitGroup
	start.Add(1)

	results := make([]error, writers)
	for i := range writers {
		done.Add(1)
		go func() {
			defer done.Done()
			start.Wait()
			results[i] = topics.Put("terms", classify.Config{
				Name:    "climate",
				Version: 2,
				// Distinguishable, so the winner is identifiable.
				Terms: []string{fmt.Sprintf("writer-%d", i)},
			})
		}()
	}

	start.Done()
	done.Wait()

	var won int
	for i, err := range results {
		if err == nil {
			won++
			continue
		}
		if !strings.Contains(err.Error(), "already exists") {
			t.Errorf("writer %d failed for the wrong reason: %v", i, err)
		}
	}
	if won != 1 {
		t.Fatalf("%d writers of one version succeeded, want exactly one", won)
	}

	// And what is on disk is one of them, whole: a torn write would be a file
	// that no longer decodes.
	one, err := topics.Load(classify.Ref{Name: "climate", Version: 2})
	if err != nil {
		t.Fatalf("the version that was written cannot be read back: %v", err)
	}
	if len(one.Terms) != 1 || !strings.HasPrefix(one.Terms[0], "writer-") {
		t.Errorf("the stored topic is not one writer's: %+v", one.Terms)
	}
}

// TestReplaceStillOverwrites, because the exclusive create must not have taken
// the update of CRUD with it.
func TestReplaceStillOverwrites(t *testing.T) {
	topics, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	if err := topics.Put("terms", classify.Config{
		Name: "climate", Version: 1, Terms: []string{"first"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := topics.Replace("terms", classify.Config{
		Name: "climate", Version: 1, Terms: []string{"corrected"},
	}); err != nil {
		t.Fatalf("replace: %v", err)
	}

	one, err := topics.Load(classify.Ref{Name: "climate", Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(one.Terms) != 1 || one.Terms[0] != "corrected" {
		t.Errorf("terms = %v, want the correction", one.Terms)
	}
}
