// SPDX-License-Identifier: GPL-3.0-or-later

package record

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	ucli "github.com/urfave/cli/v3"

	"github.com/rangertaha/scour/internal/cli"
	"github.com/rangertaha/scour/internal/store"
)

// Show prints one record in full.
//
// The listing truncates every value to fit a terminal width, which is right for
// scanning and wrong for reading: the field you want is usually the long one.
// This is where a record found by search or ls is actually read, with the page
// it came from, so the next question, "is that what the page says", has a url
// to follow.
func Show(a *cli.App) *ucli.Command {
	return &ucli.Command{
		Name:      "show",
		ArgsUsage: "<name> <id>",
		Usage:     "One record, with the page it came from",
		Description: "Every field in full, untruncated, in the order the item defines them.\n\n" +
			"SOURCE is the page it was read out of. A value that looks wrong is usually\n" +
			"a rule matching the wrong part of that page, so the url is the next place\n" +
			"to look, and `scour model rules` says what was matched.\n\n" +
			"Ids come from `scour record ls` and `scour record search`, and survive\n" +
			"retraining, so one read off a listing still names the same record later.",
		UsageText: "  scour record show vehicle 1042\n" +
			"  scour --json record show vehicle 1042\n\n" +
			"Find one, then read it:\n" +
			"  scour record search vehicle make:Ford\n" +
			"  scour record show vehicle 1042",
		Action: func(c context.Context, cmd *ucli.Command) error {
			args, err := cli.Need(cmd, 2, "an item name and a record id")
			if err != nil {
				return err
			}
			id, err := strconv.ParseUint(args[1], 10, 64)
			if err != nil {
				return cli.Usagef("%q is not a record id: they are numbers, from `scour record ls %s`",
					args[1], args[0])
			}
			return runShow(c, a, args[0], uint(id))
		},
	}
}

func runShow(c context.Context, a *cli.App, name string, id uint) error {
	s, err := a.Store()
	if err != nil {
		return err
	}
	item, err := s.ItemFull(c, name)
	if err != nil {
		return err
	}
	row, err := s.RecordByID(c, item.ID, id)
	if err != nil {
		return err
	}

	if a.JSON {
		return cli.WriteJSON(a.Out(), row)
	}

	line := func(k, v string) { a.Printf("%-11s %s\n", k, v) }
	line("record", strconv.FormatUint(uint64(row.ID), 10))
	line("item", item.Name)
	line("confidence", fmt.Sprintf("%.2f", row.Confidence))
	line("format", row.Format)
	if v := verdictOf(row.Label); v != "" {
		line("verdict", v)
	}
	if row.URL != "" {
		line("source", row.URL)
	} else {
		// A record whose page has gone is worth saying out loud: it is what a
		// reset leaves behind, and it is why the url column can be empty.
		line("source", "the page it came from is no longer on record")
	}
	line("extracted", row.CreatedAt.UTC().Format("2006-01-02 15:04:05Z"))

	// The item's own order first, because that is the order somebody defined
	// and the order every listing uses. Anything a page carried that the item
	// does not define follows, sorted, rather than being dropped.
	a.Println("")
	defined := map[string]bool{}
	for _, p := range item.Properties {
		defined[p.Name] = true
		if v := row.Values[p.Name]; v != "" {
			line(p.Name, v)
		} else {
			line(p.Name, "-")
		}
	}
	var extra []string
	for prop := range row.Values {
		if !defined[prop] {
			extra = append(extra, prop)
		}
	}
	sort.Strings(extra)
	for _, prop := range extra {
		line(prop, row.Values[prop])
	}
	if len(extra) > 0 {
		a.Printf("\n%d field(s) the item does not define: %s\n",
			len(extra), strings.Join(extra, ", "))
	}
	return nil
}

// verdictOf is the word a stored label prints as, empty when nothing was
// judged. Unlabelled is the ordinary state and saying "none" for it would put
// a line on every record that carries no information.
func verdictOf(l store.Label) string {
	switch l {
	case store.Valid:
		return string(store.Valid)
	case store.Invalid:
		return string(store.Invalid)
	default:
		return ""
	}
}
