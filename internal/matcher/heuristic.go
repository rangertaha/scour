// SPDX-License-Identifier: GPL-3.0-or-later

package matcher

import (
	"github.com/rangertaha/scour/internal/wom"
)

func init() {
	Register("heuristic", func(cfg Config) (wom.Matcher, error) {
		if cfg.Base != nil {
			return cfg.Base, nil
		}
		return wom.Heuristic{}, nil
	})
}

// base returns the matcher to consult first, defaulting to the heuristic.
//
// Every richer matcher is built on top of this rather than instead of it. The
// heuristic is fast, free and deterministic, and it is right most of the time;
// what a model adds is judgement in the cases where it is not.
func base(cfg Config) wom.Matcher {
	if cfg.Base != nil {
		return cfg.Base
	}
	return wom.Heuristic{}
}
