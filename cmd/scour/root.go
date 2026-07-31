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
			return cli.ShowAppHelp(cmd)
		},
		Commands: []*cli.Command{
			newAddCmd(a),
			newCrawlCmd(a),
			newExportCmd(a),
			newImportCmd(a),
			newInvalidCmd(a),
			newListCmd(a),
			newTopCmd(a),
			newStartCmd(a),
			newStopCmd(a),
			newMCPCmd(a),
			newRemoveCmd(a),
			newRunCmd(a),
			newRulesCmd(a),
			newSearchCmd(a),
			newServerCmd(a),
			newTemplatesCmd(a),
			newTrainCmd(a),
			newUnlabelCmd(a),
			newValidCmd(a),
			newVersionCmd(a),
		},
	}
	return root
}

func setupLogging(verbose bool) {
	level := slog.LevelInfo
	if verbose {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))
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
		return nil, fmt.Errorf("%s takes %s, got %d", cmd.Name, what, len(got))
	}
	return got, nil
}

// atLeast checks for a minimum number of positional arguments.
func atLeast(cmd *cli.Command, n int, what string) ([]string, error) {
	got := cmd.Args().Slice()
	if len(got) < n {
		return nil, fmt.Errorf("%s takes %s, got %d", cmd.Name, what, len(got))
	}
	return got, nil
}

// atMost checks for a maximum number of positional arguments.
func atMost(cmd *cli.Command, n int, what string) ([]string, error) {
	got := cmd.Args().Slice()
	if len(got) > n {
		return nil, fmt.Errorf("%s takes %s, got %d", cmd.Name, what, len(got))
	}
	return got, nil
}
