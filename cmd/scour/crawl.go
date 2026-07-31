// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/rangertaha/scour/internal/content"
	"github.com/rangertaha/scour/internal/crawl"
	"github.com/rangertaha/scour/internal/store"
	"github.com/rangertaha/scour/internal/train"

	// Registered by import because nothing references them directly: both are
	// asked for by name, from configuration.
	_ "github.com/rangertaha/scour/internal/score/embed"
	_ "github.com/rangertaha/scour/internal/transport/webdriver"
)

type crawlFlags struct {
	depth       int
	limit       int
	maxTime     time.Duration
	types       []string
	excludeType []string
	reset       bool
	debug       bool
	bus         bool
	browser     string
}

func newStartCmd(a *app) *cli.Command {
	var f crawlFlags

	cmd := &cli.Command{
		Category:  "SEARCH",
		Name:      "start",
		Aliases:   []string{"crawl"},
		ArgsUsage: "<name>",
		Usage:     "Start a search for items",
		Description: "Follows links out from the item's targets up to a depth, caching every page\n" +
			"it keeps and ranking what it finds by how likely it is to hold a match.\n" +
			"Until a model has been trained every URL scores the same, so the first\n" +
			"search is broad by design.\n\n" +
			"A paused item is resumed: the search carries on from the frontier rather\n" +
			"than starting over. `scour stop` is what makes it start over.",
		UsageText: "  scour start vehicle --depth 3\n" +
			"  scour start vehicle --depth 3 --type html --type pdf\n" +
			"  scour start vehicle --max-pages 200",
		Flags: []cli.Flag{
			&cli.IntFlag{
				Name:        "depth",
				Usage:       "how many links deep to follow (0 for the configured default)",
				Destination: &f.depth,
			},
			&cli.IntFlag{
				Name:        "max-pages",
				Usage:       "stop after this many pages (0 for no limit)",
				Destination: &f.limit,
			},
			&cli.DurationFlag{
				Name:        "max-time",
				Usage:       "stop after this long, keeping what was fetched (0 for no limit)",
				Destination: &f.maxTime,
			},
			&cli.StringSliceFlag{
				Name:        "type",
				Usage:       "limit this crawl to a content type (repeatable)",
				Destination: &f.types,
			},
			&cli.StringSliceFlag{
				Name:        "exclude-type",
				Usage:       "skip a content type in this crawl (repeatable)",
				Destination: &f.excludeType,
			},
			&cli.BoolFlag{
				Name:        "reset",
				Usage:       "discard the existing frontier and start over",
				Destination: &f.reset,
			},
			&cli.BoolFlag{
				Name:        "debug",
				Usage:       "log colly's own request trace",
				Destination: &f.debug,
			},
			&cli.BoolFlag{
				Name:        "bus",
				Usage:       "route results through the message bus instead of writing them directly",
				Destination: &f.bus,
			},
			&cli.StringFlag{
				Name:        "browser",
				Usage:       "when to render in a browser: never, auto or always (default from config)",
				Destination: &f.browser,
			},
		},
		Action: func(c context.Context, cmd *cli.Command) error {
			args, err := need(cmd, 1, "one item name")
			if err != nil {
				return err
			}
			return runCrawl(c, a, args[0], f)
		},
	}

	// No short form: -d already means domain on add and import.

	return cmd
}

