// SPDX-License-Identifier: GPL-3.0-or-later

// Package version reports the build version of the scour binary.
package version

import "runtime/debug"

// version is injected for release builds via:
//
//	-ldflags "-X github.com/rangertaha/scour/internal/version.version=v1.2.3"
//
// When empty, which is the common case for `go build` and source installs,
// [Version] derives a value from the build info instead.
var version string

// Version returns the build version, resolved in order of precedence:
//
//  1. the value injected at build time with -ldflags;
//  2. the module version recorded in the build info;
//  3. "dev", when neither is available.
func Version() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" {
		if v := info.Main.Version; v != "(devel)" {
			return v
		}
	}
	return "dev"
}
