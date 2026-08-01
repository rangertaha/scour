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
		Description: "Ends the search and throws away what it had queued, so the next `scour run`\n" +
			"begins from the seeds rather than carrying on.\n\n" +
			"The item's definition is untouched, and so are the cached page bodies, so a\n" +
			"fresh search costs the crawl again but not the parsing. What goes is the\n" +
			"work of deciding what to fetch next, which on a large site is hours of it.\n\n" +
			"Use `scour job pause` to stop a search and keep that work.",
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
			"  keep them:    scour job pause %s\n"+
			"  discard them: scour job stop %s --force",
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
