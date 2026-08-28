// SPDX-License-Identifier: GPL-3.0-or-later

package cli

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"time"

	ucli "github.com/urfave/cli/v3"

	"github.com/rangertaha/scour/internal/bus"
	"github.com/rangertaha/scour/internal/train"
)

// Job is everything somebody does to a job.
//
// # Why these are one command and not fifteen
//
// Because they are all the same noun, and a flat tree of `scour job-start`,
// `scour job-status`, `scour job-pause` is a list somebody reads to find the
// three commands they wanted. The tree is flat everywhere a second noun would
// have added a word and distinguished nothing; here the noun is the whole
// point, because a job is the thing this tool is about.
//
// # Two halves, and they take different arguments
//
// The document half takes a file: `init`, `valid`, `create`, `update` and `run`
// are what somebody does while writing a job, and what they have is a path. The
// cluster half takes a name: `start`, `stop`, `status` and the rest act on a job
// the cluster already holds, and the name is its identity there.
//
// A command that accepted either would have to guess which it had been given,
// and would guess wrong on the day somebody names a job after a file.
//
// # Everything in the cluster half goes through the job service
//
// Not through the bucket. The service is the only writer, because a submission
// has to be parsed, validated and reviewed against what is already running, and
// a client doing that for itself is a client that will do it differently. See
// [bus.Controller].
func Job(a *App) *ucli.Command {
	var (
		join     string
		dir      string
		file     string
		itemName string
		fresh    bool
		write    bool
		asJSON   bool
		least    float64
		limit    int
	)

	cluster := func() []ucli.Flag {
		return []ucli.Flag{
			&ucli.StringFlag{Name: "join", Usage: "the cluster, as nats://host:port", Destination: &join},
		}
	}

	// A name-taking command, which is most of this half.
	acts := func(category, name, usage, description string, act func(context.Context, *bus.ControlClient, string) error) *ucli.Command {
		return &ucli.Command{
			Name:        name,
			Category:    category,
			Usage:       usage,
			ArgsUsage:   "<job>",
			Description: description,
			Flags:       cluster(),
			Action: oneName(func(ctx context.Context, job string) error {
				return withControl(ctx, join, func(ctx context.Context, jobs *bus.ControlClient) error {
					return act(ctx, jobs, job)
				})
			}),
		}
	}

	return &ucli.Command{
		Name:            "job",
		HideHelpCommand: true,
		Category:        "Building a job",
		Usage:           "Manage jobs through the job service",
		ArgsUsage:       "<command>",
		Description: "A job document says what to crawl and what to pull out of it. These\n" +
			"commands write one, submit it to a cluster, and run it there.\n\n" +
			"The document commands take a file: init, valid, create, update, run.\n" +
			"The cluster commands take a job's name: start, stop, status and the\n" +
			"rest. The cluster is --join, then " + ServerVar + ", then whatever\n" +
			"`scour cluster join` last remembered.\n\n" +
			"Start with `scour job init > job.hcl`.",
		Commands: []*ucli.Command{
			// In the order somebody meets them: write a document, check it,
			// submit it, run it, watch it, look at it.
			Init(a),
			Validate(a),
			{
				Name:      "train",
				Category:  "Authoring a document",
				Usage:     "Read the cache, propose locators, write them back",
				ArgsUsage: "<job>",
				Description: "Works out how to find each property from the pages already fetched,\n" +
					"and writes a CSS selector in for the ones it is sure enough about.\n\n" +
					"It reads the cache and never the network, so training is free,\n" +
					"repeatable and offline. --dir is where those pages are: the cluster's\n" +
					"cache, which is the one the nodes write to.\n\n" +
					"What it writes is marked with a comment. Delete the comment to keep\n" +
					"your own version of a locator, and it will never be replaced.\n\n" +
					"With --write it resubmits the job, which is an update like any\n" +
					"other: a running job whose mutation policy refuses the change keeps\n" +
					"the revision it started with.",
				Flags: append(cluster(),
					&ucli.StringFlag{Name: "file", Usage: "train a document instead of a submitted job", Destination: &file},
					&ucli.StringFlag{Name: "item", Usage: "only this shape", Destination: &itemName},
					&ucli.StringFlag{Name: "dir", Usage: "where the cached pages are", Destination: &dir},
					&ucli.FloatFlag{Name: "min", Value: train.DefaultLeast * 100, Usage: "ignore a locator matching fewer than this share of pages, as a percentage", Destination: &least},
					&ucli.IntFlag{Name: "pages", Value: 200, Usage: "how many cached pages to learn from", Destination: &limit},
					&ucli.BoolFlag{Name: "write", Usage: "write the locators back instead of printing what would change", Destination: &write}),
				Action: func(ctx context.Context, cmd *ucli.Command) error {
					if file != "" {
						return trainFile(ctx, a, file, cmd.Args().First(), itemName, dir, least/100, limit, write)
					}
					return trainJob(ctx, a, join, cmd.Args().First(), itemName, dir, least/100, limit, write)
				},
			},
			crawlCommand(a, "run", "Authoring a document"),
			{
				Name:      "create",
				Category:  "In the cluster",
				Usage:     "Submit a job the cluster does not have yet",
				ArgsUsage: "<document.hcl>",
				Description: "Validates the document and stores it. A name already taken is\n" +
					"refused rather than replaced: use update.\n\n" +
					"Creating a job does not start it. `scour job start <job>` does.",
				Flags: cluster(),
				Action: oneFile(func(ctx context.Context, path string) error {
					return submit(ctx, a, join, path, false)
				}),
			},
			{
				Name:        "list",
				Category:    "In the cluster",
				Usage:       "Every job the cluster has, and what it is doing",
				Flags:       cluster(),
				Description: "One line per job: its phase, the revision running, and who is driving it.",
				Action: func(ctx context.Context, cmd *ucli.Command) error {
					if cmd.Args().Len() > 0 {
						return Usagef("list takes no arguments, got %q", cmd.Args().First())
					}
					return withControl(ctx, join, func(ctx context.Context, jobs *bus.ControlClient) error {
						listed, err := jobs.List(ctx)
						if err != nil {
							return serviceError(err)
						}
						return a.printJobs(listed)
					})
				},
			},
			{
				Name:      "show",
				Category:  "In the cluster",
				Usage:     "Print the resolved job: every default filled in",
				ArgsUsage: "<job>",
				Description: "What the document actually says once defaults are applied, the\n" +
					"chains in the order they will run, and the pipeline as the waves it\n" +
					"will run in.",
				Flags: append(cluster(),
					&ucli.StringFlag{Name: "file", Usage: "read a document instead of asking the cluster", Destination: &file},
					&ucli.BoolFlag{Name: "json", Usage: "print as JSON", Destination: &asJSON}),
				Action: func(ctx context.Context, cmd *ucli.Command) error {
					document, source, err := documentOf(ctx, join, file, cmd.Args().First())
					if err != nil {
						return err
					}
					doc, err := AcceptBytes(document, source)
					if err != nil {
						return err
					}
					job, err := OneJob(doc, cmd.Args().First())
					if err != nil {
						return err
					}
					resolved := job.Resolved()

					if asJSON {
						out, err := json.MarshalIndent(resolved, "", "  ")
						if err != nil {
							return Failedf("render: %v", err)
						}
						a.Println(string(out))
						return nil
					}
					a.showJob(resolved)
					return nil
				},
			},
			{
				Name:      "spec",
				Category:  "In the cluster",
				Usage:     "Print the extraction spec a spider is handed",
				ArgsUsage: "<job>",
				Description: "A spider is given the shapes to extract and nothing else: not where\n" +
					"bodies are cached, not the budget, not the exporters. This prints\n" +
					"that, as the HCL a person would have written.\n\n" +
					"On stdout and nothing else, so it can be redirected to a file.",
				Flags: append(cluster(),
					&ucli.StringFlag{Name: "file", Usage: "read a document instead of asking the cluster", Destination: &file}),
				Action: func(ctx context.Context, cmd *ucli.Command) error {
					document, source, err := documentOf(ctx, join, file, cmd.Args().First())
					if err != nil {
						return err
					}
					doc, err := AcceptBytes(document, source)
					if err != nil {
						return err
					}

					// The argument names the job either way: in the cluster it
					// is the job's identity, and in a --file document holding
					// several it says which. One argument, one meaning, rather
					// than a positional for one source and a --job flag for
					// the other.
					job, err := OneJob(doc, cmd.Args().First())
					if err != nil {
						return err
					}

					spec := job.Spec()
					a.Warnf("fingerprint %s\n", spec.Fingerprint())
					a.Printf("%s", spec.HCL())
					return nil
				},
			},
			{
				Name:      "update",
				Category:  "In the cluster",
				Usage:     "Resubmit a job the cluster already has",
				ArgsUsage: "<document.hcl>",
				Description: "Replaces the stored document. When the job is running, the change\n" +
					"is read through its `mutation` policy first, and one the policy\n" +
					"refuses leaves the running revision alone.\n\n" +
					"A running job keeps running on the revision it started with. Stop\n" +
					"and start it to pick the new one up.",
				Flags: cluster(),
				Action: oneFile(func(ctx context.Context, path string) error {
					return submit(ctx, a, join, path, true)
				}),
			},
			acts("In the cluster", "delete", "Remove a job, stopping it first",
				"Stops the crawl if it is running, then forgets the document and\n"+
					"everything recorded about it. The frontier on disk is left where it\n"+
					"is, so a job recreated under the same name resumes rather than\n"+
					"starting over. Start it fresh if that is not what you want.",
				func(ctx context.Context, jobs *bus.ControlClient, name string) error {
					if err := jobs.Delete(ctx, name); err != nil {
						return serviceError(err)
					}
					a.Printf("deleted %s\n", name)
					return nil
				}),
			{
				Name:      "start",
				Category:  "Running a crawl",
				Usage:     "Start a job's crawl",
				ArgsUsage: "<job>",
				Description: "Seeds the frontier from the job's start URLs and drives the crawl on\n" +
					"the cluster: this machine asks, and the nodes fetch and read.\n\n" +
					"The frontier survives, so starting a job that was stopped carries on\n" +
					"from where it was. --fresh forgets what a previous run queued.",
				Flags: append(cluster(),
					&ucli.BoolFlag{Name: "fresh", Usage: "forget what a previous run queued", Destination: &fresh}),
				Action: oneName(func(ctx context.Context, name string) error {
					return withControl(ctx, join, func(ctx context.Context, jobs *bus.ControlClient) error {
						status, err := jobs.Start(ctx, name, fresh)
						if err != nil {
							return serviceError(err)
						}
						a.printJobStatus(status)
						return nil
					})
				}),
			},
			acts("Running a crawl", "stop", "Stop a job's crawl, keeping the frontier",
				"The pages already in flight are finished rather than abandoned, so\n"+
					"this takes as long as the last fetch. The frontier is kept: start it\n"+
					"again and it carries on.",
				func(ctx context.Context, jobs *bus.ControlClient, name string) error {
					status, err := jobs.Stop(ctx, name)
					if err != nil {
						return serviceError(err)
					}
					a.printJobStatus(status)
					return nil
				}),
			acts("Running a crawl", "pause", "Pause a job's crawl",
				"Stop, with the intention recorded. The loop ends the same way and the\n"+
					"frontier is kept either way; what pause adds is that `resume` knows\n"+
					"to carry on rather than to seed again.",
				func(ctx context.Context, jobs *bus.ControlClient, name string) error {
					status, err := jobs.Pause(ctx, name)
					if err != nil {
						return serviceError(err)
					}
					a.printJobStatus(status)
					return nil
				}),
			acts("Running a crawl", "resume", "Carry a paused job on",
				"Starts the crawl again without seeding it, so the start URLs are not\n"+
					"re-queued and the count of what was found stays honest.",
				func(ctx context.Context, jobs *bus.ControlClient, name string) error {
					status, err := jobs.Resume(ctx, name)
					if err != nil {
						return serviceError(err)
					}
					a.printJobStatus(status)
					return nil
				}),
			acts("Running a crawl", "status", "What a job is doing", "",
				func(ctx context.Context, jobs *bus.ControlClient, name string) error {
					status, err := jobs.Status(ctx, name)
					if err != nil {
						return serviceError(err)
					}
					a.printJobStatus(status)
					return nil
				}),
			acts("Running a crawl", "stats", "How far a job's crawl has got",
				"The counters of the run in progress. A job that is not running\n"+
					"reports what is left in its frontier and nothing else, because the\n"+
					"counters belong to a run and the run is over.",
				func(ctx context.Context, jobs *bus.ControlClient, name string) error {
					stats, err := jobs.Stats(ctx, name)
					if err != nil {
						return serviceError(err)
					}
					a.printJobStats(name, stats)
					return nil
				}),
			{
				Name:      "watch",
				Category:  "Running a crawl",
				Usage:     "Follow a job's execution as it happens",
				ArgsUsage: "<job>",
				Description: "Prints what the driver reports: progress while it crawls, and the\n" +
					"phase it ends in. It runs until the job stops or until interrupted,\n" +
					"and watching costs the crawl nothing.\n\n" +
					"A job that is not running yet is waited for, so this can be started\n" +
					"first and the job started from somewhere else.",
				Flags: cluster(),
				Action: oneName(func(ctx context.Context, name string) error {
					return watchJob(ctx, a, join, name)
				}),
			},
		},
	}
}

