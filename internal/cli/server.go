// SPDX-License-Identifier: GPL-3.0-or-later

package cli

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/hashicorp/hcl/v2"
	ucli "github.com/urfave/cli/v3"

	"github.com/rangertaha/scour/internal/bus"
	"github.com/rangertaha/scour/internal/cache"
	"github.com/rangertaha/scour/internal/classify/store"
	"github.com/rangertaha/scour/internal/engine"
	"github.com/rangertaha/scour/internal/entity"
	"github.com/rangertaha/scour/internal/event"
	"github.com/rangertaha/scour/internal/jobs"
	"github.com/rangertaha/scour/internal/node"
	"github.com/rangertaha/scour/internal/secret"
)

// Server runs a machine's share of a cluster.
//
// # Why this is one command and not three
//
// It used to be two, `serve` and `service`, and the split was along the wrong
// line. It divided what a process runs rather than what an operator decides:
// somebody bringing up a cluster had to start a node, start the stores, and
// then discover that nothing at all submitted or drove a job. Three processes
// to answer one question, and the third did not exist.
//
// What this runs is decided by what it is given. Always a node and the job
// service, because those are what a machine offering itself to a cluster is
// for. The shared stores as well when a service document names them, because
// where the entity graph lives is a decision somebody makes once and writes
// down, not a flag they retype.
//
// # The first server is also the broker
//
// With no --join it starts a NATS in this process, so a single machine needs
// nothing installed. The address it prints is what the next one joins.
func Server(a *App) *ucli.Command {
	var (
		join   string
		name   string
		dir    string
		stages string
		quiet  bool
		drive  bool
	)

	return &ucli.Command{
		Name:      "server",
		Category:  "Running a cluster",
		Usage:     "Run a node, the job service, and the stores a cluster shares",
		ArgsUsage: "[service.hcl]",
		Description: "Joins a cluster and serves it: the stages this machine offers for\n" +
			"every job that appears, and the job service that submits jobs and\n" +
			"drives their crawls.\n\n" +
			"Given a service document it also answers for the stores it names: an\n" +
			"`entity`, an `event` or a `topic` block, each with a `dir`. Those are\n" +
			"shared between jobs, which is why they are a document somebody keeps\n" +
			"rather than a flag.\n\n" +
			"With no --join it starts a broker here and prints the address the\n" +
			"next server should join.",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "join", Usage: "a server to join, as nats://host:port", Destination: &join},
			&ucli.StringFlag{Name: "name", Usage: "what to call this node", Destination: &name},
			&ucli.StringFlag{Name: "dir", Usage: "where to keep the cache, the frontiers and the cluster's state", Destination: &dir},
			&ucli.StringFlag{Name: "stages", Usage: "which stages to serve: download, read, or both", Destination: &stages},
			&ucli.BoolFlag{Name: "jobs", Value: true, Usage: "run the job service here", Destination: &drive},
			&ucli.BoolFlag{Name: "quiet", Usage: "say nothing but failures", Destination: &quiet},
		},
		Action: func(ctx context.Context, cmd *ucli.Command) error {
			switch cmd.Args().Len() {
			case 0:
				return runServer(ctx, a, "", join, name, dir, stages, quiet, drive)
			case 1:
				return runServer(ctx, a, cmd.Args().First(), join, name, dir, stages, quiet, drive)
			default:
				return Usagef("one service document at a time, got %d", cmd.Args().Len())
			}
		},
	}
}

