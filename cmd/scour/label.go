// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"fmt"
	"strconv"

	"github.com/urfave/cli/v3"

	"github.com/rangertaha/scour/internal/store"
)

func newValidCmd(a *app) *cli.Command {
	return labelCmd(a, "valid", store.Valid,
		"Label records as correct",
		"Correct records are evidence of where the data really lives, and are fed\n"+
			"back into the next training run.")
}

func newInvalidCmd(a *app) *cli.Command {
	return labelCmd(a, "invalid", store.Invalid,
		"Label records as wrong",
		"Wrong records are excluded from training, so the next model stops making the\n"+
			"same mistake.")
}

func newUnlabelCmd(a *app) *cli.Command {
	return labelCmd(a, "unlabel", store.Unlabelled,
		"Remove a label from records",
		"Puts records back to unlabelled, for when a verdict was given in error.")
}

// labelCmd builds one of the three labelling commands, which differ only in
// the verdict they apply.
func labelCmd(a *app, use string, label store.Label, short, long string) *cli.Command {
	return &cli.Command{
		Name:        use,
		ArgsUsage:   "<name> <id>...",
		Usage:       short,
		Description: long,
		UsageText:   "  scour " + use + " vehicle 1042 1043",
		Action: func(c context.Context, cmd *cli.Command) error {
			args, err := atLeast(cmd, 2, "an entity name and at least one record id")
			if err != nil {
				return err
			}
			s, err := a.Store()
			if err != nil {
				return err
			}

			entity, err := s.Entity(c, args[0])
			if err != nil {
				return err
			}

			ids := make([]uint, 0, len(args)-1)
			for _, arg := range args[1:] {
				id, err := strconv.ParseUint(arg, 10, 64)
				if err != nil {
					return fmt.Errorf("%q is not a record id: %w", arg, err)
				}
				ids = append(ids, uint(id))
			}

			n, err := s.LabelRecords(c, entity.ID, ids, label)
			if err != nil {
				return err
			}
			if n == 0 {
				return fmt.Errorf("no records matched: %w", store.ErrNotFound)
			}
			if int(n) < len(ids) {
				a.Printf("%s: labelled %d of %d, the rest are not %s's records\n",
					entity.Name, n, len(ids), entity.Name)
				return nil
			}

			a.Printf("%s: %d records marked %s\n", entity.Name, n, label)
			if label != store.Unlabelled {
				a.Printf("run `scour train %s` to fold that back into the model\n", entity.Name)
			}
			return nil
		},
	}
}
