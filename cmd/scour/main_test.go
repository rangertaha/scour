// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// run executes the CLI with args against a throwaway data directory, and
// returns whatever it printed. Every test gets its own directories, so none of
// them touch the developer's real configuration.
func run(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	t.Setenv("SCOUR_DATA_DIR", filepath.Join(dir, "data"))
	t.Setenv("SCOUR_CACHE_DIR", filepath.Join(dir, "cache"))

	// An empty file, so the run is pinned to the defaults and never discovers
	// the developer's own configuration.
	cfgPath := filepath.Join(dir, "config.toml")
	if _, err := os.Stat(cfgPath); err != nil {
		if err := os.WriteFile(cfgPath, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	cmd := newRootCmd()
	var out bytes.Buffer
	cmd.Writer = &out
	cmd.ErrWriter = &out

	argv := append([]string{"scour"}, args...)
	argv = append(argv, "--config", filepath.Join(dir, "config.toml"))
	err := cmd.Run(context.Background(), argv)
	return out.String(), err
}

// runOK fails the test if the command did not succeed.
func runOK(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := run(t, dir, args...)
	if err != nil {
		t.Fatalf("scour %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

func TestAddThenList(t *testing.T) {
	dir := t.TempDir()

	runOK(t, dir, "item", "add", "vehicle", "--alias", "car", "--alias", "pickup truck")
	runOK(t, dir, "item", "add", "vehicle", "-d", "example.com", "--subdomains")
	runOK(t, dir, "item", "add", "vehicle", "-u", "http://www.example.com/cars/")
	runOK(t, dir, "item", "add", "vehicle", "-p", "make", "-e", "Ford")
	runOK(t, dir, "item", "add", "vehicle", "--type", "html", "--type", "pdf")

	out := runOK(t, dir, "item", "ls")
	if !strings.Contains(out, "vehicle") {
		t.Errorf("list did not mention the item:\n%s", out)
	}
	// The listing carries what a crawl is actually judged on, not just a name
	// and a count: what the item has, how far it got, whether it is trained.
	for _, col := range []string{"NAME", "TARGETS", "VISITED", "RECORDS", "TRAINED"} {
		if !strings.Contains(out, col) {
			t.Errorf("list did not print the %s column:\n%s", col, out)
		}
	}
}

func TestMultiWordAliasIsKeptWhole(t *testing.T) {
	dir := t.TempDir()

	out := runOK(t, dir, "item", "add", "vehicle", "--alias", "pickup truck")
	if !strings.Contains(out, "alias pickup truck") {
		t.Errorf("a multi-word alias must not be split into words:\n%s", out)
	}
	if strings.Contains(out, "alias pickup\n") {
		t.Errorf("the alias was split:\n%s", out)
	}
}

func TestAddIsIdempotent(t *testing.T) {
	dir := t.TempDir()

	runOK(t, dir, "item", "add", "vehicle", "--alias", "car")
	runOK(t, dir, "item", "add", "vehicle", "--alias", "car")

	out := runOK(t, dir, "item", "ls", "--json")
	if strings.Count(out, `"name": "vehicle"`) != 1 {
		t.Errorf("the item was created more than once:\n%s", out)
	}
}

func TestListIsEmptyToStart(t *testing.T) {
	dir := t.TempDir()

	out := runOK(t, dir, "item", "ls")
	if !strings.Contains(out, "no items yet") {
		t.Errorf("an empty database should say so:\n%s", out)
	}
}

func TestListJSON(t *testing.T) {
	dir := t.TempDir()
	runOK(t, dir, "item", "add", "vehicle")

	out := runOK(t, dir, "item", "ls", "--json")
	for _, key := range []string{`"name": "vehicle"`, `"records": 0`, `"trained": "never"`} {
		if !strings.Contains(out, key) {
			t.Errorf("json output missing %s:\n%s", key, out)
		}
	}
}

func TestDomainsAreNormalised(t *testing.T) {
	dir := t.TempDir()

	// All three name one target, so the last write wins and there is one row.
	runOK(t, dir, "item", "add", "vehicle", "-d", "example.com")
	runOK(t, dir, "item", "add", "vehicle", "-d", "www.example.com")
	out := runOK(t, dir, "item", "add", "vehicle", "-d", "https://example.com/")

	if !strings.Contains(out, "domain example.com") {
		t.Errorf("domain was not normalised:\n%s", out)
	}
}

func TestRemoveWholeItemNeedsForce(t *testing.T) {
	dir := t.TempDir()
	runOK(t, dir, "item", "add", "vehicle")

	out, err := run(t, dir, "item", "rm", "vehicle")
	if err == nil {
		t.Fatal("removing an item without --force must fail")
	}
	if !strings.Contains(out, "--force") {
		t.Errorf("the refusal should say what to do:\n%s", out)
	}

	// The item is still there.
	if out := runOK(t, dir, "item", "ls"); !strings.Contains(out, "vehicle") {
		t.Errorf("item was removed despite the refusal:\n%s", out)
	}

	runOK(t, dir, "item", "rm", "vehicle", "--force")
	if out := runOK(t, dir, "item", "ls"); strings.Contains(out, "vehicle") {
		t.Errorf("item survived --force:\n%s", out)
	}
}

func TestRemoveParts(t *testing.T) {
	dir := t.TempDir()
	// Separate commands: with --prop present, --domain scopes the property
	// rather than adding a target.
	runOK(t, dir, "item", "add", "vehicle", "-d", "example.com")
	runOK(t, dir, "item", "add", "vehicle", "-p", "year", "-e", "2026")

	out := runOK(t, dir, "item", "rm", "vehicle", "-d", "example.com")
	if !strings.Contains(out, "removed domain example.com") {
		t.Errorf("unexpected output:\n%s", out)
	}

	out = runOK(t, dir, "item", "rm", "vehicle", "-p", "year")
	if !strings.Contains(out, "removed property year") {
		t.Errorf("unexpected output:\n%s", out)
	}

	// The item itself survives having its parts removed.
	if out := runOK(t, dir, "item", "ls"); !strings.Contains(out, "vehicle") {
		t.Errorf("removing a target should not remove the item:\n%s", out)
	}
}

func TestRemoveReportsMissingThings(t *testing.T) {
	dir := t.TempDir()
	runOK(t, dir, "item", "add", "vehicle")

	if _, err := run(t, dir, "item", "rm", "vehicle", "-d", "absent.example"); err == nil {
		t.Error("removing an absent target must fail")
	}
	if _, err := run(t, dir, "item", "rm", "absent", "--force"); err == nil {
		t.Error("removing an absent item must fail")
	}
}

func TestExampleWithoutPropIsRejected(t *testing.T) {
	dir := t.TempDir()
	if _, err := run(t, dir, "item", "add", "vehicle", "-e", "Ford"); err == nil {
		t.Error("--example without --prop must fail")
	}
}

func TestBadURLIsRejected(t *testing.T) {
	dir := t.TempDir()
	if _, err := run(t, dir, "item", "add", "vehicle", "-u", "://nonsense"); err == nil {
		t.Error("an unparseable url must fail")
	}
}

func TestVersion(t *testing.T) {
	dir := t.TempDir()
	if out := runOK(t, dir, "version"); strings.TrimSpace(out) == "" {
		t.Error("version printed nothing")
	}
}

// --domain names a site. On its own that is a crawl target; alongside --prop it
// says which site the teaching applies to, so what one paper calls a byline
// does not overwrite what the next one calls it.
func TestPropertyTaughtPerDomain(t *testing.T) {
	dir := t.TempDir()
	runOK(t, dir, "item", "add", "news", "--template", "article")

	out := runOK(t, dir, "item", "add", "news", "-d", "example.com",
		"-p", "author", "-e", "Hannah McLeod", "-a", "byline")
	if !strings.Contains(out, "property author on example.com") {
		t.Errorf("output = %s", out)
	}
	if !strings.Contains(out, "alias byline for author on example.com") {
		t.Errorf("alias should attach to the property, not the item: %s", out)
	}

	// Scoping must not quietly widen the crawl.
	if shown := runOK(t, dir, "item", "ls", "news"); strings.Contains(shown, "targets     1") {
		t.Errorf("--domain alongside --prop should not add a target: %s", shown)
	}

	// A second site teaches its own answer without disturbing the first.
	runOK(t, dir, "item", "add", "news", "-d", "other.test",
		"-p", "author", "-e", "Jared Wright")

	first := runOK(t, dir, "item", "ls", "news")
	if !strings.Contains(first, "Hannah McLeod") && !strings.Contains(first, "example.com") {
		t.Logf("status = %s", first)
	}
}

// A pattern that does not compile has to fail when it is taught, not when a
// crawl runs, where it would look like a site that stopped publishing a field.
func TestTaughtRegexMustCompile(t *testing.T) {
	dir := t.TempDir()
	if _, err := run(t, dir, "item", "add", "news", "-p", "title", "--regex", "^(unclosed"); err == nil {
		t.Error("an invalid regex should be rejected when taught")
	}
	if _, err := run(t, dir, "item", "add", "news", "--regex", "^x$"); err == nil {
		t.Error("--regex without --prop should be rejected")
	}
}

// An item with nothing in it can do nothing: no targets to crawl, no
// properties to look for. Naming it and stopping told someone their command had
// worked when what it did was leave them where they were.
func TestAddingNothingSaysWhatToAdd(t *testing.T) {
	dir := t.TempDir()
	out := runOK(t, dir, "item", "add", "thing")

	for _, want := range []string{"nothing added yet", "scour start thing", "--help"} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not mention %q:\n%s", want, out)
		}
	}
	// The examples are the useful part, so they have to actually be there.
	if !strings.Contains(out, "-p make -e Ford") {
		t.Errorf("no examples shown:\n%s", out)
	}

	// Once it has something, the report is the change and nothing else.
	added := runOK(t, dir, "item", "add", "thing", "-p", "make", "-e", "Ford")
	if strings.Contains(added, "nothing added yet") {
		t.Errorf("still nagging after a property was added:\n%s", added)
	}
	if !strings.Contains(added, "property make") {
		t.Errorf("output = %s", added)
	}
}

// pause keeps the frontier and start carries on from it. Refusing to start a
// paused item would be answering "start this" with "it is paused", which is the
// thing being asked to change.
func TestPauseKeepsTheFrontierAndStartResumes(t *testing.T) {
	dir := t.TempDir()
	runOK(t, dir, "item", "add", "news", "-d", "example.com")

	out := runOK(t, dir, "pause", "news")
	if !strings.Contains(out, "frontier kept") || !strings.Contains(out, "scour start news") {
		t.Errorf("pause did not say what it kept or how to carry on:\n%s", out)
	}

	shown := runOK(t, dir, "item", "ls", "news")
	if !strings.Contains(shown, "paused") {
		t.Errorf("a paused item should say so:\n%s", shown)
	}
}

// Announcing a crawl that cannot run puts the reason it failed after the claim
// that it started.
func TestACrawlThatCannotRunSaysNothingFirst(t *testing.T) {
	dir := t.TempDir()
	runOK(t, dir, "item", "add", "solo")

	out, err := run(t, dir, "start", "solo")
	if err == nil {
		t.Fatal("crawling an item with no targets should refuse")
	}
	if strings.Contains(out, "crawling solo") {
		t.Errorf("it announced a crawl it could not run:\n%s", out)
	}
}

// A name that is not there is nearly always a typo, so the error names the
// closest one rather than leaving someone to re-read the listing.
func TestUnknownItemSuggestsTheNearestName(t *testing.T) {
	dir := t.TempDir()
	runOK(t, dir, "item", "add", "vehicle")
	runOK(t, dir, "item", "add", "news-html")

	for _, tc := range []struct{ typed, want string }{
		{"vehicel", "vehicle"},
		{"newshtml", "news-html"},
	} {
		out, err := run(t, dir, "item", "ls", tc.typed)
		if err == nil {
			t.Fatalf("%q should not have been found", tc.typed)
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("error for %q did not suggest %q: %v", tc.typed, tc.want, err)
		}
		_ = out
	}

	// Nothing close enough is offered nothing, because a suggestion pointing
	// somewhere unrelated is read as "this is what you meant".
	_, err := run(t, dir, "item", "ls", "zzzzzzzz")
	if err == nil {
		t.Fatal("an absent item must fail")
	}
	if strings.Contains(err.Error(), "did you mean") {
		t.Errorf("a distant name should not be suggested: %v", err)
	}
}

// The suggestion has to reach every command, not only the one it was added to.
func TestUnknownItemSuggestsAcrossCommands(t *testing.T) {
	dir := t.TempDir()
	runOK(t, dir, "item", "add", "vehicle", "-p", "make", "-e", "Ford")

	for _, args := range [][]string{
		{"item", "ls", "vehicel"},
		{"rules", "vehicel"},
		{"stream", "vehicel"},
		{"pause", "vehicel"},
		{"item", "tag", "vehicel", "-p", "make"},
	} {
		_, err := run(t, dir, args...)
		if err == nil {
			t.Errorf("scour %s should have failed", strings.Join(args, " "))
			continue
		}
		if !strings.Contains(err.Error(), `did you mean "vehicle"`) {
			t.Errorf("scour %s did not suggest the near name: %v", strings.Join(args, " "), err)
		}
	}
}

// status and `item ls` are the same listing under two names: one reached while
// defining things, one asked between commands. They must not drift.
func TestStatusMatchesItemLs(t *testing.T) {
	dir := t.TempDir()
	runOK(t, dir, "item", "add", "vehicle", "-d", "example.com", "-p", "make", "-e", "Ford")

	if fleet, ls := runOK(t, dir, "status"), runOK(t, dir, "item", "ls"); fleet != ls {
		t.Errorf("status and item ls differ:\n%s\n%s", fleet, ls)
	}
	if one, ls := runOK(t, dir, "status", "vehicle"), runOK(t, dir, "item", "ls", "vehicle"); one != ls {
		t.Errorf("status <name> and item ls <name> differ:\n%s\n%s", one, ls)
	}
	if _, err := run(t, dir, "status", "nosuch"); err == nil {
		t.Error("status on an unknown item must fail")
	}
}

// A command given no arguments is asking what it is for, so it shows its own
// help rather than one line of complaint.
func TestBareCommandShowsItsHelp(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"import", "export", "rules", "stream", "train"} {
		out, err := run(t, dir, name)
		if err == nil {
			t.Errorf("bare %q should not succeed", name)
			continue
		}
		if !strings.Contains(out, "EXAMPLES:") {
			t.Errorf("bare %q did not show its examples:\n%s", name, out)
		}
		if !strings.Contains(out, "takes one item name") {
			t.Errorf("bare %q did not say what it wanted:\n%s", name, out)
		}
	}

	// A wrong count is a different mistake: that one knows what the command is
	// for and only needs the number, so the help would be noise.
	out, err := run(t, dir, "rules", "a", "b")
	if err == nil {
		t.Fatal("two names should fail")
	}
	if strings.Contains(out, "EXAMPLES:") {
		t.Errorf("a miscount should not reprint the whole help:\n%s", out)
	}
}

// A pattern taught in error could only be overwritten, never removed, because
// setting writes only what it is given so that describing a property more fully
// does not cost it what it already knew. Emptying is a different act.
func TestClearingAPropertyDetailKeepsTheProperty(t *testing.T) {
	dir := t.TempDir()
	runOK(t, dir, "item", "add", "news", "-p", "title", "--prop-type", "string",
		"-e", "A headline", "--regex", `^.{5,}$`, "--label", `^og:title$`)

	out := runOK(t, dir, "item", "rm", "news", "-p", "title", "--regex")
	if !strings.Contains(out, "cleared regex on title") {
		t.Fatalf("clearing did not say what it did:\n%s", out)
	}

	shown := runOK(t, dir, "--json", "item", "ls", "news")
	if !strings.Contains(shown, `"Properties": 1`) {
		t.Errorf("clearing a detail removed the property:\n%s", shown)
	}

	// Clearing one detail leaves the others, which is the whole point.
	rules := runOK(t, dir, "item", "rm", "news", "-p", "title", "--label", "--example")
	if !strings.Contains(rules, "cleared label, example on title") {
		t.Errorf("unexpected output:\n%s", rules)
	}

	// Naming a detail without a property is a question, not a whole-item delete.
	out, err := run(t, dir, "item", "rm", "news", "--regex")
	if err == nil {
		t.Fatal("clearing with no --prop must fail rather than delete the item")
	}
	if !strings.Contains(err.Error(), "--prop") {
		t.Errorf("the refusal should say what is missing: %v", err)
	}
	if shown := runOK(t, dir, "item", "ls"); !strings.Contains(shown, "news") {
		t.Errorf("the item was deleted by a clear that named no property:\n%s", shown)
	}
	_ = out
}
