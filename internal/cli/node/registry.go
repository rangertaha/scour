// SPDX-License-Identifier: GPL-3.0-or-later

package node

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	ucli "github.com/urfave/cli/v3"

	"github.com/rangertaha/scour/internal/bus"
	"github.com/rangertaha/scour/internal/cli"
	"github.com/rangertaha/scour/internal/cluster"
)

// errNoCluster is what the node listings report when there is no shared broker
// to read a fleet from.
var errNoCluster = errors.New("no cluster configured")

// busFlag is the same override `scour node join` takes, on the commands that
// read the fleet it joined.
//
// Without it the only way to point these at a cluster is the config file, and
// somebody who joined a node with --bus-url on the command line has nowhere to
// put the same address when they come to list what they joined.
func busFlag(into *string) ucli.Flag {
	return &ucli.StringFlag{
		Name:        "bus-url",
		Usage:       "NATS server the cluster is on (default from [bus] url in the config)",
		Destination: into,
	}
}

// openRegistry connects to the fleet registry a listing reads.
//
// With no broker configured there is nothing to connect to, and this refuses
// rather than falling back. bus.Open would start an embedded broker private to
// this process, and the fleet it then reported would be the empty one this
// command had just created: a NATS server started up in order to be told that
// nothing is running. A cluster is a shared broker, so that is what this asks
// for, and the commands say so when it is missing.
//
// The returned function closes the connection and must be called.
func openRegistry(ctx context.Context, a *cli.App, busURL string) (*cluster.Registry, func(), error) {
	if busURL == "" {
		busURL = a.Cfg.Bus.URL
	}
	if strings.TrimSpace(busURL) == "" {
		return nil, nil, errNoCluster
	}

	b, err := bus.Open(ctx, bus.Options{
		URL:      busURL,
		StoreDir: a.Cfg.Bus.StoreDir,
		Name:     "scour-cli",
	})
	if err != nil {
		// A broker that was configured and cannot be reached is the service
		// being down, not the command being wrong, which is exit 5.
		return nil, nil, fmt.Errorf("%w: %w", cli.ErrUnreachable, err)
	}

	reg, err := cluster.Open(ctx, b, cluster.Options{})
	if err != nil {
		b.Close()
		return nil, nil, err
	}
	return reg, func() { b.Close() }, nil
}

// noCluster is the same explanation wherever a node command finds no broker.
//
// It is not an error. A laptop running everything in one process has no cluster
// and is not misconfigured, so saying "none" is the answer rather than the
// failure, and --strict is what turns it into one.
func noCluster(a *cli.App) error {
	return a.Empty("no cluster: nodes appear once [bus] url points at a shared broker\n")
}

// ago renders a heartbeat as its age, which is the form the number is read in:
// what matters about `seen` is how stale it is, not what o'clock it was.
func ago(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := time.Since(t)
	switch {
	case d < 0:
		// A node whose clock runs ahead of this one. Reporting a negative age
		// would look like a bug in scour rather than a difference of clocks.
		return "just now"
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours())/24)
	}
}
