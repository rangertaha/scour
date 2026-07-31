// SPDX-License-Identifier: GPL-3.0-or-later

package items

import (
	"context"

	ucli "github.com/urfave/cli/v3"

	"github.com/rangertaha/scour/internal/cli"
)

type removeFlags struct {
	domains []string
	urls    []string
	props   []string
	rules   []uint
	force   bool
}

func Remove(a *cli.App) *ucli.Command {
	var f removeFlags

	cmd := &ucli.Command{
		Name:      "rm",
		ArgsUsage: "<name>",
		Aliases:   []string{"remove"},
		Usage:     "Remove an item, or one of its targets, properties or rules",
		Description: "With no flags this deletes the item and everything belonging to it, which\n" +
			"cannot be undone, so it asks for --force. With flags it removes only what\n" +
			"the flags name.",
		UsageText: "  scour item rm vehicle -d example.com\n" +
			"  scour item rm vehicle -p year\n" +
			"  scour item rm vehicle --rule 5\n" +
			"  scour item rm vehicle --force",
		Flags: []ucli.Flag{
			&ucli.StringSliceFlag{
				Name:        "domain",
				Aliases:     []string{"d"},
				Usage:       "remove a domain target (repeatable)",
				Destination: &f.domains,
			},
			&ucli.StringSliceFlag{
				Name:        "url",
				Aliases:     []string{"u"},
				Usage:       "remove a URL target (repeatable)",
				Destination: &f.urls,
			},
			&ucli.StringSliceFlag{
				Name:        "prop",
				Aliases:     []string{"p"},
				Usage:       "remove a property (repeatable)",
				Destination: &f.props,
			},
			&ucli.UintSliceFlag{
				Name:        "rule",
				Usage:       "remove an induced rule by id (repeatable)",
				Destination: &f.rules,
			},
			&ucli.BoolFlag{
				Name:        "force",
				Usage:       "confirm deleting the whole item",
				Destination: &f.force,
			},
		},
		Action: func(c context.Context, cmd *ucli.Command) error {
			args, err := cli.Need(cmd, 1, "one item name")
			if err != nil {
				return err
			}
			return runRemove(c, a, args[0], f)
		},
	}

	return cmd
}

func runRemove(c context.Context, a *cli.App, name string, f removeFlags) error {
	s, err := a.Store()
	if err != nil {
		return err
	}

	partial := len(f.domains) > 0 || len(f.urls) > 0 || len(f.props) > 0 || len(f.rules) > 0
	if !partial {
		if !f.force {
			a.Printf("this deletes item %q and every target, rule and record it owns\n", name)
			a.Println("re-run with --force to confirm")
			return cli.ErrSilent
		}
		if err := s.DeleteItem(c, name); err != nil {
			return err
		}
		a.Printf("removed item %s\n", name)
		return nil
	}

	item, err := s.Item(c, name)
	if err != nil {
		return err
	}

	for _, d := range f.domains {
		host, err := cli.NormaliseDomain(d)
		if err != nil {
			return err
		}
		if err := s.DeleteTarget(c, item.ID, host); err != nil {
			return err
		}
		a.Printf("%s: removed domain %s\n", name, host)
	}

	for _, u := range f.urls {
		normalised, err := cli.NormaliseURL(u)
		if err != nil {
			return err
		}
		if err := s.DeleteTarget(c, item.ID, normalised); err != nil {
			return err
		}
		a.Printf("%s: removed url %s\n", name, normalised)
	}

	for _, p := range f.props {
		if err := s.DeleteProperty(c, item.ID, p); err != nil {
			return err
		}
		a.Printf("%s: removed property %s\n", name, p)
	}

	for _, id := range f.rules {
		if err := s.DeleteRule(c, item.ID, id); err != nil {
			return err
		}
		a.Printf("%s: removed rule %d\n", name, id)
	}
	return nil
}
