// SPDX-License-Identifier: GPL-3.0-or-later

package item

import (
	"context"

	ucli "github.com/urfave/cli/v3"

	"github.com/rangertaha/scour/internal/cli"
)

// Show prints everything known about one item.
//
// It is `item ls <name>` under the name the rule gives it: ls enumerates and
// show explains, which is the same split every other noun draws. Both go
// through one function, so naming an item on the listing and asking for it
// directly cannot answer differently.
func Show(a *cli.App) *ucli.Command {
	return &ucli.Command{
		Name:      "show",
		ArgsUsage: "<name>",
		Usage:     "Everything known about one item",
		UsageText: "  scour item show vehicle\n  scour --json item show vehicle",
		Action: func(c context.Context, cmd *ucli.Command) error {
			args, err := cli.Need(cmd, 1, "one item name")
			if err != nil {
				return err
			}
			return cli.RunList(c, a, args)
		},
	}
}
