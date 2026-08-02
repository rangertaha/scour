// SPDX-License-Identifier: GPL-3.0-or-later

package node

import (
	"context"
	"errors"

	ucli "github.com/urfave/cli/v3"

	"github.com/rangertaha/scour/internal/cli"
	"github.com/rangertaha/scour/internal/cluster"
)

// Leave asks a node to drain and go.
func Leave(a *cli.App) *ucli.Command {
	var busURL string

	return &ucli.Command{
		Name:      "leave",
		ArgsUsage: "[node]",
		Usage:     "Leave the cluster, draining first",
		Description: "Without a name, this machine's node, which is the one you are standing\n" +
			"in front of when something needs restarting.\n\n" +
			"Leaving is a request, not a kill. The node stops taking new work, finishes\n" +
			"the pages it already holds, and then exits and removes itself from the\n" +
			"listing, so nothing is dropped and no URL has to wait for a lease to expire.\n" +
			"It shows as `draining` until it goes.\n\n" +
			"A node that is already down has nothing left to finish, so its entry is just\n" +
			"removed.",
		UsageText: "  scour node leave\n" +
			"  scour node leave worker-3\n" +
			"  scour --json node leave worker-3",
		Flags: []ucli.Flag{busFlag(&busURL)},
		Action: func(c context.Context, cmd *ucli.Command) error {
			args, err := cli.AtMost(cmd, 1, "at most one node name")
			if err != nil {
				return err
			}
			name := cluster.DefaultName()
			if len(args) == 1 {
				name = args[0]
			}

			reg, done, err := openRegistry(c, a, busURL)
			if errors.Is(err, errNoCluster) {
				return noCluster(a)
			}
			if err != nil {
				return err
			}
			defer done()

			node, err := reg.Leave(c, name)
			if err != nil {
				return err
			}
			if a.JSON {
				return cli.WriteJSON(a.Out(), node)
			}
			// Two different things happened, and which one matters: a draining
			// node is still fetching for a while yet, and a removed one was
			// already gone before the command was typed.
			if node.State == cluster.StateDown {
				a.Printf("%s was down, removed\n", node.Name)
				return nil
			}
			a.Printf("%s is draining, and will leave when it has finished what it holds\n", node.Name)
			return nil
		},
	}
}
