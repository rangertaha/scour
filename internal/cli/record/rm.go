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

// Remove drops records.
//
// Named ids or a query, and a query needs --force, because a mistyped term is
// the difference between removing four rows and removing four thousand and
// there is no undo. The pages stay: a record is what was read out of a page,
// and a bad reading is not a reason to refetch the site. `scour model train`
// reads the same page again.
func Remove(a *cli.App) *ucli.Command {
	var (
		force bool
		all   bool
		f     streamFlags
	)

	cmd := &ucli.Command{
		Name:      "rm",
		Aliases:   []string{"remove", "delete"},
		ArgsUsage: "<name> <id>... | <name> <query>...",
		Usage:     "Drop records, by id or by what matches",
		Description: "Ids come from `scour record ls` and `scour record search`.\n\n" +
			"Given a query instead, this removes everything that matches, which needs\n" +
			"--force: the same query typed into `scour record search` first will show\n" +
			"you exactly what is about to go.\n\n" +
			"The cached pages are kept, so the next `scour model train` extracts from\n" +
			"them again. Removing records is how you clear out a bad extraction run\n" +
			"without paying to crawl the site a second time.",
		UsageText: "  scour record rm vehicle 1042 1043\n\n" +
			"Everything a query matches, checked first:\n" +
			"  scour record search vehicle make:Ford\n" +
			"  scour record rm vehicle make:Ford --force\n\n" +
			"Everything one job's pages produced, or everything at once:\n" +
			"  scour record rm vehicle -j uk --force\n" +
			"  scour record rm vehicle --all --force",
		Flags: append([]ucli.Flag{
			&ucli.BoolFlag{
				Name:        "force",
				Usage:       "confirm removing everything a query matches",
				Destination: &force,
			},
			&ucli.BoolFlag{
				Name:        "all",
				Usage:       "every record of the item, which no query can ask for by accident",
				Destination: &all,
			},
		}, filterFlags(&f)...),
		Action: func(c context.Context, cmd *ucli.Command) error {
			args, err := cli.AtLeast(cmd, 1, "an item name")
			if err != nil {
				return err
			}
			return runRemove(c, a, args[0], args[1:], force, all, f)
		},
	}
	return cmd
}

func runRemove(c context.Context, a *cli.App, name string, rest []string, force, all bool, f streamFlags) error {
	s, err := a.Store()
	if err != nil {
		return err
	}
	item, err := s.ItemFull(c, name)
	if err != nil {
		return err
	}

	// All numbers means named records, which is the exact case and needs no
	// confirming. Anything else is a query, which does.
	if ids, ok := recordIDs(rest); ok && len(ids) > 0 {
		return removeByID(c, a, s, item, ids)
	}

	q, err := query.Parse(rest, propNames(item))
	if err != nil {
		return fmt.Errorf("%s: %w", strings.Join(rest, " "), err)
	}
	label, err := parseLabel(f.label)
	if err != nil {
		return err
	}
	jobID, err := jobFilter(c, s, f.job)
	if err != nil {
		return err
	}
	rq := store.RecordQuery{
		Terms:         q.Terms,
		JobID:         jobID,
		MinConfidence: f.confidence,
		Formats:       f.types,
		ExcludeFormat: f.excludeType,
		Label:         label,
	}
	// Removing every record of an item on a bare `record rm <item>` would be a
	// destructive default reachable by typing the item name and pressing
	// return, so clearing them all is asked for in words. --all rather than an
	// empty query, because an empty query is what a shell expanding a variable
	// to nothing also produces.
	narrowed := !q.Empty() || rq.MinConfidence > 0 || len(rq.Formats) > 0 ||
		len(rq.ExcludeFormat) > 0 || rq.Label != "" || rq.JobID > 0
	if !narrowed && !all {
		return cli.Usagef("record rm needs ids, a query, or --all\n"+
			"  some:  scour record rm %s 1042 1043\n"+
			"  match: scour record rm %s make:Ford --force\n"+
			"  all:   scour record rm %s --all --force",
			name, name, name)
	}
	if narrowed && all {
		return cli.Usagef("--all removes every record, so it takes no query or filter")
	}

	rows, total, err := s.SearchRecords(c, item.ID, rq)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return a.Empty("nothing matched, so nothing was removed\n")
	}
	if !force {
		a.Printf("this removes %d of %s's %d records\n", total, item.Name, countAll(c, s, item.ID))
		// Whatever narrowed the set is what should be echoed back, or the
		// suggestion to look first shows a different set than the one about to
		// go, which is worse than no suggestion at all.
		switch {
		case all:
			a.Println("the pages and the model are kept, so `scour model train` fills them back in")
		case q.Empty():
			a.Printf("see them first: scour record ls %s%s\n", name, filterArgs(f))
		default:
			a.Printf("see them first: scour record search %s %s%s\n", name, q, filterArgs(f))
		}
		a.Println("re-run with --force to confirm")
		return cli.ErrSilent
	}

	ids := make([]uint, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	return removeByID(c, a, s, item, ids)
}

func removeByID(c context.Context, a *cli.App, s *store.Store, item *store.Item, ids []uint) error {
	n, err := s.DeleteRecords(c, item.ID, ids)
	if err != nil {
		return err
	}
	if n == 0 {
		return a.Empty("no such records in %s\n", item.Name)
	}
	// Reported rather than assumed: an id from another item's listing is a
	// miss, and silently removing fewer than asked for reads as success.
	if int(n) < len(ids) {
		a.Printf("removed %d of %d, the rest are not %s's records\n", n, len(ids), item.Name)
	} else {
		a.Printf("removed %d record(s) from %s\n", n, item.Name)
	}
	a.Println("the pages are kept: scour model train " + item.Name + " reads them again")
	return nil
}

// recordIDs reads the arguments as record ids, reporting whether they all were.
// A single non-number makes the whole list a query, since mixing the two would
// mean guessing which half was meant.
func recordIDs(args []string) ([]uint, bool) {
	if len(args) == 0 {
		return nil, false
	}
	out := make([]uint, 0, len(args))
	for _, arg := range args {
		n, err := strconv.ParseUint(arg, 10, 64)
		if err != nil {
			return nil, false
		}
		out = append(out, uint(n))
	}
	return out, true
}

// countAll is how many records the item has, for saying what a removal is a
// fraction of. A failure here is not worth failing the removal over.
func countAll(c context.Context, s *store.Store, itemID uint) int64 {
	_, total, err := s.SearchRecords(c, itemID, store.RecordQuery{})
	if err != nil {
		return 0
	}
	return total
}

// filterArgs renders the narrowing flags back as they were typed, so the
// suggestion to look at a set before removing it names that exact set.
func filterArgs(f streamFlags) string {
	var b strings.Builder
	if f.job != "" {
		fmt.Fprintf(&b, " -j %s", f.job)
	}
	if f.confidence > 0 {
		fmt.Fprintf(&b, " --confidence %v", f.confidence)
	}
	for _, t := range f.types {
		fmt.Fprintf(&b, " -t %s", t)
	}
	for _, t := range f.excludeType {
		fmt.Fprintf(&b, " --exclude-type %s", t)
	}
	if f.label != "" {
		fmt.Fprintf(&b, " --verdict %s", f.label)
	}
	return b.String()
}
