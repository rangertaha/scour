// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"fmt"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/rangertaha/scour/internal/bus"
	"github.com/rangertaha/scour/internal/content"
	"github.com/rangertaha/scour/internal/crawl"
	"github.com/rangertaha/scour/internal/service"
)

// busCrawler puts the bus between the crawler and the database.
//
// It starts an embedded broker and the store service in this process, so a
// single-process run behaves like a distributed one without anything to
// install. The returned function waits for the writer to catch up, and must be
// called before reading back what the crawl produced.
func (a *app) busCrawler(ctx context.Context, crawler *crawl.Crawler, item string) (*crawl.Crawler, func() error, error) {
	s, err := a.Store()
	if err != nil {
		return nil, nil, err
	}

	b, err := bus.Open(ctx, bus.Options{URL: a.cfg.Bus.URL, Name: "scour-crawl"})
	if err != nil {
		return nil, nil, err
	}

	// The store service runs alongside the crawl rather than after it, so
	// writes happen while pages are still being fetched.
	serviceCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		done <- service.New(service.NewStore(b, s)).Run(serviceCtx)
	}()

	var settled bool
	settle := func() error {
		if settled {
			return nil
		}
		settled = true

		defer func() {
			stop()
			<-done
			b.Close()
		}()

		if err := b.Flush(); err != nil {
			return err
		}

		drainCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if err := b.Drain(drainCtx, bus.StreamCrawl); err != nil {
			return fmt.Errorf("waiting for the store service: %w", err)
		}
		return nil
	}

	return crawler.
		WithSink(service.NewBusSink(b, item)).
		WithMeter(service.NewBusMeter(b, item)), settle, nil
}

func newJoinCmd(a *app) *cli.Command {
	var roles, busURL string

	cmd := &cli.Command{
		Category: "SERVER",
		Name:     "join",
		Aliases:  []string{"run"},
		Usage:    "Join a cluster for distributed workload",
		Description: "Starts the components named by --role and serves them until interrupted.\n" +
			"With no --role it starts all of them, which is a single-process scour with\n" +
			"an embedded broker. Point --bus-url at a NATS cluster and the same roles can\n" +
			"be spread across machines.",
		UsageText: "  scour run\n" +
			"  scour run --role store\n" +
			"  scour run --role store --bus-url nats://broker:4222",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:        "role",
				Usage:       "components to run: store, crawl, or all (default all)",
				Destination: &roles,
			},
			&cli.StringFlag{
				Name:        "bus-url",
				Usage:       "NATS server to use instead of the embedded one (default from [bus] url in the config)",
				Destination: &busURL,
			},
		},
		Action: func(c context.Context, cmd *cli.Command) error {
			// The flag was advertised in the help and the examples before it
			// existed, so the documented command failed with "unknown flag".
			if busURL != "" {
				a.cfg.Bus.URL = busURL
			}
			return runServices(c, a, roles)
		},
	}

	return cmd
}

func runServices(c context.Context, a *app, spec string) error {
	wanted, err := service.ParseRoles(spec)
	if err != nil {
		return err
	}

	s, err := a.Store()
	if err != nil {
		return err
	}

	b, err := bus.Open(c, bus.Options{
		URL:      a.cfg.Bus.URL,
		StoreDir: a.cfg.Bus.StoreDir,
		Name:     "scour-run",
	})
	if err != nil {
		return err
	}
	defer b.Close()

	var services []service.Service
	for _, role := range wanted {
		switch role {
		case service.RoleStore:
			// This store dispatches: `scour run` starts a crawl role, here or
			// on another machine, to take the work.
			services = append(services, service.NewStore(b, s, service.Dispatching(a.cfg.Crawl.Rate.Duration())))
		case service.RoleCrawl:
			// Content types and the browser policy come from this process's
			// configuration rather than from the item: a crawler does not
			// read the database, and both describe the machine doing the
			// fetching rather than the thing being looked for.
			types, err := content.New(a.cfg.Crawl.ContentTypes, nil)
			if err != nil {
				return err
			}
			pages, err := a.Pages()
			if err != nil {
				return err
			}
			crawler := crawl.New(a.cfg, s, pages)
			services = append(services,
				service.NewCrawl(b, crawler, types, a.cfg.Browser.Policy))
		}
	}
	if len(services) == 0 {
		return fmt.Errorf("none of the requested roles can run yet")
	}

	a.Printf("scour running: %v\n", wanted)
	a.Println("press ctrl-c to stop")

	if err := service.New(services...).Run(c); err != nil {
		return err
	}
	a.Println("stopped")
	return nil
}
