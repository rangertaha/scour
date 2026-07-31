// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"

	"github.com/urfave/cli/v3"
)

// newItemCmd groups everything that acts on the definition of an item.
//
// These were seven top-level verbs, which put `templates` and `tag` at the same
// level as `train` and `server` and said nothing about which of them belong
// together. They are all one subject: what you are looking for, before any
// crawling happens. Grouping them says that, and leaves the top level to the
// five things scour actually does.
func newItemCmd(a *app) *cli.Command {
	return &cli.Command{
		Category: "ITEMS",
		Name:     "item",
		Usage:    "Define items to find",
		Description: "An item is the thing you are hunting for: a name, the other words a page\n" +
			"might use for it, the properties it should have, and where to look.\n\n" +
			"With no subcommand it lists what you have defined, which is the same as\n" +
			"`scour item ls`.",
		UsageText: "  scour item\n" +
			"  scour item add vehicle -d example.com -p make -e Ford\n" +
			"  scour item ls vehicle\n" +
			"  scour item tag vehicle -p make --append manufacturer\n" +
			"  scour item rm vehicle -p make",
		Commands: []*cli.Command{
			newAddCmd(a),
			newRemoveCmd(a),
			newListCmd(a),
			newTagCmd(a),
			newTemplatesCmd(a),
		},
		// Bare `scour item` lists, because the question it most likely means is
		// "what have I defined", and answering with a help page does not.
		Action: func(c context.Context, cmd *cli.Command) error {
			return runList(c, a, cmd.Args().Slice())
		},
	}
}
