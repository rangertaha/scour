// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"os"
	"strings"
	"testing"

	ucli "github.com/urfave/cli/v3"

	"github.com/rangertaha/scour/internal/cli"
)

// CLI.md is checked against the binary rather than trusted.
//
// A command surface that has drifted from its documentation is worse than an
// undocumented one: somebody reads it, types what it says, and is told there is
// no such flag.

func doc(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../../CLI.md")
	if err != nil {
		t.Fatalf("read CLI.md: %v", err)
	}
	return string(b)
}

func TestEveryCommandIsDocumented(t *testing.T) {
	src := doc(t)
	a := &cli.App{Out: os.Stdout, Err: os.Stderr}

	for _, cmd := range root(a).Commands {
		if cmd.Hidden {
			continue
		}
		if !strings.Contains(src, "scour "+cmd.Name) {
			t.Errorf("CLI.md does not document `scour %s`", cmd.Name)
		}
	}
}

func TestEveryDocumentedCommandExists(t *testing.T) {
	src := doc(t)
	a := &cli.App{Out: os.Stdout, Err: os.Stderr}

	have := map[string]bool{}
	for _, cmd := range root(a).Commands {
		have[cmd.Name] = true
	}

	// Only the commands claimed to exist today. The ones under "Running" are
	// deliberately written before they are built.
	section := between(t, src, "## What exists today", "")
	for _, name := range []string{"init", "validate", "show", "spec", "defaults"} {
		if !strings.Contains(section, "`"+name+"`") {
			continue
		}
		if !have[name] {
			t.Errorf("CLI.md says `%s` exists, and it does not", name)
		}
	}
}

// TestEveryFlagIsDocumented catches the drift that actually happens: a flag
// added to a command and never written down.
func TestEveryFlagIsDocumented(t *testing.T) {
	src := doc(t)
	a := &cli.App{Out: os.Stdout, Err: os.Stderr}

	for _, cmd := range root(a).Commands {
		section := between(t, src, "#### `scour "+cmd.Name, "####")
		if section == "" {
			if len(flagNames(cmd)) > 0 {
				t.Errorf("`scour %s` has flags and no section in CLI.md", cmd.Name)
			}
			continue
		}
		for _, name := range flagNames(cmd) {
			if !strings.Contains(section, "--"+name) {
				t.Errorf("`scour %s --%s` is not documented", cmd.Name, name)
			}
		}
	}
}

func flagNames(cmd *ucli.Command) []string {
	var out []string
	for _, f := range cmd.Flags {
		for _, n := range f.Names() {
			// Long names only; the short forms are listed beside them.
			if len(n) > 1 {
				out = append(out, n)
			}
		}
	}
	return out
}

// TestExitCodesAreDocumented keeps the table honest, since a script depends on
// it more than a person does.
func TestExitCodesAreDocumented(t *testing.T) {
	src := doc(t)
	for _, want := range []string{
		"| 0 |", "| 1 |", "| 2 |", "| 3 |",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("CLI.md does not document exit code %s", want)
		}
	}
}

// between returns the text from the first heading to the next one starting
// with stop, or to the end when stop is empty.
func between(t *testing.T, src, start, stop string) string {
	t.Helper()

	i := strings.Index(src, start)
	if i < 0 {
		return ""
	}
	rest := src[i+len(start):]
	if stop == "" {
		return rest
	}
	if j := strings.Index(rest, stop); j >= 0 {
		return rest[:j]
	}
	return rest
}
