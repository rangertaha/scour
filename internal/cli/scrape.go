// SPDX-License-Identifier: GPL-3.0-or-later

package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclparse"
	ucli "github.com/urfave/cli/v3"

	"github.com/rangertaha/scour/internal/downloader"
	"github.com/rangertaha/scour/internal/engine"
	"github.com/rangertaha/scour/internal/extract"
	"github.com/rangertaha/scour/internal/spider"
)

// Scrape fetches one page and shows what came out of it.
//
// # Why this exists
//
// It is the loop a person is in while writing a job document, and it is the
// first thing that is useful rather than merely correct. Writing extraction
// blind means guessing at a selector, running a crawl, and reading a records
// table to find out; this is that cycle, one page long.
//
// # The cache is used first, always
//
// A URL already in the cache is not fetched again, so the second run and the
// two hundredth cost nothing and the site is asked once. That is what makes it
// a development loop rather than a way to annoy somebody's server while you
// work out which class the headline has.
//
// The job's own cache plugin is used when it has one, and a directory under the
// document otherwise, because a job that has not decided where bodies live
// should still be tryable.
func Scrape(a *App) *ucli.Command {
	var (
		jobName  string
		url      string
		itemName string
		refresh  bool
		strict   bool
		asJSON   bool
	)

	return &ucli.Command{
		Name:      "scrape",
		Category:  "Building a job",
		Usage:     "Run one page and show what came out",
		ArgsUsage: "<document.hcl> [url]",
		Description: "Fetches one page, caches it, and runs it through extraction, printing\n" +
			"what each property found and where it found it.\n\n" +
			"The cache is used first, so the second run and the two hundredth ask\n" +
			"the site nothing. Use --refresh to fetch again.\n\n" +
			"Showing where each value came from is the point: a value on its own\n" +
			"does not tell you whether the locator will hold on the next page.",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "job", Usage: "which job, if the document holds several", Destination: &jobName},
			&ucli.StringFlag{Name: "url", Usage: "the page, if not given positionally", Destination: &url},
			&ucli.StringFlag{Name: "item", Usage: "only this shape", Destination: &itemName},
			&ucli.BoolFlag{Name: "refresh", Usage: "fetch even if it is cached", Destination: &refresh},
			&ucli.BoolFlag{Name: "strict", Usage: "exit non-zero if a required property found nothing", Destination: &strict},
			&ucli.BoolFlag{Name: "json", Usage: "print as JSON", Destination: &asJSON},
		},
		Action: func(ctx context.Context, cmd *ucli.Command) error {
			args := cmd.Args().Slice()
			if len(args) == 0 {
				return Usagef("no document given")
			}
			if len(args) > 2 {
				return Usagef("a document and at most one url, got %d arguments", len(args))
			}
			if len(args) == 2 {
				if url != "" {
					return Usagef("the url was given twice")
				}
				url = args[1]
			}

			return runTry(ctx, a, tryOptions{
				path:    args[0],
				job:     jobName,
				url:     url,
				item:    itemName,
				refresh: refresh,
				strict:  strict,
				json:    asJSON,
			})
		},
	}
}

type tryOptions struct {
	path    string
	job     string
	url     string
	item    string
	refresh bool
	strict  bool
	json    bool
}

func runTry(ctx context.Context, a *App, opts tryOptions) error {
	doc, err := Accept(opts.path)
	if err != nil {
		return err
	}
	job, err := OneJob(doc, opts.job)
	if err != nil {
		return err
	}

	target := opts.url
	if target == "" {
		if len(job.Start) == 0 {
			return Usagef("no url given, and job %q has no start urls", job.Name)
		}
		target = job.Start[0]
	}

	// A job with no cache of its own still gets one, beside the document,
	// because the loop this exists for is worthless if every run refetches.
	job = withCache(job, filepath.Dir(opts.path))

	fetcher, err := downloader.New(ctx, job, downloader.Options{})
	if err != nil {
		return Failedf("%v", err)
	}
	defer fetcher.Close()

	if opts.refresh {
		// Nothing to invalidate: a refresh is a fetch whose result replaces
		// whatever the key held, and the cache middleware writes on the way
		// back either way.
		a.Warnf("refreshing %s\n", target)
	}

	started := time.Now()
	resp, err := fetcher.Handle(ctx, &downloader.Request{URL: target, Job: job.Name})
	if err != nil {
		return Failedf("%v", err)
	}
	elapsed := time.Since(started)

	reader, err := spider.New(ctx, job, spider.Options{})
	if err != nil {
		return Failedf("%v", err)
	}
	defer reader.Close()

	out, err := reader.Handle(ctx, resp)
	if err != nil {
		return Failedf("%v", err)
	}

	if opts.item != "" {
		out.Items = only(out.Items, opts.item)
	}

	if opts.json {
		return printTryJSON(a, resp, out)
	}
	printScrape(a, resp, out, elapsed)

	if opts.strict {
		var missing []string
		for _, item := range out.Items {
			for _, name := range item.Missing {
				missing = append(missing, item.Name+"."+name)
			}
		}
		if len(missing) > 0 {
			return Invalidf("required properties found nothing: %s", strings.Join(missing, ", "))
		}
	}
	return nil
}

