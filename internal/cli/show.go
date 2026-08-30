// SPDX-License-Identifier: GPL-3.0-or-later

package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/rangertaha/scour/internal/engine"
)

// showJob prints a resolved job the way a person reads one.
//
// The printer, not the command: `scour job show` builds the command and this
// renders what it fetched, because the same rendering serves a job read from a
// file and one read from the cluster.
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
	a.field("max_redirects", fmt.Sprint(j.Downloader.Redirects()))
	if j.Downloader.IsExternal() {
		a.field("external", waiting(j.Downloader.ExternalWait()))
	}
	a.chain(j, engine.StageDownloader)

	a.Printf("\nspider\n")
	if j.Spider.IsExternal() {
		a.field("external", waiting(j.Spider.ExternalWait()))
	}
	a.chain(j, engine.StageSpider)

	a.Printf("\nitems\n")
	for _, item := range j.Items {
		a.Printf("  %s (%s)\n", item.Name, item.ItemType())
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

// waiting is how long an external stage has to answer, resolved.
//
// The accessor and not the raw field. A stage that said `external = true` and
// left the timeout to its default printed "yes, waiting " with nothing after
// it, which reads as a stage with no timeout at all - the opposite of what the
// document means.
func waiting(d time.Duration, err error) string {
	if err != nil {
		return "yes, and the timeout cannot be read: " + err.Error()
	}
	return "yes, waiting " + d.String()
}

// pipeline prints the graph as the waves it runs in, because the reason to
// have a graph is that independent work happens at the same time, and a flat
// list hides exactly that.
func (a *App) pipeline(j *engine.Job) {
	// Said before the waves, because it is the thing about this pipeline that
	// changes what happens rather than a detail of how it is ordered. It is
	// refused by validation, so a document showing it here arrived some other
	// way, and `scour job show` is how somebody finds out why it will not run.
	if j.Pipeline.IsExternal() {
		a.Printf("\npipeline\n")
		a.field("external", waiting(j.Pipeline.ExternalWait())+
			", and nothing serves an external pipeline")
	}

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
		a.Printf("%s%s: %s%s%s\n", indent, p.Name, p.PropertyType(), required, extra)
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