// withControl reaches the job service and hands it to one command.
//
// The connection is opened per command and closed after it, because these are
// one-shot: a command line that held a connection open would be a daemon.
func withControl(ctx context.Context, join string, fn func(context.Context, *bus.ControlClient) error) error {
	conn, err := bus.Connect(bus.Options{URL: server(join), Name: "scour"})
	if err != nil {
		return Failedf("%v", err)
	}
	defer func() { _ = conn.Close() }()

	return fn(ctx, conn.NewControl(0))
}

// serviceError gives an error from the job service the right exit code.
//
// A refusal and an unreachable cluster are different things, and the exit codes
// say which: a script retrying a job the cluster rejected would retry forever,
// and one giving up on a cluster that was merely restarting would give up too
// early. [bus.Answered] is what tells them apart.
func serviceError(err error) error {
	if bus.Answered(err) {
		return Invalidf("%v", err)
	}
	return Failedf("%v", err)
}

// submit creates or updates a job from a document on disk.
func submit(ctx context.Context, a *App, join, path string, update bool) error {
	// Read and validated here as well as in the service, and that is not
	// duplication worth removing: a document refused locally is refused
	// without a cluster and with the file's own name in the message, which is
	// what somebody writing one wants. The service checks again because it
	// cannot trust a client to have.
	document, err := os.ReadFile(path)
	if err != nil {
		return Failedf("%v", err)
	}
	if _, err := AcceptBytes(document, path); err != nil {
		return err
	}

	return withControl(ctx, join, func(ctx context.Context, jobs *bus.ControlClient) error {
		var status bus.JobStatus
		var err error
		if update {
			status, err = jobs.Update(ctx, document)
		} else {
			status, err = jobs.Create(ctx, document)
		}
		if err != nil {
			return serviceError(err)
		}

		what := "created"
		if update {
			what = "updated"
		}
		a.Printf("%s %s at revision %d\n", what, status.Name, status.Revision)
		if status.Stale() {
			a.Printf("  it is still running revision %d. Stop and start it to pick this up\n",
				status.State.Revision)
		}
		return nil
	})
}

