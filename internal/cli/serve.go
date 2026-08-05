// SPDX-License-Identifier: GPL-3.0-or-later

package cli

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	ucli "github.com/urfave/cli/v3"

	"github.com/rangertaha/scour/internal/bus"
	"github.com/rangertaha/scour/internal/cache"
	"github.com/rangertaha/scour/internal/node"

	_ "github.com/rangertaha/scour/internal/cache/local"
	_ "github.com/rangertaha/scour/internal/downloader/httpcache"
	_ "github.com/rangertaha/scour/internal/spider/httperror"
)

// Serve runs a node.
//
// # What a node is
//
// A member of a cluster that serves stages for whatever jobs appear. It joins,
// watches, and offers what it was told to offer. Nothing elects anything and
// nothing assigns anything: work is distributed by queue group, and adding a
// machine is a matter of starting one.
//
// # The first node is also the server
//
// With no `--join`, this starts a NATS in the process, so a single node needs
// nothing installed. The address it prints is what the next node joins.
func Serve(a *App) *ucli.Command {
	var (
		join   string
		name   string
		dir    string
		stages string
		quiet  bool
	)

	return &ucli.Command{
		Name:  "serve",
		Usage: "Run a node: serve stages for whatever jobs the cluster has",
		Description: "Joins a cluster, watches the jobs, and serves the stages this node\n" +
			"offers for every job that appears.\n\n" +
			"With no --join it starts a broker in this process and prints the\n" +
			"address the next node should join. Submitted jobs go in the cluster's\n" +
			"own store, so a node needs an address and nothing else.",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "join", Usage: "a node to join, as nats://host:port", Destination: &join},
			&ucli.StringFlag{Name: "name", Usage: "what to call this node", Destination: &name},
			&ucli.StringFlag{Name: "dir", Usage: "where to keep the cache and the cluster's state", Destination: &dir},
			&ucli.StringFlag{Name: "stages", Usage: "which stages to serve: download, read, or both", Destination: &stages},
			&ucli.BoolFlag{Name: "quiet", Usage: "say nothing but failures", Destination: &quiet},
		},
		Action: func(ctx context.Context, cmd *ucli.Command) error {
			if cmd.Args().Len() > 0 {
				return Usagef("serve takes no arguments, got %q", cmd.Args().First())
			}
			return serveNode(ctx, a, join, name, dir, stages, quiet)
		},
	}
}

func serveNode(ctx context.Context, a *App, join, name, dir, stages string, quiet bool) error {
	if name == "" {
		host, err := os.Hostname()
		if err != nil || host == "" {
			host = "node"
		}
		name = strings.ReplaceAll(host, ".", "-")
	}
	if dir == "" {
		dir = ".scour"
	}

	level := slog.LevelInfo
	if quiet {
		level = slog.LevelWarn
	}
	log := slog.New(slog.NewTextHandler(a.Err, &slog.HandlerOptions{Level: level}))

	conn, err := bus.Connect(bus.Options{
		URL:      join,
		Name:     name,
		StoreDir: filepath.Join(dir, "bus"),
	})
	if err != nil {
		return Failedf("%v", err)
	}
	defer conn.Close()

	// The cache is how a body gets from the node that fetched it to the node
	// that reads it, because a body never crosses the bus.
	bodies, err := cache.New(ctx, cache.Config{Dir: filepath.Join(dir, "cache")})
	if err != nil {
		return Failedf("%v", err)
	}
	defer bodies.Close()

	joined, err := node.Join(ctx, conn, node.Options{
		Name:   name,
		Serve:  serving(stages),
		Bodies: bodies,
		Log:    log,
	})
	if err != nil {
		return Failedf("%v", err)
	}
	defer joined.Close()

	if conn.Embedded() {
		a.Printf("%s is serving, and is the broker\n", name)
		a.Printf("join it with: scour serve --join %s\n", conn.Address())
	} else {
		a.Printf("%s joined %s\n", name, conn.Address())
	}

	// Ctrl-C leaves properly: the node drains what it was serving, so a
	// request it had already taken is answered rather than dropped.
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := joined.Watch(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return Failedf("%v", err)
	}
	a.Printf("%s has left\n", name)
	return nil
}

func serving(stages string) []string {
	var out []string
	for _, name := range strings.Split(stages, ",") {
		if name = strings.TrimSpace(name); name != "" {
			out = append(out, name)
		}
	}
	return out
}
