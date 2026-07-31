// SPDX-License-Identifier: GPL-3.0-or-later

package items

import (
	"context"
	"errors"
	"strings"

	ucli "github.com/urfave/cli/v3"

	"github.com/rangertaha/scour/internal/cli"

	"github.com/rangertaha/scour/internal/store"
)

// addExamples is shown both in --help and by runAdd when a bare "scour add
// <name>" leaves the item with nothing to crawl, so the two never drift.
const addExamples = "  scour item add vehicle --alias car --alias 'pickup truck'\n" +
	"  scour item add vehicle -d example.com --subdomains\n" +
	"  scour item add vehicle -u http://www.example.com/cars/\n" +
	"  scour item add vehicle -p make -e Ford\n" +
	"  scour item add news -d example.com -p author -e 'Hannah McLeod' -a byline\n" +
	"  scour item add news -d example.com -p author --regex '^[^@]+$'\n" +
	"  scour item add news -p title --label '^(og:|twitter:)?title$'\n" +
	"  scour item add vehicle --type html --type pdf"

type addFlags struct {
	aliases    []string
	domains    []string
	urls       []string
	types      []string
	prop       string
	template   string
	propType   string
	example    string
	regex      string
	label      string
	subdomains bool
	depth      int
}

func Add(a *cli.App) *ucli.Command {
	var f addFlags

	cmd := &ucli.Command{
		Name:      "add",
		ArgsUsage: "<name>",
		Usage:     "Define an item, or add targets, properties and aliases to one",
		Description: "Creates the item if it does not exist, then applies whatever else is given.\n" +
			"Every form is idempotent, so repeating a command is never an error.\n\n" +
			"--prop names the subject. With it, --example, --alias, --label and --regex\n" +
			"describe the property; without it --alias describes the item. --domain adds a crawl\n" +
			"target on its own, and scopes the teaching when --prop is given, so what one\n" +
			"site calls a byline does not overwrite what the next one calls it.",
		UsageText: addExamples,
		Flags: []ucli.Flag{
			&ucli.StringSliceFlag{
				Name:        "alias",
				Aliases:     []string{"a"},
				Usage:       "another word a page might use; for the property when --prop is given, else for the item (repeatable)",
				Destination: &f.aliases,
			},
			&ucli.StringSliceFlag{
				Name:        "domain",
				Aliases:     []string{"d"},
				Usage:       "add a whole domain as a crawl target, or scope the property when --prop is given (repeatable)",
				Destination: &f.domains,
			},
			&ucli.StringSliceFlag{
				Name:        "url",
				Aliases:     []string{"u"},
				Usage:       "add a single URL as a crawl target (repeatable)",
				Destination: &f.urls,
			},
			&ucli.StringSliceFlag{
				Name:        "type",
				Usage:       "restrict crawls to a content type (repeatable)",
				Destination: &f.types,
			},
			&ucli.StringFlag{
				Name:        "template",
				Usage:       "start from a built-in schema: see scour list templates",
				Destination: &f.template,
			},
			&ucli.StringFlag{
				Name:        "prop",
				Aliases:     []string{"p"},
				Usage:       "add a property",
				Destination: &f.prop,
			},
			&ucli.StringFlag{
				Name:        "prop-type",
				Usage:       "the property's type: string, number, bool, date, url, email (date covers times)",
				Destination: &f.propType,
			},
			&ucli.StringFlag{
				Name:        "example",
				Aliases:     []string{"e"},
				Usage:       "an example value for the property",
				Destination: &f.example,
			},
			&ucli.StringFlag{
				Name:        "regex",
				Usage:       "what a valid value looks like; capture group one is the value if there is one",
				Destination: &f.regex,
			},
			&ucli.StringFlag{
				Name:        "label",
				Usage:       "what the name beside the value must look like, e.g. '^(og:|twitter:)?title$'",
				Destination: &f.label,
			},
			&ucli.BoolFlag{
				Name:        "subdomains",
				Usage:       "follow subdomains of the added domains",
				Destination: &f.subdomains,
			},
			&ucli.IntFlag{
				Name:        "depth",
				Usage:       "depth limit for the added targets (0 for the configured default)",
				Destination: &f.depth,
			},
		},
		Action: func(c context.Context, cmd *ucli.Command) error {
			args, err := cli.Need(cmd, 1, "one item name")
			if err != nil {
				return err
			}
			return runAdd(c, a, args[0], f)
		},
	}

	return cmd
}

