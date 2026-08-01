// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"fmt"

	ucli "github.com/urfave/cli/v3"

	"github.com/rangertaha/scour/internal/cli"
	"github.com/rangertaha/scour/internal/cli/item"
	"github.com/rangertaha/scour/internal/cli/job"
	"github.com/rangertaha/scour/internal/cli/model"
	"github.com/rangertaha/scour/internal/cli/node"
	"github.com/rangertaha/scour/internal/cli/record"
	"github.com/rangertaha/scour/internal/cli/serve"
	"github.com/rangertaha/scour/internal/config"
	"github.com/rangertaha/scour/internal/fuzzy"
	"github.com/rangertaha/scour/internal/version"
)

func newRootCmd() *ucli.Command {
	cli.InstallHelpOrder()
	a := &cli.App{}

	root := &ucli.Command{
		Name:  "scour",
		Usage: "A focused web crawler that ranks links by how likely they are to hold what you want",
		Description: "scour crawls outward from your seed targets, assigning every discovered URL a\n" +
			"probability that it holds a match, so the crawl budget is spent on the pages\n" +
			"most likely to pay off.",
		Version: version.Version(),
		// A mistyped command should suggest the one that was meant rather than
		// only saying it does not exist.
		Suggest:               true,
		EnableShellCompletion: true,
		Flags: []ucli.Flag{
			&ucli.StringFlag{
				Name:        "config",
				Usage:       "configuration `file` (default: /etc/scour/config.toml, else the user config)",
				Destination: &a.ConfigPath,
			},
			&ucli.BoolFlag{
				Name:        "verbose",
				Aliases:     []string{"v"},
				Usage:       "log at debug level",
				Destination: &a.Verbose,
			},
			&ucli.BoolFlag{
				Name:        "json",
				Usage:       "print machine-readable output",
				Destination: &a.JSON,
			},
			&ucli.IntFlag{
				Name:        "limit",
				Usage:       "cap the number of rows printed (0 for no cap)",
				Destination: &a.Limit,
			},
		},
		Before: func(ctx context.Context, cmd *ucli.Command) (context.Context, error) {
			a.SetWriters(cmd.Root().Writer, cmd.Root().ErrWriter)
			cli.SetupLogging(a.Verbose)

			cfg, err := config.Load(a.ConfigPath)
			if err != nil {
				return ctx, err
			}
			a.Cfg = cfg

			// Help and version need no directories on disk.
			switch cmd.Args().First() {
			case "help", "version", "":
				return ctx, nil
			}
			return ctx, cfg.MkdirAll()
		},
		After: func(_ context.Context, _ *ucli.Command) error {
			return a.Close()
		},
		Action: func(_ context.Context, cmd *ucli.Command) error {
			// urfave falls back to the root action for a word it does not
			// recognise, so without this a typo prints the help and exits 0,
			// which reads as success to anything scripting scour.
			if name := cmd.Args().First(); name != "" {
				// Not cli.SuggestCommand: it always names its nearest match
				// however far away it is, so "bogus" came back as "top", and a
				// suggestion that points somewhere unrelated is worse than none.
				names := make([]string, 0, len(cmd.Commands))
				for _, c := range cmd.Commands {
					names = append(names, c.Names()...)
				}
				if near := fuzzy.Nearest(name, names); near != "" {
					return fmt.Errorf("unknown command %q, did you mean `scour %s`?", name, near)
				}
				return fmt.Errorf("unknown command %q, run `scour --help`", name)
			}
			return ucli.ShowAppHelp(cmd)
		},
		Commands: []*ucli.Command{
			// The five nouns, in the order you meet them.
			item.Item(a),
			job.Job(a),
			record.Record(a),
			model.Model(a),
			node.Node(a),

			// The shortcuts, for what is typed all day. Each is an alias for
			// one canonical command, so there is one place the behaviour lives.
			job.Run(a),
			job.Status(a),
			node.TopShortcut(a),

			// These act on the install rather than on any one noun, so they
			// have no noun to sit under.
			serve.Server(a),
			serve.MCP(a),
			newVersionCmd(a),
		},
	}
	return root
}

func newVersionCmd(a *cli.App) *ucli.Command {
	return &ucli.Command{
		Name:  "version",
		Usage: "Print the version",
		Action: func(_ context.Context, _ *ucli.Command) error {
			a.Println(version.Version())
			return nil
		},
	}
}