// documentOf is the document a command should work on: a file when one was
// named, and the cluster's copy otherwise.
func documentOf(ctx context.Context, join, file, name string) ([]byte, string, error) {
	if file != "" {
		document, err := os.ReadFile(file)
		if err != nil {
			return nil, "", Failedf("%v", err)
		}
		return document, file, nil
	}
	if name == "" {
		return nil, "", Usagef("no job named, and no --file given")
	}

	var document []byte
	err := withControl(ctx, join, func(ctx context.Context, jobs *bus.ControlClient) error {
		got, err := jobs.Document(ctx, name)
		if err != nil {
			return serviceError(err)
		}
		document = got
		return nil
	})
	return document, name, err
}

// trainJob learns locators for a submitted job and, with --write, resubmits it.
func trainJob(ctx context.Context, a *App, join, name, itemName, dir string,
	least float64, limit int, write bool) error {
	if name == "" {
		return Usagef("no job named")
	}
	if dir == "" {
		dir = ".scour/cache"
	}

	document, _, err := documentOf(ctx, join, "", name)
	if err != nil {
		return err
	}

	proposals, job, err := trainLocators(ctx, a, document, name, name, itemName, dir, least, limit)
	if err != nil {
		return err
	}

	if !write {
		a.Warnf("nothing written. Pass --write to update %s\n", name)
		return nil
	}

	edited, written, err := train.Write(document, job.Name, proposals)
	if err != nil {
		return Failedf("%v", err)
	}
	if written == 0 {
		a.Warnf("no locators to write, so %s is unchanged\n", name)
		return nil
	}

	return withControl(ctx, join, func(ctx context.Context, jobs *bus.ControlClient) error {
		status, err := jobs.Update(ctx, edited)
		if err != nil {
			return serviceError(err)
		}
		a.Warnf("wrote %d locators into %s, now at revision %d\n", written, name, status.Revision)
		return nil
	})
}

