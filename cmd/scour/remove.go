// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"github.com/spf13/cobra"
)

type removeFlags struct {
	domains []string
	urls    []string
	props   []string
	rules   []uint
	force   bool
}

func newRemoveCmd(a *app) *cobra.Command {
	var f removeFlags

	cmd := &cobra.Command{
		Use:     "remove <name>",
		Aliases: []string{"rm"},
		Short:   "Remove an entity, or one of its targets, properties or rules",
		Long: "With no flags this deletes the entity and everything belonging to it, which\n" +
			"cannot be undone, so it asks for --force. With flags it removes only what\n" +
			"the flags name.",
		Example: "  scour remove vehicle -d example.com\n" +
			"  scour remove vehicle -p year\n" +
			"  scour remove vehicle --rule 5\n" +
			"  scour remove vehicle --force",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRemove(cmd, a, args[0], f)
		},
	}

	fl := cmd.Flags()
	fl.StringArrayVarP(&f.domains, "domain", "d", nil, "remove a domain target (repeatable)")
	fl.StringArrayVarP(&f.urls, "url", "u", nil, "remove a URL target (repeatable)")
	fl.StringArrayVarP(&f.props, "prop", "p", nil, "remove a property (repeatable)")
	fl.UintSliceVar(&f.rules, "rule", nil, "remove an induced rule by id (repeatable)")
	fl.BoolVar(&f.force, "force", false, "confirm deleting the whole entity")

	return cmd
}

func runRemove(cmd *cobra.Command, a *app, name string, f removeFlags) error {
	s, err := a.Store()
	if err != nil {
		return err
	}
	c := ctx(cmd)

	partial := len(f.domains) > 0 || len(f.urls) > 0 || len(f.props) > 0 || len(f.rules) > 0
	if !partial {
		if !f.force {
			cmd.Printf("this deletes entity %q and every target, rule and record it owns\n", name)
			cmd.Println("re-run with --force to confirm")
			return errSilent
		}
		if err := s.DeleteEntity(c, name); err != nil {
			return err
		}
		cmd.Printf("removed entity %s\n", name)
		return nil
	}

	entity, err := s.Entity(c, name)
	if err != nil {
		return err
	}

	for _, d := range f.domains {
		host, err := normaliseDomain(d)
		if err != nil {
			return err
		}
		if err := s.DeleteTarget(c, entity.ID, host); err != nil {
			return err
		}
		cmd.Printf("%s: removed domain %s\n", name, host)
	}

	for _, u := range f.urls {
		normalised, err := normaliseURL(u)
		if err != nil {
			return err
		}
		if err := s.DeleteTarget(c, entity.ID, normalised); err != nil {
			return err
		}
		cmd.Printf("%s: removed url %s\n", name, normalised)
	}

	for _, p := range f.props {
		if err := s.DeleteProperty(c, entity.ID, p); err != nil {
			return err
		}
		cmd.Printf("%s: removed property %s\n", name, p)
	}

	for _, id := range f.rules {
		if err := s.DeleteRule(c, entity.ID, id); err != nil {
			return err
		}
		cmd.Printf("%s: removed rule %d\n", name, id)
	}
	return nil
}
