// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"

	ucli "github.com/urfave/cli/v3"

	"github.com/rangertaha/scour/internal/cli"
	"github.com/rangertaha/scour/internal/version"
)

// root assembles the command tree.
//
// Flat, not nested. A job is the only thing these commands act on, so naming it
// would add a word to every line and distinguish nothing. Where a second noun
// genuinely arrives it becomes its own plural command.
//
// The tree is built here and nowhere else: no command reaches into another, so
// each can be read on its own.
func root(a *cli.App) *ucli.Command {
	return &ucli.Command{
		Name:  "scour",
		Usage: "A focused web crawler that ranks links by how likely they are to hold what you want",
		Description: "A job document says what to crawl, what to pull out of it, and how each\n" +
			"stage should behave. It carries everything, so a job resubmitted next\n" +
			"month does what it did today.\n\n" +
			"Start with `scour init > job.hcl`.",
		Version:               version.String(),
		Reader:                a.In,
		Writer:                a.Out,
		ErrWriter:             a.Err,
		HideHelpCommand:       true,
		EnableShellCompletion: true,
		Commands: []*ucli.Command{
			// In the order somebody meets them.
			cli.Init(a),
			cli.Validate(a),
			cli.Show(a),
			cli.Spec(a),
			cli.Try(a),
			cli.Crawl(a),
			cli.Train(a),
			cli.Defaults(a),
			cli.Serve(a),
			cli.Secret(a),
		},
		// Reached when nothing matched. An unknown command is a usage error
		// rather than a crash or a silent success, and naming what was typed
		// is most of the help somebody needs.
		Action: func(_ context.Context, cmd *ucli.Command) error {
			if name := cmd.Args().First(); name != "" {
				return cli.Usagef("unknown command %q. Run `scour --help` for what there is", name)
			}
			return ucli.ShowAppHelp(cmd)
		},
	}
}
