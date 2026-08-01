// SPDX-License-Identifier: GPL-3.0-or-later

package job

import (
	ucli "github.com/urfave/cli/v3"

	"github.com/rangertaha/scour/internal/cli"
)

// The shortcuts are the commands typed all day, lifted to the top level.
//
// Each is the canonical command under another name rather than a second
// implementation, so there is one place the behaviour lives and no way for the
// two spellings to drift. The rule is `scour <noun> <verb>`; these are the
// exceptions you are allowed not to remember.

// Run is `scour job start` at the top level, and creates the job if the item
// has none, which is what makes a first crawl one command rather than two.
func Run(a *cli.App) *ucli.Command {
	c := Start(a)
	c.Name = "run"
	c.Aliases = []string{"crawl", "start"}
	c.Category = "SHORTCUT"
	c.Usage = "Start a job, creating it if the item has no targets yet"
	return c
}

// Status is `scour job ls` at the top level.
func Status(a *cli.App) *ucli.Command {
	c := List(a)
	c.Name = "status"
	c.Aliases = nil
	c.Category = "SHORTCUT"
	return c
}
