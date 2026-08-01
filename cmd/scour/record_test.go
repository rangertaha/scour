// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// records builds an item whose feed has been crawled and trained, which is the
// shortest route to a handful of real records to act on.
func records(t *testing.T) string {
	t.Helper()
	srv := newsroom(t)
	dir := newsDir(t, srv)

	runOK(t, dir, "start", "article", "--depth", "1")
	if out, err := run(t, dir, "model", "train", "article"); err != nil {
		t.Fatalf("model train: %v\n%s", err, out)
	}
	return dir
}

// A bare word matches any field; field:value narrows to one.
func TestRecordSearchNarrowsByField(t *testing.T) {
	dir := records(t)

	all := runOK(t, dir, "record", "search", "article", "Harbour")
	if !strings.Contains(all, "Harbour plan approved") {
		t.Errorf("a bare word found nothing:\n%s", all)
	}

	byField := runOK(t, dir, "record", "search", "article", "author:Okafor")
	if !strings.Contains(byField, "Jane Okafor") {
		t.Errorf("a field query found nothing:\n%s", byField)
	}
	if strings.Contains(byField, "Lindqvist") {
		t.Errorf("author:Okafor also matched another byline:\n%s", byField)
	}

	// A term nothing carries is an empty answer rather than everything.
	none, _ := run(t, dir, "record", "search", "article", "author:nobodyatall")
	if strings.Contains(none, "Okafor") {
		t.Errorf("an unmatched query returned records:\n%s", none)
	}
}

// The top-level shortcut is the same command, so the two spellings cannot say
// different things.
func TestSearchShortcutMatchesTheVerb(t *testing.T) {
	dir := records(t)

	viaNoun := runOK(t, dir, "record", "search", "article", "Harbour")
	viaShortcut := runOK(t, dir, "search", "article", "Harbour")
	if viaNoun != viaShortcut {
		t.Errorf("the shortcut answered differently:\n%s\n---\n%s", viaNoun, viaShortcut)
	}
}

// show is every field in full, which is what you reach for when a value looks
// wrong and you want the page it came from.
func TestRecordShowNamesItsSource(t *testing.T) {
	dir := records(t)

	out := runOK(t, dir, "record", "show", "article", "1")
	for _, want := range []string{"title", "author", "source"} {
		if !strings.Contains(out, want) {
			t.Errorf("show does not print %q:\n%s", want, out)
		}
	}
	// The source is the page it was read out of, which is the next place to
	// look when a value is wrong.
	if !strings.Contains(out, "127.0.0.1") {
		t.Errorf("show does not name the page it came from:\n%s", out)
	}

	if _, err := run(t, dir, "record", "show", "article", "9999"); err == nil {
		t.Error("showing a record that does not exist succeeded")
	}
}

// Written out, records are grouped by the domain they came from, so an export
// is diffable and a site that changed is a changed file.
func TestRecordWriteProducesFiles(t *testing.T) {
	dir := records(t)
	out := filepath.Join(dir, "export")

	runOK(t, dir, "record", "write", "article", "--format", "json", "--to", out)

	// Grouped by the domain they came from, under a directory per item, so
	// the tree is walked rather than listed.
	var wrote, files int
	err := filepath.WalkDir(out, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		files++
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(body), "Harbour plan approved") {
			wrote++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("nothing was written to %s: %v", out, err)
	}
	if files == 0 {
		t.Fatalf("%s holds no files", out)
	}
	if wrote == 0 {
		t.Errorf("none of the %d exported files carries a record", files)
	}
}

// Removing by id is exact. Removing by query is not, so it needs --force.
func TestRecordRemoveByIDAndByQuery(t *testing.T) {
	dir := records(t)

	before := runOK(t, dir, "record", "ls", "article")
	if !strings.Contains(before, "Harbour plan approved") {
		t.Fatalf("nothing to remove:\n%s", before)
	}

	runOK(t, dir, "record", "rm", "article", "1")
	if after := runOK(t, dir, "record", "ls", "article"); strings.Contains(after, "showing 3 of 3") {
		t.Errorf("the record count did not fall:\n%s", after)
	}

	// A query removes everything matching, which is why it is not the default.
	if _, err := run(t, dir, "record", "rm", "article", "author:Raman"); err == nil {
		t.Error("removing by query without --force succeeded")
	}
	runOK(t, dir, "record", "rm", "article", "author:Raman", "--force")
	if after := runOK(t, dir, "record", "ls", "article"); strings.Contains(after, "Raman") {
		t.Errorf("the queried records survived:\n%s", after)
	}
}

// Marking is what training reads back as a correction, so the verdict has to
// survive being written and listed.
func TestRecordMarkSurvivesAListing(t *testing.T) {
	dir := records(t)

	runOK(t, dir, "record", "mark", "article", "1", "--verdict", "valid")
	if out := runOK(t, dir, "record", "ls", "article", "--verdict", "valid"); !strings.Contains(out, "Harbour") {
		t.Errorf("the marked record is not in the valid listing:\n%s", out)
	}
	if out := runOK(t, dir, "record", "ls", "article", "--verdict", "invalid"); strings.Contains(out, "Harbour") {
		t.Errorf("a record marked valid appears as invalid:\n%s", out)
	}
}
