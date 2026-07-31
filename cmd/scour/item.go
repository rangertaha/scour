// SPDX-License-Identifier: GPL-3.0-or-later

package main

import "github.com/urfave/cli/v3"

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
			"might use for it, the properties it should have, and where to look.",
		UsageText: "  scour item ls                 # what is defined\n" +
			"  scour item add vehicle -d example.com\n" +
			"  scour item add vehicle -p make -e Ford\n" +
			"  scour item tag vehicle -p make --append manufacturer\n" +
			"  scour item rm vehicle -p make\n\n" +
			"Start from a schema instead of naming every property:\n" +
			"  scour item templates\n" +
			"  scour item add news --template article",
		Commands: []*cli.Command{
			newAddCmd(a),
			newRemoveCmd(a),
			newListCmd(a),
			newTagCmd(a),
			newTemplatesCmd(a),
		},
	}
}
