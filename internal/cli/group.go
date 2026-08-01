// SPDX-License-Identifier: GPL-3.0-or-later

package cli

import (
	"context"

	ucli "github.com/urfave/cli/v3"

	"github.com/rangertaha/scour/internal/fuzzy"
)

// Group builds a command that holds other commands and nothing else.
//
// It exists for what urfave does with a word it does not recognise: the group's
// own action runs, and with no action set that prints "No help topic for 'add'"
// and exits 3. A noun answering a mistyped verb with the vocabulary of the help
// system, rather than with its own verbs, is the surface admitting it is built
// out of a library.
//
// So a group names what it has and suggests the nearest, exactly as the root
// does for a mistyped noun. Called bare it prints its own help, because a noun
// with no verb is a question about what the verbs are.
func Group(c *ucli.Command) *ucli.Command {
	c.Action = func(_ context.Context, cmd *ucli.Command) error {
		name := cmd.Args().First()
		if name == "" {
			return ucli.ShowSubcommandHelp(cmd)
		}

		// Not ucli.SuggestCommand: it always names its nearest match however
		// far away it is, so a suggestion can point somewhere unrelated, which
		// is worse than offering none.
		verbs := make([]string, 0, len(cmd.Commands))
		for _, sub := range cmd.Commands {
			verbs = append(verbs, sub.Names()...)
		}
		if near := fuzzy.Nearest(name, verbs); near != "" {
			return Usagef("%s has no %q, did you mean `scour %s %s`?",
				cmd.Name, name, cmd.Name, near)
		}
		return Usagef("%s has no %q, run `scour %s --help`", cmd.Name, name, cmd.Name)
	}
	return c
}
