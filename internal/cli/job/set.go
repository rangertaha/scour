// SPDX-License-Identifier: GPL-3.0-or-later

package job

import (
	"context"
	"time"

	ucli "github.com/urfave/cli/v3"

	"github.com/rangertaha/scour/internal/cli"
	"github.com/rangertaha/scour/internal/store"
)

// Set overwrites a bound on a job.
//
// It is the third verb because it overwrites where add accumulates, and that is
// the whole test: `job set uk --depth 12` replaces the depth that was there,
// `job add uk -d example.com` leaves the domains that were there and adds one
// more. It is about what happens to the existing value, not about how many
// values you may give.
//
// The same edit `scour run` makes when given these flags, without the crawl,
// for changing a budget on something that is not running.
func Set(a *cli.App) *ucli.Command {
	var depth, maxPages int
	var maxTime time.Duration

	return &ucli.Command{
		Name:      "set",
		ArgsUsage: "<name>",
		Usage:     "Change a bound on a job",
		Description: "Only the bounds given are written, so setting a depth leaves the budgets\n" +
			"alone. A bound of zero means no bound: --max-pages 0 removes the page\n" +
			"budget rather than stopping the crawl before it starts.",
		UsageText: "  scour job set uk --depth 12\n" +
			"  scour job set uk --max-pages 5000 --max-time 30m\n" +
			"  scour job set uk --max-pages 0        # no page budget",
		Flags: []ucli.Flag{
			&ucli.IntFlag{Name: "depth", Usage: "how many links deep to follow", Destination: &depth},
			&ucli.IntFlag{Name: "max-pages", Usage: "stop a run after this many pages, 0 for no bound", Destination: &maxPages},
			&ucli.DurationFlag{Name: "max-time", Usage: "stop a run after this long, 0 for no bound", Destination: &maxTime},
		},
		Action: func(c context.Context, cmd *ucli.Command) error {
			args, err := cli.Need(cmd, 1, "one job name")
			if err != nil {
				return err
			}

			// Only what was given is written, so a flag left off is not the
			// same as a flag set to zero: one leaves the bound alone and the
			// other removes it.
			var p store.JobPolicy
			if cmd.IsSet("depth") {
				p.Depth = &depth
			}
			if cmd.IsSet("max-pages") {
				p.MaxPages = &maxPages
			}
			if cmd.IsSet("max-time") {
				p.MaxTime = &maxTime
			}
			if p.Depth == nil && p.MaxPages == nil && p.MaxTime == nil {
				return cli.ErrNothingToSet
			}

			s, err := a.Store()
			if err != nil {
				return err
			}
			job, err := s.Job(c, args[0])
			if err != nil {
				return err
			}
			if err := s.SetJobPolicy(c, job.ID, p); err != nil {
				return err
			}

			after, err := s.Job(c, job.Name)
			if err != nil {
				return err
			}
			a.Printf("%s: depth %d, max pages %d, max time %s\n",
				after.Name, after.Depth, after.MaxPages, time.Duration(after.MaxTime))
			return nil
		},
	}
}
