// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"fmt"
	"strconv"

	"github.com/spf13/cobra"
)

func newRulesCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "rules <name>",
		Short: "List the extraction rules learned for an entity",
		Long: "Rules nest: the parent locates each record on the page, and its children\n" +
			"pull one property out of that record. HIT is the share of matching pages\n" +
			"where the rule fires.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := a.Store()
			if err != nil {
				return err
			}
			c := ctx(cmd)

			entity, err := s.Entity(c, args[0])
			if err != nil {
				return err
			}
			rules, err := s.Rules(c, entity.ID)
			if err != nil {
				return err
			}

			if a.jsonOut {
				return writeJSON(cmd.OutOrStdout(), rules)
			}
			if len(rules) == 0 {
				cmd.Printf("no rules yet: scour train %s\n", entity.Name)
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
			return t.render(cmd.OutOrStdout())
		},
	}
}
