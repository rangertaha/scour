// SPDX-License-Identifier: GPL-3.0-or-later

package store_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
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
