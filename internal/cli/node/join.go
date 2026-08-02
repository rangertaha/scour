// SPDX-License-Identifier: GPL-3.0-or-later

package node

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	ucli "github.com/urfave/cli/v3"

	"github.com/rangertaha/scour/internal/cli"

	"github.com/rangertaha/scour/internal/bus"
	"github.com/rangertaha/scour/internal/cluster"
	"github.com/rangertaha/scour/internal/content"
	"github.com/rangertaha/scour/internal/crawl"
	"github.com/rangertaha/scour/internal/service"
	"github.com/rangertaha/scour/internal/version"
)

func Join(a *cli.App) *ucli.Command {
	var roles, busURL, name string

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
			&ucli.StringFlag{
				Name:        "name",
				Usage:       "what to register this node as (default this machine's `hostname`)",
				Destination: &name,
			},
		},
		Action: func(c context.Context, cmd *ucli.Command) error {
			// The flag was advertised in the help and the examples before it
			// existed, so the documented command failed with "unknown flag".
			if busURL != "" {
				a.Cfg.Bus.URL = busURL
			}
			return runServices(c, a, roles, name)
		},
	}

	return cmd
}

func runServices(c context.Context, a *cli.App, spec, name string) error {
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

	// Register this process in the fleet, so `scour node ls` has something to
	// list and `scour node leave` has something to ask.
	//
	// A registry that cannot be reached is not a reason not to crawl, which is
	// why the failure is a warning rather than a return, and why the nil
	// registry that follows is a working value: every call on it does nothing.
	// An unlisted node still fetches pages, and fetching pages is the job.
	registry, err := cluster.Open(c, b, cluster.Options{})
	if err != nil {
		slog.Warn("no node registry, this node will not be listed", "err", err)
	}
	member, err := registry.Join(c, cluster.Node{
		Name:    name,
		Role:    roleNames(wanted),
		Host:    hostname(),
		Version: version.Version(),
	}, service.Health(services...))
	if err != nil {
		slog.Warn("this node will not be listed", "err", err)
	}
	defer func() {
		// Its own deadline, and deliberately not the context that has just
		// been cancelled to stop the services: removing the entry is a write,
		// and a cancelled context writes nothing, which would leave the node
		// looking alive until it aged out.
		bye, cancel := context.WithTimeout(context.WithoutCancel(c), 5*time.Second)
		defer cancel()
		member.Close(bye)
	}()

	// `scour node leave`, typed in another process, arrives here as a closed
	// channel. Draining is then cancelling the services, because cancelling is
	// already what they treat as shutdown and the crawl role's shutdown is a
	// drain: it closes its feeds, which stops new work being taken, and waits
	// for the fetches already running to finish.
	running, drain := context.WithCancel(c)
	defer drain()
	go func() {
		select {
		case <-running.Done():
		case <-member.Leaving():
			drain()
		}
	}()

	a.Printf("scour running: %v\n", wanted)
	if id := member.Name(); id != "" {
		a.Printf("node %s, leave it with: scour node leave %s\n", id, id)
	}
	a.Println("press ctrl-c to stop")

	if err := service.New(services...).Run(running); err != nil {
		return err
	}
	a.Println("stopped")
	return nil
}

// roleNames is what the registry records a node as running.
func roleNames(roles []service.Role) cluster.Roles {
	out := make(cluster.Roles, 0, len(roles))
	for _, r := range roles {
		out = append(out, string(r))
	}
	return out
}

// hostname is the machine, kept beside the node name because the two come
// apart: a second scour on one machine registers under a suffixed name, and
// the host is then the only thing saying where it actually is.
func hostname() string {
	host, err := os.Hostname()
	if err != nil {
		return ""
	}
	return host
}
