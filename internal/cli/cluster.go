// SPDX-License-Identifier: GPL-3.0-or-later

package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	ucli "github.com/urfave/cli/v3"

	"github.com/rangertaha/scour/internal/bus"
	"github.com/rangertaha/scour/internal/safefile"
)

// Cluster is who is out there.
//
// # Why joining is remembered
//
// Because every other command needs the address, and typing it on each one is
// how people end up pointing half their commands at the wrong cluster. `scour
// cluster join` writes it down once, and everything else looks it up.
//
// It is remembered rather than configured: a file somebody edits would be a
// second place for the address to be wrong, so this writes the file and the
// commands read it, and the flag and the environment still win over it.
func Cluster(a *App) *ucli.Command {
	var join string

	return &ucli.Command{
		Name:            "cluster",
		HideHelpCommand: true,
		Category:        "Running a cluster",
		Usage:           "Join a cluster and see who is in it",
		ArgsUsage:       "<command>",
		Description: "A cluster is a bus, the nodes serving stages on it, and the services\n" +
			"answering for what they share.\n\n" +
			"`join` remembers an address so the other commands do not need one.\n" +
			"`list` says who is there now.",
		Commands: []*ucli.Command{
			{
				Name:      "join",
				Usage:     "Remember a cluster, after checking it answers",
				ArgsUsage: "<nats://host:port>",
				Description: "Connects, lists who is there, and writes the address down. Every\n" +
					"later command uses it unless --join or " + ServerVar + " says otherwise.\n\n" +
					"It joins nothing by itself: a machine offers work by running\n" +
					"`scour server --join <address>`. This is the client end.",
				Action: func(ctx context.Context, cmd *ucli.Command) error {
					switch cmd.Args().Len() {
					case 1:
						return joinCluster(ctx, a, cmd.Args().First())
					case 0:
						return Usagef("no address given, as nats://host:port")
					default:
						return Usagef("one address at a time, got %d", cmd.Args().Len())
					}
				},
			},
			{
				Name:  "list",
				Usage: "Who is in the cluster now",
				Flags: []ucli.Flag{
					&ucli.StringFlag{Name: "join", Usage: "a cluster to ask, as nats://host:port", Destination: &join},
				},
				Action: func(ctx context.Context, cmd *ucli.Command) error {
					if cmd.Args().Len() > 0 {
						return Usagef("list takes no arguments, got %q", cmd.Args().First())
					}
					return listCluster(ctx, a, join)
				},
			},
		},
	}
}

func joinCluster(ctx context.Context, a *App, url string) error {
	if !strings.Contains(url, "://") {
		// A bare host:port is what people type, and refusing it by name beats
		// connecting to something surprising: nats.Connect fills in a scheme
		// of its own, so a typo becomes a timeout rather than a complaint.
		return Usagef("%q is not an address. It should look like nats://host:port", url)
	}

	conn, err := bus.Connect(bus.Options{URL: url, Name: "scour"})
	if err != nil {
		return Failedf("%v", err)
	}
	defer func() { _ = conn.Close() }()

	if err := printMembers(ctx, a, conn); err != nil {
		return err
	}

	// Written only once the cluster has answered. Remembering an address that
	// does not work is worse than not remembering one: every later command
	// fails against it, and nothing says where the address came from.
	where, err := remember(url)
	if err != nil {
		return Failedf("%v", err)
	}
	a.Printf("joined %s, remembered in %s\n", url, where)
	return nil
}

func listCluster(ctx context.Context, a *App, join string) error {
	conn, err := bus.Connect(bus.Options{URL: server(join), Name: "scour"})
	if err != nil {
		return Failedf("%v", err)
	}
	defer func() { _ = conn.Close() }()

	a.Warnf("cluster %s\n", conn.Address())
	return printMembers(ctx, a, conn)
}

// printMembers lists the nodes that have announced themselves.
func printMembers(ctx context.Context, a *App, conn *bus.Conn) error {
	nodes, err := conn.OpenNodes(ctx)
	if err != nil {
		return Failedf("%v", err)
	}

	here, err := nodes.Here(ctx)
	if err != nil {
		return Failedf("%v", err)
	}
	if len(here) == 0 {
		// Not an error. A cluster with a broker and no nodes is a cluster
		// somebody has just started, and it is exactly what they should be
		// told rather than an empty success.
		a.Printf("no nodes. Start one with `scour server`\n")
		return nil
	}

	names := make([]string, 0, len(here))
	for name := range here {
		names = append(names, name)
	}
	sort.Strings(names)

	a.Printf("%-24s %-20s %s\n", "NODE", "STAGES", "BUS")
	for _, name := range names {
		var announced struct {
			Stages []string `json:"stages"`
			Bus    string   `json:"bus"`
		}
		stages := "unreadable"
		address := ""
		if err := json.Unmarshal(here[name], &announced); err == nil {
			stages = strings.Join(announced.Stages, ",")
			address = announced.Bus
			if stages == "" {
				stages = "none"
			}
		}
		a.Printf("%-24s %-20s %s\n", name, stages, address)
	}
	return nil
}

// remembered is the cluster `scour cluster join` last accepted, or empty.
//
// A failure to read is not reported, and this is the one place that is right:
// a missing or unreadable note means nothing was remembered, and the caller
// falls through to the default. Refusing to run because a cache file is
// unreadable would be worse than ignoring it.
func remembered() string {
	path, err := clusterFile()
	if err != nil {
		return ""
	}
	src, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(src))
}

// remember writes the address down and says where it went.
func remember(url string) (string, error) {
	path, err := clusterFile()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}

	// Replaced rather than truncated, which is the rule this repository
	// already keeps for every file it rewrites. See [internal/safefile].
	if err := safefile.Replace(path, []byte(url+"\n"), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// clusterFile is where the remembered address lives.
func clusterFile() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("cannot find a config directory to remember the cluster in: %w", err)
	}
	return filepath.Join(dir, "scour", "cluster"), nil
}
