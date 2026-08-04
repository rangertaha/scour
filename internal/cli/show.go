// SPDX-License-Identifier: GPL-3.0-or-later

package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	ucli "github.com/urfave/cli/v3"

	"github.com/rangertaha/scour/internal/engine"
)

// Show prints what a document means once every default is filled in.
//
// Deliberately not called plan. A plan is a comparison against something
// running; this is what a document means on its own. Giving one name to both
// would mean the command changed meaning the day the server arrived.
func Show(a *App) *ucli.Command {
	var asJSON bool
	var jobName string

	return &ucli.Command{
		Name:      "show",
		Usage:     "Print the resolved job: every default filled in",
		ArgsUsage: "<document.hcl>",
		Description: "Shows what the document actually says once defaults are applied, the\n" +
			"chains in the order they will run, and the pipeline as the waves it\n" +
			"will run in. This is the form that should be stored, so a job\n" +
			"resubmitted next month behaves the way it does today.",
		Flags: []ucli.Flag{
			&ucli.BoolFlag{Name: "json", Usage: "print as JSON", Destination: &asJSON},
			&ucli.StringFlag{Name: "job", Usage: "which job, if the document holds several", Destination: &jobName},
		},
		Action: oneFile(func(path string) error {
			doc, err := Accept(path)
			if err != nil {
				return err
			}
			job, err := OneJob(doc, jobName)
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
		}),
	}
}

func (a *App) showJob(j *engine.Job) {
	a.Printf("job %q\n", j.Name)

	a.Printf("\nscope\n")
	a.field("start", strings.Join(j.Start, ", "))
	a.field("domains", strings.Join(j.Domains, ", "))
	a.field("included", strings.Join(j.Included, ", "))
	a.field("excluded", strings.Join(j.Excluded, ", "))

	a.Printf("\nscheduler\n")
	a.field("policy", j.Scheduler.OrderPolicy())
	a.field("rate", j.Scheduler.Rate)
	a.field("concurrency", fmt.Sprint(j.Scheduler.Parallelism()))
	a.field("max_depth", fmt.Sprint(j.Scheduler.Depth()))
	a.field("max_pages", budget(j.Scheduler.Pages()))
	a.field("max_time", or(j.Scheduler.MaxTime, "no limit"))
	a.chain(j, engine.StageScheduler)

	a.Printf("\ndownloader\n")
	a.field("robots", fmt.Sprint(j.Downloader.ObeysRobots()))
	a.field("user_agent", j.Downloader.Agent())
	a.field("timeout", j.Downloader.Timeout)
	a.field("max_body", fmt.Sprint(j.Downloader.BodyBytes()))
	if j.Downloader.IsExternal() {
		a.field("external", "yes, waiting "+j.Downloader.ExternalTimeout)
	}
	a.chain(j, engine.StageDownloader)

	a.Printf("\nspider\n")
	if j.Spider.IsExternal() {
		a.field("external", "yes, waiting "+j.Spider.ExternalTimeout)
	}
	a.chain(j, engine.StageSpider)

	a.Printf("\nitems\n")
	for _, item := range j.Items {
		a.Printf("  %s (%s)\n", item.Name, item.Type)
		showProperties(a, item.Properties, "    ")
	}

	a.pipeline(j)

	if len(j.Exporters) > 0 {
		a.Printf("\nexporters\n")
		for _, e := range j.Exporters {
			a.Printf("  %-10s -> %s\n", e.Format, e.Item)
		}
	}

	a.Printf("\nmutation\n")
	a.field("costly", j.Mutation.Costly)
	a.field("out_of_scope", j.Mutation.OutOfScope)
	a.field("stale_records", j.Mutation.StaleRecords)
	a.field("orphaned_cache", j.Mutation.OrphanedCache)
}

func (a *App) chain(j *engine.Job, stage engine.Stage) {
	links := j.Chain(stage)
	if len(links) == 0 {
		a.Printf("  %-14s %s\n", "chain", "empty")
		return
	}
	names := make([]string, 0, len(links))
	for _, p := range links {
		names = append(names, fmt.Sprintf("%s(%d)", p.Name, p.Order))
	}
	a.Printf("  %-14s %s\n", "chain", strings.Join(names, " -> "))
}

// pipeline prints the graph as the waves it runs in, because the reason to
// have a graph is that independent work happens at the same time, and a flat
// list hides exactly that.
func (a *App) pipeline(j *engine.Job) {
	waves, err := j.Waves()
	if err != nil {
		a.Printf("\npipeline\n  %s\n", err)
		return
	}
	if len(waves) == 0 {
		return
	}

	width, _ := j.Width()
	a.Printf("\npipeline: %d wave(s), %d at once at the widest\n", len(waves), width)
	for i, wave := range waves {
		names := make([]string, 0, len(wave))
		for _, s := range wave {
			names = append(names, s.Address())
		}
		a.Printf("  %d. %s\n", i+1, strings.Join(names, ", "))
	}
}

func showProperties(a *App, props []*engine.Property, indent string) {
	for _, p := range props {
		required := ""
		if p.Required {
			required = " required"
		}
		extra := ""
		if len(p.Transforms) > 0 {
			extra = " [" + strings.Join(p.Transforms, " ") + "]"
		}
		a.Printf("%s%s: %s%s%s\n", indent, p.Name, p.Type, required, extra)
		showProperties(a, p.Properties, indent+"  ")
	}
}

func (a *App) field(name, value string) {
	if value == "" {
		return
	}
	a.Printf("  %-14s %s\n", name, value)
}

func budget(n int) string {
	if n == 0 {
		return "no limit"
	}
	return fmt.Sprint(n)
}

func or(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
