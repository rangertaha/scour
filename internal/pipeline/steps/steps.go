// SPDX-License-Identifier: GPL-3.0-or-later

// Package steps is the four pipeline kinds that need no interpreter.
//
// Import it for its side effect to make them available:
//
//	import _ "github.com/rangertaha/scour/internal/pipeline/steps"
//
// They are in one package because they are one idea: the work every job does to
// every item, which is tidying it, checking it, dropping what has been seen and
// putting the rest in an order. A job that needs something else writes a script
// step, and those live where their interpreters do.
package steps

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/rangertaha/scour/internal/pipeline"
	"github.com/rangertaha/scour/internal/record"
)

func init() {
	pipeline.Register("clean", newClean)
	pipeline.Register("validate", newValidate)
	pipeline.Register("dedupe", newDedupe)
	pipeline.Register("rank", newRank)
}

// clean is rule-driven tidying.
//
// Whitespace, and the fields a site leaves as an empty string rather than
// leaving out. Deliberately not clever: a step that guessed at what a value
// should have been would be extraction, in the wrong place and with less
// information.
type cleanConfig struct {
	// Trim removes surrounding whitespace and collapses runs of it. On by
	// default, because markup indentation is in every value read from an
	// element and nobody wants it.
	Trim *bool `hcl:"trim,optional"`

	// Drop removes named values entirely, for the ones a job extracts in order
	// to compute something and does not want written out.
	Drop []string `hcl:"drop,optional"`

	// Empty drops values that are empty once trimmed, so that "there and
	// blank" stops being distinguishable from "not there" at the point where
	// nothing downstream cares about the difference.
	Empty bool `hcl:"drop_empty,optional"`
}

func newClean(_ context.Context, cfg pipeline.Config) (pipeline.Step, error) {
	var c cleanConfig
	if err := cfg.Body.Decode(&c); err != nil {
		return nil, err
	}
	trim := c.Trim == nil || *c.Trim

	return pipeline.Func(func(_ context.Context, records []*record.Record) ([]*record.Record, error) {
		out := make([]*record.Record, 0, len(records))
		for _, r := range records {
			if !mine(cfg, r) {
				out = append(out, r)
				continue
			}

			cleaned := r.Clone()
			for _, name := range c.Drop {
				delete(cleaned.Values, name)
			}
			for name, value := range cleaned.Values {
				if trim {
					value = strings.Join(strings.Fields(value), " ")
					cleaned.Values[name] = value
				}
				if c.Empty && value == "" {
					delete(cleaned.Values, name)
				}
			}
			out = append(out, cleaned)
		}
		return out, nil
	}), nil
}

// validate enforces what the shape said.
//
// A record missing a required property is not a record: exporting it produces a
// row with a hole in it that nothing downstream can tell from a page that
// genuinely had no title.
type validateConfig struct {
	// Drop removes the records that fail rather than failing the run. On by
	// default: most of what a crawl fetches is not what it was looking for,
	// and stopping on the first one would stop every crawl immediately.
	Drop *bool `hcl:"drop,optional"`

	// Require are extra properties this step insists on, beyond the ones the
	// shape marked required.
	Require []string `hcl:"require,optional"`
}

func newValidate(_ context.Context, cfg pipeline.Config) (pipeline.Step, error) {
	var c validateConfig
	if err := cfg.Body.Decode(&c); err != nil {
		return nil, err
	}
	drop := c.Drop == nil || *c.Drop

	required := append([]string(nil), c.Require...)
	if cfg.Item != nil {
		for _, p := range cfg.Item.Properties {
			if p.Required {
				required = append(required, p.Name)
			}
		}
	}

	return pipeline.Func(func(_ context.Context, records []*record.Record) ([]*record.Record, error) {
		out := make([]*record.Record, 0, len(records))
		for _, r := range records {
			if !mine(cfg, r) {
				out = append(out, r)
				continue
			}

			var missing []string
			for _, name := range required {
				if strings.TrimSpace(r.Values[name]) == "" {
					missing = append(missing, name)
				}
			}
			switch {
			case len(missing) == 0:
				out = append(out, r)
			case drop:
				continue
			default:
				return nil, fmt.Errorf("%s: %s found nothing", r.URL, strings.Join(missing, ", "))
			}
		}
		return out, nil
	}), nil
}

