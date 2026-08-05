// SPDX-License-Identifier: GPL-3.0-or-later

package cli

import (
	"context"
	"strings"

	ucli "github.com/urfave/cli/v3"
)

// Validate checks a document and reports everything wrong with it.
//
// Everything, not the first thing: a person fixing a document one error per run
// gives up, and so does a build script.
func Validate(a *App) *ucli.Command {
	return &ucli.Command{
		Name:      "validate",
		Usage:     "Check a job document, reporting every problem at once",
		ArgsUsage: "<document.hcl>",
		Description: "Parses and validates the document. It does not reach the network, so\n" +
			"it works offline and in CI, and it cannot know whether a plugin\n" +
			"somebody else's node registers exists.\n\n" +
			"Exits 0 if the document would be accepted, 1 if it would be refused,\n" +
			"and 3 if the file could not be read.",
		Action: oneFile(func(_ context.Context, path string) error {
			doc, err := Load(path)
			if err != nil {
				return err
			}
			if err := doc.Validate(); err != nil {
				a.Warnf("%s: refused\n%s\n", path, Problems(err))
				return Invalidf("refused")
			}
			a.Printf("%s: ok, %d job(s): %s\n", path, len(doc.Jobs), strings.Join(doc.Names(), ", "))
			return nil
		}),
	}
}

// oneFile adapts an action that takes exactly one document.
//
// One at a time: a document holds as many jobs as it likes and they are
// accepted or refused together, but two documents would need rules about what
// happens when the first is accepted and the second is not.
// # Why the context is a parameter
//
// Because dropping it here made ctrl-c stop nothing. This helper took a
// callback of one path and threw the context away, so a command that needed one
// had no choice but to make its own, and both of the long-running commands did:
// `scour run` and `scour train` each called context.Background(). The context
// main builds is the one signal.NotifyContext cancels on an interrupt, so
// discarding it meant a crawl could not be interrupted at all. It kept crawling
// until it ran out of pages, and the only way to stop it was to press ctrl-c a
// second time, which kills the process outright and is exactly what the
// resumable frontier exists to avoid.
//
// The context threads through now, so a command cannot fail to have one and
// nothing has a reason to invent one.
func oneFile(fn func(ctx context.Context, path string) error) func(context.Context, *ucli.Command) error {
	return func(ctx context.Context, cmd *ucli.Command) error {
		switch cmd.Args().Len() {
		case 1:
			return fn(ctx, cmd.Args().First())
		case 0:
			return Usagef("no document given")
		default:
			return Usagef("one document at a time, got %d", cmd.Args().Len())
		}
	}
}
