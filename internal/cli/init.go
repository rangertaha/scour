// SPDX-License-Identifier: GPL-3.0-or-later

package cli

import (
	"context"
	"os"

	ucli "github.com/urfave/cli/v3"

	"github.com/rangertaha/scour/internal/templates"
)

// Init prints a starter job document.
//
// To stdout by default, so it composes:
//
//	scour job init news --template news > news.hcl
//
// A path can be given instead, and then it refuses to overwrite: somebody
// running this twice in a directory they have been working in should not lose
// what they wrote the first time.
func Init(a *App) *ucli.Command {
	var out, template string
	var force, list bool

	return &ucli.Command{
		Name:      "init",
		Category:  "Authoring a document",
		Usage:     "Print a starter job document",
		ArgsUsage: "[name]",
		Description: "Writes a job that validates as it stands, so it can be run and then\n" +
			"grown. Everything left out of it has a default, which `scour defaults`\n" +
			"will list.\n\n" +
			"The templates differ in what they extract, not in how they crawl: all\n" +
			"of them are polite, budgeted and cached. None contains a locator,\n" +
			"because the ones that work depend on the site. Crawl a few hundred\n" +
			"pages and `scour job train` will propose them.",
		Flags: []ucli.Flag{
			&ucli.StringFlag{
				Name:        "template",
				Aliases:     []string{"t"},
				Usage:       "which starting point: " + join(templates.Names()),
				Value:       templates.Default,
				Destination: &template,
			},
			&ucli.BoolFlag{
				Name:        "list",
				Usage:       "list the templates and what they are for",
				Destination: &list,
			},
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
			if list {
				for _, t := range templates.All() {
					a.Printf("%-10s %s\n", t.Name, t.Summary)
				}
				return nil
			}

			if cmd.Args().Len() > 1 {
				return Usagef("one name at a time, got %d", cmd.Args().Len())
			}

			doc, err := templates.Render(template, cmd.Args().First())
			if err != nil {
				return Usagef("%v", err)
			}

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
			a.Warnf("wrote %s from the %s template\n", out, template)
			return nil
		},
	}
}

func join(names []string) string {
	out := ""
	for i, n := range names {
		if i > 0 {
			out += ", "
		}
		out += n
	}
	return out
}
