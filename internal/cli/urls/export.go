// SPDX-License-Identifier: GPL-3.0-or-later

package urls

import (
	"bufio"
	"context"
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	ucli "github.com/urfave/cli/v3"

	"github.com/rangertaha/scour/internal/cli"

	"github.com/rangertaha/scour/internal/store"
)

type urlExportFlags struct {
	urls     string
	domains  string
	props    string
	aliases  string
	toStdout bool
}

// Export is the other half of import.
//
// What an item is worth keeping outside scour is the list it was built from:
// the domains and URLs, and the properties and words taught for them. Those
// arrive by import from a file, and until now there was no way back out, so a
// list assembled over a long crawl existed only inside one database.
func Export(a *cli.App) *ucli.Command {
	var f urlExportFlags

	return &ucli.Command{
		Category:  "URLS",
		Name:      "export",
		ArgsUsage: "<name>",
		Usage:     "Write domains and urls to file",
		Description: "Writes what `scour import` reads, in the same formats, so an item can be\n" +
			"moved between databases or kept under version control.\n\n" +
			"With no flags the domains and urls go to stdout, which is the quick look.\n" +
			"Naming a file writes it there instead.",
		UsageText: "  scour export vehicle\n" +
			"  scour export vehicle --domains domains.txt --urls urls.txt\n" +
			"  scour export vehicle --props props.csv --aliases aliases.txt\n" +
			"  scour export vehicle --urls - > urls.txt",
		Flags: []ucli.Flag{
			&ucli.StringFlag{
				Name:        "urls",
				Usage:       "write URL targets to this `file`, or - for stdout",
				Destination: &f.urls,
			},
			&ucli.StringFlag{
				Name:        "domains",
				Usage:       "write domain targets to this `file`, or - for stdout",
				Destination: &f.domains,
			},
			&ucli.StringFlag{
				Name:        "props",
				Usage:       "write properties to this CSV `file`, or - for stdout",
				Destination: &f.props,
			},
			&ucli.StringFlag{
				Name:        "aliases",
				Usage:       "write the item's aliases to this `file`, or - for stdout",
				Destination: &f.aliases,
			},
		},
		Action: func(c context.Context, cmd *ucli.Command) error {
			args, err := cli.Need(cmd, 1, "one item name")
			if err != nil {
				return err
			}
			return runURLExport(c, a, args[0], f)
		},
	}
}

func runURLExport(c context.Context, a *cli.App, name string, f urlExportFlags) error {
	s, err := a.Store()
	if err != nil {
		return err
	}
	item, err := s.Item(c, name)
	if err != nil {
		return err
	}

	// No flags means the two lists that matter, to stdout.
	if f.urls == "" && f.domains == "" && f.props == "" && f.aliases == "" {
		f.urls, f.domains = "-", "-"
	}

	targets, err := s.TargetsFor(c, item.ID)
	if err != nil {
		return err
	}
	var urls, domains []string
	for _, t := range targets {
		switch t.Kind {
		case store.TargetURL:
			urls = append(urls, t.Value)
		case store.TargetDomain:
			// Written the way import reads it back, so a subdomain target does
			// not quietly narrow to the bare host on the way through. A
			// trailing comment would not do: import strips a line that starts
			// with #, not one that ends with it, so the marker came back as
			// part of the hostname.
			if t.Subdomains {
				domains = append(domains, "*."+t.Value)
				continue
			}
			domains = append(domains, t.Value)
		}
	}
	sort.Strings(urls)
	sort.Strings(domains)

	wrote := 0
	if f.domains != "" {
		n, err := writeLines(a, f.domains, domains, "domains")
		if err != nil {
			return err
		}
		wrote += n
	}
	if f.urls != "" {
		n, err := writeLines(a, f.urls, urls, "urls")
		if err != nil {
			return err
		}
		wrote += n
	}
	if f.aliases != "" {
		var words []string
		for _, al := range item.Aliases {
			words = append(words, al.Word)
		}
		sort.Strings(words)
		n, err := writeLines(a, f.aliases, words, "aliases")
		if err != nil {
			return err
		}
		wrote += n
	}
	if f.props != "" {
		n, err := writeProps(c, a, s, item, f.props)
		if err != nil {
			return err
		}
		wrote += n
	}

	if wrote == 0 {
		a.Printf("%s has nothing to export yet: scour item add %s -d <domain>\n", item.Name, item.Name)
	}
	return nil
}

// writeLines writes one value per line, to a file or to stdout.
func writeLines(a *cli.App, dest string, lines []string, what string) (int, error) {
	if len(lines) == 0 {
		return 0, nil
	}
	if dest == "-" {
		for _, l := range lines {
			a.Printf("%s\n", l)
		}
		return len(lines), nil
	}

	if dir := filepath.Dir(dest); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return 0, fmt.Errorf("create %s: %w", dir, err)
		}
	}
	file, err := os.Create(dest) //nolint:gosec // the path is operator supplied
	if err != nil {
		return 0, fmt.Errorf("write %s: %w", dest, err)
	}
	defer file.Close()

	w := bufio.NewWriter(file)
	for _, l := range lines {
		if _, err := fmt.Fprintln(w, l); err != nil {
			return 0, fmt.Errorf("write %s: %w", dest, err)
		}
	}
	if err := w.Flush(); err != nil {
		return 0, fmt.Errorf("write %s: %w", dest, err)
	}
	a.Printf("%s: %d %s\n", dest, len(lines), what)
	return len(lines), nil
}

// writeProps writes the CSV `scour import --props` reads.
func writeProps(c context.Context, a *cli.App, s *store.Store, item *store.Item, dest string) (int, error) {
	props, err := s.PropertiesFor(c, item.ID, "")
	if err != nil {
		return 0, err
	}
	if len(props) == 0 {
		return 0, nil
	}

	out := os.Stdout
	if dest != "-" {
		if dir := filepath.Dir(dest); dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o750); err != nil {
				return 0, fmt.Errorf("create %s: %w", dir, err)
			}
		}
		file, err := os.Create(dest) //nolint:gosec // the path is operator supplied
		if err != nil {
			return 0, fmt.Errorf("write %s: %w", dest, err)
		}
		defer file.Close()
		out = file
	}

	w := csv.NewWriter(out)
	// The header import expects, so a file written here reads back unchanged.
	rows := [][]string{{"name", "type", "example", "description", "regex"}}
	for _, p := range props {
		rows = append(rows, []string{p.Name, p.Type, p.Example, p.Description, p.Regex})
	}
	if err := w.WriteAll(rows); err != nil {
		return 0, fmt.Errorf("write %s: %w", dest, err)
	}
	if dest != "-" {
		a.Printf("%s: %d properties\n", dest, len(props))
	}
	return len(props), nil
}
