// SPDX-License-Identifier: GPL-3.0-or-later

package model

import (
	"context"
	"fmt"
	"os"

	ucli "github.com/urfave/cli/v3"

	"github.com/rangertaha/scour/internal/cli"
)

// Show reports what was learned, and from how much.
//
// The counts are the point. A model trained on forty pages and one trained on
// twelve hundred are the same file with very different standing, and nothing
// else in the surface says which you have.
func Show(a *cli.App) *ucli.Command {
	return &ucli.Command{
		Name:      "show",
		ArgsUsage: "<item>",
		Usage:     "What it learned, and from how much",
		UsageText: "  scour model show vehicle\n  scour --json model show vehicle",
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
			st, err := s.Status(c, item.ID)
			if err != nil {
				return err
			}
			if st.Model == nil {
				return a.Empty("%s has no model yet: scour model train %s\n", item.Name, item.Name)
			}
			if a.JSON {
				return cli.WriteJSON(a.Out(), st.Model)
			}

			line := func(label, value string) { a.Printf("%-12s  %s\n", label, value) }
			line("item", item.Name)
			line("algorithm", st.Model.Algorithm)
			line("trained", st.Model.TrainedAt.Format("2006-01-02 15:04"))
			// Accuracy is only measured when there were enough examples to hold
			// some back, so printing 0.00 for a model that was never scored
			// would read as a model that scored nothing.
			if st.Model.Accuracy > 0 {
				line("accuracy", fmt.Sprintf("%.2f", st.Model.Accuracy))
			} else {
				line("accuracy", "not measured: too few examples to hold any back")
			}
			line("observations", fmt.Sprintf("%d", st.Model.Observations))
			line("rules", fmt.Sprintf("%d", st.Rules))
			line("records", fmt.Sprintf("%d", st.Matches))
			line("from", fmt.Sprintf("%d pages fetched", st.Visited))
			if st.Model.Path != "" {
				if _, err := os.Stat(st.Model.Path); err == nil {
					line("path", st.Model.Path)
				} else {
					// The meta says there is a model and the file is gone,
					// which a training run would silently replace and a crawl
					// would silently score without.
					line("path", st.Model.Path+"  (missing on disk)")
				}
			}
			return nil
		},
	}
}
