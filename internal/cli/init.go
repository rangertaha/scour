// SPDX-License-Identifier: GPL-3.0-or-later

package cli

import (
	"context"
	"os"

	ucli "github.com/urfave/cli/v3"

	"github.com/rangertaha/scour/internal/engine"
)

// Init prints a starter job document.
//
// To stdout by default, so it composes:
//
//	scour init news > news.hcl
//
// A path can be given instead, and then it refuses to overwrite: somebody
// running this twice in a directory they have been working in should not lose
// what they wrote the first time.
func Init(a *App) *ucli.Command {
	var out string
	var force bool

	return &ucli.Command{
		Name:      "init",
		Usage:     "Print a starter job document",
		ArgsUsage: "[name]",
		Description: "Writes a small, commented job that validates as it stands, so it can\n" +
			"be run and then grown. Everything left out of it has a default,\n" +
			"which `scour defaults` will list.",
		Flags: []ucli.Flag{
			&ucli.StringFlag{
				Name:        "out",
				Aliases:     []string{"o"},
				Usage:       "write to a file instead of stdout",
				Destination: &out,
			},
			&ucli.BoolFlag{
				Name:        "force",
				Usage:       "overwrite the file if it is already there",
				Destination: &force,
			},
		},
		Action: func(_ context.Context, cmd *ucli.Command) error {
			if cmd.Args().Len() > 1 {
				return Usagef("one name at a time, got %d", cmd.Args().Len())
			}

			doc := engine.Example(cmd.Args().First())

			if out == "" {
				a.Printf("%s", doc)
				return nil
			}

			if _, err := os.Stat(out); err == nil && !force {
				return Failedf("%s already exists. Pass --force to overwrite it", out)
			}
			if err := os.WriteFile(out, doc, 0o644); err != nil {
				return Failedf("%v", err)
			}
			a.Warnf("wrote %s\n", out)
			return nil
		},
	}
}