// dedupe drops items already seen.
//
// By the values that identify the item rather than by the whole record, because
// the same article at two URLs is one article and a crawl that followed both
// paths to it should not say otherwise.
type dedupeConfig struct {
	// By names the values that identify an item. Empty uses the shape's tags,
	// and failing that the URL, which is the weakest useful answer.
	By []string `hcl:"by,optional"`
}

func newDedupe(_ context.Context, cfg pipeline.Config) (pipeline.Step, error) {
	var c dedupeConfig
	if err := cfg.Body.Decode(&c); err != nil {
		return nil, err
	}

	by := c.By
	if len(by) == 0 && cfg.Item != nil {
		by = cfg.Item.Tags()
	}

	return pipeline.Func(func(_ context.Context, records []*record.Record) ([]*record.Record, error) {
		seen := map[string]bool{}
		out := make([]*record.Record, 0, len(records))

		for _, r := range records {
			if !mine(cfg, r) {
				out = append(out, r)
				continue
			}

			key := r.URL
			if len(by) > 0 {
				parts := make([]string, 0, len(by))
				for _, name := range by {
					parts = append(parts, r.Values[name])
				}
				key = strings.Join(parts, "\x00")
			}
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, r)
		}
		return out, nil
	}), nil
}

// rank scores and orders.
//
// A stable sort, so records that score the same come out in the order they
// arrived and two runs over one corpus produce the same file.
type rankConfig struct {
	// By is the value to order on. Empty orders by the item's own time if it
	// has one, which is what a feed wants.
	By string `hcl:"by,optional"`

	// Descending puts the largest first.
	Descending bool `hcl:"descending,optional"`

	// Limit keeps only the first n. Zero keeps everything.
	Limit int `hcl:"limit,optional"`
}

func newRank(_ context.Context, cfg pipeline.Config) (pipeline.Step, error) {
	var c rankConfig
	if err := cfg.Body.Decode(&c); err != nil {
		return nil, err
	}
	if c.Limit < 0 {
		return nil, fmt.Errorf("limit: %d is negative", c.Limit)
	}

	by := c.By
	if by == "" && cfg.Item != nil {
		by = cfg.Item.Time
	}

	return pipeline.Func(func(_ context.Context, records []*record.Record) ([]*record.Record, error) {
		mineOnly := make([]*record.Record, 0, len(records))
		others := make([]*record.Record, 0, len(records))
		for _, r := range records {
			if mine(cfg, r) {
				mineOnly = append(mineOnly, r)
			} else {
				others = append(others, r)
			}
		}

		sort.SliceStable(mineOnly, func(i, j int) bool {
			left, right := value(mineOnly[i], by), value(mineOnly[j], by)
			if c.Descending {
				return less(right, left)
			}
			return less(left, right)
		})

		if c.Limit > 0 && len(mineOnly) > c.Limit {
			mineOnly = mineOnly[:c.Limit]
		}
		return append(mineOnly, others...), nil
	}), nil
}

// mine reports whether a step should touch this record.
//
// A step is named for the item it works on, so `clean.article` leaves a price
// alone. A step whose name is not an item's works on everything, which is what
// a `python` step called "enrich" means.
func mine(cfg pipeline.Config, r *record.Record) bool {
	if cfg.Item == nil {
		return true
	}
	return r.Item == cfg.Name
}

func value(r *record.Record, name string) string {
	if name == "" {
		return r.URL
	}
	return r.Values[name]
}

// less orders two values, as numbers when both are and as text otherwise. A
// price of "9" sorting after "10" is the kind of thing nobody notices until a
// report is wrong.
func less(a, b string) bool {
	left, leftOK := strconv.ParseFloat(strings.TrimSpace(a), 64)
	right, rightOK := strconv.ParseFloat(strings.TrimSpace(b), 64)
	if leftOK == nil && rightOK == nil {
		return left < right
	}
	return a < b
}

// Kinds lists what this package registers.
func Kinds() []string { return []string{"clean", "dedupe", "rank", "validate"} }
