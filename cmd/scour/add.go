// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/urfave/cli/v3"

	"github.com/rangertaha/scour/internal/store"
)

// addExamples is shown both in --help and by runAdd when a bare "scour add
// <name>" leaves the entity with nothing to crawl, so the two never drift.
const addExamples = "  scour add vehicle --alias car --alias 'pickup truck'\n" +
	"  scour add vehicle -d example.com --subdomains\n" +
	"  scour add vehicle -u http://www.example.com/cars/\n" +
	"  scour add vehicle -p make -e Ford\n" +
	"  scour add news -d example.com -p author -e 'Hannah McLeod' -a byline\n" +
	"  scour add news -d example.com -p author --regex '^[^@]+$'\n" +
	"  scour add news -p title --label '^(og:|twitter:)?title$'\n" +
	"  scour add vehicle --type html --type pdf"

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

func newAddCmd(a *app) *cli.Command {
	var f addFlags

	cmd := &cli.Command{
		Name:      "add",
		ArgsUsage: "<name>",
		Usage:     "Define an entity, or add targets, properties and aliases to one",
		Description: "Creates the entity if it does not exist, then applies whatever else is given.\n" +
			"Every form is idempotent, so repeating a command is never an error.\n\n" +
			"--prop names the subject. With it, --example, --alias, --label and --regex\n" +
			"describe the property; without it --alias describes the entity. --domain adds a crawl\n" +
			"target on its own, and scopes the teaching when --prop is given, so what one\n" +
			"site calls a byline does not overwrite what the next one calls it.",
		UsageText: addExamples,
		Flags: []cli.Flag{
			&cli.StringSliceFlag{
				Name:        "alias",
				Aliases:     []string{"a"},
				Usage:       "another word a page might use; for the property when --prop is given, else for the entity (repeatable)",
				Destination: &f.aliases,
			},
			&cli.StringSliceFlag{
				Name:        "domain",
				Aliases:     []string{"d"},
				Usage:       "add a whole domain as a crawl target, or scope the property when --prop is given (repeatable)",
				Destination: &f.domains,
			},
			&cli.StringSliceFlag{
				Name:        "url",
				Aliases:     []string{"u"},
				Usage:       "add a single URL as a crawl target (repeatable)",
				Destination: &f.urls,
			},
			&cli.StringSliceFlag{
				Name:        "type",
				Usage:       "restrict crawls to a content type (repeatable)",
				Destination: &f.types,
			},
			&cli.StringFlag{
				Name:        "template",
				Usage:       "start from a built-in schema: see scour list templates",
				Destination: &f.template,
			},
			&cli.StringFlag{
				Name:        "prop",
				Aliases:     []string{"p"},
				Usage:       "add a property",
				Destination: &f.prop,
			},
			&cli.StringFlag{
				Name:        "prop-type",
				Usage:       "the property's type: string, number, bool, date, url, email (date covers times)",
				Destination: &f.propType,
			},
			&cli.StringFlag{
				Name:        "example",
				Aliases:     []string{"e"},
				Usage:       "an example value for the property",
				Destination: &f.example,
			},
			&cli.StringFlag{
				Name:        "regex",
				Usage:       "what a valid value looks like; capture group one is the value if there is one",
				Destination: &f.regex,
			},
			&cli.StringFlag{
				Name:        "label",
				Usage:       "what the name beside the value must look like, e.g. '^(og:|twitter:)?title$'",
				Destination: &f.label,
			},
			&cli.BoolFlag{
				Name:        "subdomains",
				Usage:       "follow subdomains of the added domains",
				Destination: &f.subdomains,
			},
			&cli.IntFlag{
				Name:        "depth",
				Usage:       "depth limit for the added targets (0 for the configured default)",
				Destination: &f.depth,
			},
		},
		Action: func(c context.Context, cmd *cli.Command) error {
			args, err := need(cmd, 1, "one entity name")
			if err != nil {
				return err
			}
			return runAdd(c, a, args[0], f)
		},
	}

	return cmd
}

