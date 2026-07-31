// SPDX-License-Identifier: GPL-3.0-or-later

package search

import (
	"context"

	ucli "github.com/urfave/cli/v3"

	"github.com/rangertaha/scour/internal/cli"
)

// Status is the one-shot form of `scour top`, and the same answer as
// `scour item ls`.
//
// Two names for one listing is not duplication here. Under `item` it is part of
// defining things, and you reach it while writing the definition down. At the
// top level it is the question asked between commands, without having to know
// that the listing lives under a noun. `top` is the same view again, kept open.
func Status(a *cli.App) *ucli.Command {
	return &ucli.Command{
		Category:  "SEARCH",
		Name:      "status",
		ArgsUsage: "[name]",
		Usage:     "Show what every item has and how far its search has got",
		Description: "With no name, a line per item: what it has, how far its search has got and\n" +
			"whether it has been trained. With a name, everything known about that one.\n\n" +
			"The same listing as `scour item ls`, and the same view `scour top` keeps\n" +
			"open. Searches resume from the stored frontier, so this is also where you\n" +
			"see what a restarted one will pick up.",
		UsageText: "  scour status                  # a line per item\n" +
			"  scour status vehicle          # everything known about one\n" +
			"  scour --json status vehicle\n\n" +
			"Watch it live instead:\n" +
			"  scour top",
		Action: func(c context.Context, cmd *ucli.Command) error {
			args, err := cli.AtMost(cmd, 1, "at most one item name")
			if err != nil {
				return err
			}
			return cli.RunList(c, a, args)
		},
	}
}
