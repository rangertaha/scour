// SPDX-License-Identifier: GPL-3.0-or-later

package node

import (
	ucli "github.com/urfave/cli/v3"

	"github.com/rangertaha/scour/internal/cli"
)

// TopShortcut is `scour node top` at the top level, which is where anybody
// watching a crawl reaches for it.
func TopShortcut(a *cli.App) *ucli.Command {
	c := Top(a)
	c.Category = "SHORTCUT"
	return c
}
