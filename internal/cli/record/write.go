// SPDX-License-Identifier: GPL-3.0-or-later

package record

import (
	"context"
	"fmt"
	"strings"
	"time"

	ucli "github.com/urfave/cli/v3"

	"github.com/rangertaha/scour/internal/cli"
	"github.com/rangertaha/scour/internal/query"

	"github.com/rangertaha/scour/internal/export"
	"github.com/rangertaha/scour/internal/store"
)

// exportFlags are the destination flags stream carries, so records can be
// written somewhere as well as printed.
type exportFlags struct {
	format     string
	to         string
	tokenEnv   string
	confidence float64
	label      string
	limit      int
	// types and excludeType narrow to content types, so a write can be pinned
	// the same way a listing can.
	types       []string
	excludeType []string

	// terms is the search a listing was narrowed by, so exporting what is on
	// screen does not mean restating it as filter flags.
	terms []string
}

func runExport(c context.Context, a *cli.App, name string, f exportFlags) error {
	s, err := a.Store()
	if err != nil {
		return err
	}

	item, err := s.ItemFull(c, name)
	if err != nil {
		return err
	}

	q, err := query.Parse(f.terms, propNames(item))
	if err != nil {
		return err
	}

	if f.label != "" {
		switch store.Label(f.label) {
		case store.Valid, store.Invalid, store.Unlabelled:
		default:
			return fmt.Errorf("label must be valid, invalid or unlabelled, got %q", f.label)
		}
	}

	rows, total, err := s.SearchRecords(c, item.ID, store.RecordQuery{
		Terms:         q.Terms,
		MinConfidence: f.confidence,
		Formats:       f.types,
		ExcludeFormat: f.excludeType,
		Label:         store.Label(f.label),
		Limit:         f.limit,
	})
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		// Not an error: a filter that matches nothing is a legitimate answer,
		// and writing an empty file would be worse than saying so.
		a.Printf("no records to export for %s\n", item.Name)
		return nil
	}

	// One file per day, so re-running overwrites rather than accumulating.
	stamp := time.Now().UTC().Format("2006-01-02")

	// --to means a directory for file formats and a URL for the webhook, so
	// only one of them may take it.
	dir := a.Cfg.ExportsDir()
	if f.format != "webhook" && f.to != "" {
		dir = f.to
	}
	if f.format == "webhook" && f.to == "" {
		return fmt.Errorf("--format webhook needs --to <url>")
	}

	exporter, err := export.New(f.format, export.Config{
		Dir:       dir,
		URL:       f.to,
		TokenEnv:  f.tokenEnv,
		Timestamp: stamp,
	})
	if err != nil {
		return err
	}

	result, err := exporter.Export(c, item.Name, rows)
	if result != nil && len(result.Destinations) > 0 {
		// Print what did go out even when the export failed part way, so a
		// retry does not silently duplicate what was already delivered.
		for _, dest := range result.Destinations {
			a.Printf("%s\n", dest)
		}
	}
	if err != nil {
		return err
	}

	if a.JSON {
		return cli.WriteJSON(a.Out(), result)
	}

	a.Printf("\n%d of %d records exported as %s to %d %s\n",
		result.Records, total, exporter.Name(),
		len(result.Destinations), cli.Plural(len(result.Destinations), "destination"))
	return nil
}

// exportFormats is the help text listing what is registered.
func exportFormats() string { return strings.Join(export.Names(), ", ") }

// Write takes records out of scour.
//
// A command as well as a flag on the listing. `record ls --write csv` is how
// you export what you were already looking at; this is how a pipeline exports
// without pretending to be a person reading a table, and it is the form worth
// putting in a cron entry.
//
// It takes the same query as `record search`, so a search that found the right
// rows on screen exports exactly those rows rather than being restated as a set
// of filter flags.
func Write(a *cli.App) *ucli.Command {
	var f streamFlags

	return &ucli.Command{
		Name:      "write",
		Aliases:   []string{"export"},
		ArgsUsage: "<name> [query]...",
		Usage:     "Write records out, as csv, json, jsonl or a webhook",
		Description: "Records are the product, so they belong wherever the rest of your pipeline\n" +
			"reads. Written out they are grouped by the domain they came from, one file\n" +
			"per site, so an export is diffable and a site that changed is a changed file.\n\n" +
			"The columns are the union of every record's fields rather than the first\n" +
			"record's, so a field only some pages carry still gets a column, and the\n" +
			"verdict travels with the record, because an export is also how records get\n" +
			"corrected outside scour.\n\n" +
			"One file per day, so re-running overwrites rather than accumulating.",
		UsageText: "  scour record write vehicle --format csv --to ./out\n" +
			"  scour record write vehicle --format jsonl --to ./out\n\n" +
			"Only what matches, the same query record search takes:\n" +
			"  scour record write vehicle make:Ford --format csv --to ./out\n\n" +
			"Post them somewhere instead:\n" +
			"  scour record write vehicle --format webhook --to https://example.com/ingest",
		Flags: append(append(filterFlags(&f),
			&ucli.StringFlag{
				Name:        "format",
				Usage:       "`format` to write: " + exportFormats(),
				Destination: &f.out.format,
			},
		), destinationFlags(&f)...),
		Action: func(c context.Context, cmd *ucli.Command) error {
			args, err := cli.AtLeast(cmd, 1, "an item name")
			if err != nil {
				return err
			}
			if f.out.format == "" {
				return cli.Usagef("record write needs --format: %s", exportFormats())
			}
			f.out.confidence, f.out.label, f.out.limit = f.confidence, f.label, a.Limit
			f.out.types, f.out.excludeType = f.types, f.excludeType
			f.out.terms = args[1:]
			return runExport(c, a, args[0], f.out)
		},
	}
}
