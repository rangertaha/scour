// SPDX-License-Identifier: GPL-3.0-or-later

package cachetest_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"iter"
	"maps"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/rangertaha/scour/internal/cache"
	"github.com/rangertaha/scour/internal/cache/cachetest"
)

// memory is a correct backend, kept here so the suite is checked against
// something that should pass it.
//
// A conformance suite nothing passes is not a suite, it is a wish, and the way
// that goes unnoticed is when every implementation is also new.
type memory struct {
	mu   sync.RWMutex
	held map[string][]byte
}

func newMemory() *memory { return &memory{held: map[string][]byte{}} }

func (m *memory) Put(_ context.Context, key string, r io.Reader) error {
	if err := cache.CheckKey(key); err != nil {
		return err
	}
	body, err := io.ReadAll(r)
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.held[key] = body
	return nil
}

func (m *memory) Get(_ context.Context, key string) (io.ReadCloser, error) {
	if err := cache.CheckKey(key); err != nil {
		return nil, err
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	body, ok := m.held[key]
	if !ok {
		return nil, fmt.Errorf("%w: %s", cache.ErrNotFound, key)
	}
	return io.NopCloser(bytes.NewReader(body)), nil
}

func (m *memory) Has(_ context.Context, key string) (bool, error) {
	if err := cache.CheckKey(key); err != nil {
		return false, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.held[key]
	return ok, nil
}

func (m *memory) Delete(_ context.Context, key string) error {
	if err := cache.CheckKey(key); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.held, key)
	return nil
}

func (m *memory) Keys(context.Context) iter.Seq2[string, error] {
	m.mu.RLock()
	keys := slices.Collect(maps.Keys(m.held))
	m.mu.RUnlock()

	return func(yield func(string, error) bool) {
		for _, k := range keys {
			if !yield(k, nil) {
				return
			}
		}
	}
}

func (m *memory) Close() error { return nil }

// TestSuitePassesACorrectBackend is the suite testing itself.
func TestSuitePassesACorrectBackend(t *testing.T) {
	cachetest.Run(t, func(t *testing.T) cache.Store { return newMemory() })
}

// broken is a backend that lies in every way the contract forbids: it loses
// bodies, forgets keys, accepts traversal, and swallows deletes.
type broken struct{ memory }

func (b *broken) Put(context.Context, string, io.Reader) error { return nil } // drops it
func (b *broken) Get(context.Context, string) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader([]byte("wrong"))), nil // never not-found
}
func (b *broken) Has(context.Context, string) (bool, error) { return false, nil }
func (b *broken) Delete(context.Context, string) error      { return errors.New("no") }
func (b *broken) Keys(context.Context) iter.Seq2[string, error] {
	return func(func(string, error) bool) {} // lists nothing
}

// TestSuiteCatchesABrokenBackend runs the contract against that, in a child
// process, because a suite that reported the failures here would fail this run.
//
// Without this the suite is unfalsifiable: every backend it has ever been
// pointed at passed, so nothing shows it is capable of saying no.
func TestSuiteCatchesABrokenBackend(t *testing.T) {
	if os.Getenv("SCOUR_RUN_BROKEN") == "1" {
		cachetest.Run(t, func(t *testing.T) cache.Store { return &broken{} })
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=TestSuiteCatchesABrokenBackend", "-test.v")
	cmd.Env = append(os.Environ(), "SCOUR_RUN_BROKEN=1")

	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("the contract passed a backend that fails all of it:\n%s", out)
	}

	// Not merely that it failed, but that each case failed. Matching the name
	// alone matched the `=== RUN` line that -test.v prints for every subtest
	// whether it passes or fails, so this said nothing: gutting a case to do
	// no work at all left the child reporting PASS for it and this test green.
	//
	// The failure line is what distinguishes them, so that is what is matched.
	for _, want := range []string{
		"RoundTrip", "MissingIsNotFound", "Has", "Delete", "Keys", "BadKeysRejected",
	} {
		if !strings.Contains(string(out), "--- FAIL: TestSuiteCatchesABrokenBackend/"+want) {
			t.Errorf("the contract did not report %s against a backend that breaks it", want)
		}
	}
}
