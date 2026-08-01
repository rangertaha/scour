// SPDX-License-Identifier: GPL-3.0-or-later

package train

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rangertaha/scour/internal/config"
)

// trainerFor builds a trainer configured with one matcher. Nothing here needs
// a store or a cache: choosing a matcher happens before any page is read, and
// that is the point of checking it separately.
func trainerFor(m config.Model) *Trainer {
	cfg := config.Default()
	cfg.Model = m
	return New(cfg, nil, nil)
}

func vectorFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "vectors.txt")
	body := "make 1.0 0.0 0.0\nmanufacturer 0.9 0.1 0.0\nprice 0.0 1.0 0.0\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// The vectors path reaches the matcher from [model]. Without this the embed
// matcher is registered, selectable, and always fails to build.
func TestAMatcherReceivesTheConfiguredVectors(t *testing.T) {
	tr := trainerFor(config.Model{Matcher: "embed", Vectors: vectorFile(t)})

	m, report, err := tr.matcherFor()
	if err != nil {
		t.Fatalf("building the embed matcher from [model]: %v", err)
	}
	if m == nil {
		t.Fatal("no matcher was built, so induction would silently use the heuristic")
	}
	if report == nil {
		t.Error("no report hook, so the run could not say what the matcher cost")
	}
}

// An operator who configured a matcher and got the heuristic would have no way
// to tell, and would conclude the matcher was useless rather than unconfigured.
func TestAMatcherMissingItsVectorsSaysSo(t *testing.T) {
	tr := trainerFor(config.Model{Matcher: "embed"})

	if _, _, err := tr.matcherFor(); err == nil {
		t.Fatal("a matcher with no vectors built anyway")
	} else if !strings.Contains(err.Error(), "vectors") {
		t.Errorf("the error does not name what is missing: %v", err)
	}
}

// The heuristic is the default and is not built through the registry, so a run
// that asks for it should get no matcher and no error.
func TestTheHeuristicNeedsNoMatcher(t *testing.T) {
	for _, name := range []string{"", "heuristic"} {
		tr := trainerFor(config.Model{Matcher: name})

		m, _, err := tr.matcherFor()
		if err != nil || m != nil {
			t.Errorf("matcher %q gave (%v, %v), want no matcher and no error", name, m, err)
		}
	}
}

func TestAnUnknownMatcherNamesWhatIsAvailable(t *testing.T) {
	tr := trainerFor(config.Model{Matcher: "nonexistent"})

	_, _, err := tr.matcherFor()
	if err == nil {
		t.Fatal("an unknown matcher was accepted")
	}
	for _, want := range []string{"nonexistent", "embed", "heuristic"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error never mentions %q: %v", want, err)
		}
	}
}
