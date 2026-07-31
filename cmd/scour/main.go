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

	if err := newRootCmd().Run(ctx, os.Args); err != nil {
		// urfave has already printed usage errors; anything else is ours.
		if !errors.Is(err, cli.ErrSilent) {
			fmt.Fprintln(os.Stderr, "scour:", err)
		}
		os.Exit(1)
	}
}
