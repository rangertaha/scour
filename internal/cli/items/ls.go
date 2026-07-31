// SPDX-License-Identifier: GPL-3.0-or-later

package items

import (
	"context"

	ucli "github.com/urfave/cli/v3"

	"github.com/rangertaha/scour/internal/cli"
)

func List(a *cli.App) *ucli.Command {
	return &ucli.Command{
		Name:      "ls",
		Aliases:   []string{"list"},
		ArgsUsage: "[name]",
		Usage:     "List items, or show everything known about one",
		Description: "With no name, a line per item: what it has, how far its crawl has got\n" +
			"and whether it has been trained. With a name, everything known about that\n" +
			"one.\n\n" +
			"Crawls resume from the stored frontier, so this is also where you see what a\n" +
			"restarted crawl will pick up.",
		UsageText: "  scour item ls                 # a line per item\n" +
			"  scour item ls vehicle         # everything known about one\n" +
			"  scour --json item ls vehicle",
		Action: func(c context.Context, cmd *ucli.Command) error {
			args, err := cli.AtMost(cmd, 1, "at most one item name")
			if err != nil {
				return err
			}
			return cli.RunList(c, a, args)
		},
	}
}
