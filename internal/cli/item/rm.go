// SPDX-License-Identifier: GPL-3.0-or-later

package item

import (
	"context"
	"fmt"
	"strings"

	ucli "github.com/urfave/cli/v3"

	"github.com/rangertaha/scour/internal/cli"
	"github.com/rangertaha/scour/internal/normalise"
)

type removeFlags struct {
	domains []string
	urls    []string
	props   []string
	rules   []uint
	force   bool

	// clear names the details to blank on the properties given with --prop,
	// rather than removing the properties themselves.
	//
	// One flag taking the detail's name rather than a boolean per detail: an
	// --example that means "set this example" on one command and "throw the
	// example away" on another is the same fault as -d meaning both domain and
	// delete.
	clear []string
	on    string
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
			&ucli.StringFlag{
				Name:        "on",
				Usage:       "the `domain` a property was taught on, for clearing a scoped one",
				Destination: &f.on,
			},
			&ucli.StringSliceFlag{
				Name: "clear",
				Usage: "with --prop, clear one detail rather than removing the property: " +
					"example, label, regex or description (repeatable)",
				Destination: &f.clear,
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

// clearing lists the details named for blanking, rejecting a name that is not
// one, so a typo fails here rather than silently clearing nothing.
func (f removeFlags) clearing() ([]string, error) {
	known := map[string]bool{"example": true, "label": true, "regex": true, "description": true}
	out := make([]string, 0, len(f.clear))
	for _, name := range f.clear {
		name = strings.ToLower(strings.TrimSpace(name))
		if !known[name] {
			return nil, fmt.Errorf("--clear takes example, label, regex or description, got %q", name)
		}
		out = append(out, name)
	}
	return out, nil
}

func runRemove(c context.Context, a *cli.App, name string, f removeFlags) error {
	s, err := a.Store()
	if err != nil {
		return err
	}

	// Naming a detail to clear is a partial removal even with no --prop yet:
	// without this, `rm news --regex` fell through to deleting the whole item.
	partial := len(f.domains) > 0 || len(f.urls) > 0 || len(f.props) > 0 ||
		len(f.rules) > 0 || len(f.clear) > 0
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

	// --prop names a property to remove, unless a detail is named too, in
	// which case it names the property to take that detail off. Removing the
	// whole property to drop a regex taught in error is a poor trade: the
	// example, the type and every word taught for it go with it.
	fields, err := f.clearing()
	if err != nil {
		return err
	}
	if len(fields) > 0 {
		if len(f.props) == 0 {
			return fmt.Errorf("--%s needs --prop to say which property to clear", fields[0])
		}
		scope := ""
		if f.on != "" {
			if scope, err = normalise.Domain(f.on); err != nil {
				return err
			}
		}
		for _, prop := range f.props {
			if err := s.ClearPropertyFields(c, item.ID, scope, prop, fields); err != nil {
				return err
			}
			where := ""
			if scope != "" {
				where = " on " + scope
			}
			a.Printf("%s: cleared %s on %s%s\n", item.Name, strings.Join(fields, ", "), prop, where)
		}
		return nil
	}

	job, err := s.JobForItem(c, item)
	if err != nil {
		return err
	}

	for _, d := range f.domains {
		host, err := normalise.Domain(d)
		if err != nil {
			return err
		}
		if err := s.DeleteTarget(c, job.ID, host); err != nil {
			return err
		}
		a.Printf("%s: removed domain %s\n", name, host)
	}

	for _, u := range f.urls {
		normalised, err := normalise.URL(u)
		if err != nil {
			return err
		}
		if err := s.DeleteTarget(c, job.ID, normalised); err != nil {
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