func runAdd(c context.Context, a *cli.App, name string, f addFlags) error {
	if f.example != "" && f.prop == "" {
		return errors.New("--example needs --prop")
	}
	if f.regex != "" && f.prop == "" {
		return errors.New("--regex needs --prop")
	}
	if f.label != "" && f.prop == "" {
		return errors.New("--label needs --prop")
	}
	// --domain is a crawl target on its own and a scope alongside --prop, so
	// asking for both at once would mean two different things by one word.
	if f.prop != "" && len(f.domains) > 1 {
		return errors.New("--prop takes one --domain to scope it")
	}

	s, err := a.Store()
	if err != nil {
		return err
	}

	item, err := s.CreateItem(c, name)
	if err != nil {
		return err
	}

	var changes []string

	// With --prop the domain scopes the teaching; without it, it is a target.
	scope := ""
	if f.prop != "" && len(f.domains) == 1 {
		host, err := cli.NormaliseDomain(f.domains[0])
		if err != nil {
			return err
		}
		scope = host
	}

	if f.prop == "" {
		for _, alias := range f.aliases {
			alias = strings.TrimSpace(alias)
			if alias == "" {
				continue
			}
			if err := s.AddAlias(c, item.ID, alias); err != nil {
				return err
			}
			changes = append(changes, "alias "+alias)
		}
	}

	if scope == "" {
		for _, d := range f.domains {
			host, err := cli.NormaliseDomain(d)
			if err != nil {
				return err
			}
			if err := s.AddTarget(c, item.ID, store.TargetDomain, host, f.subdomains, f.depth); err != nil {
				return err
			}
			changes = append(changes, "domain "+host)
		}
	}

	for _, u := range f.urls {
		normalised, err := cli.NormaliseURL(u)
		if err != nil {
			return err
		}
		if err := s.AddTarget(c, item.ID, store.TargetURL, normalised, f.subdomains, f.depth); err != nil {
			return err
		}
		changes = append(changes, "url "+normalised)
	}

	if f.template != "" {
		added, err := applyTemplate(c, s, item.ID, f.template)
		if err != nil {
			return err
		}
		changes = append(changes, added...)
	}

	if f.prop != "" {
		if err := s.AddPropertyDetail(c, item.ID, store.PropertyDetail{
			Domain: scope, Name: f.prop, Type: f.propType,
			Example: f.example, Regex: f.regex, Label: f.label}); err != nil {
			return err
		}
		where := ""
		if scope != "" {
			where = " on " + scope
		}
		changes = append(changes, "property "+f.prop+where)

		for _, alias := range f.aliases {
			alias = strings.TrimSpace(alias)
			if alias == "" {
				continue
			}
			if err := s.AddPropertyAlias(c, item.ID, scope, f.prop, alias); err != nil {
				return err
			}
			changes = append(changes, "alias "+alias+" for "+f.prop+where)
		}
	}

	for _, t := range f.types {
		if err := s.AddContentType(c, item.ID, strings.ToLower(t)); err != nil {
			return err
		}
		changes = append(changes, "type "+t)
	}

	if len(changes) == 0 {
		// The item exists now, which is worth saying, but on its own it can
		// do nothing: it has no targets to crawl and no properties to look for.
		// Naming it and stopping tells someone their command worked when what
		// it did was leave them exactly where they were.
		a.Printf("item %s: nothing added yet, so there is nothing to crawl\n\n", item.Name)
		a.Printf("Add what to look for, and where to look:\n%s\n\n", addExamples)
		a.Printf("Then: scour start %s\nSee also: scour item add --help\n", item.Name)
		return nil
	}
	for _, change := range changes {
		a.Printf("%s: %s\n", item.Name, change)
	}
	return nil
}
