// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"fmt"

	"github.com/urfave/cli/v3"
)

// pause and stop are the two ways a search ends, and they are not the same.
//
// pause freezes: the frontier keeps its order and its leases, and starting
// again carries on from where it got to. stop discards: the frontier goes, and
// starting again begins from the seeds.
//
// Both are durable state rather than a signal, so they reach a crawl wherever
// it is running, including crawlers on other machines being fed by a store.
func newPauseCmd(a *app) *cli.Command {
	return &cli.Command{
		Category:  "SEARCH",
		Name:      "pause",
		ArgsUsage: "<name>",
		Usage:     "Pause a search for items, keeping its frontier",
		Description: "Freezes a search wherever it is running: in the foreground on this machine,\n" +
			"or on crawlers being fed by a store elsewhere.\n\n" +
			"Nothing is discarded. The frontier keeps its order and its leases, so\n" +
			"`scour start` carries on rather than starting again, and the item stays\n" +
			"paused until it does.",
		UsageText: "  scour pause news",
		Action: func(c context.Context, cmd *cli.Command) error {
			args, err := need(cmd, 1, "one item name")
			if err != nil {
				return err
			}
			return setPaused(c, a, args[0], true)
		},
	}
}

func newStopCmd(a *app) *cli.Command {
	var force bool
	return &cli.Command{
		Category:  "SEARCH",
		Name:      "stop",
		ArgsUsage: "<name>",
		Usage:     "Stop a search for items, discarding its frontier",
		Description: "Ends the search and throws away what it had queued, so the next `scour start`\n" +
			"begins from the seeds rather than carrying on.\n\n" +
			"The item's definition is untouched, and so are the cached page bodies, so a\n" +
			"fresh search costs the crawl again but not the parsing. What goes is the\n" +
			"work of deciding what to fetch next, which on a large site is hours of it.\n\n" +
			"Use `scour pause` to stop a search and keep that work.",
		UsageText: "  scour pause news    # the one you probably want\n" +
			"  scour stop news --force",
		Flags: []cli.Flag{
			&cli.BoolFlag{
				Name:        "force",
				Usage:       "confirm discarding a frontier that has something in it",
				Destination: &force,
			},
		},
		Action: func(c context.Context, cmd *cli.Command) error {
			args, err := need(cmd, 1, "one item name")
			if err != nil {
				return err
			}
			return runStop(c, a, args[0], force)
		},
	}
}

func runStop(c context.Context, a *app, name string, force bool) error {
	s, err := a.Store()
	if err != nil {
		return err
	}
	item, err := s.Item(c, name)
	if err != nil {
		return err
	}
	st, err := s.Status(c, item.ID)
	if err != nil {
		return err
	}

	// Naming a destructive default "stop" is how someone loses a frontier they
	// meant to keep, so the cost is stated and confirmed rather than assumed.
	// Nothing to lose costs nothing to ask about.
	held := st.Queued + st.Visited
	if held > 0 && !force {
		return fmt.Errorf("%s has %d queued and %d visited urls to discard\n"+
			"  keep them:    scour pause %s\n"+
			"  discard them: scour stop %s --force",
			item.Name, st.Queued, st.Visited, item.Name, item.Name)
	}

	// Paused is cleared as well: the frontier is gone, so leaving the item
	// paused would mean a later start silently did nothing.
	if err := s.SetPaused(c, item.ID, false); err != nil {
		return err
	}
	if err := s.ResetFrontier(c, item.ID); err != nil {
		return err
	}

	a.Printf("%s: stopped, discarded %d queued and %d visited urls\n",
		item.Name, st.Queued, st.Visited)
	a.Printf("the definition and the cached pages are untouched\n")
	a.Printf("search again from the seeds: scour start %s\n", item.Name)
	return nil
}

func setPaused(c context.Context, a *app, name string, paused bool) error {
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
		a.Printf("%s: paused, frontier kept\ncarry on: scour start %s\n",
			item.Name, item.Name)
		return nil
	}
	a.Printf("%s: unpaused\n", item.Name)
	return nil
}
