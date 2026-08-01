// SPDX-License-Identifier: GPL-3.0-or-later

package cli

import (
	"errors"
	"fmt"
	"testing"

	"github.com/rangertaha/scour/internal/store"
)

// The codes are what a script branches on, so each one has to mean the thing it
// says and nothing else.
func TestExitCode(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		{"success", nil, ExitOK},
		{"anything else", errors.New("boom"), ExitFailed},
		{"usage", Usagef("takes one name"), ExitUsage},
		{"not found", store.ErrNotFound, ExitNotFound},
		{"empty under strict", ErrEmpty, ExitEmpty},
		{"unreachable", ErrUnreachable, ExitUnreached},

		// Wrapped, because that is how they actually arrive.
		{"wrapped not found", fmt.Errorf("item %q: %w", "x", store.ErrNotFound), ExitNotFound},
		{"wrapped usage", fmt.Errorf("in job: %w", Usagef("bad")), ExitUsage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ExitCode(tc.err); got != tc.want {
				t.Errorf("ExitCode = %d, want %d", got, tc.want)
			}
		})
	}
}

// An empty result is an answer, not a fault, so it only fails when asked to.
func TestEmptyOnlyFailsUnderStrict(t *testing.T) {
	a := &App{}
	if err := a.Empty("nothing here\n"); err != nil {
		t.Errorf("an empty result should succeed by default: %v", err)
	}

	a.Strict = true
	if err := a.Empty("nothing here\n"); !errors.Is(err, ErrEmpty) {
		t.Errorf("--strict should turn an empty result into ErrEmpty, got %v", err)
	}
}
