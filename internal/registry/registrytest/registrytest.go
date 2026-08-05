// SPDX-License-Identifier: GPL-3.0-or-later

// Package registrytest catches a test that leaves something in a global
// registry.
//
// # What it is for
//
// Six packages here keep a package-global table of named implementations, and
// a test that needs one of its own has to put it there. Registering the same
// name twice panics, deliberately, so a test that registers and does not remove
// makes its whole package impossible to run more than once in a process: `go
// test -count=2` panics on the second registration, and `-shuffle=on` panics as
// soon as it reorders a package that registers in more than one test.
//
// That is worse than it sounds. Running the suite repeatedly is how a flaky
// test is found at all, so a package that cannot be run repeatedly is a package
// whose flakiness nobody will ever see. Three packages were in that state, and
// it was found by trying to shuffle the suite rather than by anything failing.
//
// It also produced a test that passed for the wrong reason: one asserted that
// the downloader's table listed "test-drop", a name belonging to a different
// test, and it only passed because that test leaked. Its result depended on a
// second test having run first and having failed to clean up.
//
// # How to use it
//
// Register through a helper that pairs the registration with t.Cleanup, and add
// one line to the package's tests:
//
//	func TestMain(m *testing.M) { registrytest.Main(m, pipeline.Registered) }
//
// Then a leak fails on an ordinary run with a message naming what was left,
// rather than as a panic on the next run that happens to use -count=2.
//
// A package that already has a TestMain wraps [Watch] instead, because os.Exit
// does not compose:
//
//	func TestMain(m *testing.M) {
//		slog.SetDefault(quiet)
//		done := registrytest.Watch(cache.Registered)
//		os.Exit(done(m.Run()))
//	}
package registrytest

import (
	"fmt"
	"os"
	"slices"
	"testing"
)

// Main runs a package's tests and fails if any of them left a name behind.
func Main(m *testing.M, registered func() []string) {
	done := Watch(registered)
	os.Exit(done(m.Run()))
}

// Watch snapshots a registry now and returns the check to run afterwards, which
// takes the exit code and returns the one to exit with.
//
// Split from [Main] because os.Exit does not compose and a package may already
// have a TestMain doing something else.
//
// The snapshot is taken before the tests rather than compared against an empty
// table, so what a package registers at init for its own use is not mistaken
// for a leak: only a name that appeared during the run is one.
func Watch(registered func() []string) func(code int) int {
	before := slices.Clone(registered())
	slices.Sort(before)

	return func(code int) int {
		after := slices.Clone(registered())
		slices.Sort(after)

		var leaked []string
		for _, name := range after {
			if !slices.Contains(before, name) {
				leaked = append(leaked, name)
			}
		}
		if len(leaked) == 0 {
			return code
		}

		fmt.Fprintf(os.Stderr, `
these names were left in a registry by a test: %v

A global table a test adds to and does not remove makes this package impossible
to run twice in one process, because registering a name twice panics. Register
through the package's register(t, ...) helper, which pairs the registration with
t.Cleanup, rather than calling Register directly.
`, leaked)

		if code == 0 {
			return 1
		}
		return code
	}
}
