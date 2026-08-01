// SPDX-License-Identifier: GPL-3.0-or-later

package job

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	ucli "github.com/urfave/cli/v3"

	"github.com/rangertaha/scour/internal/cli"
	"github.com/rangertaha/scour/internal/store"
)

// Runs lists a job's history.
//
// A run is never created or edited by hand, so it has no command group of its
// own: it is listed and read through the job that produced it. This is the
// listing half, and `job log` is the detail.
func Runs(a *cli.App) *ucli.Command {
	return &ucli.Command{
		Name:      "runs",
		Aliases:   []string{"history"},
		ArgsUsage: "<name>",
		Usage:     "The run history: when each ran, how many pages, how it ended",
		Description: "One row per run, newest first.\n\n" +
			"HOW is the difference that a page count hides. `done` means the frontier\n" +
			"ran out, so the site is finished for the scope it was given. `budget` means\n" +
			"the run stopped on --max-pages or --max-time and there is more waiting.\n" +
			"`failed` carries the error, which `scour job log` prints in full.\n\n" +
			"A row still saying `running` long after it started is a crawl whose process\n" +
			"died: the run opens before the crawl and closes after it, so an interrupted\n" +
			"one leaves the opening behind.",
		UsageText: "  scour job runs uk\n" +
			"  scour --json job runs uk\n\n" +
			"Then read one of them:\n" +
			"  scour job log uk\n" +
			"  scour job log uk --run 7",
		Action: func(c context.Context, cmd *ucli.Command) error {
			args, err := cli.Need(cmd, 1, "one job name")
			if err != nil {
				return err
			}
			return runRuns(c, a, args[0])
		},
	}
}

func runRuns(c context.Context, a *cli.App, name string) error {
	s, err := a.Store()
	if err != nil {
		return err
	}
	job, err := s.Job(c, name)
	if err != nil {
		return err
	}
	runs, err := s.Runs(c, job.ID, a.Limit)
	if err != nil {
		return err
	}
	if len(runs) == 0 {
		return a.Empty("%s has not run yet: scour job start %s\n", name, name)
	}

	if a.JSON {
		return cli.WriteJSON(a.Out(), runs)
	}

	t := cli.NewTable(
		[]string{"RUN", "STARTED", "TOOK", "FETCHED", "FAILED", "SKIPPED", "HOW"},
		cli.AlignRight, cli.AlignLeft, cli.AlignRight,
		cli.AlignRight, cli.AlignRight, cli.AlignRight, cli.AlignLeft)
	for _, r := range runs {
		t.Add(
			strconv.FormatUint(uint64(r.ID), 10),
			r.StartedAt.Local().Format("2006-01-02 15:04"),
			took(&r),
			strconv.Itoa(r.Fetched),
			strconv.Itoa(r.Failed),
			strconv.Itoa(r.Skipped),
			how(&r),
		)
	}
	if err := t.Render(a.Out()); err != nil {
		return err
	}
	a.Printf("\nread one: scour job log %s --run %d\n", name, runs[0].ID)
	return nil
}

// how says what ended a run, in one column.
//
// The budget word is folded in because "budget" alone leaves the next question
// unanswered, and which budget was spent is what decides whether to raise the
// page limit or the time limit.
func how(r *store.Run) string {
	switch r.State {
	case store.RunRunning:
		return "running"
	case store.RunBudget:
		if r.Budget != "" {
			return "budget: " + r.Budget
		}
		return "budget"
	case store.RunFailed:
		return "failed"
	case store.RunStopped:
		return "stopped"
	default:
		return "done"
	}
}

// took is how long a run lasted, rounded to something readable. A run still
// going says so rather than reporting a duration that grows while you read it.
func took(r *store.Run) string {
	d := r.Elapsed()
	switch {
	case r.EndedAt == nil:
		return "..."
	case d < time.Second:
		return d.Round(time.Millisecond).String()
	case d < time.Minute:
		return d.Round(100 * time.Millisecond).String()
	default:
		return d.Round(time.Second).String()
	}
}

// statusLine renders the response histogram most common first, which is the
// order that answers "was this run healthy" at a glance.
func statusLine(counts map[int]int) string {
	if len(counts) == 0 {
		return ""
	}
	codes := make([]int, 0, len(counts))
	for code := range counts {
		codes = append(codes, code)
	}
	sort.Slice(codes, func(i, j int) bool {
		if counts[codes[i]] != counts[codes[j]] {
			return counts[codes[i]] > counts[codes[j]]
		}
		return codes[i] < codes[j]
	})
	parts := make([]string, 0, len(codes))
	for _, code := range codes {
		parts = append(parts, fmt.Sprintf("%d×%d", counts[code], code))
	}
	return strings.Join(parts, "  ")
}
