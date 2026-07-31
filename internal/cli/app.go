// SPDX-License-Identifier: GPL-3.0-or-later

// Package cli holds what every scour command needs and no command owns: the
// resolved configuration, a lazily opened store, where to write, and the
// helpers for saying a thing the same way twice.
//
// It exists so the command groups can live in their own packages without each
// reaching into the others for a table renderer or an argument check.
package cli

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/rangertaha/scour/internal/cache"
	"github.com/rangertaha/scour/internal/config"
	"github.com/rangertaha/scour/internal/store"
)

// ErrSilent suppresses the top-level error print for failures that have
// already been reported.
var ErrSilent = errors.New("silent")

// App carries what every command needs: the resolved configuration, a lazily
// opened store, and where to write. Commands that only print help never touch
// the database.
//
// Output goes through the App rather than through the command, so the commands
// say nothing about which library parses the arguments.
type App struct {
	Cfg   config.Config
	store *store.Store
	pages cache.Store

	out io.Writer
	err io.Writer

	ConfigPath string
	Verbose    bool
	JSON       bool
	Limit      int
}

// Printf writes to the command's output.
func (a *App) Printf(format string, args ...any) { fmt.Fprintf(a.writer(), format, args...) }

// Println writes a line to the command's output.
func (a *App) Println(args ...any) { fmt.Fprintln(a.writer(), args...) }

// Print writes to the command's output.
func (a *App) Print(args ...any) { fmt.Fprint(a.writer(), args...) }

// Errorf writes to the command's error output.
func (a *App) Errorf(format string, args ...any) { fmt.Fprintf(a.errWriter(), format, args...) }

func (a *App) writer() io.Writer {
	if a.out != nil {
		return a.out
	}
	return os.Stdout
}

func (a *App) errWriter() io.Writer {
	if a.err != nil {
		return a.err
	}
	return os.Stderr
}

// Out is the writer commands hand to anything that renders into it.
func (a *App) Out() io.Writer { return a.writer() }

// Store opens the database on first use and reuses it afterwards.
func (a *App) Store() (*store.Store, error) {
	if a.store != nil {
		return a.store, nil
	}
	s, err := store.Open(a.Cfg.Store.DSN)
	if err != nil {
		return nil, err
	}
	a.store = s
	return s, nil
}

// Close releases the store if one was opened.
func (a *App) Close() error {
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
func (a *App) Pages() (cache.Store, error) {
	if a.pages != nil {
		return a.pages, nil
	}
	url := a.Cfg.Cache.URL
	if url == "" {
		url = a.Cfg.PagesDir()
	}
	p, err := cache.New(a.Cfg.Cache.Driver, cache.Config{
		URL:     url,
		Options: a.Cfg.Cache.Options,
	})
	if err != nil {
		return nil, err
	}
	a.pages = p
	return p, nil
}

var logLevel = new(slog.LevelVar)

// SetupLogging makes a command quiet by default.
//
// A command that prints a table is not a service, and interleaving log lines
// with the table is how "2 fetched, 0 failed" ends up underneath a paragraph of
// key=value pairs nobody asked for. Anything that genuinely needs saying to
// someone running a command is printed, not logged.
//
// Warnings and errors still come through: those are not progress, they are the
// reason the answer might be wrong.
func SetupLogging(Verbose bool) {
	logLevel.Set(slog.LevelWarn)
	if Verbose {
		logLevel.Set(slog.LevelDebug)
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel})))
}

// LogProgress turns on the running commentary, for the commands whose whole job
// is to keep running: the API server, a cluster member, the stdio MCP server.
// Their log is the only thing they produce.
func LogProgress() {
	if logLevel.Level() > slog.LevelInfo {
		logLevel.Set(slog.LevelInfo)
	}
}

// SetWriters points a command's output at somewhere other than the terminal,
// which is what the test harness and the root's Before hook both need.
func (a *App) SetWriters(out, err io.Writer) { a.out, a.err = out, err }

// Err is the writer for anything that is not the answer.
func (a *App) Err() io.Writer { return a.errWriter() }
