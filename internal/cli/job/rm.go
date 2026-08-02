// SPDX-License-Identifier: GPL-3.0-or-later

package job

import (
	"context"
	"strings"

	ucli "github.com/urfave/cli/v3"

	"github.com/rangertaha/scour/internal/cli"
	"github.com/rangertaha/scour/internal/normalise"
)

// Remove drops a job, or one of its targets.
//
// The same rule as add: given only a name it acts on the job, given a member
// flag it acts on that member.
//
// The cached pages stay. They belong to the item's corpus and the next job over
// the same site should not refetch them, which is why dropping them is a flag
// rather than the default: removing a job is a common thing to do and
// refetching a site is an expensive one.
func Remove(a *cli.App) *ucli.Command {
	var domains, urls, types []string
	var force, pages bool

	return &ucli.Command{
		Name:      "rm",
		Aliases:   []string{"remove"},
		ArgsUsage: "<name>",
		Usage:     "Remove a job, or one of its targets",
		UsageText: "  scour job rm uk -d example.co.uk\n" +
			"  scour job rm uk -t pdf\n" +
			"  scour job rm uk --force\n" +
			"  scour job rm uk --force --pages    # and its cached pages",
		Flags: []ucli.Flag{
			&ucli.StringSliceFlag{Name: "domain", Aliases: []string{"d"}, Usage: "remove a domain target (repeatable)", Destination: &domains},
			&ucli.StringSliceFlag{Name: "url", Aliases: []string{"u"}, Usage: "remove a URL target (repeatable)", Destination: &urls},
			&ucli.StringSliceFlag{Name: "type", Aliases: []string{"t"}, Usage: "stop allowing a content type (repeatable)", Destination: &types},
			&ucli.BoolFlag{Name: "force", Usage: "confirm removing the whole job", Destination: &force},
			&ucli.BoolFlag{Name: "pages", Usage: "with --force, also drop the pages it cached", Destination: &pages},
		},
		Action: func(c context.Context, cmd *ucli.Command) error {
			args, err := cli.Need(cmd, 1, "one job name")
			if err != nil {
				return err
			}
			name := args[0]

			s, err := a.Store()
			if err != nil {
				return err
			}
			job, err := s.Job(c, name)
			if err != nil {
				return err
			}

			partial := len(domains) > 0 || len(urls) > 0 || len(types) > 0
			if !partial {
				if !force {
					// The size of what is about to go, because "and its
					// frontier" reads as a detail until it is a number.
					queued, err := s.QueueSize(c, job.ID)
					if err != nil {
						return err
					}
					a.Printf("this removes job %q and its frontier of %d queued urls\n", name, queued)
					a.Println("the item, its records and its model are not touched")
					a.Println("re-run with --force to confirm")
					return cli.ErrSilent
				}
				if err := s.DeleteJob(c, name); err != nil {
					return err
				}
				a.Printf("removed job %s\n", name)
				if pages {
					a.Println("the cached pages belong to the item's corpus and are kept " +
						"until `scour item rm` takes them")
				}
				return nil
			}

			for _, d := range domains {
				host, err := normalise.Domain(d)
				if err != nil {
					return err
				}
				if err := s.DeleteTarget(c, job.ID, host); err != nil {
					return err
				}
				a.Printf("%s: removed domain %s\n", name, host)
			}
			for _, u := range urls {
				normalised, err := normalise.URL(u)
				if err != nil {
					return err
				}
				if err := s.DeleteTarget(c, job.ID, normalised); err != nil {
					return err
				}
				a.Printf("%s: removed url %s\n", name, normalised)
			}
			for _, t := range types {
				if err := s.DeleteContentType(c, job.ID, t); err != nil {
					return err
				}
				a.Printf("%s: removed type %s\n", name, strings.ToLower(t))
			}
			return nil
		},
	}
}
