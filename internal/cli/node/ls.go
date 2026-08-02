// SPDX-License-Identifier: GPL-3.0-or-later

package node

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	ucli "github.com/urfave/cli/v3"

	"github.com/rangertaha/scour/internal/cli"
	"github.com/rangertaha/scour/internal/cluster"
)

// List prints a line per node.
//
// There is no `scour node status` to go with it. There is already one status in
// the surface and it reports jobs, so the health columns a node status would
// have printed are here instead, unasked and always: one command less, and one
// meaning of the word status less.
func List(a *cli.App) *ucli.Command {
	var busURL string

	return &ucli.Command{
		Name:      "ls",
		Aliases:   []string{"list"},
		ArgsUsage: "[node]",
		Usage:     "A line per node: role, health, queue depth, throughput",
		Description: "With a name, everything about that one, which is `scour node show`.\n\n" +
			"QUEUE is what the node has left to fetch and RATE is pages a second, which\n" +
			"together say whether a crawl is discovering faster than the fleet can fetch.\n" +
			"SEEN is the age of the last heartbeat, so a node that has gone away shows as\n" +
			"stale rather than vanishing: a partition and a clean shutdown would otherwise\n" +
			"look the same. `down` is a node whose heartbeat aged out; a node that said\n" +
			"goodbye is not listed at all.\n\n" +
			"Nodes register through the broker, so a listing needs one that is shared. A\n" +
			"single-process scour has an embedded broker of its own and no fleet to show.",
		UsageText: "  scour node ls\n" +
			"  scour node ls worker-3\n" +
			"  scour --json node ls",
		Flags: []ucli.Flag{busFlag(&busURL)},
		Action: func(c context.Context, cmd *ucli.Command) error {
			args, err := cli.AtMost(cmd, 1, "at most one node name")
			if err != nil {
				return err
			}

			reg, done, err := openRegistry(c, a, busURL)
			if errors.Is(err, errNoCluster) {
				return noCluster(a)
			}
			if err != nil {
				return err
			}
			defer done()

			// A name means one node, which is what show prints, so the two
			// cannot answer differently.
			if len(args) == 1 {
				node, err := reg.Get(c, args[0])
				if err != nil {
					return err
				}
				return showNode(a, node)
			}

			nodes, err := reg.List(c)
			if err != nil {
				return err
			}
			if len(nodes) == 0 {
				return a.Empty("no nodes: scour node join --role crawl --bus-url %s\n", a.Cfg.Bus.URL)
			}
			if a.Limit > 0 && len(nodes) > a.Limit {
				nodes = nodes[:a.Limit]
			}

			if a.JSON {
				return cli.WriteJSON(a.Out(), nodes)
			}
			t := cli.NewTable(
				[]string{"NAME", "ROLE", "STATE", "QUEUE", "RATE", "SEEN"},
				cli.AlignLeft, cli.AlignLeft, cli.AlignLeft,
				cli.AlignRight, cli.AlignRight, cli.AlignLeft,
			)
			for _, n := range nodes {
				t.Add(n.Name, n.Role.String(), string(n.State),
					strconv.FormatInt(n.Queue, 10),
					fmt.Sprintf("%.1f/s", n.Rate), ago(n.Seen))
			}
			return t.Render(a.Out())
		},
	}
}

// Show is everything about one node.
func Show(a *cli.App) *ucli.Command {
	var busURL string

	return &ucli.Command{
		Name:      "show",
		ArgsUsage: "<node>",
		Usage:     "Everything about one node",
		Description: "The listing's columns, plus the machine it is on, the version it is\n" +
			"running and how long it has been in the cluster. A fleet part-way through an\n" +
			"upgrade is running two versions, and this is where that shows.",
		UsageText: "  scour node show worker-3\n" +
			"  scour --json node show worker-3",
		Flags: []ucli.Flag{busFlag(&busURL)},
		Action: func(c context.Context, cmd *ucli.Command) error {
			args, err := cli.Need(cmd, 1, "one node name")
			if err != nil {
				return err
			}

			reg, done, err := openRegistry(c, a, busURL)
			if errors.Is(err, errNoCluster) {
				return noCluster(a)
			}
			if err != nil {
				return err
			}
			defer done()

			node, err := reg.Get(c, args[0])
			if err != nil {
				return err
			}
			return showNode(a, node)
		},
	}
}

// showNode renders one node, and is what both `node ls <name>` and `node show`
// reach, so naming a node on the listing and asking for it directly print the
// same thing.
func showNode(a *cli.App, n cluster.Node) error {
	if a.JSON {
		return cli.WriteJSON(a.Out(), n)
	}
	line := func(label, value string) { a.Printf("%-10s  %s\n", label, value) }
	line("node", n.Name)
	line("role", n.Role.String())
	line("state", string(n.State))
	if n.Host != "" {
		line("host", n.Host)
	}
	if n.Version != "" {
		line("version", n.Version)
	}
	line("queue", strconv.FormatInt(n.Queue, 10))
	line("rate", fmt.Sprintf("%.1f/s", n.Rate))
	line("seen", ago(n.Seen))
	if !n.Joined.IsZero() {
		line("joined", n.Joined.Local().Format("2006-01-02 15:04"))
	}
	return nil
}
