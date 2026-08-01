// SPDX-License-Identifier: GPL-3.0-or-later

package node

import (
	"context"
	"fmt"

	ucli "github.com/urfave/cli/v3"

	"github.com/rangertaha/scour/internal/cli"

	"github.com/rangertaha/scour/internal/bus"
	"github.com/rangertaha/scour/internal/content"
	"github.com/rangertaha/scour/internal/crawl"
	"github.com/rangertaha/scour/internal/service"
)

func Join(a *cli.App) *ucli.Command {
	var roles, busURL string

	cmd := &ucli.Command{
		Name:    "join",
		Aliases: []string{"run"},
		Usage:   "Join a cluster for distributed workload",
		Description: "Starts the components named by --role and serves them until interrupted.\n" +
			"With no --role it starts all of them, which is a single-process scour with\n" +
			"an embedded broker. Point --bus-url at a NATS cluster and the same roles can\n" +
			"be spread across machines.",
		UsageText: "  scour join\n\n" +
			"One machine holds the frontier and hands out work:\n" +
			"  scour join --role store --bus-url nats://broker:4222\n\n" +
			"Any number of others fetch it:\n" +
			"  scour join --role crawl --bus-url nats://broker:4222",
		Flags: []ucli.Flag{
			&ucli.StringFlag{
				Name:        "role",
				Usage:       "components to run: store, crawl, or all (default all)",
				Destination: &roles,
			},
			&ucli.StringFlag{
				Name:        "bus-url",
				Usage:       "NATS server to use instead of the embedded one (default from [bus] url in the config)",
				Destination: &busURL,
			},
		},
		Action: func(c context.Context, cmd *ucli.Command) error {
			// The flag was advertised in the help and the examples before it
			// existed, so the documented command failed with "unknown flag".
			if busURL != "" {
				a.Cfg.Bus.URL = busURL
			}
			return runServices(c, a, roles)
		},
	}

	return cmd
}

func runServices(c context.Context, a *cli.App, spec string) error {
	cli.LogProgress()
	wanted, err := service.ParseRoles(spec)
	if err != nil {
		return err
	}

	s, err := a.Store()
	if err != nil {
		return err
	}

	b, err := bus.Open(c, bus.Options{
		URL:      a.Cfg.Bus.URL,
		StoreDir: a.Cfg.Bus.StoreDir,
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
			services = append(services, service.NewStore(b, s, service.Dispatching(a.Cfg.Crawl.Rate.Duration())))
		case service.RoleCrawl:
			// Content types and the browser policy come from this process's
			// configuration rather than from the item: a crawler does not
			// read the database, and both describe the machine doing the
			// fetching rather than the thing being looked for.
			types, err := content.New(a.Cfg.Crawl.ContentTypes, nil)
			if err != nil {
				return err
			}
			pages, err := a.Pages()
			if err != nil {
				return err
			}
			crawler := crawl.New(a.Cfg, s, pages)
			services = append(services,
				service.NewCrawl(b, crawler, types, a.Cfg.Browser.Policy))
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
