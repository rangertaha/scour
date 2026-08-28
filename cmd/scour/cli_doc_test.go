// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"os"
	"regexp"
	"strings"
	"testing"

	ucli "github.com/urfave/cli/v3"

	"github.com/rangertaha/scour/internal/cli"
)

// docs/cli.md is checked against the binary rather than trusted.
//
// A command surface that has drifted from its documentation is worse than an
// undocumented one: somebody reads it, types what it says, and is told there is
// no such flag.
//
// This read CLI.md, which was the command reference at the repository root and
// has been folded into the book's own chapter on the command line. The chapter
// carries the tables these checks need: what exists today, and a section per
// command with its flags.

func doc(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../../docs/cli/index.md")
	if err != nil {
		t.Fatalf("read the cli chapter: %v", err)
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
			t.Errorf("the cli chapter does not document `scour %s`", cmd.Name)
		}
	}
}

// builtRow matches a row of the "What exists today" table: | `scour crawl` | ... |
var builtRow = regexp.MustCompile("(?m)^\\|\\s*`scour ([a-z]+)`\\s*\\|")

// TestWhatExistsTodayIsWhatExists holds that table to the binary, in both
// directions.
//
// The direction that matters is the second. A command built and left out of the
// table reads as a command that does not exist, and this file spent a while
// saying five things were built when there were twelve: everything a person
// could actually run, from `scour crawl` to `scour server`, was described here
// as waiting on the stages.
func TestWhatExistsTodayIsWhatExists(t *testing.T) {
	a := &cli.App{Out: os.Stdout, Err: os.Stderr}

	have := map[string]bool{}
	for _, cmd := range root(a).Commands {
		if !cmd.Hidden {
			have[cmd.Name] = true
		}
	}

	section := between(t, doc(t), "## What exists today", true)
	claimed := map[string]bool{}
	for _, row := range builtRow.FindAllStringSubmatch(section, -1) {
		claimed[row[1]] = true
	}
	if len(claimed) == 0 {
		t.Fatal("the cli chapter has no table of what exists, so this check is not checking anything")
	}

	for name := range claimed {
		if !have[name] {
			t.Errorf("the cli chapter says `scour %s` is built, and the binary has no such command", name)
		}
	}
	for name := range have {
		if !claimed[name] {
			t.Errorf("`scour %s` is built, and the cli chapter's table of what exists leaves it out", name)
		}
	}
}

// TestEveryFlagIsDocumented catches the drift that actually happens: a flag
// added to a command and never written down.
func TestEveryFlagIsDocumented(t *testing.T) {
	src := doc(t)
	a := &cli.App{Out: os.Stdout, Err: os.Stderr}

	for _, cmd := range root(a).Commands {
		section := between(t, src, "#### `scour "+cmd.Name, false)
		if section == "" {
			if len(flagNames(cmd)) > 0 {
				t.Errorf("`scour %s` has flags and no section in the cli chapter", cmd.Name)
			}
			continue
		}
		// A command's subcommands are documented in its own section, as one
		// table: `--corpus` belongs to `topic propose` and `topic train`, and
		// splitting the table three ways would say less.
		for _, name := range flagNames(cmd) {
			if !strings.Contains(section, "--"+name) {
				t.Errorf("`scour %s --%s` is not documented", cmd.Name, name)
			}
		}
	}
}

// flagRow matches a documented flag: | `--pages <n>` | How many ... |
var flagRow = regexp.MustCompile("(?m)^\\|\\s*`(--[a-z-]+)")

// TestEveryDocumentedFlagExists is the other direction, and it is the one that
// wastes somebody's afternoon.
//
// A flag written down and never built is not a gap, it is an instruction that
// fails. `scour job train` was documented here with `--url`, `-i` and `--replace`,
// which sound like exactly what somebody teaching it an answer would reach for,
// and the binary has never had any of them.
//
// Only the flag tables, not the prose: a section explaining why `--write` is
// needed is not a claim that this command takes it.
func TestEveryDocumentedFlagExists(t *testing.T) {
	src := doc(t)
	a := &cli.App{Out: os.Stdout, Err: os.Stderr}

	checked := 0
	for _, cmd := range root(a).Commands {
		section := between(t, src, "#### `scour "+cmd.Name, false)
		if section == "" {
			continue
		}

		have := map[string]bool{}
		for _, name := range flagNames(cmd) {
			have[name] = true
		}
		// A subcommand's flags are documented in the parent's table, so they
		// are what the parent's table is allowed to name.
		for _, sub := range cmd.Commands {
			for _, name := range flagNames(sub) {
				have[name] = true
			}
		}

		for _, row := range flagRow.FindAllStringSubmatch(section, -1) {
			checked++
			name := strings.TrimPrefix(row[1], "--")
			if !have[name] {
				t.Errorf("the cli chapter documents `scour %s --%s`, and nothing takes it", cmd.Name, name)
			}
		}
	}

	if checked == 0 {
		t.Fatal("no documented flags were found, so this check is not checking anything")
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
			t.Errorf("the cli chapter does not document exit code %s", want)
		}
	}
}

// between returns the text under a heading, up to the next heading of any
// level, or to the end of the file when toEnd is set.
//
// Any level, and that matters. It used to stop at the next "####", so the
// `scour server` section ran on through `### Secrets` and claimed its flag
// table: the reverse flag check then reported that `scour server` documents a
// `--key-file` it does not take, which was the check being wrong rather than
// the document.
func between(t *testing.T, src, start string, toEnd bool) string {
	t.Helper()

	i := strings.Index(src, start)
	if i < 0 {
		return ""
	}
	rest := src[i+len(start):]
	if toEnd {
		return rest
	}
	if j := strings.Index(rest, "\n#"); j >= 0 {
		return rest[:j]
	}
	return rest
}
