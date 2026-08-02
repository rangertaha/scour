// SPDX-License-Identifier: GPL-3.0-or-later

package item

import (
	"context"
	"errors"
	"fmt"
	"strings"

	ucli "github.com/urfave/cli/v3"

	"github.com/rangertaha/scour/internal/cli"
	"github.com/rangertaha/scour/internal/normalise"
)

type tagFlags struct {
	prop   string
	domain string
	append []string
	delete []string
	update []string
}

func Tag(a *cli.App) *ucli.Command {
	var f tagFlags

	return &ucli.Command{
		Name:      "tag",
		ArgsUsage: "<name>",
		Usage:     "Edit the words a property might be labelled with on a page",
		Description: "`scour item add -p author -a byline` only ever adds a word. This edits the set:\n" +
			"--add adds, --rm removes, --set declares it outright. With none of\n" +
			"them the current words are printed and nothing changes.\n\n" +
			"Each flag carries one word, and repeats for more, because a tag is often a\n" +
			"phrase: \"pickup truck\", \"model year\", \"asking price\". Splitting a single\n" +
			"argument on spaces would eventually cut one of those in half.\n\n" +
			"--on scopes the edit to a domain, so changing what one site calls a byline\n" +
			"leaves every other site alone.",
		UsageText: "  scour item tag news -p author\n" +
			"  scour item tag news -p author --add byline --add 'written by'\n" +
			"  scour item tag news -p author --rm by\n" +
			"  scour item tag news -p author --set byline --set author\n" +
			"  scour item tag news -p author --on example.com --add 'staff writer'",
		Flags: []ucli.Flag{
			&ucli.StringFlag{
				Name:        "prop",
				Aliases:     []string{"p"},
				Usage:       "the property whose words are being edited",
				Destination: &f.prop,
			},
			&ucli.StringFlag{
				Name:        "on",
				Usage:       "scope the edit to one `domain`, the same one scour item add takes",
				Destination: &f.domain,
			},
			&ucli.StringSliceFlag{
				Name:        "add",
				Aliases:     []string{"append"},
				Usage:       "add a word to the set (repeatable)",
				Destination: &f.append,
			},
			&ucli.StringSliceFlag{
				Name:        "rm",
				Aliases:     []string{"delete"},
				Usage:       "remove a word from the set (repeatable)",
				Destination: &f.delete,
			},
			&ucli.StringSliceFlag{
				Name:        "set",
				Aliases:     []string{"update"},
				Usage:       "declare the whole set, these words and no others (repeatable)",
				Destination: &f.update,
			},
		},
		Action: func(c context.Context, cmd *ucli.Command) error {
			args, err := cli.Need(cmd, 1, "one item name")
			if err != nil {
				return err
			}
			return runTag(c, a, args[0], f)
		},
	}
}

func runTag(c context.Context, a *cli.App, name string, f tagFlags) error {
	if strings.TrimSpace(f.prop) == "" {
		return errors.New("tag needs --prop to say which property's words to edit")
	}
	// --set means "the set is exactly this", so combining it with the two
	// incremental verbs asks for two different final states at once.
	if len(f.update) > 0 && (len(f.append) > 0 || len(f.delete) > 0) {
		return errors.New("--set declares the whole set, so it cannot be combined with --add or --rm")
	}

	s, err := a.Store()
	if err != nil {
		return err
	}
	item, err := s.Item(c, name)
	if err != nil {
		return err
	}

	scope := ""
	if f.domain != "" {
		if scope, err = normalise.Domain(f.domain); err != nil {
			return err
		}
	}
	where := ""
	if scope != "" {
		where = " on " + scope
	}

	switch {
	case len(f.update) > 0:
		// No per-word line here: replacing the set is one act, and the summary
		// below already says what it ended up as.
		if err := s.SetPropertyAliases(c, item.ID, scope, f.prop, f.update); err != nil {
			return err
		}

	default:
		for _, word := range f.delete {
			n, err := s.RemovePropertyAliases(c, item.ID, scope, f.prop, []string{word})
			if err != nil {
				return err
			}
			if n == 0 {
				// Reporting a removal that removed nothing is how someone ends
				// up believing a word is gone while a crawl still matches it.
				a.Printf("%s: %s%s was not tagged %q\n", item.Name, f.prop, where, word)
				continue
			}
			a.Printf("%s: %s%s no longer reads %q\n", item.Name, f.prop, where, word)
		}
		for _, word := range f.append {
			if err := s.AddPropertyAlias(c, item.ID, scope, f.prop, word); err != nil {
				return err
			}
			a.Printf("%s: %s%s also reads %q\n", item.Name, f.prop, where, word)
		}
	}

	words, err := s.PropertyAliases(c, item.ID, scope, f.prop)
	if err != nil {
		return err
	}
	if len(f.append) == 0 && len(f.delete) == 0 && len(f.update) == 0 {
		if len(words) == 0 {
			a.Printf("%s: %s%s has no tags yet\n\n", item.Name, f.prop, where)
			a.Printf("Teach it what a page might call it:\n  scour item tag %s -p %s --add '<word>'\n",
				item.Name, f.prop)
			return nil
		}
		a.Printf("%s: %s%s reads %s\n", item.Name, f.prop, where, quoteList(words))
		return nil
	}

	a.Printf("\n%s%s now reads %s\n", f.prop, where, quoteList(words))
	a.Printf("run `scour model train %s` to fold that into the model\n", item.Name)
	return nil
}

// quoteList renders words so a phrase is visibly one word.
func quoteList(words []string) string {
	if len(words) == 0 {
		return "nothing"
	}
	out := make([]string, 0, len(words))
	for _, w := range words {
		out = append(out, fmt.Sprintf("%q", w))
	}
	return strings.Join(out, ", ")
}
