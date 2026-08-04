// SPDX-License-Identifier: GPL-3.0-or-later

package registry_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/rangertaha/scour/internal/registry"
)

type config struct{ Name string }

type thing struct{ built string }

func newRegistry(t *testing.T) *registry.Registry[config, *thing] {
	t.Helper()
	return registry.New[config, *thing]("widget")
}

func factory(label string) registry.Factory[config, *thing] {
	return func(_ context.Context, cfg config) (*thing, error) {
		return &thing{built: label + ":" + cfg.Name}, nil
	}
}

func TestRegisterAndBuild(t *testing.T) {
	reg := newRegistry(t)
	reg.Register("brass", factory("brass"))

	got, err := reg.New(context.Background(), "brass", config{Name: "cog"})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if got.built != "brass:cog" {
		t.Errorf("built %q", got.built)
	}
}

func TestKind(t *testing.T) {
	if got := newRegistry(t).Kind(); got != "widget" {
		t.Errorf("kind = %q", got)
	}
}

func TestNames(t *testing.T) {
	reg := newRegistry(t)
	reg.Register("zinc", factory("zinc"))
	reg.Register("brass", factory("brass"))

	// Sorted, not in registration order: registration order is import order,
	// and a list that changes when somebody adds an import cannot be diffed.
	if got := strings.Join(reg.Names(), ","); got != "brass,zinc" {
		t.Errorf("names = %q, want them sorted", got)
	}
}

func TestHas(t *testing.T) {
	reg := newRegistry(t)
	reg.Register("brass", factory("brass"))

	if !reg.Has("brass") {
		t.Error("a registered name is not there")
	}
	if reg.Has("tin") {
		t.Error("an unregistered name is")
	}
	// With no default, the empty name is nothing.
	if reg.Has("") {
		t.Error("the empty name resolved without a default")
	}
}

func TestDefault(t *testing.T) {
	reg := newRegistry(t).Default("brass")
	reg.Register("brass", factory("brass"))

	got, err := reg.New(context.Background(), "", config{Name: "cog"})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if got.built != "brass:cog" {
		t.Errorf("built %q", got.built)
	}
	if !reg.Has("") {
		t.Error("the empty name did not resolve to the default")
	}
}

func TestUnknownNameListsWhatThereIs(t *testing.T) {
	reg := newRegistry(t)
	reg.Register("brass", factory("brass"))

	_, err := reg.New(context.Background(), "tin", config{})
	if err == nil {
		t.Fatal("built something that was never registered")
	}
	for _, want := range []string{"widget", "tin", "brass"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q: %v", want, err)
		}
	}
}

// TestEmptyRegistrySaysWhyItIsEmpty: an empty registry is almost always a
// missing side-effect import rather than a typo, so the message says so.
func TestEmptyRegistrySaysWhyItIsEmpty(t *testing.T) {
	_, err := newRegistry(t).New(context.Background(), "brass", config{})
	if err == nil {
		t.Fatal("built something from an empty registry")
	}
	if !strings.Contains(err.Error(), "import the package") {
		t.Errorf("error does not suggest the likely cause: %v", err)
	}
}

func TestNoNameAndNoDefault(t *testing.T) {
	_, err := newRegistry(t).New(context.Background(), "", config{})
	if err == nil {
		t.Fatal("built something with no name and no default")
	}
	if !strings.Contains(err.Error(), "no default") {
		t.Errorf("error does not explain: %v", err)
	}
}

func TestFactoryErrorIsWrapped(t *testing.T) {
	boom := errors.New("no brass left")

	reg := newRegistry(t)
	reg.Register("brass", func(context.Context, config) (*thing, error) { return nil, boom })

	_, err := reg.New(context.Background(), "brass", config{})
	if !errors.Is(err, boom) {
		t.Fatalf("the factory's error did not survive: %v", err)
	}
	// Wrapped with what was being built, or the message says only "no brass
	// left" and nobody knows which registry said it.
	if !strings.Contains(err.Error(), "widget") || !strings.Contains(err.Error(), "brass") {
		t.Errorf("error lost its context: %v", err)
	}
}

func TestContextReachesTheFactory(t *testing.T) {
	type key struct{}

	reg := newRegistry(t)
	reg.Register("brass", func(ctx context.Context, _ config) (*thing, error) {
		v, _ := ctx.Value(key{}).(string)
		return &thing{built: v}, nil
	})

	got, err := reg.New(context.WithValue(context.Background(), key{}, "carried"), "brass", config{})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if got.built != "carried" {
		t.Errorf("built %q, want the context through", got.built)
	}
}

// Registering badly is a programming mistake, not a runtime condition: two
// implementations answering to one name means which you get depends on import
// order, and that is not a thing to discover in production.

func TestDuplicateRegistrationPanics(t *testing.T) {
	defer expectPanic(t, "twice")()

	reg := newRegistry(t)
	reg.Register("brass", factory("brass"))
	reg.Register("brass", factory("other"))
}

func TestRegisterWithoutANamePanics(t *testing.T) {
	defer expectPanic(t, "without a name")()
	newRegistry(t).Register("", factory("brass"))
}

func TestRegisterWithoutAFactoryPanics(t *testing.T) {
	defer expectPanic(t, "no factory")()
	newRegistry(t).Register("brass", nil)
}

func TestNewWithoutAKindPanics(t *testing.T) {
	defer expectPanic(t, "kind")()
	registry.New[config, *thing]("")
}

func expectPanic(t *testing.T, want string) func() {
	t.Helper()
	return func() {
		r := recover()
		if r == nil {
			t.Errorf("did not panic, want a panic mentioning %q", want)
			return
		}
		if msg, ok := r.(string); ok && !strings.Contains(msg, want) {
			t.Errorf("panic %q does not mention %q", msg, want)
		}
	}
}

// TestConcurrentUse: registration happens from init and lookups happen from
// everywhere, so the table has to survive both at once.
func TestConcurrentUse(t *testing.T) {
	reg := newRegistry(t)
	reg.Register("brass", factory("brass"))

	done := make(chan struct{})
	for range 8 {
		go func() {
			defer func() { done <- struct{}{} }()
			for range 50 {
				reg.Names()
				reg.Has("brass")
				if _, err := reg.New(context.Background(), "brass", config{}); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	for range 8 {
		<-done
	}
}
