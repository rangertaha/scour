// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"strings"

	ucli "github.com/urfave/cli/v3"

	"github.com/rangertaha/scour/internal/cli"
	"github.com/rangertaha/scour/internal/version"
)

// root assembles the command tree.
//
// # Two levels, and what decides which
//
// A command is nested when it acts on a noun the cluster owns, and top level
// when it acts on a file in front of you. `scour job start news` needs the
// cluster to know what `news` is; `scour crawl job.hcl` needs a path. Grouping
// them the other way round is how a tree ends up with `scour job-status` beside
// `scour job-stats` and nowhere to put the third one.
//
// The tree was flat before this, back when a job was the only noun these
// commands acted on. It stopped being: a cluster, its jobs, its topics and its
// secrets are four things somebody manages separately, and three of them had
// nowhere to live.
//
// # Ordered and grouped by what somebody is doing
//
// The categories are the workflow: build a job, then run it on a cluster, then
// look after what that cluster shares. Help is read by somebody who does not
// yet know which command they want, so it is arranged for them rather than
// alphabetically.
//
// The tree is built here and nowhere else: no command reaches into another, so
// each can be read on its own.
func root(a *cli.App) *ucli.Command {
	// Subcommand help groups by category too.
	//
	// The library groups the root's commands and nobody else's: its subcommand
	// template lists them flat. That is the wrong way round for a tree whose
	// second level is where the commands actually are. `scour job` has
	// seventeen of them, and seventeen in one flat list is the thing categories
	// exist to prevent.
	//
	// The library's own template with one line changed, so the rest of the help
	// keeps whatever it looks like.
	ucli.SubcommandHelpTemplate = strings.Replace(
		ucli.SubcommandHelpTemplate,
		`COMMANDS:{{template "visibleCommandTemplate" .}}`,
		`COMMANDS:{{template "visibleCommandCategoryTemplate" .}}`,
		1)

	return &ucli.Command{
		Name:  "scour",
		Usage: "A focused web crawler that ranks links by how likely they are to hold what you want",
		Description: "A job document says what to crawl, what to pull out of it, and how each\n" +
			"stage should behave. It carries everything, so a job resubmitted next\n" +
			"month does what it did today.\n\n" +
			"Writing one:   scour job init > job.hcl\n" +
			"               scour job valid job.hcl\n" +
			"               scour scrape job.hcl        one page, to check extraction\n" +
			"               scour crawl job.hcl         the whole thing, here\n\n" +
			"Running it on a cluster:\n" +
			"               scour server                start one, or --join another\n" +
			"               scour cluster join <url>    remember it\n" +
			"               scour job create job.hcl\n" +
			"               scour job start news\n" +
			"               scour job watch news",
		Version:               version.String(),
		Reader:                a.In,
		Writer:                a.Out,
		ErrWriter:             a.Err,
		HideHelpCommand:       true,
		EnableShellCompletion: true,
		Commands: []*ucli.Command{
			// In the order somebody meets them, and grouped the same way.
			cli.Job(a),
			cli.Scrape(a),
			cli.Crawl(a),
			cli.Defaults(a),
			cli.Cluster(a),
			cli.Server(a),
			cli.Topic(a),
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
