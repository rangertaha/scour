// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"strings"
	"testing"
)

func TestTagShowsAppendsAndDeletes(t *testing.T) {
	dir := t.TempDir()
	runOK(t, dir, "item", "add", "news", "-p", "author", "--alias", "byline")

	out := runOK(t, dir, "item", "tag", "news", "-p", "author")
	if !strings.Contains(out, `"byline"`) {
		t.Fatalf("the taught word should be listed:\n%s", out)
	}

	runOK(t, dir, "item", "tag", "news", "-p", "author", "--add", "written by")
	out = runOK(t, dir, "item", "tag", "news", "-p", "author")
	if !strings.Contains(out, `"written by"`) || !strings.Contains(out, `"byline"`) {
		t.Fatalf("append should add without replacing:\n%s", out)
	}

	runOK(t, dir, "item", "tag", "news", "-p", "author", "--rm", "byline")
	out = runOK(t, dir, "item", "tag", "news", "-p", "author")
	if strings.Contains(out, `"byline"`) {
		t.Fatalf("delete should have removed the word:\n%s", out)
	}
	if !strings.Contains(out, `"written by"`) {
		t.Fatalf("delete should have left the others alone:\n%s", out)
	}
}

// A phrase has to survive as one word. Aliases are rows rather than a delimited
// column for exactly this reason, so a flag that split on spaces would undo it.
func TestTagKeepsPhrasesWhole(t *testing.T) {
	dir := t.TempDir()
	runOK(t, dir, "item", "add", "vehicle", "-p", "kind")
	runOK(t, dir, "item", "tag", "vehicle", "-p", "kind", "--add", "pickup truck", "--add", "model year")

	out := runOK(t, dir, "item", "tag", "vehicle", "-p", "kind")
	if !strings.Contains(out, `"pickup truck"`) || !strings.Contains(out, `"model year"`) {
		t.Fatalf("phrases were split:\n%s", out)
	}
	if strings.Contains(out, `"pickup"`) || strings.Contains(out, `"truck"`) {
		t.Fatalf("a phrase became separate words:\n%s", out)
	}
}

func TestTagUpdateReplacesTheSet(t *testing.T) {
	dir := t.TempDir()
	runOK(t, dir, "item", "add", "news", "-p", "author", "--alias", "byline", "--alias", "reporter")

	runOK(t, dir, "item", "tag", "news", "-p", "author", "--set", "author")
	out := runOK(t, dir, "item", "tag", "news", "-p", "author")
	if strings.Contains(out, `"byline"`) || strings.Contains(out, `"reporter"`) {
		t.Fatalf("update should have replaced the whole set:\n%s", out)
	}
	if !strings.Contains(out, `"author"`) {
		t.Fatalf("update should have left what it was given:\n%s", out)
	}
}

// --update states the final set and --append/--delete change it, so together
// they name two different outcomes and the command must not pick one.
func TestTagUpdateRefusesToCombine(t *testing.T) {
	dir := t.TempDir()
	runOK(t, dir, "item", "add", "news", "-p", "author", "--alias", "byline")

	if _, err := run(t, dir, "item", "tag", "news", "-p", "author", "--set", "x", "--add", "y"); err == nil {
		t.Error("--update with --append must fail")
	}
	if _, err := run(t, dir, "item", "tag", "news", "-p", "author", "--set", "x", "--rm", "byline"); err == nil {
		t.Error("--update with --delete must fail")
	}

	out := runOK(t, dir, "item", "tag", "news", "-p", "author")
	if !strings.Contains(out, `"byline"`) {
		t.Errorf("a refused command must not have changed anything:\n%s", out)
	}
}

// Teaching is scoped so one site's vocabulary cannot overwrite another's, which
// only holds if editing a domain leaves the unscoped set alone.
func TestTagOnDomainLeavesTheDefaultAlone(t *testing.T) {
	dir := t.TempDir()
	runOK(t, dir, "item", "add", "news", "-p", "author", "--alias", "byline")
	runOK(t, dir, "item", "add", "news", "-d", "example.com", "-p", "author", "--alias", "staff reporter")

	runOK(t, dir, "item", "tag", "news", "-p", "author", "--on", "example.com", "--set", "our correspondent")

	scoped := runOK(t, dir, "item", "tag", "news", "-p", "author", "--on", "example.com")
	if !strings.Contains(scoped, `"our correspondent"`) {
		t.Errorf("the scoped set was not replaced:\n%s", scoped)
	}

	def := runOK(t, dir, "item", "tag", "news", "-p", "author")
	if !strings.Contains(def, `"byline"`) {
		t.Errorf("editing a domain changed the default:\n%s", def)
	}
	if strings.Contains(def, `"our correspondent"`) {
		t.Errorf("a domain's word leaked into the default:\n%s", def)
	}
}

func TestTagNeedsProp(t *testing.T) {
	dir := t.TempDir()
	runOK(t, dir, "item", "add", "news", "-p", "author")

	if _, err := run(t, dir, "item", "tag", "news"); err == nil {
		t.Error("tag without --prop must say so rather than guess")
	}
	if _, err := run(t, dir, "item", "tag", "news", "-p", "nosuchprop"); err == nil {
		t.Error("tagging a property that does not exist must fail")
	}
}

// Removing a word that was never there is a no-op, and saying otherwise leaves
// someone believing a crawl has stopped matching it.
func TestTagDeleteSaysWhenNothingMatched(t *testing.T) {
	dir := t.TempDir()
	runOK(t, dir, "item", "add", "news", "-p", "author", "--alias", "byline")

	out := runOK(t, dir, "item", "tag", "news", "-p", "author", "--rm", "nosuchword")
	if !strings.Contains(out, "was not tagged") {
		t.Errorf("a removal that removed nothing must say so:\n%s", out)
	}
}
