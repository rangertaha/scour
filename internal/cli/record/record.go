// SPDX-License-Identifier: GPL-3.0-or-later

package record

import (
	ucli "github.com/urfave/cli/v3"

	"github.com/rangertaha/scour/internal/cli"
)

// Record groups reading, marking and exporting what a crawl found.
//
// Records belong to the item rather than to the job that produced them, because
// two jobs hunting one item are filling one table.
func Record(a *cli.App) *ucli.Command {
	return cli.Group(&ucli.Command{
		Category: "MANAGE",
		Name:     "record",
		Usage:    "Read, mark and export what was found",
		Description: "The records are the product. This is where you read them, tell the model\n" +
			"which ones it got wrong, and take the rest out.",
		UsageText: "  scour record ls vehicle\n" +
			"  scour record ls vehicle --follow\n" +
			"  scour record mark vehicle 1042 --verdict invalid\n" +
			"  scour record write vehicle --format csv --to ./out",
		Commands: []*ucli.Command{List(a), Mark(a)},
	})
}
