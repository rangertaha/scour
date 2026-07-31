// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"bufio"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/rangertaha/scour/internal/store"
)

type importFlags struct {
	urls       []string
	domains    []string
	props      []string
	aliases    []string
	subdomains bool
	depth      int
}

func newImportCmd(a *app) *cobra.Command {
	var f importFlags

	cmd := &cobra.Command{
		Use:   "import <name>",
		Short: "Load targets, properties and aliases into an entity from files",
		Long: "The same additions `scour add` makes one at a time, from a file. Every form\n" +
			"is idempotent, so re-importing a file that has grown only adds what is new.\n\n" +
			"Blank lines and lines starting with # are ignored, so a list can carry notes.",
		Example: "  scour import vehicle --urls urls.txt\n" +
			"  scour import vehicle --domains domains.txt --subdomains\n" +
			"  scour import vehicle --props props.csv",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runImport(cmd, a, args[0], f)
		},
	}

	fl := cmd.Flags()
	fl.StringArrayVar(&f.urls, "urls", nil, "file of URLs, one per line (repeatable)")
	fl.StringArrayVar(&f.domains, "domains", nil, "file of domains, one per line (repeatable)")
	fl.StringArrayVar(&f.props, "props", nil, "CSV of properties (repeatable)")
	fl.StringArrayVar(&f.aliases, "aliases", nil, "file of aliases, one per line (repeatable)")
	fl.BoolVar(&f.subdomains, "subdomains", false, "follow subdomains of the imported domains")
	fl.IntVar(&f.depth, "depth", 0, "depth limit for the imported targets (0 for the configured default)")

	return cmd
}

// importResult counts what a file contributed.
type importResult struct {
	added   int
	skipped int
}

func runImport(cmd *cobra.Command, a *app, name string, f importFlags) error {
	if len(f.urls) == 0 && len(f.domains) == 0 && len(f.props) == 0 && len(f.aliases) == 0 {
		return errors.New("nothing to import: pass --urls, --domains, --props or --aliases")
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

	total := importResult{}

	for _, path := range f.urls {
		res, err := importTargets(c, s, entity.ID, path, store.TargetURL, normaliseURL, f)
		if err != nil {
			return err
		}
		report(cmd, path, "urls", res)
		total.add(res)
	}

	for _, path := range f.domains {
		res, err := importTargets(c, s, entity.ID, path, store.TargetDomain, normaliseDomain, f)
		if err != nil {
			return err
		}
		report(cmd, path, "domains", res)
		total.add(res)
	}

	for _, path := range f.aliases {
		res, err := eachLine(path, func(line string) error {
			return s.AddAlias(c, entity.ID, line)
		})
		if err != nil {
			return err
		}
		report(cmd, path, "aliases", res)
		total.add(res)
	}

	for _, path := range f.props {
		res, err := importProps(c, s, entity.ID, path)
		if err != nil {
			return err
		}
		report(cmd, path, "properties", res)
		total.add(res)
	}

	cmd.Printf("\n%s: %d imported", entity.Name, total.added)
	if total.skipped > 0 {
		cmd.Printf(", %d skipped", total.skipped)
	}
	cmd.Println()
	return nil
}

func (r *importResult) add(other importResult) {
	r.added += other.added
	r.skipped += other.skipped
}

func report(cmd *cobra.Command, path, kind string, res importResult) {
	cmd.Printf("%s: %d %s", path, res.added, kind)
	if res.skipped > 0 {
		cmd.Printf(", %d skipped", res.skipped)
	}
	cmd.Println()
}

// importTargets reads a list of targets and writes them in batches.
//
// Batching is not an optimisation here, it is the difference between usable and
// not. Inserting one target per statement measured 284 rows a second on a real
// list of news sites, which is nearly an hour for a million URLs and almost all
// of it spent committing a transaction per row.
func importTargets(
	ctx context.Context,
	s *store.Store,
	entityID uint,
	path string,
	kind store.TargetKind,
	normalise func(string) (string, error),
	f importFlags,
) (importResult, error) {
	var res importResult
	batch := make([]string, 0, store.TargetBatch)

	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		n, err := s.AddTargets(ctx, entityID, kind, batch, f.subdomains, f.depth)
		if err != nil {
			return err
		}
		res.added += n
		batch = batch[:0]
		return nil
	}

	lines, err := eachLine(path, func(line string) error {
		value, err := normalise(line)
		if err != nil {
			return err
		}
		batch = append(batch, value)
		if len(batch) >= store.TargetBatch {
			return flush()
		}
		return nil
	})
	if err != nil {
		return res, err
	}
	if err := flush(); err != nil {
		return res, err
	}

	// eachLine counted the lines it accepted; the batch counted the rows
	// actually written, which is lower when a file repeats itself.
	res.skipped = lines.skipped
	return res, nil
}

