// SPDX-License-Identifier: GPL-3.0-or-later

// Package version reports what this build is.
package version

// Version is the release, set by the linker for a built binary and left at
// "dev" for one somebody compiled themselves.
var Version = "dev"

// Commit is the revision this was built from, set by the linker.
var Commit = ""

// String is the version as it is printed.
func String() string {
	if Commit == "" {
		return Version
	}
	return Version + " (" + Commit + ")"
}