// withCache gives a job a local cache if it has not configured one.
//
// Edited on a copy: the document on disk is what somebody wrote, and a command
// that quietly rewrote it would be a command nobody could trust with a file.
func withCache(job *engine.Job, dir string) *engine.Job {
	for _, p := range job.Chain(engine.StageDownloader) {
		if p.Name == "cache" {
			return job
		}
	}

	copied := *job
	down := &engine.Downloader{}
	if job.Downloader != nil {
		down = &engine.Downloader{}
		*down = *job.Downloader
	}
	down.Plugins = append(append([]*engine.Plugin(nil), down.Plugins...), &engine.Plugin{
		Name:   "cache",
		Config: cacheBody(filepath.Join(dir, ".scour", "cache")),
	})
	copied.Downloader = down
	return &copied
}

func printScrape(a *App, resp *downloader.Response, out *spider.Output, elapsed time.Duration) {
	where := "fetched"
	if resp.Cached {
		where = "cached "
	}

	a.Printf("%s %s  %d  %s  %s", where, resp.URL, resp.Status, size(len(resp.Body)), contentType(resp))
	if resp.Cached {
		a.Printf("  (fetched %s)", resp.Fetched.Format(time.RFC3339))
	} else {
		a.Printf("  (%s)", elapsed.Round(time.Millisecond))
	}
	a.Printf("\n")

	if len(out.Items) == 0 {
		a.Printf("\nnothing matched. %d links found.\n", len(out.Links))
		return
	}

	var found, wanted int
	for _, item := range out.Items {
		a.Printf("\n%s\n", item.Name)

		names := sorted(item.Values)
		width := 0
		for _, name := range names {
			width = max(width, len(name))
		}
		for _, name := range item.Missing {
			width = max(width, len(name))
		}

		for _, name := range names {
			value := item.Values[name]
			a.Printf("  %-*s  %-34s  %s\n", width, name, quote(value.Text), value.From)
			found++
			wanted++
			for _, inner := range sorted(value.Nested) {
				nested := value.Nested[inner]
				a.Printf("    %-*s  %-32s  %s\n", width, inner, quote(nested.Text), nested.From)
			}
		}
		for _, name := range item.Missing {
			a.Printf("  %-*s  %-34s  %s\n", width, name, "-", "required, found nothing")
			wanted++
		}
	}

	a.Printf("\n%d of %d properties found. %d links.\n", found, wanted, len(out.Links))
}

func printTryJSON(a *App, resp *downloader.Response, out *spider.Output) error {
	type value struct {
		Text   string            `json:"text"`
		Raw    string            `json:"raw,omitempty"`
		From   string            `json:"from"`
		How    string            `json:"how"`
		Nested map[string]string `json:"nested,omitempty"`
	}
	type item struct {
		Name    string           `json:"name"`
		Values  map[string]value `json:"values"`
		Missing []string         `json:"missing,omitempty"`
	}

	payload := struct {
		URL    string   `json:"url"`
		Status int      `json:"status"`
		Cached bool     `json:"cached"`
		Bytes  int      `json:"bytes"`
		Spec   string   `json:"spec"`
		Items  []item   `json:"items"`
		Links  []string `json:"links"`
	}{
		URL:    resp.URL,
		Status: resp.Status,
		Cached: resp.Cached,
		Bytes:  len(resp.Body),
		Spec:   out.Spec,
	}

	for _, got := range out.Items {
		converted := item{Name: got.Name, Values: map[string]value{}, Missing: got.Missing}
		for name, v := range got.Values {
			out := value{Text: v.Text, From: v.From, How: v.How}
			if v.Raw != v.Text {
				out.Raw = v.Raw
			}
			if len(v.Nested) > 0 {
				out.Nested = map[string]string{}
				for inner, nested := range v.Nested {
					out.Nested[inner] = nested.Text
				}
			}
			converted.Values[name] = out
		}
		payload.Items = append(payload.Items, converted)
	}
	for _, link := range out.Links {
		payload.Links = append(payload.Links, link.URL)
	}

	encoded, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return Failedf("%v", err)
	}
	a.Printf("%s\n", encoded)
	return nil
}

func only(items []*extract.Item, name string) []*extract.Item {
	for _, item := range items {
		if item.Name == name {
			return []*extract.Item{item}
		}
	}
	return nil
}

func sorted[T any](m map[string]T) []string {
	out := make([]string, 0, len(m))
	for name := range m {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// quote shows a value the way a person reads one: short values in full, long
// ones as a count, because a four thousand character body pasted into a
// terminal is not information.
func quote(s string) string {
	const limit = 32

	s = strings.Join(strings.Fields(s), " ")
	if len(s) > limit {
		return fmt.Sprintf("%d characters", len(s))
	}
	return `"` + s + `"`
}

func size(n int) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f kB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	}
}

func contentType(resp *downloader.Response) string {
	ct := resp.ContentType()
	if ct == "" {
		return "no content-type"
	}
	if i := strings.Index(ct, ";"); i >= 0 {
		ct = ct[:i]
	}
	return strings.TrimSpace(ct)
}

// cacheBody builds the configuration for a cache plugin nobody wrote.
//
// Parsed from text rather than constructed, because an hcl.Body is what the
// plugin seam hands a plugin and there is no other way to make one. The plugin
// then decodes it exactly as it would decode a block somebody had written,
// which is the point: this borrows the real path rather than a second one.
func cacheBody(dir string) hcl.Body {
	src := fmt.Sprintf("dir = %q\n", dir)

	parsed, diags := hclparse.NewParser().ParseHCL([]byte(src), "try.hcl")
	if diags.HasErrors() {
		// The only input is a quoted path this built itself.
		panic("cli: the generated cache configuration does not parse: " + diags.Error())
	}
	return parsed.Body
}