// watchJob follows one job until it stops or the watcher is interrupted.
func watchJob(ctx context.Context, a *App, join, name string) error {
	conn, err := bus.Connect(bus.Options{URL: server(join), Name: "scour"})
	if err != nil {
		return Failedf("%v", err)
	}
	defer func() { _ = conn.Close() }()

	// Subscribed before the status is read, so a job that ends in between is
	// not missed. The other order is a watcher that starts, is told the job is
	// running, and then waits forever for an event that has already been sent.
	watching, cancel := context.WithCancel(ctx)
	defer cancel()

	events, stop, err := conn.WatchJob(watching, name)
	if err != nil {
		return Failedf("%v", err)
	}
	defer func() { _ = stop() }()

	status, err := conn.NewControl(0).Status(ctx, name)
	if err != nil {
		return serviceError(err)
	}
	a.printJobStatus(status)

	for {
		select {
		case <-ctx.Done():
			return nil
		case event, open := <-events:
			if !open {
				return nil
			}

			at := event.At.Format(time.TimeOnly)
			if event.Message != "" {
				a.Printf("%s  %-8s %s\n", at, event.Phase, event.Message)
			} else {
				s := event.Stats
				a.Printf("%s  %-8s fetched %d  items %d  exported %d  queued %d\n",
					at, event.Phase, s.Fetched, s.Items, s.Exported, s.Waiting)
			}

			// The watch ends when the execution does. A job that is started
			// again is a new thing to watch, and a watcher that stayed would
			// be reporting on a run its reader stopped caring about.
			if !event.Phase.Live() {
				return nil
			}
		}
	}
}

