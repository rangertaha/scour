// SPDX-License-Identifier: GPL-3.0-or-later

package learn

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	ucli "github.com/urfave/cli/v3"

	"github.com/rangertaha/scour/internal/cli"
	"github.com/rangertaha/scour/internal/store"
)

// Mark records a person's verdict on extracted records.
//
// It is called mark rather than label because a label is a tag: the words a
// page might use to name a property, which is what `scour item tag` edits. This
// is the other thing entirely, a judgement about a record that was already
// extracted, and giving the two one word made it impossible to say which was
// meant.
//
// One command with three flags rather than three commands. The verdicts are
// mutually exclusive and the record ids are the same argument in every case, so
// three verbs would differ only in a word.
func Mark(a *cli.App) *ucli.Command {
	var valid, invalid, clear bool

	return &ucli.Command{
		Category:  "TRAIN",
		Name:      "mark",
		ArgsUsage: "<name> <id>...",
		Usage:     "Mark extracted records right or wrong",
		Description: "A mark is what training learns from after the first pass.\n\n" +
			"--invalid holds a record out of the next training run, so the model stops\n" +
			"making that mistake. --valid does more than confirm: at least one marked\n" +
			"record is what tells `scour train` to fit the field order chain at all, so\n" +
			"a corpus with none keeps its induced locators and trains nothing else.\n\n" +
			"Ids come from `scour stream`, and survive retraining, so an id read off one\n" +
			"listing still names the same record on the next.",
		UsageText: "  scour stream news                 # find the ids\n" +
			"  scour mark news 1042 1043 --valid\n" +
			"  scour mark news 1088 --invalid\n" +
			"  scour mark news 1088 --clear      # undo a verdict given in error\n" +
			"  scour train news                  # fold the marks into the model",
		Flags: []ucli.Flag{
			&ucli.BoolFlag{
				Name:        "valid",
				Usage:       "the record is right, and is evidence of where the fields live",
				Destination: &valid,
			},
			&ucli.BoolFlag{
				Name:        "invalid",
				Usage:       "the record is wrong, and is held out of training",
				Destination: &invalid,
			},
			&ucli.BoolFlag{
				Name:        "clear",
				Usage:       "remove a verdict, putting the record back to unmarked",
				Destination: &clear,
			},
		},
		Action: func(c context.Context, cmd *ucli.Command) error {
			args, err := cli.AtLeast(cmd, 2, "an item name and at least one record id")
			if err != nil {
				return err
			}
			label, err := verdict(valid, invalid, clear)
			if err != nil {
				return err
			}
			return runMark(c, a, args[0], args[1:], label)
		},
	}
}

// verdict resolves the three flags to one label.
func verdict(valid, invalid, clear bool) (store.Label, error) {
	n := 0
	for _, set := range []bool{valid, invalid, clear} {
		if set {
			n++
		}
	}
	switch {
	case n == 0:
		return "", errors.New("mark needs one of --valid, --invalid or --clear")
	case n > 1:
		// A record holds one verdict, so two of them is not a combination but
		// a question about which was meant.
		return "", errors.New("--valid, --invalid and --clear are alternatives, not a combination")
	case valid:
		return store.Valid, nil
	case invalid:
		return store.Invalid, nil
	default:
		return store.Unlabelled, nil
	}
}

func runMark(c context.Context, a *cli.App, name string, rawIDs []string, label store.Label) error {
	s, err := a.Store()
	if err != nil {
		return err
	}
	item, err := s.Item(c, name)
	if err != nil {
		return err
	}

	ids := make([]uint, 0, len(rawIDs))
	for _, raw := range rawIDs {
		id, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			return fmt.Errorf("%q is not a record id: %w", raw, err)
		}
		ids = append(ids, uint(id))
	}

	n, err := s.MarkRecords(c, item.ID, ids, label)
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("no records matched: %w", store.ErrNotFound)
	}

	what := string(label)
	if label == store.Unlabelled {
		what = "unmarked"
	}
	if int(n) < len(ids) {
		// Saying "marked 3" when 5 ids were given leaves someone believing two
		// verdicts were recorded that were not.
		a.Printf("%s: marked %d of %d %s, the rest are not %s's records\n",
			item.Name, n, len(ids), what, item.Name)
	} else {
		a.Printf("%s: %d records marked %s\n", item.Name, n, what)
	}

	if label == store.Unlabelled {
		return nil
	}
	a.Printf("fold it into the model: scour train %s\n", item.Name)
	if label == store.Valid {
		return nil
	}

	// An invalid mark alone trains nothing new: the chain is fitted only when
	// something has been confirmed, so a corpus marked wrong and never right
	// keeps the locators it induced and learns no field order.
	confirmed, err := s.MarkedCount(c, item.ID, store.Valid)
	if err != nil || confirmed > 0 {
		return err //nolint:nilerr // a count that failed is not worth an error here
	}
	a.Printf("\nnothing is marked valid yet, and the field order chain is only fitted\n")
	a.Printf("once something is: scour mark %s <id> --valid\n", item.Name)
	return nil
}
