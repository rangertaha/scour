// SPDX-License-Identifier: GPL-3.0-or-later

package job

import (
	"context"
	"fmt"
	"strconv"
	"time"

	ucli "github.com/urfave/cli/v3"

	"github.com/rangertaha/scour/internal/cli"
	"github.com/rangertaha/scour/internal/store"
)

// followInterval is how often a follower asks what is new.
//
// A crawl fetches at most a few pages a second, so anything shorter is mostly
// empty queries against a database the crawl is trying to write to.
const followInterval = time.Second

// Log prints one run in detail.
//
// It defaults to the most recent, because the moment you want this is the
// moment a run ended badly and the run you mean is the one that just ended.
func Log(a *cli.App) *ucli.Command {
	var (
		run    int
		follow bool
		failed bool
	)

	return &ucli.Command{
		Name:      "log",
		ArgsUsage: "<name>",
		Usage:     "One run's detail, defaulting to the last",
		Description: "The summary of how the run went, then the pages it fetched, newest first.\n\n" +
			"--failed is the one you want after a bad run: it drops everything that\n" +
			"worked and leaves the pages that did not, with the status they came back\n" +
			"with.\n\n" +
			"An old run's pages thin out as the site is recrawled, because a page\n" +
			"belongs to the run that fetched it last. The counts in the summary are the\n" +
			"run's own and do not change.",
		UsageText: "  scour job log uk              # the last run\n" +
			"  scour job log uk --run 7\n" +
			"  scour job log uk --failed\n" +
			"  scour job log uk --follow     # watch the one that is running\n\n" +
			"Find the run first:\n" +
			"  scour job runs uk",
		Flags: []ucli.Flag{
			&ucli.IntFlag{
				Name:        "run",
				Usage:       "the `run` to read, from `scour job runs`; the last by default",
				Destination: &run,
			},
			&ucli.BoolFlag{
				Name:        "follow",
				Aliases:     []string{"f"},
				Usage:       "keep printing pages as they are fetched",
				Destination: &follow,
			},
			&ucli.BoolFlag{
				Name:        "failed",
				Usage:       "only the pages that did not come back",
				Destination: &failed,
			},
		},
		Action: func(c context.Context, cmd *ucli.Command) error {
			args, err := cli.Need(cmd, 1, "one job name")
			if err != nil {
				return err
			}
			return runLog(c, a, args[0], uint(run), follow, failed)
		},
	}
}

func runLog(c context.Context, a *cli.App, name string, id uint, follow, onlyFailed bool) error {
	s, err := a.Store()
	if err != nil {
		return err
	}
	job, err := s.Job(c, name)
	if err != nil {
		return err
	}

	var r *store.Run
	if id > 0 {
		r, err = s.RunByID(c, job.ID, id)
	} else {
		r, err = s.LastRun(c, job.ID)
	}
	if err != nil {
		return err
	}

	pages, err := s.RunPages(c, r.ID, a.Limit)
	if err != nil {
		return err
	}
	if onlyFailed {
		pages = onlyBad(pages)
	}

	if a.JSON {
		return cli.WriteJSON(a.Out(), struct {
			Run   *store.Run  `json:"run"`
			Pages []store.URL `json:"pages"`
		}{r, pages})
	}

	line := func(k, v string) { a.Printf("%-10s %s\n", k, v) }
	line("run", strconv.FormatUint(uint64(r.ID), 10))
	line("job", job.Name)
	line("started", r.StartedAt.Local().Format("2006-01-02 15:04:05"))
	line("took", took(r))
	line("how", how(r))
	line("pages", fmt.Sprintf("%d fetched, %d failed, %d skipped", r.Fetched, r.Failed, r.Skipped))
	if r.Bytes > 0 {
		line("bytes", cli.FormatBytes(r.Bytes))
	}
	if codes := statusLine(r.StatusCounts()); codes != "" {
		line("statuses", codes)
	}
	if r.Error != "" {
		// Last of the summary and unwrapped, because it is the thing being
		// looked for and wrapping it would hide the end of the message.
		line("error", r.Error)
	}

	if len(pages) == 0 {
		if onlyFailed {
			a.Printf("\nno failed pages in run %d\n", r.ID)
		} else {
			// Distinguished from "it fetched nothing", which is a different
			// problem entirely.
			a.Printf("\nno pages are still attributed to run %d", r.ID)
			if r.Fetched > 0 {
				a.Printf(", though it fetched %d: a later run has been over them", r.Fetched)
			}
			a.Println("")
		}
		return followIfAsked(c, a, s, r, follow, onlyFailed)
	}

	a.Println("")
	t := cli.NewTable(
		[]string{"WHEN", "STATUS", "CODE", "TOOK", "SIZE", "URL"},
		cli.AlignLeft, cli.AlignLeft, cli.AlignRight,
		cli.AlignRight, cli.AlignRight, cli.AlignLeft)
	for _, p := range pages {
		t.Add(pageCells(p)...)
	}
	if err := t.Render(a.Out()); err != nil {
		return err
	}
	total, err := s.RunPageCount(c, r.ID)
	if err != nil {
		return err
	}
	a.Printf("\nshowing %d of %d pages still attributed to run %d\n", len(pages), total, r.ID)
	return followIfAsked(c, a, s, r, follow, onlyFailed)
}

