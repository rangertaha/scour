// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"github.com/spf13/cobra"
)

// newStopCmd and newStartCmd are the scriptable form of what `scour top` does
// with a keypress.
//
// The view is not the only place this is needed: stopping a crawl over a
// connection with no terminal, or from whatever is orchestrating a fleet, is
// the ordinary case rather than the exception.
func newStopCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "stop <name>",
		Short: "Stop crawling an entity, keeping its frontier",
		Long: "Stops a crawl wherever it is running: in a foreground `scour crawl` on this\n" +
			"machine, or on crawlers being fed by a store elsewhere.\n\n" +
			"Nothing is discarded. The frontier keeps its order and its leases, so a\n" +
			"resumed crawl carries on rather than starting again, and the entity stays\n" +
			"stopped until it is started.",
		Example: "  scour stop news",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return setPaused(cmd, a, args[0], true)
		},
	}
}

func newStartCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "start <name>",
		Short: "Let an entity be crawled again",
		Long: "Clears what `scour stop` set. Crawlers fed by a store pick the entity up on\n" +
			"their own; a foreground crawl is still started with `scour crawl`.",
		Example: "  scour start news",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return setPaused(cmd, a, args[0], false)
		},
	}
}

func setPaused(cmd *cobra.Command, a *app, name string, paused bool) error {
	s, err := a.Store()
	if err != nil {
		return err
	}
	c := ctx(cmd)

	entity, err := s.Entity(c, name)
	if err != nil {
		return err
	}
	if err := s.SetPaused(c, entity.ID, paused); err != nil {
		return err
	}

	if paused {
		cmd.Printf("%s: stopped, frontier kept\nstart it again: scour start %s\n",
			entity.Name, entity.Name)
		return nil
	}
	cmd.Printf("%s: started\ncrawl it here: scour crawl %s\n", entity.Name, entity.Name)
	return nil
}
