// SPDX-License-Identifier: GPL-3.0-or-later

package node

import (
	ucli "github.com/urfave/cli/v3"

	"github.com/rangertaha/scour/internal/cli"
)

// Node groups the engine: this process, or a cluster of them.
//
// It is a command group without being one of the things scour stores, for the
// opposite reason runs are not: it has verbs and no rows.
//
// `node ls` prints rows without making that untrue. They are the fleet as it is
// this second, held in the broker and expiring on their own, so nothing here is
// a record of what happened: switch the machines off and there is nothing left
// to read.
func Node(a *cli.App) *ucli.Command {
	return cli.Group(&ucli.Command{
		Category: "MANAGE",
		Name:     "node",
		Usage:    "Monitor the engine, and cluster it",
		UsageText: "  scour node top\n" +
			"  scour node join --role crawl\n" +
			"  scour node ls\n" +
			"  scour node leave",
		Commands: []*ucli.Command{Top(a), List(a), Show(a), Join(a), Leave(a)},
	})
}
