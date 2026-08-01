// SPDX-License-Identifier: GPL-3.0-or-later

package schedule

// The policies scour ships with. Each is a few lines, which is the point: the
// interesting part of a scheduler is the decision, not the machinery.

// fixed always answers the same way.
type fixed struct {
	name  string
	order Order
}

func (f fixed) Name() string      { return f.name }
func (f fixed) Order(State) Order { return f.order }

// best is best first, and the default: the highest scoring URL waiting.
//
// Until a model has been trained every score is equal, so this degrades to
// insertion order on the first crawl, which is the breadth first walk colly
// would have done anyway.
func best() Policy { return fixed{name: "best", order: ByScore} }

// warmup crawls breadth first until a model exists, then best first.
//
// The first crawl of a site scores nothing meaningfully, so ordering by score
// is ordering by noise. Breadth first until there is a model to consult spends
// that crawl on coverage instead, which is what the model is then induced
// from. After that it is best first like any other.
type warmup struct{ after int }

func (w warmup) Name() string { return "warmup" }

func (w warmup) Order(s State) Order {
	if s.Trained && s.Fetched >= w.after {
		return ByScore
	}
	return Breadth
}

func init() {
	Register("best", func(Config) (Policy, error) { return best(), nil })
	Register("breadth", func(Config) (Policy, error) {
		return fixed{name: "breadth", order: Breadth}, nil
	})
	Register("depth", func(Config) (Policy, error) {
		return fixed{name: "depth", order: Depth}, nil
	})
	Register("random", func(Config) (Policy, error) {
		return fixed{name: "random", order: Random}, nil
	})
	Register("warmup", func(cfg Config) (Policy, error) {
		after := cfg.Switch
		if after <= 0 {
			// Enough pages for the scorer to have seen more than one site's
			// worth of markup, and small enough to matter on a short crawl.
			after = 50
		}
		return warmup{after: after}, nil
	})
}
