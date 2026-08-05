// SPDX-License-Identifier: GPL-3.0-or-later

package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	ucli "github.com/urfave/cli/v3"

	"github.com/rangertaha/scour/internal/bus"
	"github.com/rangertaha/scour/internal/engine"
	"github.com/rangertaha/scour/internal/entity"
	"github.com/rangertaha/scour/internal/event"
)

// Service runs the stores a cluster shares.
//
// # Why this is a command of its own
//
// Because what it runs outlives any crawl. `scour serve` joins a cluster and
// serves stages for whatever jobs turn up, which is work that only exists while
// there are jobs. The entity graph and the event store are the opposite: they
// are what the jobs accumulate into, they are shared between jobs that have
// never met, and they are still there when nothing is crawling.
//
// They also take a document of their own rather than a job, for the reason
// [engine.Service] gives: a job says it wants entities, and does not say where
// they live.
func Service(a *App) *ucli.Command {
	var join string

	return &ucli.Command{
		Name:      "service",
		Usage:     "Run the entity and event stores this cluster shares",
		ArgsUsage: "<service.hcl>",
		Description: "Reads a service document and answers for the stores it names, on the\n" +
			"bus, until interrupted.\n\n" +
			"A service document is not a job document: it says where a store lives,\n" +
			"and a job never does. See `scour service --help` for the blocks.",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "join", Usage: "a node to join, as nats://host:port", Destination: &join},
		},
		Action: oneFile(func(ctx context.Context, path string) error {
			return runService(ctx, a, path, join)
		}),
	}
}

func runService(ctx context.Context, a *App, path, join string) error {
	src, err := os.ReadFile(path)
	if err != nil {
		return Failedf("%v", err)
	}

	doc, err := engine.ParseService(src, filepath.Base(path))
	if err != nil {
		return Invalidf("%v", err)
	}
	if err := doc.Validate(); err != nil {
		return Invalidf("%v", err)
	}

	conn, err := bus.Connect(bus.Options{URL: join, Name: "scour-service"})
	if err != nil {
		return Failedf("%v", err)
	}
	defer conn.Close()

	// Everything opened here is closed here, in the order it was opened, and
	// the services go first: a store closed while its subscriptions were still
	// taking work would answer requests with a closed database.
	var stop []func() error
	defer func() {
		for i := len(stop) - 1; i >= 0; i-- {
			stop[i]()
		}
	}()

	if c := doc.Entity; c != nil {
		store, err := entity.New(ctx, entity.Config{Dir: c.Dir})
		if err != nil {
			return Failedf("%v", err)
		}
		stop = append(stop, store.Close)

		service, err := conn.ServeEntities(store)
		if err != nil {
			return Failedf("%v", err)
		}
		stop = append(stop, service.Close)

		a.Warnf("entities: serving %s on %s\n", c.Dir, bus.EntitySubject("*"))
	}

	if c := doc.Event; c != nil {
		store, err := event.Open(c.Dir)
		if err != nil {
			return Failedf("%v", err)
		}
		stop = append(stop, store.Close)

		service, err := conn.ServeEvents(store)
		if err != nil {
			return Failedf("%v", err)
		}
		stop = append(stop, service.Close)

		a.Warnf("events: serving %s on %s\n", c.Dir, bus.EventSubject("*"))
	}

	if conn.Embedded() {
		a.Printf("listening on %s\n", conn.Address())
	}
	a.Warnf("ready. Interrupt to stop\n")

	<-ctx.Done()
	if err := ctx.Err(); err != nil && !errors.Is(err, context.Canceled) {
		return Failedf("%v", err)
	}
	a.Printf("stopped\n")
	return nil
}
