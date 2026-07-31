// SPDX-License-Identifier: MIT

package wom_test

import (
	"testing"

	"github.com/rangertaha/scour/internal/wom"
)

func TestVersion(t *testing.T) {
	t.Parallel()

	if got := wom.Version(); got == "" {
		t.Error("Version() = empty string, want a non-empty version")
	}
}
