// SPDX-License-Identifier: GPL-3.0-or-later

// Command scour is a focused web crawler.
//
// This file does nothing but wire the command tree to the process: the exit
// code, the streams, and the signal that cancels a run. Everything it can do
// lives under internal/cli, so it can be tested without starting a process.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/rangertaha/scour/internal/cli"
)

func main() {
	// Cancelled on the first interrupt, so a long crawl stops where it is and
	// stays resumable. A second one is left to the runtime, which kills us:
	// somebody pressing it twice means now.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	a := cli.New()
	os.Exit(cli.Run(ctx, a, root(a), os.Args))
}
