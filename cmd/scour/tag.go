// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/urfave/cli/v3"
)

type tagFlags struct {
	prop   string
	domain string
	append []string
	delete []string
	update []string
}

func newTagCmd(a *app) *cli.Command {
	var f tagFlags

	return &cli.Command{
		Category:  "Defining what to look for",
		Name:      "tag",
		ArgsUsage: "<name>",
		Usage:     "Edit the words a property might be labelled with on a page",
		Description: "`scour add -p author -a byline` only ever adds a word. This edits the set:\n" +
			"--append adds, --delete removes, --update replaces it outright. With none of\n" +
			"them the current words are printed and nothing changes.\n\n" +
			"Each flag carries one word, and repeats for more, because a tag is often a\n" +
			"phrase: \"pickup truck\", \"model year\", \"asking price\". Splitting a single\n" +
			"argument on spaces would eventually cut one of those in half.\n\n" +
			"--on scopes the edit to a domain, so changing what one site calls a byline\n" +
			"leaves every other site alone.",
		UsageText: "  scour tag news -p author\n" +
			"  scour tag news -p author --append byline --append 'written by'\n" +
			"  scour tag news -p author --delete by\n" +
			"  scour tag news -p author --update byline --update author\n" +
			"  scour tag news -p author --on example.com --append 'staff writer'",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:        "prop",
				Aliases:     []string{"p"},
				Usage:       "the property whose words are being edited",
				Destination: &f.prop,
			},
			&cli.StringFlag{
				Name:        "on",
				Usage:       "scope the edit to one domain, matching `scour add --domain`",
				Destination: &f.domain,
			},
			&cli.StringSliceFlag{
				Name:        "append",
				Aliases:     []string{"a"},
				Usage:       "add a word (repeatable)",
				Destination: &f.append,
			},
			&cli.StringSliceFlag{
				Name:        "delete",
				Aliases:     []string{"d"},
				Usage:       "remove a word (repeatable)",
				Destination: &f.delete,
			},
			&cli.StringSliceFlag{
				Name:        "update",
				Aliases:     []string{"u"},
				Usage:       "replace the whole set with these words (repeatable)",
				Destination: &f.update,
			},
		},
		Action: func(c context.Context, cmd *cli.Command) error {
			args, err := need(cmd, 1, "one entity name")
			if err != nil {
				return err
			}
			return runTag(c, a, args[0], f)
		},
	}
}

func runTag(c context.Context, a *app, name string, f tagFlags) error {
	if strings.TrimSpace(f.prop) == "" {
		return errors.New("tag needs --prop to say which property's words to edit")
	}
	// --update means "the set is exactly this", so combining it with the two
	// incremental verbs asks for two different final states at once.
	if len(f.update) > 0 && (len(f.append) > 0 || len(f.delete) > 0) {
		return errors.New("--update replaces the whole set, so it cannot be combined with --append or --delete")
	}

	s, err := a.Store()
	if err != nil {
		return err
	}
	entity, err := s.Entity(c, name)
	if err != nil {
		return err
	}

	scope := ""
	if f.domain != "" {
		if scope, err = normaliseDomain(f.domain); err != nil {
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
		if err := s.SetPropertyAliases(c, entity.ID, scope, f.prop, f.update); err != nil {
			return err
		}

	default:
		for _, word := range f.delete {
			n, err := s.RemovePropertyAliases(c, entity.ID, scope, f.prop, []string{word})
			if err != nil {
				return err
			}
			if n == 0 {
				// Reporting a removal that removed nothing is how someone ends
				// up believing a word is gone while a crawl still matches it.
				a.Printf("%s: %s%s was not tagged %q\n", entity.Name, f.prop, where, word)
				continue
			}
			a.Printf("%s: %s%s no longer reads %q\n", entity.Name, f.prop, where, word)
		}
		for _, word := range f.append {
			if err := s.AddPropertyAlias(c, entity.ID, scope, f.prop, word); err != nil {
				return err
			}
			a.Printf("%s: %s%s also reads %q\n", entity.Name, f.prop, where, word)
		}
	}

	words, err := s.PropertyAliases(c, entity.ID, scope, f.prop)
	if err != nil {
		return err
	}
	if len(f.append) == 0 && len(f.delete) == 0 && len(f.update) == 0 {
		if len(words) == 0 {
			a.Printf("%s: %s%s has no tags yet\n\n", entity.Name, f.prop, where)
			a.Printf("Teach it what a page might call it:\n  scour tag %s -p %s --append byline\n",
				entity.Name, f.prop)
			return nil
		}
		a.Printf("%s: %s%s reads %s\n", entity.Name, f.prop, where, quoteList(words))
		return nil
	}

	a.Printf("\n%s%s now reads %s\n", f.prop, where, quoteList(words))
	a.Printf("run `scour train %s` to fold that into the model\n", entity.Name)
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