func pageCells(p store.URL) []string {
	when := ""
	if p.FetchedAt != nil {
		when = p.FetchedAt.Local().Format("15:04:05")
	}
	code := ""
	if p.StatusCode > 0 {
		code = strconv.Itoa(p.StatusCode)
	}
	latency := ""
	if p.Latency > 0 {
		latency = p.Latency.Round(time.Millisecond).String()
	}
	size := ""
	if p.Size > 0 {
		size = cli.FormatBytes(p.Size)
	}
	return []string{when, string(p.Status), code, latency, size, p.URL}
}

// onlyBad keeps the pages that did not come back, which is what somebody
// reading a log after a bad run is looking for.
func onlyBad(pages []store.URL) []store.URL {
	out := pages[:0]
	for _, p := range pages {
		if p.Status == store.URLFailed || p.StatusCode >= 400 {
			out = append(out, p)
		}
	}
	return out
}

// followIfAsked keeps printing pages as they arrive.
//
// It polls rather than subscribing to the bus, for the same reason the record
// follower does: a crawl on this machine writes to the database and publishes
// nothing, so a follower built on the bus would show nothing at all in the
// single-process case, which is where it is most likely to be used.
func followIfAsked(c context.Context, a *cli.App, s *store.Store,
	r *store.Run, follow, onlyFailed bool,
) error {
	if !follow {
		return nil
	}
	if r.EndedAt != nil {
		// Following a run that is over would print nothing for ever. Saying so
		// is better than looking like a hang.
		a.Printf("\nrun %d has already ended, so there is nothing to follow\n", r.ID)
		return nil
	}

	seen := map[uint]bool{}
	for _, p := range mustPages(c, s, r.ID) {
		seen[p.ID] = true
	}

	tick := time.NewTicker(followInterval)
	defer tick.Stop()
	for {
		select {
		case <-c.Done():
			// Ending because the reader asked is not a failure.
			return nil
		case <-tick.C:
		}

		pages, err := s.RunPages(c, r.ID, 0)
		if err != nil {
			return err
		}
		if onlyFailed {
			pages = onlyBad(pages)
		}
		// Oldest first here: a stream reads in the order things happened,
		// while the table above reads newest first because it is a snapshot.
		for i := len(pages) - 1; i >= 0; i-- {
			p := pages[i]
			if seen[p.ID] {
				continue
			}
			seen[p.ID] = true
			cells := pageCells(p)
			a.Printf("%-9s %-8s %4s %8s %8s %s\n",
				cells[0], cells[1], cells[2], cells[3], cells[4], cells[5])
		}

		// The run may have ended between polls, and the pages printed above
		// are the last of it.
		fresh, err := s.RunByID(c, r.JobID, r.ID)
		if err != nil {
			return err
		}
		if fresh.EndedAt != nil {
			a.Printf("\nrun %d ended: %s\n", fresh.ID, how(fresh))
			return nil
		}
	}
}

// mustPages reads a run's pages, treating a failure as none. Used only to seed
// the set a follower has already printed, where being wrong means printing a
// line twice rather than losing one.
func mustPages(c context.Context, s *store.Store, runID uint) []store.URL {
	pages, err := s.RunPages(c, runID, 0)
	if err != nil {
		return nil
	}
	return pages
}
