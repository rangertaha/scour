// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/rangertaha/scour/internal/bus"
	"github.com/rangertaha/scour/internal/cache"
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
	var roles, busURL string

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
	cmd.Flags().StringVar(&busURL, "bus-url", "",
		"NATS server to use instead of the embedded one (default from [bus] url in the config)")
	// The flag was advertised in the help and the examples before it existed,
	// so the documented command failed with "unknown flag".
	cmd.PreRun = func(*cobra.Command, []string) {
		if busURL != "" {
			a.cfg.Bus.URL = busURL
		}
	}
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
			// This store dispatches: `scour run` starts a crawl role, here or
			// on another machine, to take the work.
			services = append(services, service.NewStore(b, s, service.Dispatching()))
		case service.RoleCrawl:
			// Content types and the browser policy come from this process's
			// configuration rather than from the entity: a crawler does not
			// read the database, and both describe the machine doing the
			// fetching rather than the thing being looked for.
			types, err := content.New(a.cfg.Crawl.ContentTypes, nil)
			if err != nil {
				return err
			}
			crawler := crawl.New(a.cfg, s, cache.New(a.cfg.PagesDir()))
			services = append(services,
				service.NewCrawl(b, crawler, types, a.cfg.Browser.Policy))
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
