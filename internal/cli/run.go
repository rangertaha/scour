// SPDX-License-Identifier: GPL-3.0-or-later

package cli

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	ucli "github.com/urfave/cli/v3"

	"github.com/rangertaha/scour/internal/frontier"
	"github.com/rangertaha/scour/internal/frontier/sqlite"
	"github.com/rangertaha/scour/internal/run"
)

// Run crawls a job here, without a server.
//
// # Why this is local
//
// The same reason `try` is. A job being developed should be runnable without
// standing up a cluster, and the whole engine fits in one process: the stages
// call each other directly, the frontier is a file, and the bodies are a
// directory. When the same job runs on a cluster it produces the same records,
// which is a claim worth being able to check by running both.
//
// # It resumes
//
// The frontier is on disk, so a crawl that was stopped, or that hit its budget,
// continues where it left off. That is why the summary says why it ended: a
// crawl that finished a site and one that ran out of budget look identical
// otherwise and mean opposite things.
func Crawl(a *App) *ucli.Command {
	var (
		jobName string
		dir     string
		verbose bool
		fresh   bool
	)

	return &ucli.Command{
		Name:      "run",
		Usage:     "Crawl a job here, without a server",
		ArgsUsage: "<document.hcl>",
		Description: "Runs the whole engine in this process: scheduler, downloader, spider,\n" +
			"pipeline and exporters, wired to each other directly.\n\n" +
			"State goes under .scour beside the document, so a crawl that was\n" +
			"stopped continues where it left off. --fresh starts over.",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "job", Usage: "which job, if the document holds several", Destination: &jobName},
			&ucli.StringFlag{Name: "dir", Usage: "where to keep the frontier and the cache", Destination: &dir},
			&ucli.BoolFlag{Name: "verbose", Aliases: []string{"v"}, Usage: "log every page", Destination: &verbose},
			&ucli.BoolFlag{Name: "fresh", Usage: "forget what a previous run queued", Destination: &fresh},
		},
		Action: oneFile(func(path string) error {
			return runCrawl(context.Background(), a, path, jobName, dir, verbose, fresh)
		}),
	}
}

func runCrawl(ctx context.Context, a *App, path, jobName, dir string, verbose, fresh bool) error {
	doc, err := Accept(path)
	if err != nil {
		return err
	}
	job, err := OneJob(doc, jobName)
	if err != nil {
		return err
	}

	if dir == "" {
		dir = filepath.Join(filepath.Dir(path), ".scour")
	}
	job = withCache(job, filepath.Dir(path))

	level := slog.LevelWarn
	if verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(a.Err, &slog.HandlerOptions{Level: level}))

	crawl, err := run.New(ctx, job, run.Options{
		Dir:  dir,
		Log:  log,
		Open: func(cfg frontier.Config) (frontier.Frontier, error) { return sqlite.Open(cfg) },
	})
	if err != nil {
		return Failedf("%v", err)
	}
	defer crawl.Close()

	if fresh {
		queue, err := sqlite.Open(frontier.Config{Dir: dir})
		if err != nil {
			return Failedf("%v", err)
		}
		if err := queue.Remove(ctx, job.Name); err != nil {
			queue.Close()
			return Failedf("%v", err)
		}
		queue.Close()
	}

	seeded, err := crawl.Seed(ctx)
	if err != nil {
		return Failedf("%v", err)
	}

	waiting, err := crawl.Waiting(ctx)
	if err != nil {
		return Failedf("%v", err)
	}
	a.Warnf("crawling %s: %d seeded, %d queued\n", job.Name, seeded, waiting)

	started := time.Now()
	ending, err := crawl.Do(ctx)
	elapsed := time.Since(started)
	if err != nil {
		return Failedf("%v", err)
	}

	left, err := crawl.Waiting(ctx)
	if err != nil {
		return Failedf("%v", err)
	}

	stats := crawl.Stats()
	a.Printf("%s in %s\n", ending, elapsed.Round(time.Millisecond))
	a.Printf("  fetched   %d (%d from the cache)\n", stats.Fetched.Load(), stats.Cached.Load())
	a.Printf("  dropped   %d\n", stats.Dropped.Load())
	a.Printf("  failed    %d\n", stats.Failed.Load())
	a.Printf("  items     %d\n", stats.Items.Load())
	a.Printf("  exported  %d\n", stats.Exported.Load())
	if left > 0 {
		a.Printf("  queued    %d still waiting, which the next run will take\n", left)
	}
	if len(job.Exporters) > 0 {
		written := make([]string, 0, len(job.Exporters))
		for _, e := range job.Exporters {
			written = append(written, e.Address())
		}
		a.Printf("  wrote     %s\n", strings.Join(written, ", "))
	}
	return nil
}
