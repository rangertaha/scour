// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/urfave/cli/v3"

	"github.com/rangertaha/scour/internal/server"
)

func newServerCmd(a *app) *cli.Command {
	var listen string

	cmd := &cli.Command{
		Category: "SERVER",
		Name:     "server",
		Usage:    "Run as a service, serving the HTTP API and MCP",
		Description: "Serves the same scour the command line drives: one database, one set of\n" +
			"models, one cache. Reads answer immediately; crawling and training return a\n" +
			"job id to poll, because they run for minutes.\n\n" +
			"The default listen address is loopback and auth is off. To reach it from\n" +
			"another machine, bind an external address and set token_file, or leave it on\n" +
			"loopback behind a reverse proxy.",
		UsageText: "  scour server\n" +
			"  scour server --listen 0.0.0.0:8080",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:        "listen",
				Usage:       "address to listen on (overrides config)",
				Destination: &listen,
			},
		},
		Action: func(c context.Context, cmd *cli.Command) error {
			return runServer(c, a, listen)
		},
	}

	return cmd
}

func runServer(c context.Context, a *app, listen string) error {
	s, err := a.Store()
	if err != nil {
		return err
	}

	cfg := a.cfg
	if listen != "" {
		cfg.Server.Listen = listen
	}

	pages, err := a.Pages()
	if err != nil {
		return err
	}

	srv, err := server.New(cfg, s, pages)
	if err != nil {
		return err
	}

	httpSrv := srv.HTTPServer(srv.Handler())

	// Signals are handled here rather than left to the default, because the
	// default kills the process mid-crawl and loses whatever that crawl had
	// not yet written.
	ctx, stop := signal.NotifyContext(c, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	slog.Info("server starting",
		"listen", cfg.Server.Listen,
		"mcp", cfg.Server.MCP,
		"metrics", cfg.Server.Metrics != "",
		"auth", cfg.Server.TokenFile != "")

	errs := make(chan error, 1)
	go func() {
		a.Printf("scour listening on %s\n", cfg.Server.Listen)
		if cfg.Server.MCP {
			a.Printf("mcp at http://%s/mcp\n", cfg.Server.Listen)
		}
		if cfg.Server.Metrics != "" {
			a.Printf("metrics at http://%s%s\n", cfg.Server.Listen, cfg.Server.Metrics)
		}
		if cfg.Server.TokenFile == "" {
			a.Println("auth is off: set token_file to require a bearer token")
		}

		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs <- err
		}
	}()

	select {
	case err := <-errs:
		return err
	case <-ctx.Done():
		a.Println("\nshutting down")
		slog.Info("server stopping")
	}

	// Stop accepting first, then let running jobs finish. A crawl abandoned
	// halfway leaves a frontier that says pages were queued and never fetched.
	shutdown, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()

	if err := httpSrv.Shutdown(shutdown); err != nil {
		slog.Warn("http shutdown", "err", err)
	}

	done := make(chan struct{})
	go func() {
		srv.Jobs().Wait()
		close(done)
	}()

	select {
	case <-done:
		slog.Info("server stopped")
	case <-shutdown.Done():
		// A crawl killed here leaves a frontier claiming pages were queued and
		// never fetched, so this is worth a level someone will actually see.
		slog.Warn("server stopped with jobs still running", "grace", shutdownGrace)
		a.Println("jobs still running after the grace period, exiting anyway")
	}
	return nil
}

// shutdownGrace is how long a stop waits for in-flight work.
const shutdownGrace = 30 * time.Second

// closed reports whether an error is just the session ending.
//
// The string check is deliberate and unfortunate. The MCP SDK signals a closed
// connection with an error from its internal jsonrpc2 package, and formats the
// underlying cause with %v rather than %w, so neither the sentinel nor the
// io.EOF beneath it can be reached with errors.Is. Matching the message is the
// only way to tell a client hanging up from a real failure, and getting that
// wrong means every clean exit is reported as a crash.
func closed(err error) bool {
	return errors.Is(err, io.EOF) ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, mcp.ErrConnectionClosed) ||
		strings.Contains(err.Error(), "server is closing")
}

func newMCPCmd(a *app) *cli.Command {
	return &cli.Command{
		Category: "SERVER",
		Name:     "mcp",
		Usage:    "Run as an MCP server over stdio",
		Description: "Speaks the Model Context Protocol on stdin and stdout, which is what a local\n" +
			"agent launches directly. A running `scour server` also serves MCP over HTTP\n" +
			"at /mcp for agents that attach instead of spawning.\n\n" +
			"Both views share one database, so an item defined over MCP is the item\n" +
			"the CLI sees.",
		Action: func(c context.Context, cmd *cli.Command) error {
			s, err := a.Store()
			if err != nil {
				return err
			}

			pages, err := a.Pages()
			if err != nil {
				return err
			}

			srv, err := server.New(a.cfg, s, pages)
			if err != nil {
				return err
			}

			// Nothing may be written to stdout but protocol: a stray line makes
			// the transport unparseable to the agent on the other end.
			ctx, stop := signal.NotifyContext(c, syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			// An agent that closes stdin has ended the session, which is the
			// ordinary way one finishes. Reporting that as a failure would make
			// every clean exit look like a crash to whatever supervises it.
			if err := srv.MCP().Run(ctx, &mcp.StdioTransport{}); err != nil && !closed(err) {
				return err
			}
			return nil
		},
	}
}
