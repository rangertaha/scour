// SPDX-License-Identifier: GPL-3.0-or-later

package job

import (
	"context"
	"errors"
	"fmt"
	"strings"

	ucli "github.com/urfave/cli/v3"

	"github.com/rangertaha/scour/internal/cli"
	"github.com/rangertaha/scour/internal/store"
)

type addFlags struct {
	file       string
	item       string
	domains    []string
	urls       []string
	types      []string
	subdomains bool
}

// Add creates a job, or adds a target to one.
//
// One rule covers both, and it is the same rule `item add` follows: given only
// a name it acts on the noun, given a member flag it acts on that member. That
// is why there is no `create` alongside it, which would have made one act carry
// two names depending on which noun it acted on.
func Add(a *cli.App) *ucli.Command {
	var f addFlags

	return &ucli.Command{
		Name:      "add",
		ArgsUsage: "<name>",
		Usage:     "Add a job, or a target to one",
		Description: "A job is one item, a set of targets, and a policy. Naming an item creates\n" +
			"the job; naming a target adds it to one that exists.\n\n" +
			"You do not have to start here. A first crawl creates a job named after the\n" +
			"item, so `scour run vehicle -d example.com` is the whole of it. This is for\n" +
			"the second job: when one item needs another set of sites, or the same sites\n" +
			"under a different policy.\n\n" +
			"Targets accumulate. To replace a bound rather than add to a set, see\n" +
			"`scour job set`.",
		UsageText: "  scour job add uk -i vehicle\n" +
			"  scour job add uk -d example.co.uk --subdomains\n" +
			"  scour job add uk -u https://www.example.co.uk/cars/\n" +
			"  scour job add uk -t html -t pdf\n" +
			"  scour job add -f uk.toml",
		Flags: []ucli.Flag{
			&ucli.StringFlag{
				Name:        "file",
				Aliases:     []string{"f"},
				Usage:       "apply a job config `file` instead of flags",
				Destination: &f.file,
			},
			&ucli.StringFlag{
				Name:        "item",
				Aliases:     []string{"i"},
				Usage:       "the `item` this job hunts for, required when creating one",
				Destination: &f.item,
			},
			&ucli.StringSliceFlag{
				Name:        "domain",
				Aliases:     []string{"d"},
				Usage:       "add a whole domain as a target (repeatable)",
				Destination: &f.domains,
			},
			&ucli.StringSliceFlag{
				Name:        "url",
				Aliases:     []string{"u"},
				Usage:       "add a single URL as a target (repeatable)",
				Destination: &f.urls,
			},
			&ucli.StringSliceFlag{
				Name:        "type",
				Aliases:     []string{"t"},
				Usage:       "allow a content type (repeatable)",
				Destination: &f.types,
			},
			&ucli.BoolFlag{
				Name:        "subdomains",
				Usage:       "follow subdomains of the added domains",
				Destination: &f.subdomains,
			},
		},
		Action: func(c context.Context, cmd *ucli.Command) error {
			if f.file != "" {
				// The file carries the name, so a positional one would be a
				// second answer to a question already answered.
				if cmd.Args().Len() > 0 {
					return fmt.Errorf("with -f the name comes from the file, so %q is a second one", cmd.Args().First())
				}
				return runAddFile(c, a, f.file)
			}
			args, err := cli.Need(cmd, 1, "one job name")
			if err != nil {
				return err
			}
			return runAdd(c, a, args[0], f)
		},
	}
}

