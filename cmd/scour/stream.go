// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/rangertaha/scour/internal/store"
)

type streamFlags struct {
	confidence  float64
	types       []string
	excludeType []string
	label       string
	follow      bool

	// out carries the record to somewhere other than the terminal. Records
	// used to leave through `scour export`, which now writes the url list, so
	// the destination moved to the command that owns records.
	out exportFlags
}

func newStreamCmd(a *app) *cli.Command {
	var f streamFlags

	cmd := &cli.Command{
		Category:  "ITEMS",
		Name:      "stream",
		Aliases:   []string{"search"},
		ArgsUsage: "<name>",
		Usage:     "Stream live items to STDOUT",
		Description: "One row per match, one column per property you defined. FORMAT is the content\n" +
			"type the record came from, which is how you tell whether one source is\n" +
			"dragging the results down.\n\n" +
			"--follow keeps the stream open and prints each record as it is extracted,\n" +
			"so a search running elsewhere can be watched from here. With --json each\n" +
			"record is one line, which is what a pipe on the other end wants.",
		UsageText: "  scour stream vehicle --confidence 0.5\n" +
			"  scour stream vehicle --type pdf\n" +
			"  scour stream vehicle --follow\n" +
			"  scour --json stream vehicle --follow | jq .",
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
			&cli.BoolFlag{
				Name:        "follow",
				Aliases:     []string{"f"},
				Usage:       "keep printing records as they are extracted",
				Destination: &f.follow,
			},
			&cli.StringFlag{
				Name:        "write",
				Usage:       "write the records out instead of printing them: " + exportFormats(),
				Destination: &f.out.format,
			},
			&cli.StringFlag{
				Name:        "to",
				Usage:       "`destination` for --write: a directory, or a URL for the webhook format",
				Destination: &f.out.to,
			},
			&cli.StringFlag{
				Name:        "token-env",
				Usage:       "environment `variable` holding the webhook bearer token",
				Destination: &f.out.tokenEnv,
			},
		},
		Action: func(c context.Context, cmd *cli.Command) error {
			args, err := need(cmd, 1, "one item name")
			if err != nil {
				return err
			}
			return runStream(c, a, args[0], f)
		},
	}

	// The FORMAT column is a content type, while a property named "type" is
	// the user's own; --format says which one is meant.

	return cmd
}

func runStream(c context.Context, a *app, name string, f streamFlags) error {
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

	// Writing records somewhere is a different job from printing them, and
	// following a destination that is a file would mean rewriting it every
	// second.
	if f.out.format != "" {
		if f.follow {
			return fmt.Errorf("--write and --follow are different jobs: one ends, the other does not")
		}
		f.out.confidence, f.out.label, f.out.limit = f.confidence, f.label, a.limit
		return runExport(c, a, name, f.out)
	}

	item, err := s.ItemFull(c, name)
	if err != nil {
		return err
	}

	query := store.RecordQuery{
		MinConfidence: f.confidence,
		Formats:       f.types,
		ExcludeFormat: f.excludeType,
		Label:         label,
		Limit:         a.limit,
	}
	rows, total, err := s.SearchRecords(c, item.ID, query)
	if err != nil {
		return err
	}

	if a.jsonOut {
		return writeJSON(a.Out(), rows)
	}
	if len(rows) == 0 && !f.follow {
		filtered := f.confidence > 0 || len(f.types) > 0 || len(f.excludeType) > 0 || label != ""
		if filtered {
			// total is already filtered, so it says nothing about whether the
			// item has records at all. Ask again without the filters rather
			// than telling someone to train a model they have already trained.
			_, all, err := s.SearchRecords(c, item.ID, store.RecordQuery{})
			if err != nil {
				return err
			}
			a.Printf("no records matched, out of %d\n", all)
			return nil
		}
		a.Printf("no records yet: scour train %s\n", item.Name)
		return nil
	}

	props := propOrder(item, rows)
	if len(rows) == 0 && f.follow {
		a.Printf("waiting for records from %s\n", item.Name)
		return follow(c, a, s, item, query, props, 0)
	}
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

	if f.follow {
		var mark uint
		for _, r := range rows {
			if r.ID > mark {
				mark = r.ID
			}
		}
		return follow(c, a, s, item, query, props, mark)
	}
	return nil
}

// followInterval is how often a follower asks for what is new.
//
// A search fetches at most a few pages a second and only some of them yield a
// record, so anything shorter is mostly empty queries against a database a
// crawl is trying to write to.
const followInterval = time.Second

// follow prints records as they are extracted, until the context is cancelled.
//
// It polls rather than subscribing to the bus. A search on this machine writes
// to the database and publishes nothing, so a follower built on the bus would
// show nothing at all in the ordinary single-process case, which is where it is
// most likely to be used.
func follow(c context.Context, a *app, s *store.Store, item *store.Item,
	query store.RecordQuery, props []string, mark uint,
) error {
	tick := time.NewTicker(followInterval)
	defer tick.Stop()

	for {
		select {
		case <-c.Done():
			// Ending because the reader asked is not a failure.
			return nil
		case <-tick.C:
		}

		q := query
		q.SinceID = mark
		q.Limit = 0
		fresh, _, err := s.SearchRecords(c, item.ID, q)
		if err != nil {
			return err
		}
		for _, r := range fresh {
			if r.ID > mark {
				mark = r.ID
			}
			if a.jsonOut {
				// One record per line, so the far end of a pipe can read them
				// as they arrive instead of waiting for a closing bracket.
				line, err := json.Marshal(r)
				if err != nil {
					return err
				}
				a.Printf("%s\n", line)
				continue
			}
			// Padded to the widths the table above used, so a followed row
			// lines up with the ones already printed instead of starting a
			// second, narrower table underneath the first.
			cells := []string{
				fmt.Sprintf("%4s", strconv.FormatUint(uint64(r.ID), 10)),
				fmt.Sprintf("%.2f", r.Confidence),
				fmt.Sprintf("%-6s", r.Format),
			}
			for _, p := range props {
				cells = append(cells, fmt.Sprintf("%-24s", truncate(r.Values[p], 24)))
			}
			a.Printf("%s\n", strings.TrimRight(strings.Join(cells, "  "), " "))
		}
		if flusher, ok := a.Out().(interface{ Flush() error }); ok {
			_ = flusher.Flush()
		}
	}
}

// propOrder lists the columns to print: the item's own properties in the
// order they were defined, then anything extraction found that is not one of
// them, so a surprise field is visible rather than silently dropped.
func propOrder(item *store.Item, rows []store.RecordRow) []string {
	seen := map[string]bool{}
	props := make([]string, 0, len(item.Properties))
	for _, p := range item.Properties {
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
