// SPDX-License-Identifier: GPL-3.0-or-later

package record

import (
	"context"
	"fmt"
	"strings"
	"time"

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