func runServer(ctx context.Context, a *App, path, join, name, dir, stages string, quiet, drive bool) (err error) {
	if name == "" {
		host, err := os.Hostname()
		if err != nil || host == "" {
			host = "node"
		}
		name = strings.ReplaceAll(host, ".", "-")
	}
	if dir == "" {
		dir = ".scour"
	}

	// The service document is read before anything is opened, so a document
	// with a typo in it fails before a broker, a cache and a node have been
	// started and have to be taken down again.
	var services *engine.Service
	if path != "" {
		src, err := os.ReadFile(path)
		if err != nil {
			return Failedf("%v", err)
		}
		if services, err = engine.ParseService(src, filepath.Base(path)); err != nil {
			return Invalidf("%v", err)
		}
		if err := services.Validate(); err != nil {
			return Invalidf("%v", err)
		}
	}

	level := slog.LevelInfo
	if quiet {
		level = slog.LevelWarn
	}
	log := slog.New(slog.NewTextHandler(a.Err, &slog.HandlerOptions{Level: level}))

	// The document's own address when it gives one, and --join over it, which
	// is the rule `scour server` already kept: a flag is what somebody types
	// to point a server somewhere else for one run.
	url := join
	if url == "" && services != nil {
		url = serviceURL(services)
	}

	conn, err := bus.Connect(bus.Options{
		URL:      url,
		Name:     name,
		StoreDir: filepath.Join(dir, "bus"),
	})
	if err != nil {
		return Failedf("%v", err)
	}
	defer func() { _ = conn.Close() }()

	// Everything opened here is closed here, in the order it was opened, and
	// what answers the bus goes down before what it answers from: a store
	// closed while its subscriptions were still taking work would answer
	// requests with a closed database.
	//
	// What a close reports is not thrown away. Shutting down is where the
	// expensive failures live: an exporter writing its footer, a store
	// committing its last batch, an object-store bucket being let go. A server
	// that swallowed those exited zero having lost the tail of the work it had
	// just reported doing, which is the failure `scour crawl` closes early to
	// avoid and the one this had no way to notice at all.
	var stop []func() error
	defer func() {
		var problems []error
		for i := len(stop) - 1; i >= 0; i-- {
			if closeErr := stop[i](); closeErr != nil {
				problems = append(problems, closeErr)
			}
		}
		if len(problems) == 0 {
			return
		}

		// Not over the top of a failure that is already being reported: that
		// one is the reason the shutdown is happening, and it is the more
		// diagnostic of the two.
		joined := errors.Join(problems...)
		if err == nil {
			err = Failedf("%v", joined)
			return
		}
		a.Warnf("while shutting down: %v\n", joined)
	}()

	// One cache, shared by the node that fetches and the job service that
	// drives. It has to be shared: a body never crosses the bus, so the stage
	// writes it here and the driver reads it back, and two directories would
	// be a driver that could never read anything it asked for.
	//
	// On one machine a directory does that. A cluster wants a cache plugin
	// every machine can see, which is what the object-store backends are for.
	bodies, err := cache.New(ctx, cache.Config{Dir: filepath.Join(dir, "cache")})
	if err != nil {
		return Failedf("%v", err)
	}
	stop = append(stop, bodies.Close)

	// A server with a sealing key can resolve the secrets a job's plugins ask
	// for. One without still serves: most jobs use none, and a job that does is
	// refused here by name rather than failing later with a message about
	// authentication.
	var eval *hcl.EvalContext
	if key, err := secret.Key(""); err == nil {
		if secrets, err := secret.Open(ctx, conn, key); err == nil {
			eval = secret.Eval(ctx, secrets)
			a.Printf("%s can resolve secrets\n", name)
		}
	}

	if services != nil {
		if err := serveStores(ctx, a, conn, services, &stop); err != nil {
			return err
		}
	}

	joined, err := node.Join(ctx, conn, node.Options{
		Name:   name,
		Serve:  serving(stages),
		Bodies: bodies,
		Log:    log,
		Eval:   eval,
	})
	if err != nil {
		return Failedf("%v", err)
	}
	stop = append(stop, joined.Close)

	if drive {
		manager, err := jobs.New(ctx, conn, jobs.Options{
			Dir:    dir,
			Bodies: bodies,
			Name:   name,
			Log:    log,
			Eval:   eval,
		})
		if err != nil {
			return Failedf("%v", err)
		}
		stop = append(stop, manager.Close)

		// The subscriptions come down before the manager does, so a request
		// already taken is answered by a manager that is still working.
		service, err := conn.ServeControl(manager, 0)
		if err != nil {
			return Failedf("%v", err)
		}
		stop = append(stop, service.Close)

		a.Warnf("jobs: serving on %s\n", bus.ControlSubject("*"))
	}

	// The address on the first line and the command to use it on the second.
	// Both, because they are read by different people: somebody watching a log
	// wants to know where this ended up listening, and somebody bringing a
	// second machine up wants a line to paste.
	if conn.Embedded() {
		a.Printf("%s is serving, and is the broker listening on %s\n", name, conn.Address())
		a.Printf("join it with: scour server --join %s\n", conn.Address())
	} else {
		a.Printf("%s joined %s\n", name, conn.Address())
	}

	// Ctrl-C leaves properly: the node drains what it was serving, so a
	// request it had already taken is answered rather than dropped, and the
	// job service stops its crawls and waits for them.
	ctx, ended := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer ended()

	if err := joined.Watch(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return Failedf("%v", err)
	}
	a.Printf("%s has left\n", name)
	return nil
}

