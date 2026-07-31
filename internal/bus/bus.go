// SPDX-License-Identifier: GPL-3.0-or-later

// Package bus is how scour's components talk to each other.
//
// Components never call each other directly: they publish and subscribe. In
// the default single-process mode the broker is an embedded NATS server
// running in-process, so a laptop user gets a normal CLI tool with nothing to
// install. Point the same code at an external cluster and the components
// spread across machines without changing.
//
// Delivery is at-least-once. Every consumer must therefore be idempotent, and
// every write keyed on a stable hash, which is why the frontier keys on a URL
// hash and records key on a fingerprint.
package bus

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// Options configures a bus.
type Options struct {
	// URL of an external NATS server. Empty starts an embedded one, which is
	// the single-process default.
	URL string
	// StoreDir is where JetStream keeps its data when embedded. Empty keeps
	// streams in memory, which is right for tests and for a one-shot crawl.
	StoreDir string
	// Name identifies this process in NATS monitoring.
	Name string
	// Timeout bounds startup and requests.
	Timeout time.Duration
}

func (o Options) timeout() time.Duration {
	if o.Timeout <= 0 {
		return 10 * time.Second
	}
	return o.Timeout
}

// Bus is a connection to the broker, and the embedded server when there is one.
type Bus struct {
	conn     *nats.Conn
	js       jetstream.JetStream
	embedded *server.Server
}

// Open connects to the broker, starting an embedded one when no URL is given.
func Open(ctx context.Context, opts Options) (*Bus, error) {
	b := &Bus{}

	url := opts.URL
	if url == "" {
		srv, err := startEmbedded(opts)
		if err != nil {
			return nil, err
		}
		b.embedded = srv
		url = srv.ClientURL()
	}

	name := opts.Name
	if name == "" {
		name = "scour"
	}

	conn, err := nats.Connect(url,
		nats.Name(name),
		nats.Timeout(opts.timeout()),
		nats.MaxReconnects(-1),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			// A deliberate drain reports no error; warning about it would make
			// every clean shutdown look like a fault.
			if err != nil {
				slog.Warn("bus disconnected", "err", err)
			}
		}),
		nats.ReconnectHandler(func(c *nats.Conn) {
			slog.Info("bus reconnected", "url", c.ConnectedUrl())
		}),
	)
	if err != nil {
		b.stopEmbedded()
		return nil, fmt.Errorf("connect to bus at %s: %w", url, err)
	}
	b.conn = conn

	js, err := jetstream.New(conn)
	if err != nil {
		b.Close()
		return nil, fmt.Errorf("open jetstream: %w", err)
	}
	b.js = js

	if err := b.createStreams(ctx); err != nil {
		b.Close()
		return nil, err
	}

	slog.Debug("bus open", "url", url, "embedded", b.embedded != nil)
	return b, nil
}

// startEmbedded runs a NATS server inside this process.
func startEmbedded(opts Options) (*server.Server, error) {
	cfg := &server.Options{
		ServerName:         "scour-embedded",
		Host:               "127.0.0.1",
		Port:               server.RANDOM_PORT,
		NoLog:              true,
		NoSigs:             true,
		JetStream:          true,
		JetStreamMaxMemory: 256 << 20,
		DontListen:         false,
	}
	if opts.StoreDir != "" {
		cfg.StoreDir = opts.StoreDir
	} else {
		// No store directory means memory-only streams, which suits a
		// one-shot crawl: the database is the durable record, not the bus.
		cfg.JetStreamMaxStore = -1
	}

	srv, err := server.NewServer(cfg)
	if err != nil {
		return nil, fmt.Errorf("create embedded broker: %w", err)
	}
	go srv.Start()

	if !srv.ReadyForConnections(opts.timeout()) {
		srv.Shutdown()
		return nil, errors.New("embedded broker did not start in time")
	}
	return srv, nil
}

// Close releases the connection and shuts the embedded server down.
func (b *Bus) Close() error {
	if b.conn != nil {
		// Drain rather than Close, so anything already published is delivered
		// before the process goes away.
		if err := b.conn.Drain(); err != nil {
			b.conn.Close()
		}
		b.conn = nil
	}
	b.stopEmbedded()
	return nil
}

func (b *Bus) stopEmbedded() {
	if b.embedded != nil {
		b.embedded.Shutdown()
		b.embedded.WaitForShutdown()
		b.embedded = nil
	}
}

// Conn exposes the underlying connection for the few places that need it.
func (b *Bus) Conn() *nats.Conn { return b.conn }

// JetStream exposes the stream API.
func (b *Bus) JetStream() jetstream.JetStream { return b.js }

// Flush waits for everything published so far to reach the server. It is the
// barrier a command uses before reporting results gathered by another
// component.
func (b *Bus) Flush() error {
	if b.conn == nil {
		return nil
	}
	if err := b.conn.Flush(); err != nil {
		return fmt.Errorf("flush bus: %w", err)
	}
	return nil
}
