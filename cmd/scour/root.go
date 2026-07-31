// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/rangertaha/scour/internal/cache"
	"github.com/rangertaha/scour/internal/config"
	"github.com/rangertaha/scour/internal/fuzzy"
	"github.com/rangertaha/scour/internal/store"
	"github.com/rangertaha/scour/internal/version"
)

// errSilent suppresses the top-level error print for failures that have
// already been reported.
var errSilent = errors.New("silent")

// app carries what every command needs: the resolved configuration, a lazily
// opened store, and where to write. Commands that only print help never touch
// the database.
//
// Output goes through the app rather than through the command, so the commands
// say nothing about which library parses the arguments.
type app struct {
	cfg   config.Config
	store *store.Store
	pages cache.Store

	out io.Writer
	err io.Writer

	configPath string
	verbose    bool
	jsonOut    bool
	limit      int
}

// Printf writes to the command's output.
func (a *app) Printf(format string, args ...any) { fmt.Fprintf(a.writer(), format, args...) }

// Println writes a line to the command's output.
func (a *app) Println(args ...any) { fmt.Fprintln(a.writer(), args...) }

// Print writes to the command's output.
func (a *app) Print(args ...any) { fmt.Fprint(a.writer(), args...) }

// Errorf writes to the command's error output.
func (a *app) Errorf(format string, args ...any) { fmt.Fprintf(a.errWriter(), format, args...) }

func (a *app) writer() io.Writer {
	if a.out != nil {
		return a.out
	}
	return os.Stdout
}

func (a *app) errWriter() io.Writer {
	if a.err != nil {
		return a.err
	}
	return os.Stderr
}

// Out is the writer commands hand to anything that renders into it.
func (a *app) Out() io.Writer { return a.writer() }

// Store opens the database on first use and reuses it afterwards.
func (a *app) Store() (*store.Store, error) {
	if a.store != nil {
		return a.store, nil
	}
	s, err := store.Open(a.cfg.Store.DSN)
	if err != nil {
		return nil, err
	}
	a.store = s
	return s, nil
}

// Close releases the store if one was opened.
func (a *app) Close() error {
	if a.store == nil {
		return nil
	}
	err := a.store.Close()
	a.store = nil
	return err
}

// Pages returns the store fetched bodies are kept in, built from the
// configuration and reused for the life of the command.
//
// The default is a directory on this machine, which is right until crawlers run
// on more than one: each would write to its own disk and the trainer would read
// an empty cache with a database full of keys pointing at nothing.
func (a *app) Pages() (cache.Store, error) {
	if a.pages != nil {
		return a.pages, nil
	}
	url := a.cfg.Cache.URL
	if url == "" {
		url = a.cfg.PagesDir()
	}
	p, err := cache.New(a.cfg.Cache.Driver, cache.Config{
		URL:     url,
		Options: a.cfg.Cache.Options,
	})
	if err != nil {
		return nil, err
	}
	a.pages = p
	return p, nil
}

func newRootCmd() *cli.Command {
	installHelpOrder()
	a := &app{}

	root := &cli.Command{
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
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:        "config",
				Usage:       "configuration `file` (default: /etc/scour/config.toml, else the user config)",
				Destination: &a.configPath,
			},
			&cli.BoolFlag{
				Name:        "verbose",
				Aliases:     []string{"v"},
				Usage:       "log at debug level",
				Destination: &a.verbose,
			},
			&cli.BoolFlag{
				Name:        "json",
				Usage:       "print machine-readable output",
				Destination: &a.jsonOut,
			},
			&cli.IntFlag{
				Name:        "limit",
				Usage:       "cap the number of rows printed (0 for no cap)",
				Destination: &a.limit,
			},
		},
		Before: func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
			a.out, a.err = cmd.Root().Writer, cmd.Root().ErrWriter
			setupLogging(a.verbose)

			cfg, err := config.Load(a.configPath)
			if err != nil {
				return ctx, err
			}
			a.cfg = cfg

			// Help and version need no directories on disk.
			switch cmd.Args().First() {
			case "help", "version", "":
				return ctx, nil
			}
			return ctx, cfg.MkdirAll()
		},
		After: func(_ context.Context, _ *cli.Command) error {
			return a.Close()
		},
		Action: func(_ context.Context, cmd *cli.Command) error {
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
			return cli.ShowAppHelp(cmd)
		},
		Commands: []*cli.Command{
			newImportCmd(a),
			newExportCmd(a),
			newItemCmd(a),
			newStreamCmd(a),
			newRulesCmd(a),
			newTrainCmd(a),
			newInvalidCmd(a),
			newStatusCmd(a),
			newTopCmd(a),
			newStartCmd(a),
			newStopCmd(a),
			newPauseCmd(a),
			newMCPCmd(a),
			newServerCmd(a),
			newJoinCmd(a),
			newVersionCmd(a),
		},
	}
	return root
}

