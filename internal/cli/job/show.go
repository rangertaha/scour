// SPDX-License-Identifier: GPL-3.0-or-later

package job

import (
	"context"
	"fmt"

	ucli "github.com/urfave/cli/v3"

	"github.com/rangertaha/scour/internal/cli"
)

// Show prints everything about one job.
//
// ls enumerates and show explains: the listing answers "what is there", and
// this answers "what will happen when I start this one", which is the question
// worth asking about a job in particular. Both go through showJob, so naming a
// job on the listing and asking for it directly cannot answer differently.
func Show(a *cli.App) *ucli.Command {
	return &ucli.Command{
		Name:      "show",
		ArgsUsage: "<name>",
		Usage:     "Everything about one job",
		UsageText: "  scour job show uk\n  scour --json job show uk",
		Action: func(c context.Context, cmd *ucli.Command) error {
			args, err := cli.Need(cmd, 1, "one job name")
			if err != nil {
				return err
			}
			s, err := a.Store()
			if err != nil {
				return err
			}
			return showJob(c, a, s, args[0])
		},
	}
}

// bound renders a zero as what a zero means there. A job showing "0" for a
// budget reads as a crawl that stops before it starts, when it means no bound.
func bound(n int, zero string) string {
	if n == 0 {
		return zero
	}
	return fmt.Sprintf("%d", n)
}