func runCrawl(c context.Context, a *app, name string, f crawlFlags) error {
	s, err := a.Store()
	if err != nil {
		return err
	}

	item, err := s.ItemFull(c, name)
	if err != nil {
		return err
	}

	// Starting a paused item resumes it. Refusing would be answering "start
	// this" with "it is paused", which is the thing being asked to change.
	if item.Paused {
		if err := s.SetPaused(c, item.ID, false); err != nil {
			return err
		}
		a.Printf("%s: resuming a paused search\n", item.Name)
	}
	// Announcing a crawl that cannot run puts the reason it failed after the
	// claim that it started.
	if len(item.Targets) == 0 {
		return fmt.Errorf("item %q has no targets: scour item add %s -d <domain>", item.Name, item.Name)
	}

	// The narrowest wins: a --type on the crawl beats the item's own
	// setting, which beats content_types in the configuration.
	allow := f.types
	if len(allow) == 0 {
		for _, t := range item.ContentTypes {
			allow = append(allow, t.Type)
		}
	}
	if len(allow) == 0 {
		allow = a.cfg.Crawl.ContentTypes
	}
	types, err := content.New(allow, f.excludeType)
	if err != nil {
		return err
	}

	if f.reset {
		if err := s.ResetFrontier(c, item.ID); err != nil {
			return err
		}
	}

	scorer, trained, err := train.Scorer(a.cfg, item)
	if err != nil {
		return err
	}

	// The chain sits on top of the per-URL scorer, crediting a link for where
	// it leads rather than only for what it says.
	scorer, chained, err := train.ChainScorer(c, s, item, scorer)
	if err != nil {
		return err
	}

	pages, err := a.Pages()
	if err != nil {
		return err
	}
	crawler := crawl.New(a.cfg, s, pages)

	// The bus path publishes results for the store service to write. It is the
	// same crawl either way; only where the results go differs.
	var settle func() error
	if f.bus {
		var err error
		crawler, settle, err = a.busCrawler(c, crawler, item.Name)
		if err != nil {
			return err
		}
		// settle is safe to call twice: the happy path calls it before reading
		// results back, and this catches the paths that return early.
		defer func() {
			if err := settle(); err != nil {
				a.Errorf("bus did not settle: %v\n", err)
			}
		}()
	}

	depth := f.depth
	if depth <= 0 {
		depth = a.cfg.Crawl.Depth
	}

	// Anything printed alongside --json would corrupt the output for whatever
	// is parsing it, so the progress line is for humans only.
	if !a.jsonOut {
		scoring := "seeded from aliases and examples"
		if trained {
			scoring = "trained model"
		}
		if chained {
			scoring += " with crawl chain"
		}
		a.Printf("crawling %s: %d targets, depth %d, types %s, scoring %s\n",
			item.Name, len(item.Targets), depth, strings.Join(types.Names(), " "), scoring)
	}

	result, err := crawler.Run(c, crawl.Options{
		Item:    item,
		Targets: item.Targets,
		Types:   types,
		Depth:   depth,
		Limit:   f.limit,
		MaxTime: f.maxTime,
		Browser: f.browser,
		Scorer:  scorer,
		Debug:   f.debug,
	})
	if err != nil {
		return err
	}

	// Wait for the writer before reading what it wrote. Publishing and writing
	// are different goroutines, and on the bus path they may be different
	// processes, so without this the summary reports a frontier it is about to
	// be given.
	if settle != nil {
		if err := settle(); err != nil {
			return err
		}
	}

	rows, err := s.FetchedURLs(c, item.ID)
	if err != nil {
		return err
	}

	if a.jsonOut {
		return writeJSON(a.Out(), rows)
	}

	a.Println()
	if err := renderFrontier(a, rows, result, a.cfg.Crawl.Rate.String(), a.limit); err != nil {
		return err
	}
	stopped := ""
	if result.BudgetSpent != "" {
		// Worth saying, and worth naming: the frontier is not exhausted, so the
		// next run has more to do, and the operator needs to know which number
		// to raise.
		if result.BudgetSpent == "pause" {
			stopped = ", paused"
		} else {
			stopped = fmt.Sprintf(", stopped on the %s budget", result.BudgetSpent)
		}
	}
	a.Printf("\n%d fetched, %d skipped, %d failed, %s in %s%s\n",
		result.Fetched, result.Skipped, result.Failed,
		formatBytes(result.Bytes), result.Elapsed.Round(time.Millisecond), stopped)

	// Pages on their own are not the point, and the next step is not guessable
	// from the ones already run.
	if result.Fetched > 0 && !a.jsonOut {
		a.Printf("\nnext: scour train %s\n", item.Name)
	}
	return nil
}

