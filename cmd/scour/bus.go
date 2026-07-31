// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/rangertaha/scour/internal/bus"
	"github.com/rangertaha/scour/internal/crawl"
	"github.com/rangertaha/scour/internal/service"
)

// busCrawler puts the bus between the crawler and the database.
//
// It starts an embedded broker and the store service in this process, so a
// single-process run behaves like a distributed one without anything to
// install. The returned function waits for the writer to catch up, and must be
// called before reading back what the crawl produced.
func (a *app) busCrawler(ctx context.Context, crawler *crawl.Crawler, entity string) (*crawl.Crawler, func() error, error) {
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

	return crawler.WithSink(service.NewBusSink(b, entity)), settle, nil
}

func newRunCmd(a *app) *cobra.Command {
	var roles string

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run scour's components as a long-lived process",
		Long: "Starts the components named by --role and serves them until interrupted.\n" +
			"With no --role it starts all of them, which is a single-process scour with\n" +
			"an embedded broker. Point --bus-url at a NATS cluster and the same roles can\n" +
			"be spread across machines.",
		Example: "  scour run\n" +
			"  scour run --role store\n" +
			"  scour run --role store --bus-url nats://broker:4222",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runServices(cmd, a, roles)
		},
	}

	cmd.Flags().StringVar(&roles, "role", "", "components to run: store, crawl, or all (default all)")
	return cmd
}

func runServices(cmd *cobra.Command, a *app, spec string) error {
	wanted, err := service.ParseRoles(spec)
	if err != nil {
		return err
	}

	s, err := a.Store()
	if err != nil {
		return err
	}

	c := ctx(cmd)
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
			services = append(services, service.NewStore(b, s))
		case service.RoleCrawl:
			// The crawl role is driven by `scour crawl` for now: there is no
			// scheduler yet to decide which entity to crawl unprompted, so
			// starting it here would be a component with nothing to do.
			cmd.Println("crawl role: nothing to do until a scheduler exists, skipping")
		}
	}
	if len(services) == 0 {
		return fmt.Errorf("none of the requested roles can run yet")
	}

	cmd.Printf("scour running: %v\n", wanted)
	cmd.Println("press ctrl-c to stop")

	if err := service.New(services...).Run(c); err != nil {
		return err
	}
	cmd.Println("stopped")
	return nil
}
