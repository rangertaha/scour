// SPDX-License-Identifier: GPL-3.0-or-later

package job

import (
	"context"
	"fmt"

	ucli "github.com/urfave/cli/v3"

	"github.com/rangertaha/scour/internal/cli"
	"github.com/rangertaha/scour/internal/jobfile"
	"github.com/rangertaha/scour/internal/store"
)

// Show prints everything about one job.
//
// ls enumerates and show explains: the listing answers "what is there", and
// this answers "what will happen when I start this one", which is the question
// worth asking about a job in particular. Both go through showJob, so naming a
// job on the listing and asking for it directly cannot answer differently.
func Show(a *cli.App) *ucli.Command {
	var asTOML bool

	return &ucli.Command{
		Name:      "show",
		ArgsUsage: "<name>",
		Usage:     "Everything about one job",
		UsageText: "  scour job show uk\n" +
			"  scour --json job show uk\n" +
			"  scour job show uk --toml > uk.toml",
		Flags: []ucli.Flag{
			&ucli.BoolFlag{
				Name:        "toml",
				Usage:       "print the job as a config file, in the form job add -f reads",
				Destination: &asTOML,
			},
		},
		Action: func(c context.Context, cmd *ucli.Command) error {
			args, err := cli.Need(cmd, 1, "one job name")
			if err != nil {
				return err
			}
			s, err := a.Store()
			if err != nil {
				return err
			}
			if asTOML {
				return showJobTOML(c, a, s, args[0])
			}
			return showJob(c, a, s, args[0])
		},
	}
}

// showJobTOML prints a stored job in the form `job add -f` reads, which is what
// closes the round trip: a job assembled by flags over weeks can be written to
// a file, kept, and applied somewhere else.
func showJobTOML(c context.Context, a *cli.App, s *store.Store, name string) error {
	job, err := s.Job(c, name)
	if err != nil {
		return err
	}
	item, err := s.ItemByID(c, job.ItemID)
	if err != nil {
		return err
	}
	a.Print(jobfile.Of(job, item.Name).Render())
	return nil
}

// bound renders a zero as what a zero means there. A job showing "0" for a
// budget reads as a crawl that stops before it starts, when it means no bound.
func bound(n int, zero string) string {
	if n == 0 {
		return zero
	}
	return fmt.Sprintf("%d", n)
}
