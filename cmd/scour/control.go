// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"github.com/urfave/cli/v3"
)

// newStopCmd and newStartCmd are the scriptable form of what `scour top` does
// with a keypress.
//
// The view is not the only place this is needed: stopping a crawl over a
// connection with no terminal, or from whatever is orchestrating a fleet, is
// the ordinary case rather than the exception.
func newStopCmd(a *app) *cli.Command {
	return &cli.Command{
		Category:  "Finding pages",
		Name:      "stop",
		ArgsUsage: "<name>",
		Usage:     "Stop crawling an item, keeping its frontier",
		Description: "Stops a crawl wherever it is running: in a foreground `scour crawl` on this\n" +
			"machine, or on crawlers being fed by a store elsewhere.\n\n" +
			"Nothing is discarded. The frontier keeps its order and its leases, so a\n" +
			"resumed crawl carries on rather than starting again, and the item stays\n" +
			"stopped until it is started.",
		UsageText: "  scour stop news",
		Action: func(c context.Context, cmd *cli.Command) error {
			args, err := need(cmd, 1, "one item name")
			if err != nil {
				return err
			}
			return setPaused(c, a, args[0], true)
		},
	}
}

func newStartCmd(a *app) *cli.Command {
	return &cli.Command{
		Category:  "Finding pages",
		Name:      "start",
		ArgsUsage: "<name>",
		Usage:     "Let an item be crawled again",
		Description: "Clears what `scour stop` set. Crawlers fed by a store pick the item up on\n" +
			"their own; a foreground crawl is still started with `scour crawl`.",
		UsageText: "  scour start news",
		Action: func(c context.Context, cmd *cli.Command) error {
			args, err := need(cmd, 1, "one item name")
			if err != nil {
				return err
			}
			return setPaused(c, a, args[0], false)
		},
	}
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
		a.Printf("%s: stopped, frontier kept\nstart it again: scour start %s\n",
			item.Name, item.Name)
		return nil
	}
	a.Printf("%s: started\ncrawl it here: scour crawl %s\n", item.Name, item.Name)
	return nil
}