// logLevel is raised and lowered after the logger is built, because what
// counts as worth saying depends on which command was asked for and that is
// not known when the root's Before runs.
var logLevel = new(slog.LevelVar)

// setupLogging makes a command quiet by default.
//
// A command that prints a table is not a service, and interleaving log lines
// with the table is how "2 fetched, 0 failed" ends up underneath a paragraph of
// key=value pairs nobody asked for. Anything that genuinely needs saying to
// someone running a command is printed, not logged.
//
// Warnings and errors still come through: those are not progress, they are the
// reason the answer might be wrong.
func setupLogging(verbose bool) {
	logLevel.Set(slog.LevelWarn)
	if verbose {
		logLevel.Set(slog.LevelDebug)
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel})))
}

// logProgress turns on the running commentary, for the commands whose whole job
// is to keep running: the API server, a cluster member, the stdio MCP server.
// Their log is the only thing they produce.
func logProgress() {
	if logLevel.Level() > slog.LevelInfo {
		logLevel.Set(slog.LevelInfo)
	}
}

func newVersionCmd(a *app) *cli.Command {
	return &cli.Command{
		Name:  "version",
		Usage: "Print the version",
		Action: func(_ context.Context, _ *cli.Command) error {
			a.Println(version.Version())
			return nil
		},
	}
}

// need checks the positional arguments, so every command reports a miscount the
// same way and names what it wanted.
func need(cmd *cli.Command, n int, what string) ([]string, error) {
	got := cmd.Args().Slice()
	if len(got) != n {
		return nil, wanted(cmd, got, what)
	}
	return got, nil
}

// atLeast checks for a minimum number of positional arguments.
func atLeast(cmd *cli.Command, n int, what string) ([]string, error) {
	got := cmd.Args().Slice()
	if len(got) < n {
		return nil, wanted(cmd, got, what)
	}
	return got, nil
}

// wanted reports a wrong number of arguments, and shows the command's own help
// when there were none at all.
//
// Those are different mistakes. Somebody who typed the wrong number of names
// knows what the command is for and wants the count; somebody who typed the
// command bare is asking what it does, and answering that with one line of
// error makes them run it again with --help to get the answer they were
// already owed.
func wanted(cmd *cli.Command, got []string, what string) error {
	if len(got) > 0 {
		return fmt.Errorf("%s takes %s, got %d", cmd.Name, what, len(got))
	}
	if err := cli.ShowSubcommandHelp(cmd); err != nil {
		return err
	}
	// The help has just been printed; repeating the complaint under it would
	// be the third time the same thing is said on one screen.
	fmt.Fprintf(cmd.Root().ErrWriter, "\n%s takes %s\n", cmd.Name, what)
	return errSilent
}

// atMost checks for a maximum number of positional arguments.
func atMost(cmd *cli.Command, n int, what string) ([]string, error) {
	got := cmd.Args().Slice()
	if len(got) > n {
		return nil, fmt.Errorf("%s takes %s, got %d", cmd.Name, what, len(got))
	}
	return got, nil
}