func runAdd(c context.Context, a *app, name string, f addFlags) error {
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

	entity, err := s.CreateEntity(c, name)
	if err != nil {
		return err
	}

	var changes []string

	// With --prop the domain scopes the teaching; without it, it is a target.
	scope := ""
	if f.prop != "" && len(f.domains) == 1 {
		host, err := normaliseDomain(f.domains[0])
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
			if err := s.AddAlias(c, entity.ID, alias); err != nil {
				return err
			}
			changes = append(changes, "alias "+alias)
		}
	}

	if scope == "" {
		for _, d := range f.domains {
			host, err := normaliseDomain(d)
			if err != nil {
				return err
			}
			if err := s.AddTarget(c, entity.ID, store.TargetDomain, host, f.subdomains, f.depth); err != nil {
				return err
			}
			changes = append(changes, "domain "+host)
		}
	}

	for _, u := range f.urls {
		normalised, err := normaliseURL(u)
		if err != nil {
			return err
		}
		if err := s.AddTarget(c, entity.ID, store.TargetURL, normalised, f.subdomains, f.depth); err != nil {
			return err
		}
		changes = append(changes, "url "+normalised)
	}

	if f.template != "" {
		added, err := applyTemplate(c, s, entity.ID, f.template)
		if err != nil {
			return err
		}
		changes = append(changes, added...)
	}

	if f.prop != "" {
		if err := s.AddPropertyDetail(c, entity.ID, store.PropertyDetail{
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
			if err := s.AddPropertyAlias(c, entity.ID, scope, f.prop, alias); err != nil {
				return err
			}
			changes = append(changes, "alias "+alias+" for "+f.prop+where)
		}
	}

	for _, t := range f.types {
		if err := s.AddContentType(c, entity.ID, strings.ToLower(t)); err != nil {
			return err
		}
		changes = append(changes, "type "+t)
	}

	if len(changes) == 0 {
		// The entity exists now, which is worth saying, but on its own it can
		// do nothing: it has no targets to crawl and no properties to look for.
		// Naming it and stopping tells someone their command worked when what
		// it did was leave them exactly where they were.
		a.Printf("entity %s: nothing added yet, so there is nothing to crawl\n\n", entity.Name)
		a.Printf("Add what to look for, and where to look:\n%s\n\n", addExamples)
		a.Printf("Then: scour crawl %s\nSee also: scour add --help\n", entity.Name)
		return nil
	}
	for _, change := range changes {
		a.Printf("%s: %s\n", entity.Name, change)
	}
	return nil
}

// normaliseDomain reduces a domain to its bare host, so example.com,
// www.example.com and https://example.com/ are one target.
func normaliseDomain(raw string) (string, error) {
	host := strings.TrimSpace(strings.ToLower(raw))
	if host == "" {
		return "", errors.New("domain must not be empty")
	}
	if strings.Contains(host, "://") {
		u, err := url.Parse(host)
		if err != nil {
			return "", fmt.Errorf("parse domain %q: %w", raw, err)
		}
		host = u.Host
	}
	host = strings.TrimSuffix(host, "/")
	if i := strings.IndexByte(host, '/'); i >= 0 {
		host = host[:i]
	}
	host = strings.TrimPrefix(host, "www.")
	if host == "" {
		return "", fmt.Errorf("domain %q has no host", raw)
	}
	return host, nil
}

// normaliseURL checks a URL is absolute and returns it with a scheme.
func normaliseURL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", errors.New("url must not be empty")
	}
	if !strings.Contains(trimmed, "://") {
		trimmed = "http://" + trimmed
	}
	u, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("parse url %q: %w", raw, err)
	}
	if u.Host == "" {
		return "", fmt.Errorf("url %q has no host", raw)
	}
	u.Fragment = ""
	return u.String(), nil
}