// subtree is one row of the crawl summary: everything fetched below a URL
// prefix, rolled up.
type subtree struct {
	prefix   string
	score    float64
	matches  int
	fetched  int
	latency  time.Duration
	statuses [5]int // index by status class: 1xx..5xx
}

// renderFrontier prints the crawl table. Rows are URL prefixes rather than
// single pages, because what a crawl is really reporting is which parts of a
// site paid off.
func renderFrontier(a *app, rows []store.URL, result *crawl.Result, rate string, limit int) error {
	trees := rollup(rows)
	if len(trees) == 0 {
		a.Println("nothing fetched")
		return nil
	}
	sort.Slice(trees, func(i, j int) bool {
		if trees[i].score != trees[j].score {
			return trees[i].score > trees[j].score
		}
		return trees[i].prefix < trees[j].prefix
	})
	if limit > 0 && len(trees) > limit {
		trees = trees[:limit]
	}

	seconds := result.Elapsed.Seconds()

	t := newTable(
		[]string{"PROBABILITY", "MATCHES", "SPEED", "LATENCY", "RATE", "200", "300", "400", "500", "URL"},
		alignRight, alignRight, alignRight, alignRight, alignRight,
		alignRight, alignRight, alignRight, alignRight, alignLeft,
	)
	for _, s := range trees {
		total := 0
		for _, n := range s.statuses {
			total += n
		}
		speed := "-"
		if seconds > 0 {
			speed = fmt.Sprintf("%.2f/s", float64(s.fetched)/seconds)
		}
		mean := time.Duration(0)
		if s.fetched > 0 {
			mean = s.latency / time.Duration(s.fetched)
		}
		t.add(
			fmt.Sprintf("%.2f", s.score),
			fmt.Sprintf("%d", s.matches),
			speed,
			mean.Round(time.Millisecond).String(),
			rate,
			percent(s.statuses[1], total),
			percent(s.statuses[2], total),
			percent(s.statuses[3], total),
			percent(s.statuses[4], total),
			truncate(s.prefix, 60),
		)
	}
	return t.render(a.Out())
}

// rollup aggregates fetched URLs into their directory prefixes, so a page
// contributes to every prefix above it.
func rollup(rows []store.URL) []subtree {
	byPrefix := map[string]*subtree{}

	for _, row := range rows {
		// Only pages whose body actually arrived belong in the summary; a
		// skip or a failure is reported in the counts underneath it.
		if row.Status != store.URLFetched {
			continue
		}
		for _, prefix := range prefixes(row.URL) {
			s, ok := byPrefix[prefix]
			if !ok {
				s = &subtree{prefix: prefix}
				byPrefix[prefix] = s
			}
			if row.Score > s.score {
				s.score = row.Score
			}
			s.matches += row.Matches
			s.fetched++
			s.latency += row.Latency
			if class := row.StatusCode / 100; class >= 1 && class <= 5 {
				s.statuses[class-1]++
			}
		}
	}

	out := make([]subtree, 0, len(byPrefix))
	for _, s := range byPrefix {
		out = append(out, *s)
	}
	return out
}

// prefixes returns every directory prefix of a URL, longest last, so a page at
// /a/b/c.html counts towards /a/, /a/b/ and itself. Only the directory levels
// gain a trailing slash; a leaf keeps the path it was served at.
func prefixes(rawURL string) []string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return []string{rawURL}
	}
	base := u.Scheme + "://" + u.Host
	dir := strings.HasSuffix(u.Path, "/") || u.Path == ""

	segments := strings.Split(strings.Trim(u.Path, "/"), "/")
	out := []string{base + "/"}

	acc := base
	for i, seg := range segments {
		if seg == "" {
			continue
		}
		acc += "/" + seg
		if last := i == len(segments)-1; last && !dir {
			out = append(out, acc)
			continue
		}
		out = append(out, acc+"/")
	}
	return out
}

func percent(n, total int) string {
	if total == 0 {
		return "-"
	}
	return fmt.Sprintf("%d%%", n*100/total)
}

func formatBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1fGB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1fKB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%dB", n)
	}
}
