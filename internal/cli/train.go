// SPDX-License-Identifier: GPL-3.0-or-later

package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	ucli "github.com/urfave/cli/v3"

	"github.com/rangertaha/scour/internal/cache"
	"github.com/rangertaha/scour/internal/decode"
	"github.com/rangertaha/scour/internal/train"

	_ "github.com/rangertaha/scour/internal/cache/local"
)

// Train reads the cached pages, works out how to find each property, and writes
// the locators back into the document.
//
// # Why the locators go into the document
//
// Induction is a guess, and a guess should be readable. `css = [".headline"]`
// is something a person can look at, disagree with, correct and commit; a
// weights file is something they can only retrain. It also means the crawl has
// no runtime dependency on anything this produced: the document is complete.
//
// # A correction is never overwritten
//
// What this writes is marked with a comment. Only what is marked is ever
// replaced, so a person who corrects a locator and deletes the marker has
// corrected it for good. That rule is what makes the loop converge instead of
// going in circles.
func Train(a *App) *ucli.Command {
	var (
		jobName  string
		itemName string
		dir      string
		min      float64
		write    bool
		limit    int
	)

	return &ucli.Command{
		Name:      "train",
		Usage:     "Read the cache, propose locators, write them back",
		ArgsUsage: "<document.hcl>",
		Description: "Works out how to find each property from the pages already fetched,\n" +
			"and writes a CSS selector into the document for the ones it is sure\n" +
			"enough about.\n\n" +
			"It reads the cache and never the network, so training is free,\n" +
			"repeatable and offline: the same corpus produces the same locators.\n\n" +
			"What it writes is marked with a comment. Delete the comment to keep\n" +
			"your own version of a locator, and it will never be replaced.",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "job", Usage: "which job, if the document holds several", Destination: &jobName},
			&ucli.StringFlag{Name: "item", Usage: "only this shape", Destination: &itemName},
			&ucli.StringFlag{Name: "dir", Usage: "where the cache is", Destination: &dir},
			&ucli.FloatFlag{Name: "min", Value: train.DefaultLeast * 100, Usage: "ignore a locator matching fewer than this share of pages, as a percentage", Destination: &min},
			&ucli.IntFlag{Name: "pages", Value: 200, Usage: "how many cached pages to learn from", Destination: &limit},
			&ucli.BoolFlag{Name: "write", Usage: "edit the document instead of printing what would change", Destination: &write},
		},
		Action: oneFile(func(path string) error {
			return runTrain(context.Background(), a, path, jobName, itemName, dir, min/100, limit, write)
		}),
	}
}

func runTrain(ctx context.Context, a *App, path, jobName, itemName, dir string, least float64, limit int, write bool) error {
	document, err := os.ReadFile(path)
	if err != nil {
		return Failedf("%v", err)
	}

	doc, err := Accept(path)
	if err != nil {
		return err
	}
	job, err := OneJob(doc, jobName)
	if err != nil {
		return err
	}

	// Which locators this wrote last time, which is the only thing it may
	// replace. Read from the markers in the file rather than from anywhere
	// else, so the document is the whole of the state.
	induced := train.MarkInduced(document)
	for _, item := range job.Items {
		for _, prop := range item.Properties {
			prop.Induced = induced[item.Name+"."+prop.Name]
		}
	}

	if dir == "" {
		dir = filepath.Join(filepath.Dir(path), ".scour", "cache")
	}
	pages, err := corpus(ctx, dir, limit)
	if err != nil {
		return Failedf("%v", err)
	}
	if len(pages) == 0 {
		return Invalidf("no pages in %s to learn from. Run `scour run` or `scour try` first", dir)
	}

	proposals, err := train.Learn(job, pages, train.Options{Least: least, Replace: true})
	if err != nil {
		return Failedf("%v", err)
	}

	a.Warnf("read %d cached pages\n", len(pages))
	sort.Slice(proposals, func(i, j int) bool {
		if proposals[i].Item != proposals[j].Item {
			return proposals[i].Item < proposals[j].Item
		}
		return proposals[i].Property < proposals[j].Property
	})
	kept := proposals[:0]
	for _, proposal := range proposals {
		if itemName != "" && proposal.Item != itemName {
			continue
		}
		kept = append(kept, proposal)
	}
	proposals = kept

	for _, proposal := range proposals {
		if proposal.Kept {
			a.Printf("  %-28s kept, and never replaced\n", proposal.Item+"."+proposal.Property)
			continue
		}
		a.Printf("  %-28s %-28s %d/%d pages  %s\n",
			proposal.Item+"."+proposal.Property, proposal.Selector,
			proposal.Pages, proposal.Total, short(proposal.Example))
	}

	// Printing by default and editing only when asked. This edits somebody's
	// file, and a tool that does that without being told is one people run
	// once.
	if !write {
		a.Warnf("nothing written. Pass --write to edit %s\n", path)
		return nil
	}

	written, err := train.WriteFile(path, proposals)
	if err != nil {
		return Failedf("%v", err)
	}
	a.Warnf("wrote %d locators into %s\n", written, path)
	return nil
}

// corpus reads the cached bodies, decoded.
//
// The sidecars the cache keeps beside each body are skipped: they are what makes
// a hit reconstructable and they are not pages.
func corpus(ctx context.Context, dir string, limit int) ([]train.Page, error) {
	store, err := cache.New(ctx, cache.Config{Dir: dir})
	if err != nil {
		return nil, err
	}
	defer store.Close()

	var pages []train.Page
	for key, err := range store.Keys(ctx) {
		if err != nil {
			return nil, err
		}
		if len(pages) >= limit {
			break
		}
		if isSidecar(key) {
			continue
		}

		body, err := cache.GetBytes(ctx, store, key)
		if err != nil {
			continue
		}

		// Decoded through the same function the spider uses, because the cache
		// holds what the server sent.
		text, _ := decode.Bytes(body, "")
		pages = append(pages, train.Page{URL: key, Body: text.Text})
	}
	return pages, nil
}

func isSidecar(key string) bool {
	return filepath.Ext(key) != ""
}

func short(value string) string {
	const limit = 34
	if len(value) <= limit {
		return fmt.Sprintf("%q", value)
	}
	return fmt.Sprintf("%d characters", len(value))
}