func runAdd(c context.Context, a *cli.App, name string, f addFlags) error {
	s, err := a.Store()
	if err != nil {
		return err
	}

	job, err := s.Job(c, name)
	switch {
	case err == nil:
		// It exists, so --item would be saying which item it hunts for, which
		// is already decided. Moving a job between items is not an edit, it is
		// a different job.
		if f.item != "" {
			return fmt.Errorf("job %q already hunts for an item; --item is only for creating one", name)
		}
	case errors.Is(err, store.ErrNotFound):
		if f.item == "" {
			return fmt.Errorf("job %q does not exist: name the item it hunts for with -i", name)
		}
		item, err := s.Item(c, f.item)
		if err != nil {
			return err
		}
		if job, err = s.CreateJob(c, name, item.ID); err != nil {
			return err
		}
		a.Printf("%s: job for %s\n", name, item.Name)
	default:
		return err
	}

	var changes []string
	for _, d := range f.domains {
		host, err := cli.NormaliseDomain(d)
		if err != nil {
			return err
		}
		if err := s.AddTarget(c, job.ID, store.TargetDomain, host, f.subdomains, 0); err != nil {
			return err
		}
		changes = append(changes, "domain "+host)
	}
	for _, u := range f.urls {
		normalised, err := cli.NormaliseURL(u)
		if err != nil {
			return err
		}
		if err := s.AddTarget(c, job.ID, store.TargetURL, normalised, f.subdomains, 0); err != nil {
			return err
		}
		changes = append(changes, "url "+normalised)
	}
	for _, t := range f.types {
		if err := s.AddContentType(c, job.ID, strings.ToLower(t)); err != nil {
			return err
		}
		changes = append(changes, "type "+t)
	}

	for _, change := range changes {
		a.Printf("%s: %s\n", name, change)
	}
	if len(changes) == 0 && f.item != "" {
		// The job exists and can do nothing: naming it and stopping would tell
		// someone their command worked when it left them where they were.
		a.Printf("\nsay where to look, then run it:\n")
		a.Printf("  scour job add %s -d <domain>\n  scour run %s\n", name, name)
	}
	return nil
}

// runAddFile applies a job config.
//
// It is the same act as runAdd and goes through the same store calls, so a job
// built from a file and one built from flags cannot end up different. What the
// file adds is the bounds, which flags put under `job set`: a config describes
// a whole job, and splitting it across two commands to apply one would make the
// round trip lossy.
func runAddFile(c context.Context, a *cli.App, path string) error {
	f, err := parseFile(path)
	if err != nil {
		return err
	}
	if problems := f.validate(); len(problems) > 0 {
		for _, p := range problems {
			a.Errorf("  %s\n", p)
		}
		return fmt.Errorf("%s: %d %s", path, len(problems), plural("problem", len(problems)))
	}

	s, err := a.Store()
	if err != nil {
		return err
	}

	item, err := s.Item(c, f.Item)
	if err != nil {
		return err
	}
	job, err := s.CreateJob(c, f.Name, item.ID)
	if err != nil {
		return err
	}
	a.Printf("%s: job for %s\n", job.Name, item.Name)

	maxTime, err := f.maxTime()
	if err != nil {
		return err
	}
	policy := store.JobPolicy{Depth: &f.Depth, MaxPages: &f.MaxPages, MaxTime: &maxTime}
	if err := s.SetJobPolicy(c, job.ID, policy); err != nil {
		return err
	}

	for _, d := range f.Domains {
		host, err := cli.NormaliseDomain(d.Value)
		if err != nil {
			return err
		}
		if err := s.AddTarget(c, job.ID, store.TargetDomain, host, d.Subdomains, d.Depth); err != nil {
			return err
		}
		a.Printf("%s: domain %s\n", job.Name, host)
	}
	for _, u := range f.URLs {
		normalised, err := cli.NormaliseURL(u.Value)
		if err != nil {
			return err
		}
		if err := s.AddTarget(c, job.ID, store.TargetURL, normalised, false, u.Depth); err != nil {
			return err
		}
		a.Printf("%s: url %s\n", job.Name, normalised)
	}
	for _, t := range f.Types {
		if err := s.AddContentType(c, job.ID, strings.ToLower(t)); err != nil {
			return err
		}
		a.Printf("%s: type %s\n", job.Name, t)
	}
	return nil
}
