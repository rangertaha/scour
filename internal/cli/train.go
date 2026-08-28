// SPDX-License-Identifier: GPL-3.0-or-later

package cli

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"

	"github.com/rangertaha/scour/internal/cache"
	"github.com/rangertaha/scour/internal/decode"
	"github.com/rangertaha/scour/internal/engine"
	"github.com/rangertaha/scour/internal/train"
)

// Training locators: reading the cached pages, working out how to find each
// property, and writing what it learned back into the document.
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

// trainLocators learns locators for one job from the pages already cached.
//
// It works on the document's bytes rather than on a path, because the same
// learning serves two callers that hold a document in different ways: a person
// editing a file, and the cluster holding the copy it was submitted. Writing it
// against a path meant the second had to invent a file to satisfy it.
//
// It prints what it found and returns the proposals. What to do with them is
// the caller's: a file is rewritten in place, and a submitted job is updated
// through the service that owns it.
func trainLocators(ctx context.Context, a *App, src Source, jobName, itemName, dir string,
	least float64, limit int) ([]train.Proposal, *engine.Job, error) {
	doc, err := src.Accept()
	if err != nil {
		return nil, nil, err
	}
	job, err := OneJob(doc, jobName)
	if err != nil {
		return nil, nil, err
	}

	// Which locators this wrote last time, which is the only thing it may
	// replace. Read from the markers in the document rather than from anywhere
	// else, so the document is the whole of the state.
	induced := train.MarkInduced(src.Bytes, job.Name)
	for _, item := range job.Items {
		for _, prop := range item.Properties {
			prop.Induced = induced[item.Name+"."+prop.Name]
		}
	}

	pages, err := corpus(ctx, dir, limit)
	if err != nil {
		return nil, nil, Failedf("%v", err)
	}
	if len(pages) == 0 {
		return nil, nil, Invalidf("no pages in %s to learn from. Run `scour crawl` or `scour scrape` first", dir)
	}

	proposals, err := train.Learn(job, pages, train.Options{Least: least, Replace: true})
	if err != nil {
		return nil, nil, Failedf("%v", err)
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
	return proposals, job, nil
}

// trainFile learns locators for a document on disk and writes them back into
// it.
func trainFile(ctx context.Context, a *App, path, jobName, itemName, dir string,
	least float64, limit int, write bool) error {
	src, err := FromFile(path)
	if err != nil {
		return err
	}
	if dir == "" {
		dir = filepath.Join(filepath.Dir(path), ".scour", "cache")
	}

	proposals, job, err := trainLocators(ctx, a, src, jobName, itemName, dir, least, limit)
	if err != nil {
		return err
	}

	// Printing by default and editing only when asked. This edits somebody's
	// file, and a tool that does that without being told is one people run
	// once.
	if !write {
		a.Warnf("nothing written. Pass --write to edit %s\n", path)
		return nil
	}

	written, err := train.WriteFile(path, job.Name, proposals)
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
