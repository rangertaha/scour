// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/rangertaha/scour/internal/store"
)

func newStatusCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "status [name]",
		Short: "Show target, frontier, match and model state",
		Long: "With a name, everything known about one entity. With none, a line per\n" +
			"entity, which is what you want when several are being crawled at once.\n\n" +
			"Crawls resume from the stored frontier, so this is also where you see what a\n" +
			"restarted crawl will pick up.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := a.Store()
			if err != nil {
				return err
			}
			c := ctx(cmd)

			if len(args) == 0 {
				return runFleetStatus(cmd, a, s)
			}

			entity, err := s.Entity(c, args[0])
			if err != nil {
				return err
			}
			st, err := s.Status(c, entity.ID)
			if err != nil {
				return err
			}

			if a.jsonOut {
				return writeJSON(cmd.OutOrStdout(), st)
			}
			return renderStatus(cmd, a, entity.Name, st)
		},
	}
}

func renderStatus(cmd *cobra.Command, a *app, name string, st *store.Status) error {
	out := cmd.OutOrStdout()

	line := func(label, value string) {
		fmt.Fprintf(out, "%-10s  %s\n", label, value)
	}

	line("entity", name)
	line("targets", fmt.Sprintf("%d  (%d aliases, %d properties)", st.Targets, st.Aliases, st.Properties))
	line("frontier", fmt.Sprintf("%d queued / %d visited", st.Queued, st.Visited))

	if st.Failed > 0 || st.Skipped > 0 {
		line("", fmt.Sprintf("%d failed, %d skipped", st.Failed, st.Skipped))
	}

	if len(st.Formats) > 0 {
		line("formats", joinCounts(st.Formats))
	}
	if len(st.Roles) > 0 {
		line("roles", joinCounts(st.Roles))
	}

	pages, err := a.Pages()
	if err != nil {
		return err
	}
	stats, err := pages.Stats(ctx(cmd))
	if err != nil {
		return err
	}
	line("cache", fmt.Sprintf("%d pages, %s", stats.Pages, formatBytes(stats.Bytes)))

	if st.Rules > 0 {
		line("rules", fmt.Sprintf("%d", st.Rules))
	}
	line("matches", fmt.Sprintf("%d  (%d valid, %d invalid, %d unlabelled)",
		st.Matches, st.Valid, st.Invalid, st.Unlabelled))

	if st.Model != nil {
		line("model", fmt.Sprintf("%s, trained %s, accuracy %.2f",
			st.Model.Algorithm, st.Model.TrainedAt.Format("2006-01-02"), st.Model.Accuracy))
	} else {
		line("model", "not trained yet: scour train "+name)
	}
	return nil
}

// intCounts widens a count map for [joinCounts].
func intCounts(in map[string]int) map[string]int64 {
	out := make(map[string]int64, len(in))
	for k, v := range in {
		out[k] = int64(v)
	}
	return out
}

// joinCounts renders a count map as "html 8402, pdf 401", largest first.
func joinCounts(counts map[string]int64) string {
	type kv struct {
		k string
		n int64
	}
	pairs := make([]kv, 0, len(counts))
	for k, n := range counts {
		pairs = append(pairs, kv{k, n})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].n != pairs[j].n {
			return pairs[i].n > pairs[j].n
		}
		return pairs[i].k < pairs[j].k
	})

	parts := make([]string, 0, len(pairs))
	for _, p := range pairs {
		parts = append(parts, fmt.Sprintf("%s %d", p.k, p.n))
	}
	return strings.Join(parts, ", ")
}

// runFleetStatus prints one line per entity.
//
// A service crawling several entities at once needs the shape of the whole
// fleet more often than the detail of any one of them: which are stalled,
// which are producing, which have never been trained.
func runFleetStatus(cmd *cobra.Command, a *app, s *store.Store) error {
	c := ctx(cmd)

	entities, err := s.Entities(c)
	if err != nil {
		return err
	}
	if len(entities) == 0 {
		cmd.Println("no entities yet: scour add <name>")
		return nil
	}

	type row struct {
		Name    string `json:"name"`
		Targets int64  `json:"targets"`
		Queued  int64  `json:"queued"`
		Visited int64  `json:"visited"`
		Records int64  `json:"records"`
		Rules   int64  `json:"rules"`
		Trained string `json:"trained"`
	}

	rows := make([]row, 0, len(entities))
	for _, summary := range entities {
		entity, err := s.Entity(c, summary.Name)
		if err != nil {
			return err
		}
		st, err := s.Status(c, entity.ID)
		if err != nil {
			return err
		}

		// A model that was never fitted is the single most useful thing to see
		// across a fleet, because an untrained entity is crawling blind.
		trained := "never"
		if st.Model != nil && !st.Model.TrainedAt.IsZero() {
			trained = st.Model.TrainedAt.Format("2006-01-02")
		}
		rows = append(rows, row{
			Name: summary.Name, Targets: st.Targets, Queued: st.Queued,
			Visited: st.Visited, Records: st.Matches, Rules: st.Rules,
			Trained: trained,
		})
	}

	if a.jsonOut {
		return writeJSON(cmd.OutOrStdout(), rows)
	}

	t := newTable(
		[]string{"NAME", "TARGETS", "QUEUED", "VISITED", "RECORDS", "RULES", "TRAINED"},
		alignLeft, alignRight, alignRight, alignRight, alignRight, alignRight, alignLeft,
	)
	for _, r := range rows {
		t.add(r.Name,
			fmt.Sprintf("%d", r.Targets), fmt.Sprintf("%d", r.Queued),
			fmt.Sprintf("%d", r.Visited), fmt.Sprintf("%d", r.Records),
			fmt.Sprintf("%d", r.Rules), r.Trained)
	}
	return t.render(cmd.OutOrStdout())
}
