// SPDX-License-Identifier: GPL-3.0-or-later

package registry

import (
	"strings"
	"sync"
	"testing"
)

type cfg struct{ n int }
type thing struct{ made string }

func TestRegistryBuildsWhatWasRegistered(t *testing.T) {
	r := New[cfg, thing]("widget")
	r.Register("brass", func(c cfg) (thing, error) { return thing{made: "brass"}, nil })
	r.Register("steel", func(c cfg) (thing, error) { return thing{made: "steel"}, nil })

	got, err := r.New("brass", cfg{})
	if err != nil || got.made != "brass" {
		t.Fatalf("New = %+v, %v", got, err)
	}
	if names := r.Names(); len(names) != 2 || names[0] != "brass" || names[1] != "steel" {
		t.Errorf("Names = %v, want them sorted", names)
	}
	if !r.Has("steel") || r.Has("wood") {
		t.Error("Has disagrees with what was registered")
	}
}

// The error names the kind and what is available, because "unknown" alone
// leaves someone guessing which of six registries refused them.
func TestUnknownNameSaysWhatIsAvailable(t *testing.T) {
	r := New[cfg, thing]("scorer")
	r.Register("bayes", func(c cfg) (thing, error) { return thing{}, nil })

	_, err := r.New("nonsense", cfg{})
	if err == nil {
		t.Fatal("an unregistered name must fail")
	}
	for _, want := range []string{"scorer", "nonsense", "bayes"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// An empty name is how configuration that says nothing reaches a working
// default, so every caller does not have to repeat which default that is.
func TestEmptyNameTakesTheDefault(t *testing.T) {
	r := New[cfg, thing]("matcher").Default("heuristic")
	r.Register("heuristic", func(c cfg) (thing, error) { return thing{made: "heuristic"}, nil })

	got, err := r.New("", cfg{})
	if err != nil || got.made != "heuristic" {
		t.Fatalf("New(\"\") = %+v, %v", got, err)
	}
	if !r.Has("") {
		t.Error("Has(\"\") should ask after the default")
	}

	// With no default declared, an empty name is simply unknown.
	bare := New[cfg, thing]("matcher")
	if _, err := bare.New("", cfg{}); err == nil {
		t.Error("an empty name with no default must fail")
	}
}

// Registration happens from init across many packages, and lookups happen from
// whatever goroutine is crawling.
func TestRegistryIsSafeUnderConcurrency(t *testing.T) {
	r := New[cfg, thing]("transport").Default("http")
	r.Register("http", func(c cfg) (thing, error) { return thing{made: "http"}, nil })

	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(2)
		go func() { defer wg.Done(); r.Register("x", func(c cfg) (thing, error) { return thing{}, nil }) }()
		go func() {
			defer wg.Done()
			_, _ = r.New("", cfg{})
			_ = r.Names()
			_ = r.Has("http")
		}()
		_ = i
	}
	wg.Wait()
}
