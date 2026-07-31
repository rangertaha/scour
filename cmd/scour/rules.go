// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"fmt"
	"strconv"

	"github.com/urfave/cli/v3"
)

func newRulesCmd(a *app) *cli.Command {
	return &cli.Command{
		Category:  "TRAIN",
		Name:      "rules",
		ArgsUsage: "<name>",
		Usage:     "List the extraction rules learned for an item",
		Description: "Rules nest: the parent locates each record on the page, and its children\n" +
			"pull one property out of that record. HIT is the share of matching pages\n" +
			"where the rule fires.\n\n" +
			"A low HIT on a property means the rule found it on few of the pages it was\n" +
			"tried on, which is the first thing to look at when a field comes back empty.",
		UsageText: "  scour rules vehicle\n" +
			"  scour --json rules vehicle\n" +
			"  scour --limit 20 rules vehicle\n\n" +
			"Drop one that is wrong, then train again:\n" +
			"  scour item rm vehicle --rule 5\n" +
			"  scour train vehicle",
		Action: func(c context.Context, cmd *cli.Command) error {
			args, err := need(cmd, 1, "one item name")
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

			if a.jsonOut {
				return writeJSON(a.Out(), rules)
			}
			if len(rules) == 0 {
				a.Printf("no rules yet: scour train %s\n", item.Name)
				return nil
			}
			if a.limit > 0 && len(rules) > a.limit {
				rules = rules[:a.limit]
			}

			t := newTable(
				[]string{"ID", "PID", "HIT", "PROP", "XPATH", "SELECTOR", "REGEX", "URL"},
				alignRight, alignRight, alignRight, alignLeft, alignLeft, alignLeft, alignLeft, alignLeft,
			)
			for _, r := range rules {
				parent := ""
				if r.ParentID != nil {
					parent = strconv.FormatUint(uint64(*r.ParentID), 10)
				}
				t.add(
					strconv.FormatUint(uint64(r.ID), 10),
					parent,
					fmt.Sprintf("%.2f", r.Probability),
					r.Prop,
					truncate(r.XPath, 32),
					truncate(r.Selector, 24),
					truncate(r.Regex, 16),
					truncate(r.URIPattern, 30),
				)
			}
			return t.render(a.Out())
		},
	}
}
