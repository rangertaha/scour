// SPDX-License-Identifier: GPL-3.0-or-later

package record

import (
	ucli "github.com/urfave/cli/v3"

	"github.com/rangertaha/scour/internal/cli"
)

// SearchShortcut is `scour record search` at the top level.
//
// Searching what a crawl found is the thing you do all day, and it is the
// reason the word was kept free: `record ls` answers to the listing, not to
// "search", so the name was available for the query rather than being a second
// spelling of a listing.
//
// It is the canonical command under another name rather than a second
// implementation, so the two spellings cannot drift.
func SearchShortcut(a *cli.App) *ucli.Command {
	c := Search(a)
	c.Name = "search"
	c.Aliases = nil
	c.Category = "SHORTCUT"
	return c
}
