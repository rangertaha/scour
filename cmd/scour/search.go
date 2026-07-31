// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/rangertaha/scour/internal/store"
)

type searchFlags struct {
	confidence  float64
	types       []string
	excludeType []string
	label       string
}

func newSearchCmd(a *app) *cli.Command {
	var f searchFlags

	cmd := &cli.Command{
		Category:  "Reading the results",
		Name:      "search",
		ArgsUsage: "<name>",
		Usage:     "Search the records extracted for an entity",
		Description: "One row per match, one column per property you defined. FORMAT is the content\n" +
			"type the record came from, which is how you tell whether one source is\n" +
			"dragging the results down.",
		UsageText: "  scour search vehicle --confidence 0.5\n" +
			"  scour search vehicle --type pdf\n" +
			"  scour search vehicle --exclude-type pdf --limit 50",
		Flags: []cli.Flag{
			&cli.FloatFlag{
				Name:        "confidence",
				Usage:       "only records at or above this confidence, 0 to 1",
				Destination: &f.confidence,
			},
			&cli.StringSliceFlag{
				Name:        "type",
				Usage:       "only records extracted from a content type (repeatable)",
				Destination: &f.types,
			},
			&cli.StringSliceFlag{
				Name:        "exclude-type",
				Usage:       "skip records from a content type (repeatable)",
				Destination: &f.excludeType,
			},
			&cli.StringFlag{
				Name:        "label",
				Usage:       "only records with this label: valid, invalid, unlabelled",
				Destination: &f.label,
			},
			&cli.StringSliceFlag{
				Name:        "format",
				Usage:       "alias for --type",
				Destination: &f.types,
			},
		},
		Action: func(c context.Context, cmd *cli.Command) error {
			args, err := need(cmd, 1, "one entity name")
			if err != nil {
				return err
			}
			return runSearch(c, a, args[0], f)
		},
	}

	// The FORMAT column is a content type, while a property named "type" is
	// the user's own; --format says which one is meant.

	return cmd
}

func runSearch(c context.Context, a *app, name string, f searchFlags) error {
	if f.confidence < 0 || f.confidence > 1 {
		return fmt.Errorf("--confidence must be between 0 and 1, got %v", f.confidence)
	}
	label, err := parseLabel(f.label)
	if err != nil {
		return err
	}

	s, err := a.Store()
	if err != nil {
		return err
	}

	entity, err := s.EntityFull(c, name)
	if err != nil {
		return err
	}

	rows, total, err := s.SearchRecords(c, entity.ID, store.RecordQuery{
		MinConfidence: f.confidence,
		Formats:       f.types,
		ExcludeFormat: f.excludeType,
		Label:         label,
		Limit:         a.limit,
	})
	if err != nil {
		return err
	}

	if a.jsonOut {
		return writeJSON(a.Out(), rows)
	}
	if len(rows) == 0 {
		filtered := f.confidence > 0 || len(f.types) > 0 || len(f.excludeType) > 0 || label != ""
		if filtered {
			// total is already filtered, so it says nothing about whether the
			// entity has records at all. Ask again without the filters rather
			// than telling someone to train a model they have already trained.
			_, all, err := s.SearchRecords(c, entity.ID, store.RecordQuery{})
			if err != nil {
				return err
			}
			a.Printf("no records matched, out of %d\n", all)
			return nil
		}
		a.Printf("no records yet: scour train %s\n", entity.Name)
		return nil
	}

	props := propOrder(entity, rows)
	headers := append([]string{"ID", "CONF", "FORMAT"}, upper(props)...)
	aligns := []align{alignRight, alignRight, alignLeft}
	for range props {
		aligns = append(aligns, alignLeft)
	}

	t := newTable(headers, aligns...)
	for _, r := range rows {
		cells := []string{
			strconv.FormatUint(uint64(r.ID), 10),
			fmt.Sprintf("%.2f", r.Confidence),
			r.Format,
		}
		for _, p := range props {
			cells = append(cells, truncate(r.Values[p], 24))
		}
		t.add(cells...)
	}
	if err := t.render(a.Out()); err != nil {
		return err
	}

	a.Printf("\nshowing %d of %d records\n", len(rows), total)
	return nil
}

// propOrder lists the columns to print: the entity's own properties in the
// order they were defined, then anything extraction found that is not one of
// them, so a surprise field is visible rather than silently dropped.
func propOrder(entity *store.Entity, rows []store.RecordRow) []string {
	seen := map[string]bool{}
	props := make([]string, 0, len(entity.Properties))
	for _, p := range entity.Properties {
		props = append(props, p.Name)
		seen[p.Name] = true
	}

	extra := map[string]bool{}
	for _, r := range rows {
		for prop := range r.Values {
			if !seen[prop] {
				extra[prop] = true
			}
		}
	}
	rest := make([]string, 0, len(extra))
	for prop := range extra {
		rest = append(rest, prop)
	}
	sort.Strings(rest)
	return append(props, rest...)
}

func upper(names []string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = strings.ToUpper(n)
	}
	return out
}

func parseLabel(s string) (store.Label, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "":
		return "", nil
	case "valid":
		return store.Valid, nil
	case "invalid":
		return store.Invalid, nil
	case "unlabelled", "unlabeled":
		return store.Unlabelled, nil
	default:
		return "", fmt.Errorf("unknown label %q: use valid, invalid or unlabelled", s)
	}
}
