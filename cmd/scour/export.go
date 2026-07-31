// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/rangertaha/scour/internal/export"
	"github.com/rangertaha/scour/internal/store"
)

type exportFlags struct {
	format     string
	to         string
	tokenEnv   string
	confidence float64
	label      string
	limit      int
	stamp      string
}

func newExportCmd(a *app) *cobra.Command {
	var f exportFlags

	cmd := &cobra.Command{
		Use:   "export <name>",
		Short: "Write an entity's extracted records out as CSV, JSON or to a webhook",
		Long: "Records are grouped by the domain they came from, one file per site, so an\n" +
			"export is diffable and a site that changed shows up as a changed file.\n\n" +
			"Files land under <data>/exports/<name>/<domain>/<date>.<ext>, and re-running\n" +
			"on the same day overwrites rather than accumulating.",
		Example: "  scour export vehicle\n" +
			"  scour export vehicle --format json\n" +
			"  scour export vehicle --label valid --confidence 0.8\n" +
			"  scour export vehicle --format webhook --to https://example.com/ingest",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runExport(cmd, a, args[0], f)
		},
	}

	fl := cmd.Flags()
	fl.StringVar(&f.format, "format", "", "output format: "+exportFormats()+" (default "+export.Default+")")
	fl.StringVar(&f.to, "to", "", "directory for files, or url for a webhook")
	fl.StringVar(&f.tokenEnv, "token-env", "", "environment variable holding a bearer token for the webhook")
	fl.Float64Var(&f.confidence, "confidence", 0, "only export records at or above this confidence")
	fl.StringVar(&f.label, "label", "", "only export records with this label: valid, invalid or unlabelled")
	fl.IntVar(&f.limit, "max-records", 0, "cap how many records are exported (0 for no limit)")
	fl.StringVar(&f.stamp, "name", "", "name the export file (default today's date)")

	return cmd
}

func runExport(cmd *cobra.Command, a *app, name string, f exportFlags) error {
	s, err := a.Store()
	if err != nil {
		return err
	}
	c := ctx(cmd)

	entity, err := s.Entity(c, name)
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

	rows, total, err := s.SearchRecords(c, entity.ID, store.RecordQuery{
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
		cmd.Printf("no records to export for %s\n", entity.Name)
		return nil
	}

	stamp := f.stamp
	if stamp == "" {
		stamp = time.Now().UTC().Format("2006-01-02")
	}

	// --to means a directory for file formats and a URL for the webhook, so
	// only one of them may take it.
	dir := a.cfg.ExportsDir()
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

	result, err := exporter.Export(c, entity.Name, rows)
	if result != nil && len(result.Destinations) > 0 {
		// Print what did go out even when the export failed part way, so a
		// retry does not silently duplicate what was already delivered.
		for _, dest := range result.Destinations {
			cmd.Printf("%s\n", dest)
		}
	}
	if err != nil {
		return err
	}

	if a.jsonOut {
		return writeJSON(cmd.OutOrStdout(), result)
	}

	cmd.Printf("\n%d of %d records exported as %s to %d %s\n",
		result.Records, total, exporter.Name(),
		len(result.Destinations), plural(len(result.Destinations), "destination"))
	return nil
}

// plural renders a count's noun, so a summary line reads as a sentence.
func plural(n int, noun string) string {
	if n == 1 {
		return noun
	}
	return noun + "s"
}

// exportFormats is the help text listing what is registered.
func exportFormats() string { return strings.Join(export.Names(), ", ") }
