// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/rangertaha/scour/internal/content"
	"github.com/rangertaha/scour/internal/train"
)

type trainFlags struct {
	limit   int
	types   []string
	noChain bool
}

func newTrainCmd(a *app) *cobra.Command {
	var f trainFlags

	cmd := &cobra.Command{
		Use:   "train <name>",
		Short: "Learn where an entity's properties live, from the pages already crawled",
		Long: "Reads the cached pages, works out an extraction rule per property, saves the\n" +
			"model, and applies it. Records you have labelled valid feed back in, so each\n" +
			"round of labelling sharpens the next model.",
		Example: "  scour train vehicle\n" +
			"  scour train vehicle --pages 200",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTrain(cmd, a, args[0], f)
		},
	}

	fl := cmd.Flags()
	fl.IntVar(&f.limit, "pages", 0, "cap how many cached pages to learn from (0 for all)")
	fl.StringArrayVar(&f.types, "type", nil, "learn only from a content type (repeatable)")
	fl.BoolVar(&f.noChain, "no-chain", false, "skip the crawl chain, scoring each URL on its own tokens")

	return cmd
}

func runTrain(cmd *cobra.Command, a *app, name string, f trainFlags) error {
	s, err := a.Store()
	if err != nil {
		return err
	}
	c := ctx(cmd)

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
		return writeJSON(cmd.OutOrStdout(), result)
	}

	out := cmd.OutOrStdout()
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
	}
	return nil
}