// printJobs writes one line per job.
func (a *App) printJobs(jobs []bus.JobStatus) error {
	if len(jobs) == 0 {
		a.Printf("no jobs. Submit one with `scour job create <document.hcl>`\n")
		return nil
	}

	a.Printf("%-24s %-9s %-9s %-22s %s\n", "NAME", "PHASE", "REVISION", "SINCE", "DRIVER")
	for _, job := range jobs {
		revision := itoa(job.Revision)
		if job.Stale() {
			// The running revision beside the submitted one, because a job
			// resubmitted while it crawls is the case where the two differ and
			// the difference is the thing worth seeing.
			revision = itoa(job.State.Revision) + "<" + itoa(job.Revision)
		}
		a.Printf("%-24s %-9s %-9s %-22s %s\n",
			job.Name, job.State.Phase, revision, when(job.State.Since), job.State.Driver)
	}
	return nil
}

// printJobStatus writes what one job is doing.
func (a *App) printJobStatus(status bus.JobStatus) {
	a.Printf("%s is %s\n", status.Name, status.State.Phase)
	a.Printf("  since     %s\n", when(status.State.Since))
	a.Printf("  revision  %s\n", itoa(status.Revision))

	if status.Stale() {
		a.Printf("  running   revision %s, which is not the one submitted\n", itoa(status.State.Revision))
	}
	if status.State.Driver != "" {
		a.Printf("  driver    %s\n", status.State.Driver)
	}
	// Only for a crawl that ended on its own. Everything else already said
	// how it ended in the phase, and "is paused / ending stopped" reads as two
	// answers to one question: the ending under a pause is always the caller
	// cancelling the loop, which is what pause is.
	if status.State.Phase == bus.PhaseDone && status.State.Ending != "" {
		a.Printf("  ending    %s\n", status.State.Ending)
	}
	if status.State.Error != "" {
		a.Printf("  error     %s\n", status.State.Error)
	}
}

// printJobStats writes how far a crawl has got.
func (a *App) printJobStats(name string, stats bus.JobStats) {
	a.Printf("%s\n", name)
	if stats.Elapsed > 0 {
		a.Printf("  elapsed   %s\n", stats.Elapsed.Round(time.Second))
	}
	a.Printf("  fetched   %d (%d from the cache)\n", stats.Fetched, stats.Cached)
	a.Printf("  dropped   %d\n", stats.Dropped)
	a.Printf("  failed    %d\n", stats.Failed)
	a.Printf("  items     %d\n", stats.Items)
	a.Printf("  exported  %d\n", stats.Exported)
	if stats.Lost > 0 {
		a.Printf("  lost      %d URLs a page found that could not be queued\n", stats.Lost)
	}
	if stats.Store > 0 {
		a.Printf("  store     %d writes the frontier refused, so some pages will be fetched again\n", stats.Store)
	}
	a.Printf("  queued    %d still waiting\n", stats.Waiting)
}

// when renders a time, or a dash for one that was never set.
//
// A zero time printed as 0001-01-01 is a date nobody means, and a column of
// them is worse than a column of dashes: it reads as data.
func when(at time.Time) string {
	if at.IsZero() {
		return "-"
	}
	return at.UTC().Format(time.RFC3339)
}

// itoa renders a revision, or a dash for one there is not.
//
// Revisions start at 1, so zero is "there is no revision here" rather than a
// number, and printing it as 0 would put a revision nobody can look up into a
// column of ones they can.
func itoa(v uint64) string {
	if v == 0 {
		return "-"
	}
	return strconv.FormatUint(v, 10)
}
