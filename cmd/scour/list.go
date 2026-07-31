// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"strconv"

	"github.com/spf13/cobra"
)

func newListCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the entities you have defined and how many matches each has found",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := a.Store()
			if err != nil {
				return err
			}

			rows, err := s.Entities(ctx(cmd))
			if err != nil {
				return err
			}
			if a.limit > 0 && len(rows) > a.limit {
				rows = rows[:a.limit]
			}

			if a.jsonOut {
				return writeJSON(cmd.OutOrStdout(), rows)
			}

			if len(rows) == 0 {
				cmd.Println("no entities yet: scour add <name> --alias <word>")
				return nil
			}

			t := newTable([]string{"NAME", "MATCHES"}, alignLeft, alignRight)
			for _, r := range rows {
				t.add(r.Name, strconv.Itoa(r.Matches))
			}
			return t.render(cmd.OutOrStdout())
		},
	}
}