// serveStores answers for whichever shared stores a service document names.
func serveStores(ctx context.Context, a *App, conn *bus.Conn, doc *engine.Service, stop *[]func() error) error {
	if c := doc.Entity; c != nil {
		// The document's own timeout, not the bus package's default. Validate
		// has already refused a length of time that is not one, so the error
		// here cannot happen and the wait is used as it is.
		wait, _ := c.Wait()

		graph, err := entity.New(ctx, entity.Config{Dir: c.Dir})
		if err != nil {
			return Failedf("%v", err)
		}
		*stop = append(*stop, graph.Close)

		service, err := conn.ServeEntities(graph, wait)
		if err != nil {
			return Failedf("%v", err)
		}
		*stop = append(*stop, service.Close)

		a.Warnf("entities: serving %s on %s\n", c.Dir, bus.EntitySubject("*"))
	}

	if c := doc.Event; c != nil {
		wait, _ := c.Wait()

		events, err := event.New(ctx, event.Config{Dir: c.Dir})
		if err != nil {
			return Failedf("%v", err)
		}
		*stop = append(*stop, events.Close)

		service, err := conn.ServeEvents(events, wait)
		if err != nil {
			return Failedf("%v", err)
		}
		*stop = append(*stop, service.Close)

		a.Warnf("events: serving %s on %s\n", c.Dir, bus.EventSubject("*"))
	}

	if c := doc.Topic; c != nil {
		wait, _ := c.Wait()

		topics, err := store.Open(c.Dir)
		if err != nil {
			return Failedf("%v", err)
		}

		service, err := conn.ServeTopics(topics, wait)
		if err != nil {
			return Failedf("%v", err)
		}
		*stop = append(*stop, service.Close)

		a.Warnf("topics: serving %s on %s\n", c.Dir, bus.TopicSubject("*"))
	}
	return nil
}

// serving is the stages a node was told to offer.
func serving(stages string) []string {
	var out []string
	for _, name := range strings.Split(stages, ",") {
		if name = strings.TrimSpace(name); name != "" {
			out = append(out, name)
		}
	}
	return out
}

// serviceURL is the bus a service document says to answer on.
//
// The blocks each carry one because they are configured separately, and one
// process serves whichever are present, so they have to agree. Disagreeing is a
// document saying two things, and picking one silently is how somebody ends up
// debugging why half their services are on the wrong bus.
//
// Read at all because it was not: url was parsed, validated, documented and
// ignored, so a service told to answer on nats://10.0.0.5:4222 started an
// embedded broker on an ephemeral port, printed that it was ready, and answered
// nobody. Every node pointed at the documented address failed to build its
// chain.
func serviceURL(doc *engine.Service) string {
	var urls []string
	for _, one := range []string{
		blockURL(doc.Entity != nil, func() string { return doc.Entity.URL }),
		blockURL(doc.Event != nil, func() string { return doc.Event.URL }),
		blockURL(doc.Topic != nil, func() string { return doc.Topic.URL }),
	} {
		if one != "" && !slices.Contains(urls, one) {
			urls = append(urls, one)
		}
	}
	if len(urls) == 1 {
		return urls[0]
	}
	return ""
}

func blockURL(present bool, get func() string) string {
	if !present {
		return ""
	}
	return get()
}
