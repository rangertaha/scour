// SPDX-License-Identifier: GPL-3.0-or-later

package cli

import (
	"context"
	"io"
	"os"
	"strings"

	"github.com/hashicorp/hcl/v2"

	ucli "github.com/urfave/cli/v3"

	"github.com/rangertaha/scour/internal/bus"
	"github.com/rangertaha/scour/internal/secret"
)

// Secret manages the credentials a job's plugins resolve.
//
// # There is no get
//
// A value goes in and is never read back out by a person. What a person can do
// is set one, list the names, and delete one; what a node can do is resolve one
// while building a plugin. A command that printed a credential would be a
// command that puts it in a terminal's scrollback.
//
// # Set reads stdin
//
// Not an argument. An argument is in the shell history and in the process
// table, where anybody on the machine can read it and where it stays after the
// terminal is closed.
func Secret(a *App) *ucli.Command {
	var (
		join    string
		keyFile string
	)

	shared := []ucli.Flag{
		&ucli.StringFlag{Name: "join", Usage: "the cluster, as nats://host:port", Destination: &join},
		&ucli.StringFlag{Name: "key-file", Usage: "the sealing key, if not in " + secret.KeyVar, Destination: &keyFile},
	}

	return &ucli.Command{
		Name:            "secret",
		HideHelpCommand: true,
		Category:        "Shared across jobs",
		Usage:           "Set, list and remove the credentials a job's plugins resolve",
		Description: "A job document holds secret(\"name\"), never a value. The value lives\n" +
			"here, sealed with a key the cluster is given rather than one it keeps,\n" +
			"and is resolved on the node that builds the plugin.\n\n" +
			"There is no way to read one back. `set` reads the value from stdin,\n" +
			"because an argument is in the shell history and in the process table.",
		Commands: []*ucli.Command{
			{
				Name:        "key",
				Usage:       "Print a new sealing key, once",
				Description: "Print it once and put it somewhere a service manager can give it to\nevery node. Losing it means every secret has to be set again.",
				Action: func(_ context.Context, cmd *ucli.Command) error {
					key, err := secret.NewKey()
					if err != nil {
						return Failedf("%v", err)
					}
					a.Printf("%s\n", key)
					return nil
				},
			},
			{
				Name:      "set",
				Usage:     "Store a secret, reading the value from stdin",
				ArgsUsage: "<name>",
				Flags:     shared,
				Action: func(ctx context.Context, cmd *ucli.Command) error {
					name := cmd.Args().First()
					if name == "" || cmd.Args().Len() > 1 {
						return Usagef("one name, and the value on stdin")
					}

					value, err := io.ReadAll(a.In)
					if err != nil {
						return Failedf("reading the value: %v", err)
					}
					value = []byte(strings.TrimRight(string(value), "\r\n"))

					store, close, err := secrets(ctx, join, keyFile)
					if err != nil {
						return err
					}
					defer close()

					if err := store.Set(ctx, name, value); err != nil {
						return Failedf("%v", err)
					}
					a.Warnf("set %s\n", name)
					return nil
				},
			},
			{
				Name:  "list",
				Usage: "List the names that have been set",
				Flags: shared,
				Action: func(ctx context.Context, cmd *ucli.Command) error {
					store, close, err := secrets(ctx, join, keyFile)
					if err != nil {
						return err
					}
					defer close()

					names, err := store.Names(ctx)
					if err != nil {
						return Failedf("%v", err)
					}
					for _, name := range names {
						a.Printf("%s\n", name)
					}
					return nil
				},
			},
			{
				Name:      "delete",
				Usage:     "Remove a secret",
				ArgsUsage: "<name>",
				Flags:     shared,
				Action: func(ctx context.Context, cmd *ucli.Command) error {
					name := cmd.Args().First()
					if name == "" || cmd.Args().Len() > 1 {
						return Usagef("one name")
					}

					store, close, err := secrets(ctx, join, keyFile)
					if err != nil {
						return err
					}
					defer close()

					if err := store.Delete(ctx, name); err != nil {
						return Failedf("%v", err)
					}
					a.Warnf("removed %s\n", name)
					return nil
				},
			},
		},
		Action: func(_ context.Context, cmd *ucli.Command) error {
			return ucli.ShowSubcommandHelp(cmd)
		},
	}
}

// DefaultServer is where the CLI looks for a cluster when nothing said.
const DefaultServer = "nats://127.0.0.1:4222"

// ServerVar sets the same thing, so a shell that already knows which cluster it
// is pointed at does not repeat itself.
const ServerVar = "SCOUR_SERVER"

// secrets opens the store, and says which of the two things is missing when it
// cannot: the cluster or the key.
//
// It never starts a broker of its own, which an earlier version did. A client
// that quietly stood up an empty cluster answered `secret ls` with nothing and
// looked exactly like a cluster whose secrets had been lost.
func secrets(ctx context.Context, join, keyFile string) (*secret.Store, func(), error) {
	key, err := secret.Key(keyFile)
	if err != nil {
		return nil, nil, Failedf("%v. Set %s or pass --key-file", err, secret.KeyVar)
	}

	conn, err := bus.Connect(bus.Options{URL: server(join), Name: "scour-cli"})
	if err != nil {
		return nil, nil, Failedf("%v", err)
	}

	store, err := secret.Open(ctx, conn, key)
	if err != nil {
		_ = conn.Close()
		return nil, nil, Failedf("%v", err)
	}
	return store, func() { _ = conn.Close() }, nil
}

// Resolver opens the cluster's secret store, if this node can.
//
// A node with no sealing key is not an error: most jobs use no secrets, and a
// node that refused to start without a key would make the feature compulsory.
// What it returns instead is nil, and [plugin.Config.Decode] turns that into a
// refusal that names the secret a job asked for.
//
// The caller closes what comes back.
func Resolver(ctx context.Context, join, keyFile string) (*hcl.EvalContext, func(), bool) {
	key, err := secret.Key(keyFile)
	if err != nil {
		return nil, func() {}, false
	}

	conn, err := bus.Connect(bus.Options{URL: server(join), Name: "scour"})
	if err != nil {
		return nil, func() {}, false
	}

	store, err := secret.Open(ctx, conn, key)
	if err != nil {
		_ = conn.Close()
		return nil, func() {}, false
	}
	return secret.Eval(ctx, store), func() { _ = conn.Close() }, true
}

// server is where a client points: the flag, then the environment, then the
// cluster last joined, then the address a single node listens on.
//
// In that order because each is more deliberate than the next. A flag is what
// somebody typed for this command, the environment is what their shell was set
// up to mean, and the remembered cluster is what they last joined and have
// probably forgotten. The default is the guess of last resort.
//
// One function, used by every command that reaches a cluster, so the rule
// cannot differ between them. It differing between them is the failure this
// shape exists to make impossible.
func server(join string) string {
	if join != "" {
		return join
	}
	if fromEnv := strings.TrimSpace(os.Getenv(ServerVar)); fromEnv != "" {
		return fromEnv
	}
	if joined := remembered(); joined != "" {
		return joined
	}
	return DefaultServer
}
