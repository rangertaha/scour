// SPDX-License-Identifier: GPL-3.0-or-later

package cli

import (
	"context"
	"encoding/json"

	ucli "github.com/urfave/cli/v3"

	"github.com/rangertaha/scour/internal/engine"
)

// Defaults prints every default and its value.
//
// The question it answers is "what does an empty job do", which is otherwise
// answerable only by reading the source.
func Defaults(a *App) *ucli.Command {
	var asJSON bool

	return &ucli.Command{
		Name:     "defaults",
		Category: "Building a job",
		Usage:    "Print every default and its value",
		Description: "Defaults are applied when a job is accepted, not when it runs, so a\n" +
			"stored job records what it will actually do rather than inheriting\n" +
			"whatever these were on the day it started.",
		Flags: []ucli.Flag{
			&ucli.BoolFlag{Name: "json", Usage: "print as JSON", Destination: &asJSON},
		},
		Action: func(context.Context, *ucli.Command) error {
			values := engine.Defaults()

			if asJSON {
				out, err := json.MarshalIndent(values, "", "  ")
				if err != nil {
					return Failedf("render: %v", err)
				}
				a.Println(string(out))
				return nil
			}

			for _, name := range engine.DefaultNames() {
				a.Printf("%-26s %s\n", name, values[name])
			}
			return nil
		},
	}
}
