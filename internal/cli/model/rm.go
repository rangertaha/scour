// SPDX-License-Identifier: GPL-3.0-or-later

package model

import (
	"context"
	"errors"
	"io/fs"
	"os"

	ucli "github.com/urfave/cli/v3"

	"github.com/rangertaha/scour/internal/cli"
)

// Remove discards what was learned, keeping the pages and the marks.
//
// `model rm` then `model train` is a clean retrain, and it is worth two
// commands rather than a --force on train: the first is the recoverable half
// and the second is the expensive half, and joining them would make the cheap
// act carry the cost of the dear one.
func Remove(a *cli.App) *ucli.Command {
	return &ucli.Command{
		Name:      "rm",
		Aliases:   []string{"remove"},
		ArgsUsage: "<item>",
		Usage:     "Discard the model, keeping the pages and the marks",
		Description: "The rules and the fitted chain go with it, because they are what training\n" +
			"produced. The cached pages stay, so a retrain costs the induction again and\n" +
			"not the crawl, and the marks stay, because they are the expensive part and\n" +
			"a person made them.",
		UsageText: "  scour model rm vehicle\n  scour model train vehicle",
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
			if err := s.DeleteModel(c, item.ID); err != nil {
				return err
			}

			// The files on disk are the model; the rows are what points at
			// them. Leaving the files would let a later run load a model the
			// database no longer knows about.
			for _, path := range []string{
				a.Cfg.ExtractModelPath(item.Name),
				a.Cfg.ScoreModelPath(item.Name),
			} {
				if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
					return err
				}
			}

			a.Printf("%s: discarded the model and %d rules\n", item.Name, st.Rules)
			a.Printf("the pages and the marks are kept: scour model train %s\n", item.Name)
			return nil
		},
	}
}
