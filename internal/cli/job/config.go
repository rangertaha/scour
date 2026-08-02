// SPDX-License-Identifier: GPL-3.0-or-later

package job

import (
	"context"
	"fmt"

	ucli "github.com/urfave/cli/v3"

	"github.com/rangertaha/scour/internal/cli"
	"github.com/rangertaha/scour/internal/jobfile"
)

// Config prints a commented sample job config.
//
// It writes to stdout rather than to a file so it composes: redirect it to
// start a job under version control, or read it to see what a job can carry
// without having one yet.
func Config(a *cli.App) *ucli.Command {
	return &ucli.Command{
		Name:  "config",
		Usage: "Print a commented sample config",
		Description: "A job built from flags and a job built from a file are the same job.\n" +
			"This prints a starting point; `scour job show <name> --toml` prints an\n" +
			"existing one in the same form, so a job assembled by flags can be written\n" +
			"out, kept, and applied somewhere else.",
		UsageText: "  scour job config\n" +
			"  scour job config > uk.toml\n" +
			"  scour job validate -f uk.toml\n" +
			"  scour job add -f uk.toml",
		Action: func(_ context.Context, _ *ucli.Command) error {
			a.Print(jobfile.Sample().Render())
			return nil
		},
	}
}

// Validate checks a job config without applying it.
//
// It reports everything wrong rather than the first thing, because a checker
// that stops at one fault turns fixing a file into as many runs as it has
// mistakes. It reads the file and nothing else: no store is opened, so it is
// safe to run against a config for a machine this is not.
func Validate(a *cli.App) *ucli.Command {
	var path string

	return &ucli.Command{
		Name:  "validate",
		Usage: "Check a config without applying it",
		Description: "Parses the file, rejects keys nothing reads, and checks what it can check\n" +
			"without a database: that the bounds are sane, the content types are known,\n" +
			"the domains and URLs parse, and that there is somewhere to look at all.\n\n" +
			"Whether the item exists is not checked here, because that is a question\n" +
			"about this machine and a config is often written for another.",
		UsageText: "  scour job validate -f uk.toml",
		Flags: []ucli.Flag{
			&ucli.StringFlag{
				Name:        "file",
				Aliases:     []string{"f"},
				Usage:       "the config `file` to check",
				Destination: &path,
				Required:    true,
			},
		},
		Action: func(_ context.Context, _ *ucli.Command) error {
			f, err := jobfile.Parse(path)
			if err != nil {
				return err
			}
			problems := f.Validate()
			if len(problems) == 0 {
				a.Printf("%s: ok, job %q for item %q, %d targets\n",
					path, f.Name, f.Item, len(f.Domains)+len(f.URLs))
				return nil
			}
			for _, p := range problems {
				a.Errorf("  %s\n", p)
			}
			return fmt.Errorf("%s: %d %s", path, len(problems), jobfile.Plural("problem", len(problems)))
		},
	}
}
