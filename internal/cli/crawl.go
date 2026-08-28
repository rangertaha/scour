// SPDX-License-Identifier: GPL-3.0-or-later

package cli

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	ucli "github.com/urfave/cli/v3"

	"github.com/rangertaha/scour/internal/engine"
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
func Crawl(a *App) *ucli.Command { return crawlCommand(a, "crawl", "Building a job") }

// crawlCommand builds the local crawl under a given name.
//
// Two names, one command. It is `scour crawl` at the top level, where it sits
// beside the other things somebody does to a document, and `scour job run`
// among the job commands, where somebody who has been driving a cluster wants
// the local equivalent without leaving the noun they are in. Building it twice
// would be two commands that drifted.
func crawlCommand(a *App, name, category string) *ucli.Command {
	var (
		jobName string
		dir     string
		verbose bool
		fresh   bool
	)

	return &ucli.Command{
		Name:      name,
		Category:  category,
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
		Action: oneFile(func(ctx context.Context, path string) error {
			return runCrawl(ctx, a, path, jobName, dir, verbose, fresh)
		}),
	}
}

// logLevel is how loudly a run reports itself.
//
// Off is a level rather than a separate path: `logging = false` means the crawl
// says nothing while it runs, and the summary at the end is printed rather than
// logged, so turning logging off never takes the result with it.
func logLevel(m *engine.Monitoring, verbose bool) slog.Level {
	if verbose {
		return slog.LevelDebug
	}
	if !m.LoggingOn() {
		// Above every level slog defines, so nothing is emitted.
		return slog.Level(64)
	}

	switch m.LogLevel() {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
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

	// The job's own monitoring block, with --verbose overriding it, because a
	// flag is what somebody types for one run and a document is what they mean
	// every run.
	//
	// Read at all because it was not: `monitoring { logging { level = "debug" } }`
	// was parsed, defaulted, validated and reported by `scour job show`, and the
	// run logged at warn regardless. A setting the document accepts and nothing
	// acts on is worse than one that does not exist, because the operator has
	// been told otherwise.
	log := slog.New(slog.NewTextHandler(a.Err, &slog.HandlerOptions{
		Level: logLevel(job.Monitoring, verbose),
	}))

	// A local crawl reaches the cluster's secret store if there is one and
	// this machine has the key. Without either, a job asking for a secret is
	// refused by name when its plugins are built.
	eval, closeSecrets, _ := Resolver(ctx, "", "")
	defer closeSecrets()

	crawl, err := run.New(ctx, job, run.Options{
		Dir:  dir,
		Log:  log,
		Eval: eval,
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
			_ = queue.Close()
			return Failedf("%v", err)
		}
		_ = queue.Close()
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

	// The summary is reported on a context of its own, because the crawl's may
	// be the reason there is something to report. On ctrl-c this asked the
	// frontier how much was left using the context that had just been
	// cancelled, so the run ended with "frontier/sqlite: len: context
	// canceled" and a non-zero exit instead of saying it had been stopped and
	// how far it got. What a person wants after pressing the key is exactly
	// that summary: how much was done, and how much the next run will take.
	after, done := context.WithTimeout(context.WithoutCancel(ctx), run.Shutdown)
	defer done()

	left, err := crawl.Waiting(after)
	if err != nil {
		return Failedf("%v", err)
	}

	// The exporters flush here, and this is the last thing that can fail.
	//
	// Left to the deferred Close above, it failed after the summary had already
	// said the records were written and after this function had returned nil.
	// A `json` exporter that cannot write its closing bracket, or a parquet one
	// that cannot write its footer, left an unreadable file behind while
	// `scour crawl` printed "exported 7000 / wrote out.json" and exited 0. A
	// pipeline reading the exit code treated the run as complete.
	//
	// Closed before the summary rather than checked after it, so that what the
	// summary claims and what is on disk cannot disagree. The deferred Close
	// above stays as the net for the paths that return early, so this one is
	// the second call on the successful path.
	//
	// Closing twice is safe because [run.Run.Close] makes it safe, not because
	// the things under it happen to be. This comment used to say "every
	// exporter's Close is idempotent", which was true and covered one of the
	// five closers: the cache was not, and an S3-backed crawl closed a bucket
	// twice on every successful run.
	if err := crawl.Close(); err != nil {
		return Failedf("%v", err)
	}

	stats := crawl.Stats()
	a.Printf("%s in %s\n", ending, elapsed.Round(time.Millisecond))
	a.Printf("  fetched   %d (%d from the cache)\n", stats.Fetched.Load(), stats.Cached.Load())
	a.Printf("  dropped   %d\n", stats.Dropped.Load())
	a.Printf("  failed    %d\n", stats.Failed.Load())
	if store := stats.Store.Load(); store > 0 {
		a.Printf("  store     %d writes the frontier refused, so some pages will be fetched again\n", store)
	}
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
