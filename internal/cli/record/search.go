// SPDX-License-Identifier: GPL-3.0-or-later

package record

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	ucli "github.com/urfave/cli/v3"

	"github.com/rangertaha/scour/internal/cli"
	"github.com/rangertaha/scour/internal/query"
	"github.com/rangertaha/scour/internal/store"
)

// Search finds records by what is in them.
//
// Separate from ls, and not a flag on it, because the two answer different
// questions and order their answers differently. ls enumerates: no query,
// newest or best first, "what has this crawl produced". search requires a
// query, orders by how well each record answers it, and says which field
// matched. One is for watching a crawl fill up, the other for finding the row
// you came for.
func Search(a *cli.App) *ucli.Command {
	var f streamFlags

	return &ucli.Command{
		Name:      "search",
		Aliases:   []string{"find"},
		ArgsUsage: "<name> <query>...",
		Usage:     "Records matching a query, best first",
		Description: "A bare word matches any field of the record or its url. field:value matches\n" +
			"that one field, where a field is a property you defined, or url. Several\n" +
			"terms narrow, and quotes hold a phrase together.\n\n" +
			"Results are ordered by how well each record answers the query rather than\n" +
			"by confidence: a record whose make is Ford comes above one that merely\n" +
			"mentions Ford. MATCH says which field it was found in.\n\n" +
			"Every record ls filter works here too, so a search can be pinned to one\n" +
			"content type or one confidence band.",
		UsageText: "  scour record search vehicle 'f-150'\n" +
			"  scour record search vehicle make:Ford year:2026\n" +
			"  scour record search vehicle make:Ford 'crew cab' --confidence 0.8\n" +
			"  scour record search vehicle url:example.com\n\n" +
			"Watch a running crawl for the thing you actually wanted:\n" +
			"  scour record search vehicle make:Ford --follow",
		Flags: searchFlags(&f),
		Action: func(c context.Context, cmd *ucli.Command) error {
			args, err := cli.Need(cmd, 2, "an item name and something to search for")
			if err != nil {
				return err
			}
			return runSearch(c, a, args[0], args[1:], f)
		},
	}
}

func runSearch(c context.Context, a *cli.App, name string, terms []string, f streamFlags) error {
	if f.confidence < 0 || f.confidence > 1 {
		return fmt.Errorf("--confidence must be between 0 and 1, got %v", f.confidence)
	}
	label, err := parseLabel(f.label)
	if err != nil {
		return err
	}

	s, err := a.Store()
	if err != nil {
		return err
	}
	item, err := s.ItemFull(c, name)
	if err != nil {
		return err
	}

	// Parsed against the item's own properties, because whether a colon
	// separates a field from a value depends on which fields exist.
	q, err := query.Parse(terms, propNames(item))
	if err != nil {
		return fmt.Errorf("%s: %w", strings.Join(terms, " "), err)
	}
	if q.Empty() {
		return cli.Usagef("search needs something to look for: scour record search %s <query>", name)
	}

	rq := store.RecordQuery{
		Terms:         q.Terms,
		MinConfidence: f.confidence,
		Formats:       f.types,
		ExcludeFormat: f.excludeType,
		Label:         label,
		Limit:         a.Limit,
	}

	// A search that found the right rows on screen should export exactly those
	// rows, rather than being rewritten as a set of filter flags.
	if f.out.format != "" {
		if f.follow {
			return fmt.Errorf("--write and --follow are different jobs: one ends, the other does not")
		}
		f.out.confidence, f.out.label, f.out.limit = f.confidence, f.label, a.Limit
		f.out.terms = terms
		return runExport(c, a, name, f.out)
	}

	rows, total, err := s.SearchRecords(c, item.ID, rq)
	if err != nil {
		return err
	}

	if a.JSON {
		return cli.WriteJSON(a.Out(), rows)
	}
	if len(rows) == 0 && !f.follow {
		// The count is of what matched, so it says nothing about whether the
		// item has records at all. Ask again without the query rather than
		// suggesting a model that is already trained.
		_, all, err := s.SearchRecords(c, item.ID, store.RecordQuery{})
		if err != nil {
			return err
		}
		if all == 0 {
			return a.Empty("no records yet: scour model train %s\n", item.Name)
		}
		return a.Empty("nothing matched %s, out of %d records\n", q, all)
	}

	props := propOrder(item, rows)
	if len(rows) == 0 && f.follow {
		a.Printf("waiting for records from %s matching %s\n", item.Name, q)
		return follow(c, a, s, item, rq, props, 0)
	}

	headers := append([]string{"ID", "CONF", "MATCH"}, upper(props)...)
	aligns := []cli.Align{cli.AlignRight, cli.AlignRight, cli.AlignLeft}
	for range props {
		aligns = append(aligns, cli.AlignLeft)
	}
	t := cli.NewTable(headers, aligns...)
	for _, r := range rows {
		cells := []string{
			strconv.FormatUint(uint64(r.ID), 10),
			fmt.Sprintf("%.2f", r.Confidence),
			strings.Join(r.Matched, ","),
		}
		for _, p := range props {
			cells = append(cells, cli.Truncate(r.Values[p], 24))
		}
		t.Add(cells...)
	}
	if err := t.Render(a.Out()); err != nil {
		return err
	}
	a.Printf("\nshowing %d of %d records matching %s\n", len(rows), total, q)

	if f.follow {
		var mark uint
		for _, r := range rows {
			if r.ID > mark {
				mark = r.ID
			}
		}
		return follow(c, a, s, item, rq, props, mark)
	}
	return nil
}

// propNames is the fields a query may name, which is what the item defines.
// The url is added by the parser, since it belongs to every item.
func propNames(item *store.Item) []string {
	out := make([]string, 0, len(item.Properties))
	for _, p := range item.Properties {
		out = append(out, p.Name)
	}
	return out
}
