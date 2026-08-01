// SPDX-License-Identifier: GPL-3.0-or-later

// Command scour is a focused web crawler that scores links by how likely they
// are to describe the thing you are looking for.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/rangertaha/scour/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// The exit code is what a script reads, so it distinguishes the cases a
	// caller acts on differently: a missing item is a thing to create, a failed
	// command is a thing to retry, and an empty result is usually neither.
	if err := newRootCmd().Run(ctx, os.Args); err != nil {
		// urfave has already printed usage errors; anything else is ours, and
		// an empty result was printed by the command that found nothing.
		if !errors.Is(err, cli.ErrSilent) && !errors.Is(err, cli.ErrEmpty) {
			fmt.Fprintln(os.Stderr, "scour:", err)
		}
		os.Exit(cli.ExitCode(err))
	}
}
