// SPDX-License-Identifier: GPL-3.0-or-later

package model

import (
	"context"
	"fmt"
	"strconv"

	ucli "github.com/urfave/cli/v3"

	"github.com/rangertaha/scour/internal/cli"
)

func Rules(a *cli.App) *ucli.Command {
	return &ucli.Command{
		Name:      "rules",
		ArgsUsage: "<name>",
		Usage:     "List the extraction rules learned for an item",
		Description: "Rules nest: the parent locates each record on the page, and its children\n" +
			"pull one property out of that record. HIT is the share of matching pages\n" +
			"where the rule fires.\n\n" +
			"A low HIT on a property means the rule found it on few of the pages it was\n" +
			"tried on, which is the first thing to look at when a field comes back empty.",
		UsageText: "  scour model rules vehicle\n" +
			"  scour --json rules vehicle\n" +
			"  scour --limit 20 rules vehicle\n\n" +
			"Drop one that is wrong, then train again:\n" +
			"  scour item rm vehicle --rule 5\n" +
			"  scour model train vehicle",
		Action: func(c context.Context, cmd *ucli.Command) error {
			args, err := cli.Need(cmd, 1, "one item name")
			if err != nil {
				return err
			}
			s, err := a.Store()
			if err != nil {
				return err
			}

			item, err := s.Item(c, args[0])
			if err != nil {
				return err
			}
			rules, err := s.Rules(c, item.ID)
			if err != nil {
				return err
			}

			if a.JSON {
				return cli.WriteJSON(a.Out(), rules)
			}
			if len(rules) == 0 {
				return a.Empty("no rules yet: scour model train %s\n", item.Name)
			}
			if a.Limit > 0 && len(rules) > a.Limit {
				rules = rules[:a.Limit]
			}

			t := cli.NewTable(
				[]string{"ID", "PID", "HIT", "PROP", "XPATH", "SELECTOR", "REGEX", "URL"},
				cli.AlignRight, cli.AlignRight, cli.AlignRight, cli.AlignLeft, cli.AlignLeft, cli.AlignLeft, cli.AlignLeft, cli.AlignLeft,
			)
			for _, r := range rules {
				parent := ""
				if r.ParentID != nil {
					parent = strconv.FormatUint(uint64(*r.ParentID), 10)
				}
				t.Add(
					strconv.FormatUint(uint64(r.ID), 10),
					parent,
					fmt.Sprintf("%.2f", r.Probability),
					r.Prop,
					cli.Truncate(r.XPath, 32),
					cli.Truncate(r.Selector, 24),
					cli.Truncate(r.Regex, 16),
					cli.Truncate(r.URIPattern, 30),
				)
			}
			return t.Render(a.Out())
		},
	}
}
