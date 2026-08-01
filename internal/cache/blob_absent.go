// SPDX-License-Identifier: GPL-3.0-or-later

//go:build !cloud

package cache

import (
	"fmt"

	"github.com/rangertaha/scour/internal/registry"
)

// The object stores are compiled in only with -tags cloud, because linking the
// AWS and Google SDKs costs 41MB and most crawls keep their pages in a
// directory.
//
// They are still registered here, failing with an explanation. A driver named
// in a configuration file is not a typo, and telling someone their build does
// not include it is a different message from telling them it does not exist.
func init() {
	for _, name := range []string{driverS3, driverGCS} {
		Register(name, missingDriver(name))
	}
}

func missingDriver(name string) registry.Factory[Config, Store] {
	return func(Config) (Store, error) {
		return nil, fmt.Errorf(
			"cache driver %q needs a build with the object stores: go build -tags cloud", name)
	}
}