// eachLine applies fn to every meaningful line of a file.
//
// A line that cannot be used is counted and reported rather than fatal. These
// files are usually assembled by hand or by another tool, and abandoning a
// thousand good URLs over one typo would be the wrong trade.
func eachLine(path string, fn func(string) error) (importResult, error) {
	f, err := os.Open(path) //nolint:gosec // the path is given on the command line
	if err != nil {
		return importResult{}, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	var res importResult
	sc := bufio.NewScanner(f)
	// A URL can be long, and the default buffer would truncate one silently.
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)

	for line := 1; sc.Scan(); line++ {
		text := strings.TrimSpace(sc.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		if err := fn(text); err != nil {
			fmt.Fprintf(os.Stderr, "%s:%d: %v\n", path, line, err)
			res.skipped++
			continue
		}
		res.added++
	}
	if err := sc.Err(); err != nil {
		return res, fmt.Errorf("read %s: %w", path, err)
	}
	return res, nil
}

// importProps reads properties from a CSV.
//
// The columns are named by a header row when there is one, because a schema
// file is edited by people and remembering positional order is exactly the kind
// of thing that goes wrong quietly. A file with no header is read as
// name,example, which is the shape someone writes by hand.
func importProps(ctx context.Context, s *store.Store, entityID uint, path string) (importResult, error) {
	f, err := os.Open(path) //nolint:gosec // the path is given on the command line
	if err != nil {
		return importResult{}, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	r := csv.NewReader(f)
	r.FieldsPerRecord = -1
	r.TrimLeadingSpace = true

	var res importResult
	var cols map[string]int
	var line int

	for {
		record, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return res, fmt.Errorf("read %s: %w", path, err)
		}
		line++

		if len(record) == 0 || strings.HasPrefix(strings.TrimSpace(record[0]), "#") {
			continue
		}
		if cols == nil {
			if header := headerOf(record); header != nil {
				cols = header
				continue
			}
			// No header, so the positional form.
			cols = map[string]int{"name": 0, "example": 1}
		}

		prop := field(record, cols, "name")
		if prop == "" {
			res.skipped++
			continue
		}

		err = s.AddPropertyDetail(ctx, entityID, "", prop,
			field(record, cols, "type"),
			field(record, cols, "example"),
			field(record, cols, "description"),
			field(record, cols, "regex"))
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s:%d: %v\n", path, line, err)
			res.skipped++
			continue
		}

		// Aliases share one cell, separated by semicolons, because a comma
		// would need quoting in a CSV and a name like "model year" has to
		// survive intact.
		for _, alias := range strings.Split(field(record, cols, "aliases"), ";") {
			if alias = strings.TrimSpace(alias); alias != "" {
				if err := s.AddPropertyAlias(ctx, entityID, "", prop, alias); err != nil {
					fmt.Fprintf(os.Stderr, "%s:%d: %v\n", path, line, err)
				}
			}
		}
		res.added++
	}
	return res, nil
}

// headerOf recognises a header row, returning the column positions it names.
func headerOf(record []string) map[string]int {
	known := map[string]bool{
		"name": true, "type": true, "example": true,
		"description": true, "aliases": true, "regex": true,
	}

	cols := map[string]int{}
	for i, cell := range record {
		name := strings.ToLower(strings.TrimSpace(cell))
		if !known[name] {
			return nil
		}
		cols[name] = i
	}
	// A single column called "name" is a header; anything without one is data.
	if _, ok := cols["name"]; !ok {
		return nil
	}
	return cols
}

func field(record []string, cols map[string]int, name string) string {
	i, ok := cols[name]
	if !ok || i >= len(record) {
		return ""
	}
	return strings.TrimSpace(record[i])
}
