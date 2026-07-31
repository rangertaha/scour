// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"fmt"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/rangertaha/scour/internal/content"
	"github.com/rangertaha/scour/internal/train"
)

type trainFlags struct {
	limit   int
	types   []string
	noChain bool
}

func newTrainCmd(a *app) *cli.Command {
	var f trainFlags

	cmd := &cli.Command{
		Category:  "Learning where the data is",
		Name:      "train",
		ArgsUsage: "<name>",
		Usage:     "Learn where an entity's properties live, from the pages already crawled",
		Description: "Reads the cached pages, works out an extraction rule per property, saves the\n" +
			"model, and applies it. Records you have labelled valid feed back in, so each\n" +
			"round of labelling sharpens the next model.",
		UsageText: "  scour train vehicle\n" +
			"  scour train vehicle --pages 200",
		Flags: []cli.Flag{
			&cli.IntFlag{
				Name:        "pages",
				Usage:       "cap how many cached pages to learn from (0 for all)",
				Destination: &f.limit,
			},
			&cli.StringSliceFlag{
				Name:        "type",
				Usage:       "learn only from a content type (repeatable)",
				Destination: &f.types,
			},
			&cli.BoolFlag{
				Name:        "no-chain",
				Usage:       "skip the crawl chain, scoring each URL on its own tokens",
				Destination: &f.noChain,
			},
		},
		Action: func(c context.Context, cmd *cli.Command) error {
			args, err := need(cmd, 1, "one entity name")
			if err != nil {
				return err
			}
			return runTrain(c, a, args[0], f)
		},
	}

	return cmd
}

func runTrain(c context.Context, a *app, name string, f trainFlags) error {
	s, err := a.Store()
	if err != nil {
		return err
	}

	entity, err := s.EntityFull(c, name)
	if err != nil {
		return err
	}

	var types *content.Set
	if len(f.types) > 0 {
		if types, err = content.New(f.types, nil); err != nil {
			return err
		}
	}

	pages, err := a.Pages()
	if err != nil {
		return err
	}
	trainer := train.New(a.cfg, s, pages)
	result, err := trainer.Run(c, entity, train.Options{
		Limit:   f.limit,
		Types:   types,
		NoChain: f.noChain,
	})
	if err != nil {
		return err
	}

	if a.jsonOut {
		return writeJSON(a.Out(), result)
	}

	out := a.Out()
	fmt.Fprintf(out, "pages       %d read, %d skipped, %s\n", result.Pages, result.Skipped, formatBytes(result.Bytes))
	if result.Corrected > 0 {
		fmt.Fprintf(out, "labels      %d valid records fed back in\n", result.Corrected)
	}
	fmt.Fprintf(out, "rules       %d\n", result.Rules)
	fmt.Fprintf(out, "records     %d\n", result.Records)

	if sc := result.Score; sc != nil {
		fmt.Fprintf(out, "examples    %d positive / %d negative", sc.Positive, sc.Negative)
		if sc.Accuracy > 0 {
			fmt.Fprintf(out, "  (accuracy %.2f, held out)", sc.Accuracy)
		}
		fmt.Fprintln(out)
	}
	if cl := result.Classify; cl != nil {
		fmt.Fprintf(out, "pages read  %s", joinCounts(intCounts(cl.Categories)))
		if cl.Rescued > 0 {
			// The number the classifier exists to produce: pages that are
			// plainly relevant but that extraction has not yet succeeded on.
			fmt.Fprintf(out, "  (%d rescued)", cl.Rescued)
		}
		fmt.Fprintln(out)
		fmt.Fprintf(out, "classifier  %s: %d asked, %d cached", cl.Name, cl.Calls, cl.Cached)
		if cl.Errors > 0 {
			fmt.Fprintf(out, ", %d failed", cl.Errors)
		}
		fmt.Fprintln(out)
	}
	if ch := result.Chain; ch != nil && ch.Pages > 0 {
		fmt.Fprintf(out, "roles       %s  (over %d paths)\n", joinCounts(intCounts(ch.Roles)), ch.Paths)
	}
	fmt.Fprintf(out, "elapsed     %s\n", result.Elapsed.Round(time.Millisecond))

	if sc := result.Score; sc != nil && len(sc.Top) > 0 {
		fmt.Fprintln(out)
		t := newTable([]string{"FEATURE", "WEIGHT"}, alignLeft, alignRight)
		for _, w := range sc.Top {
			t.add(w.Token, fmt.Sprintf("%+.2f", w.Weight))
		}
		for _, w := range sc.Worst {
			t.add(w.Token, fmt.Sprintf("%+.2f", w.Weight))
		}
		if err := t.render(out); err != nil {
			return err
		}
	}

	fmt.Fprintf(out, "\nmodel written to %s\n", result.ModelPath)
	if sc := result.Score; sc != nil {
		fmt.Fprintf(out, "scorer written to %s\n", sc.Path)
	}

	if result.Records == 0 {
		fmt.Fprintf(out, "\nno records extracted: check `scour rules %s`, and that the crawl reached pages holding the data\n", name)
	} else {
		// What was learned is only worth having if it gets looked at.
		fmt.Fprintf(out, "\nnext: scour search %s\n", name)
	}
	return nil
}
