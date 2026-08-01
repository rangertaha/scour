// SPDX-License-Identifier: GPL-3.0-or-later

package job

import (
	"context"
	"fmt"
	"time"

	ucli "github.com/urfave/cli/v3"

	"github.com/rangertaha/scour/internal/cli"
	"github.com/rangertaha/scour/internal/store"
)

// List prints a line per job.
//
// A job is where the crawling happens, so this is the listing somebody watching
// a crawl wants: what each one is hunting, where, how far it got, and what
// starting it would do. `scour status` is the same listing at the top level.
func List(a *cli.App) *ucli.Command {
	var item string

	return &ucli.Command{
		Name:      "ls",
		Aliases:   []string{"list"},
		ArgsUsage: "[name]",
		Usage:     "A line per job: item, targets, progress, state",
		Description: "With a name, everything about that one, which is `scour job show`.\n\n" +
			"The state says what a start would do. budget and done both end with the\n" +
			"frontier intact, and they are separate because one means there is more to\n" +
			"fetch and the other means there is not.",
		UsageText: "  scour job ls\n" +
			"  scour job ls -i vehicle\n" +
			"  scour --json job ls",
		Flags: []ucli.Flag{
			&ucli.StringFlag{
				Name:        "item",
				Aliases:     []string{"i"},
				Usage:       "only the jobs of one `item`",
				Destination: &item,
			},
		},
		Action: func(c context.Context, cmd *ucli.Command) error {
			args, err := cli.AtMost(cmd, 1, "at most one job name")
			if err != nil {
				return err
			}
			s, err := a.Store()
			if err != nil {
				return err
			}

			// A name means one job, which is what show prints.
			if len(args) == 1 {
				return showJob(c, a, s, args[0])
			}

			var itemID uint
			if item != "" {
				it, err := s.Item(c, item)
				if err != nil {
					return err
				}
				itemID = it.ID
			}

			jobs, err := s.Jobs(c, itemID)
			if err != nil {
				return err
			}
			if len(jobs) == 0 {
				if item != "" {
					return a.Empty("%s has no jobs yet: scour run %s -d <domain>\n", item, item)
				}
				return a.Empty("no jobs yet: scour run <item> -d <domain>\n")
			}

			type row struct {
				Name    string `json:"name"`
				Item    string `json:"item"`
				Targets int    `json:"targets"`
				Queued  int64  `json:"queued"`
				Visited int64  `json:"visited"`
				Records int64  `json:"records"`
				State   string `json:"state"`
				LastRun string `json:"last_run"`
			}

			rows := make([]row, 0, len(jobs))
			for _, j := range jobs {
				it, err := s.ItemByID(c, j.ItemID)
				if err != nil {
					return err
				}
				// The frontier and the records are still counted per item, so
				// two jobs of one item report the same figures until the
				// frontier moves onto the job.
				st, err := s.Status(c, j.ItemID)
				if err != nil {
					return err
				}
				last := "never"
				if j.LastRunAt != nil {
					last = j.LastRunAt.Format("2006-01-02")
				}
				rows = append(rows, row{
					Name: j.Name, Item: it.Name, Targets: len(j.Targets),
					Queued: st.Queued, Visited: st.Visited, Records: st.Matches,
					State: string(j.State), LastRun: last,
				})
			}

			if a.JSON {
				return cli.WriteJSON(a.Out(), rows)
			}
			t := cli.NewTable(
				[]string{"NAME", "ITEM", "TARGETS", "QUEUED", "VISITED", "RECORDS", "STATE", "LAST RUN"},
				cli.AlignLeft, cli.AlignLeft, cli.AlignRight, cli.AlignRight,
				cli.AlignRight, cli.AlignRight, cli.AlignLeft, cli.AlignLeft,
			)
			for _, r := range rows {
				t.Add(r.Name, r.Item, fmt.Sprintf("%d", r.Targets),
					fmt.Sprintf("%d", r.Queued), fmt.Sprintf("%d", r.Visited),
					fmt.Sprintf("%d", r.Records), r.State, r.LastRun)
			}
			return t.Render(a.Out())
		},
	}
}

// showJob is `job show` reached by naming one on the listing, so the two cannot
// answer differently.
func showJob(c context.Context, a *cli.App, s *store.Store, name string) error {
	job, err := s.Job(c, name)
	if err != nil {
		return err
	}
	item, err := s.ItemByID(c, job.ItemID)
	if err != nil {
		return err
	}
	if a.JSON {
		return cli.WriteJSON(a.Out(), job)
	}
	line := func(label, value string) { a.Printf("%-10s  %s\n", label, value) }
	line("job", job.Name)
	line("item", item.Name)
	line("state", string(job.State))
	line("targets", fmt.Sprintf("%d", len(job.Targets)))
	line("depth", bound(job.Depth, "the configured default"))
	line("max pages", bound(job.MaxPages, "no bound"))
	if job.MaxTime == 0 {
		line("max time", "no bound")
	} else {
		line("max time", time.Duration(job.MaxTime).String())
	}
	return nil
}
