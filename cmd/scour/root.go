// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"log/slog"
	"os"

	"github.com/spf13/cobra"

	"github.com/rangertaha/scour/internal/cache"
	"github.com/rangertaha/scour/internal/config"
	"github.com/rangertaha/scour/internal/store"
	"github.com/rangertaha/scour/internal/version"
)

// errSilent suppresses the top-level error print for failures that have
// already been reported.
var errSilent = errors.New("silent")

// app carries what every command needs: the resolved configuration and a lazily
// opened store. Commands that only print help never touch the database.
type app struct {
	cfg   config.Config
	store *store.Store
	pages cache.Store

	configPath string
	verbose    bool
	jsonOut    bool
	limit      int
}

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

func newRootCmd() *cobra.Command {
	a := &app{}

	cmd := &cobra.Command{
		Use:   "scour",
		Short: "A focused web crawler that ranks links by how likely they are to hold what you want",
		Long: "scour crawls outward from your seed targets, assigning every discovered URL a\n" +
			"probability that it holds a match, so the crawl budget is spent on the pages\n" +
			"most likely to pay off.",
		Version:       version.Version(),
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			setupLogging(a.verbose)

			cfg, err := config.Load(a.configPath)
			if err != nil {
				return err
			}
			a.cfg = cfg

			// Help and version need no directories on disk.
			if cmd.Name() == "help" || cmd.Name() == "version" {
				return nil
			}
			return cfg.MkdirAll()
		},
		PersistentPostRunE: func(_ *cobra.Command, _ []string) error {
			return a.Close()
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}

	f := cmd.PersistentFlags()
	f.StringVar(&a.configPath, "config", "", "configuration file (default: /etc/scour/config.toml, else the user config)")
	f.BoolVarP(&a.verbose, "verbose", "v", false, "log at debug level")
	f.BoolVar(&a.jsonOut, "json", false, "print machine-readable output")
	f.IntVar(&a.limit, "limit", 0, "cap the number of rows printed (0 for no cap)")

	cmd.AddCommand(
		newAddCmd(a),
		newCrawlCmd(a),
		newExportCmd(a),
		newImportCmd(a),
		newInvalidCmd(a),
		newListCmd(a),
		newMCPCmd(a),
		newRemoveCmd(a),
		newRunCmd(a),
		newRulesCmd(a),
		newSearchCmd(a),
		newServerCmd(a),
		newStatusCmd(a),
		newTemplatesCmd(a),
		newTrainCmd(a),
		newUnlabelCmd(a),
		newValidCmd(a),
		newVersionCmd(),
	)
	return cmd
}

func setupLogging(verbose bool) {
	level := slog.LevelInfo
	if verbose {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.Println(version.Version())
			return nil
		},
	}
}

// ctx returns the command's context, which carries cancellation from SIGINT.
func ctx(cmd *cobra.Command) context.Context {
	if c := cmd.Context(); c != nil {
		return c
	}
	return context.Background()
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
