// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"github.com/rangertaha/scour/internal/store"
)

type addFlags struct {
	aliases    []string
	domains    []string
	urls       []string
	types      []string
	prop       string
	template   string
	propType   string
	example    string
	subdomains bool
	depth      int
}

func newAddCmd(a *app) *cobra.Command {
	var f addFlags

	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Define an entity, or add targets, properties and aliases to one",
		Long: "Creates the entity if it does not exist, then applies whatever else is given.\n" +
			"Every form is idempotent, so repeating a command is never an error.",
		Example: "  scour add vehicle --alias car --alias 'pickup truck'\n" +
			"  scour add vehicle -d example.com --subdomains\n" +
			"  scour add vehicle -u http://www.example.com/cars/\n" +
			"  scour add vehicle -p make -e Ford\n" +
			"  scour add vehicle --type html --type pdf",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAdd(cmd, a, args[0], f)
		},
	}

	fl := cmd.Flags()
	fl.StringArrayVar(&f.aliases, "alias", nil, "another word or phrase a page might use for the entity (repeatable)")
	fl.StringArrayVarP(&f.domains, "domain", "d", nil, "add a whole domain as a crawl target (repeatable)")
	fl.StringArrayVarP(&f.urls, "url", "u", nil, "add a single URL as a crawl target (repeatable)")
	fl.StringArrayVar(&f.types, "type", nil, "restrict crawls to a content type (repeatable)")
	fl.StringVar(&f.template, "template", "", "start from a built-in schema: see scour list templates")
	fl.StringVarP(&f.prop, "prop", "p", "", "add a property")
	fl.StringVar(&f.propType, "prop-type", "", "the property's type: string, number, date")
	fl.StringVarP(&f.example, "example", "e", "", "an example value for the property")
	fl.BoolVar(&f.subdomains, "subdomains", false, "follow subdomains of the added domains")
	fl.IntVar(&f.depth, "depth", 0, "depth limit for the added targets (0 for the configured default)")

	return cmd
}

func runAdd(cmd *cobra.Command, a *app, name string, f addFlags) error {
	if f.example != "" && f.prop == "" {
		return errors.New("--example needs --prop")
	}

	s, err := a.Store()
	if err != nil {
		return err
	}
	c := ctx(cmd)

	entity, err := s.CreateEntity(c, name)
	if err != nil {
		return err
	}

	var changes []string

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
		if err := s.AddProperty(c, entity.ID, f.prop, f.propType, f.example); err != nil {
			return err
		}
		changes = append(changes, "property "+f.prop)
	}

	for _, t := range f.types {
		if err := s.AddContentType(c, entity.ID, strings.ToLower(t)); err != nil {
			return err
		}
		changes = append(changes, "type "+t)
	}

	if len(changes) == 0 {
		cmd.Printf("entity %s\n", entity.Name)
		return nil
	}
	for _, change := range changes {
		cmd.Printf("%s: %s\n", entity.Name, change)
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
