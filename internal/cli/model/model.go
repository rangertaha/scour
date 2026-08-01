// SPDX-License-Identifier: GPL-3.0-or-later

package model

import (
	ucli "github.com/urfave/cli/v3"

	"github.com/rangertaha/scour/internal/cli"
)

// Model groups training and inspecting what was learned.
//
// A model belongs to an item, not to a job: it is trained from every page every
// job of that item has cached, because the rules describe the item.
func Model(a *cli.App) *ucli.Command {
	return cli.Group(&ucli.Command{
		Category: "MANAGE",
		Name:     "model",
		Usage:    "Train and inspect what was learned",
		UsageText: "  scour model train vehicle\n" +
			"  scour model rules vehicle",
		Commands: []*ucli.Command{Train(a), Rules(a)},
	})
}
