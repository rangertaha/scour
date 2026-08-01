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
func Node(a *cli.App) *ucli.Command {
	return cli.Group(&ucli.Command{
		Category:  "MANAGE",
		Name:      "node",
		Usage:     "Monitor the engine, and cluster it",
		UsageText: "  scour node top\n  scour node join --role crawl",
		Commands:  []*ucli.Command{Top(a), Join(a)},
	})
}
