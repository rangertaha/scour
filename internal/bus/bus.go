// SPDX-License-Identifier: GPL-3.0-or-later

// Package bus is the stages talking over NATS instead of calling each other.
//
// # What has to be true
//
// The same job produces the same records whether the stages are wired directly
// or through the broker. That is the whole claim, and it is a test rather than
// an aspiration: [internal/run] is the direct wiring, this is the other one,
// and the two are run over one site and compared.
//
// If that equivalence holds, a stage can be somewhere else, and a spider
// somebody wrote in another language is a spider like any other. If it does not,
// the bus is a second implementation of the crawler with its own bugs.
//
// # A body never crosses the bus
//
// The message from the downloader to the spider carries a status, the headers
// and a cache key. The body went into the cache, and the reader fetches it from
// there.
//
// That keeps a megabyte of HTML off the message bus, which matters at any real
// rate, and it is why decoding is a function both stages call rather than a
// link in either chain. It also means a spider node needs the cache, which is
// the one thing this arrangement asks of it.
//
// # An embedded server
//
// A laptop needs nothing installed. The server runs in the process unless one
// was given, so the difference between a single node and a cluster is an
// address rather than an installation.
package bus

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
)

// Subjects the stages answer on.
//
// Named by job, so two jobs on one cluster do not queue behind each other, and
// prefixed so that a NATS somebody else is already using does not have to be
// given over to this.
const (
	// Prefix is what every subject starts with.
	Prefix = "scour"

	// DownloadSubject is where a fetch is asked for: scour.<job>.download.
	DownloadSubject = "download"

	// ReadSubject is where a page is read: scour.<job>.read.
	ReadSubject = "read"
)

// Subject builds one stage's subject for one job.
func Subject(job, stage string) string {
	return Prefix + "." + job + "." + stage
}

// Queue is the group a stage's workers join, so that one message goes to one
// of them rather than to all of them.
//
// The whole of how work is distributed: two nodes running the same stage for
// one job join the same group, and NATS hands each request to one. Nothing
// coordinates, nothing elects, and a node that dies takes nothing with it but
// the request it was holding.
func Queue(job, stage string) string { return "scour-" + job + "-" + stage }

// Timeout is how long a client waits for a stage to answer.
//
// A page fetch can be slow, so this is generous. A stage that is not there at
// all fails fast anyway, because NATS answers "no responders" immediately
// rather than waiting.
const Timeout = 2 * time.Minute

// ErrNoStage reports that nothing is serving a stage.
//
// Distinguished from a timeout on purpose: nothing listening is a
// misconfiguration, and something listening that is slow is a busy cluster.
var ErrNoStage = errors.New("bus: nothing is serving that stage")

// Conn is a connection to the bus, and the embedded server if this started one.
type Conn struct {
	*nats.Conn
	embedded  *server.Server
	temporary string // a store directory this made, and so has to remove
}

// Options are how a node reaches the bus.
type Options struct {
	// URL is a server to connect to. Empty starts one in this process, which
	// is what a laptop wants and what makes a single node need nothing
	// installed.
	URL string

	// Name identifies this node in the server's own reporting, which is most
	// of what makes a cluster debuggable.
	Name string

	// StoreDir is where an embedded server keeps JetStream's state: the jobs,
	// the node registry, and anything else the cluster remembers.
	//
	// Empty gets a temporary directory that is removed when the server stops,
	// which is what a test wants and what nothing durable should use. It is
	// not left to NATS's own default on purpose: that default is shared
	// between servers, so two embedded ones on a machine would silently see
	// each other's jobs.
	StoreDir string

	// Ready is how long to wait for an embedded server to come up.
	Ready time.Duration
}

// Connect reaches the bus, starting a server if no address was given.
func Connect(opts Options) (*Conn, error) {
	if opts.Ready == 0 {
		opts.Ready = 5 * time.Second
	}
	if opts.Name == "" {
		opts.Name = "scour"
	}

	if opts.URL != "" {
		conn, err := nats.Connect(opts.URL, nats.Name(opts.Name))
		if err != nil {
			return nil, fmt.Errorf("bus: %s: %w", opts.URL, err)
		}
		return &Conn{Conn: conn}, nil
	}

	store, temporary := opts.StoreDir, ""
	if store == "" {
		made, err := os.MkdirTemp("", "scour-bus-")
		if err != nil {
			return nil, fmt.Errorf("bus: %w", err)
		}
		store, temporary = made, made
	}

	// Port -1 asks the operating system for one, so two embedded servers on a
	// machine do not fight over a number.
	embedded, err := server.NewServer(&server.Options{
		ServerName:         opts.Name,
		Port:               -1,
		NoLog:              true,
		NoSigs:             true,
		JetStream:          true,
		StoreDir:           store,
		DontListen:         false,
		MaxPayload:         8 << 20,
		JetStreamMaxMemory: 256 << 20,
	})
	if err != nil {
		cleanup(temporary)
		return nil, fmt.Errorf("bus: embedded server: %w", err)
	}

	go embedded.Start()
	if !embedded.ReadyForConnections(opts.Ready) {
		embedded.Shutdown()
		cleanup(temporary)
		return nil, errors.New("bus: the embedded server did not come up")
	}

	conn, err := nats.Connect(embedded.ClientURL(), nats.Name(opts.Name))
	if err != nil {
		embedded.Shutdown()
		cleanup(temporary)
		return nil, fmt.Errorf("bus: embedded server: %w", err)
	}
	return &Conn{Conn: conn, embedded: embedded, temporary: temporary}, nil
}

// Address is where this connection points, which is what a node tells another
// node to join.
func (c *Conn) Address() string {
	if c.embedded != nil {
		return c.embedded.ClientURL()
	}
	return c.Conn.ConnectedUrl()
}

// Embedded reports whether this process is also the server.
func (c *Conn) Embedded() bool { return c.embedded != nil }

// Close drains the connection and stops the server if this started one.
//
// Drain rather than close, so a stage that is mid-request answers it. A node
// leaving a cluster should finish what it took.
func (c *Conn) Close() error {
	var problems []error

	if c.Conn != nil {
		if err := c.Conn.Drain(); err != nil {
			problems = append(problems, err)
		}
		// Drain is asynchronous, so the connection has to be given a moment to
		// finish before the server it is talking to is taken away.
		for range 100 {
			if c.Conn.IsClosed() {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		c.Conn.Close()
	}
	if c.embedded != nil {
		c.embedded.Shutdown()
		c.embedded.WaitForShutdown()
		cleanup(c.temporary)
	}
	return errors.Join(problems...)
}

func cleanup(dir string) {
	if dir != "" {
		_ = os.RemoveAll(dir)
	}
}

// noResponders turns NATS's own answer into ours, because "nothing is serving
// that stage" is a misconfiguration and a timeout is a busy cluster, and a
// node's operator needs to tell them apart.
func noResponders(err error) error {
	if errors.Is(err, nats.ErrNoResponders) {
		return ErrNoStage
	}
	return err
}
