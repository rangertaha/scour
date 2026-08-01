// SPDX-License-Identifier: GPL-3.0-or-later

package job

import (
	ucli "github.com/urfave/cli/v3"

	"github.com/rangertaha/scour/internal/cli"
)

// Job groups everything that acts on a crawl: where to look, and running it.
//
// An item says what you are hunting and knows nothing about where it might be
// found. A job holds the targets, the policy, the frontier and the run state,
// which is what lets one item be hunted over two site sets with one of them
// paused and the other running.
//
// A job is created by the first crawl and named after the item, so nothing here
// has to be learned before crawling anything. The word earns its place when one
// item needs two different target sets.
func Job(a *cli.App) *ucli.Command {
	return cli.Group(&ucli.Command{
		Category: "MANAGE",
		Name:     "job",
		Usage:    "Define where to look, and run it",
		Description: "A job is one item, a set of targets, and a policy. Two jobs can share an\n" +
			"item, and both feed the one model, because a model belongs to the item.",
		UsageText: "  scour job ls\n" +
			"  scour job start news\n" +
			"  scour job add news -d example.com\n" +
			"  scour job pause news\n" +
			"  scour job import news --urls urls.txt",
		Commands: []*ucli.Command{
			Add(a), Remove(a), Set(a), List(a), Show(a),
			Start(a), Pause(a), Stop(a), Runs(a), Log(a),
			Config(a), Validate(a), Import(a), Export(a),
		},
	})
}
