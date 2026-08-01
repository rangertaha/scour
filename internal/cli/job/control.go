// SPDX-License-Identifier: GPL-3.0-or-later

package job

import (
	"context"
	"fmt"

	ucli "github.com/urfave/cli/v3"

	"github.com/rangertaha/scour/internal/cli"
)

// pause and stop are the two ways a search ends, and they are not the same.
//
// pause freezes: the frontier keeps its order and its leases, and starting
// again carries on from where it got to. stop discards: the frontier goes, and
// starting again begins from the seeds.
//
// Both are durable state rather than a signal, so they reach a crawl wherever
// it is running, including crawlers on other machines being fed by a store.
func Pause(a *cli.App) *ucli.Command {
	return &ucli.Command{
		Name:      "pause",
		ArgsUsage: "<name>",
		Usage:     "Pause a search for items, keeping its frontier",
		Description: "Freezes a search wherever it is running: in the foreground on this machine,\n" +
			"or on crawlers being fed by a store elsewhere.\n\n" +
			"Nothing is discarded. The frontier keeps its order and its leases, so\n" +
			"`scour run` carries on rather than starting again, and the item stays\n" +
			"paused until it does.",
		UsageText: "  scour job pause news\n" +
			"  scour run news    # carry on from the frontier\n\n" +
			"To throw the frontier away instead:\n" +
			"  scour job stop news --force",
		Action: func(c context.Context, cmd *ucli.Command) error {
			args, err := cli.Need(cmd, 1, "one item name")
			if err != nil {
				return err
			}
			return setPaused(c, a, args[0], true)
		},
	}
}

func Stop(a *cli.App) *ucli.Command {
	var force bool
	return &ucli.Command{
		Name:      "stop",
		ArgsUsage: "<name>",
		Usage:     "Stop a search for items, discarding its frontier",
		Description: "Ends the search and throws away what this job had queued, so the next\n" +
			"`scour job start` begins from the seeds rather than carrying on.\n\n" +
			"Only this job's frontier goes. The definition, the pages already fetched,\n" +
			"the records and the model are all kept, and so is the frontier of any other\n" +
			"job over the same item. What goes is the work of deciding what to fetch\n" +
			"next, which on a large site is hours of it.\n\n" +
			"Because the pages are kept, a later start finds what is new rather than\n" +
			"fetching the site again. Use `scour job start --reset` for that.\n\n" +
			"Use `scour job pause` to stop a search and keep the frontier.",
		UsageText: "  scour job pause news    # the one you probably want\n" +
			"  scour job stop news --force",
		Flags: []ucli.Flag{
			&ucli.BoolFlag{
				Name:        "force",
				Usage:       "confirm discarding a frontier that has something in it",
				Destination: &force,
			},
		},
		Action: func(c context.Context, cmd *ucli.Command) error {
			args, err := cli.Need(cmd, 1, "one item name")
			if err != nil {
				return err
			}
			return runStop(c, a, args[0], force)
		},
	}
}

func runStop(c context.Context, a *cli.App, name string, force bool) error {
	s, err := a.Store()
	if err != nil {
		return err
	}
	item, err := s.Item(c, name)
	if err != nil {
		return err
	}
	job, err := s.JobForItem(c, item)
	if err != nil {
		return err
	}
	// What stop actually discards, which is this job's frontier. The visited
	// pages are the item's corpus and are kept, so counting them here was
	// offering to throw away something stop does not touch.
	queued, err := s.QueueSize(c, job.ID)
	if err != nil {
		return err
	}

	// Naming a destructive default "stop" is how someone loses a frontier they
	// meant to keep, so the cost is stated and confirmed rather than assumed.
	// Nothing to lose costs nothing to ask about.
	if queued > 0 && !force {
		return fmt.Errorf("%s has %d queued urls to discard\n"+
			"  keep them:    scour job pause %s\n"+
			"  discard them: scour job stop %s --force",
			item.Name, queued, item.Name, item.Name)
	}

	// Paused is cleared as well: the frontier is gone, so leaving the item
	// paused would mean a later start silently did nothing.
	if err := s.SetPaused(c, item.ID, false); err != nil {
		return err
	}
	if err := s.StopJob(c, job.ID); err != nil {
		return err
	}

	a.Printf("%s: stopped, discarded %d queued urls\n", item.Name, queued)
	a.Printf("the definition, the cached pages and the records are untouched\n")
	a.Printf("search again from the seeds: scour run %s\n", item.Name)
	return nil
}

func setPaused(c context.Context, a *cli.App, name string, paused bool) error {
	s, err := a.Store()
	if err != nil {
		return err
	}

	item, err := s.Item(c, name)
	if err != nil {
		return err
	}
	if err := s.SetPaused(c, item.ID, paused); err != nil {
		return err
	}

	if paused {
		a.Printf("%s: paused, frontier kept\ncarry on: scour run %s\n",
			item.Name, item.Name)
		return nil
	}
	a.Printf("%s: unpaused\n", item.Name)
	return nil
}
