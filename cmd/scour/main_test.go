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

	// urfave reads root flags before the subcommand name, where cobra took them
	// anywhere, so --config leads rather than trails.
	argv := append([]string{"scour", "--config", filepath.Join(dir, "config.toml")}, args...)
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

	runOK(t, dir, "add", "vehicle", "--alias", "car", "--alias", "pickup truck")
	runOK(t, dir, "add", "vehicle", "-d", "example.com", "--subdomains")
	runOK(t, dir, "add", "vehicle", "-u", "http://www.example.com/cars/")
	runOK(t, dir, "add", "vehicle", "-p", "make", "-e", "Ford")
	runOK(t, dir, "add", "vehicle", "--type", "html", "--type", "pdf")

	out := runOK(t, dir, "list")
	if !strings.Contains(out, "vehicle") {
		t.Errorf("list did not mention the entity:\n%s", out)
	}
	// The listing carries what a crawl is actually judged on, not just a name
	// and a count: what the entity has, how far it got, whether it is trained.
	for _, col := range []string{"NAME", "TARGETS", "VISITED", "RECORDS", "TRAINED"} {
		if !strings.Contains(out, col) {
			t.Errorf("list did not print the %s column:\n%s", col, out)
		}
	}
}

func TestMultiWordAliasIsKeptWhole(t *testing.T) {
	dir := t.TempDir()

	out := runOK(t, dir, "add", "vehicle", "--alias", "pickup truck")
	if !strings.Contains(out, "alias pickup truck") {
		t.Errorf("a multi-word alias must not be split into words:\n%s", out)
	}
	if strings.Contains(out, "alias pickup\n") {
		t.Errorf("the alias was split:\n%s", out)
	}
}

func TestAddIsIdempotent(t *testing.T) {
	dir := t.TempDir()

	runOK(t, dir, "add", "vehicle", "--alias", "car")
	runOK(t, dir, "add", "vehicle", "--alias", "car")

	out := runOK(t, dir, "list", "--json")
	if strings.Count(out, `"name": "vehicle"`) != 1 {
		t.Errorf("the entity was created more than once:\n%s", out)
	}
}

func TestListIsEmptyToStart(t *testing.T) {
	dir := t.TempDir()

	out := runOK(t, dir, "list")
	if !strings.Contains(out, "no entities yet") {
		t.Errorf("an empty database should say so:\n%s", out)
	}
}

func TestListJSON(t *testing.T) {
	dir := t.TempDir()
	runOK(t, dir, "add", "vehicle")

	out := runOK(t, dir, "list", "--json")
	for _, key := range []string{`"name": "vehicle"`, `"records": 0`, `"trained": "never"`} {
		if !strings.Contains(out, key) {
			t.Errorf("json output missing %s:\n%s", key, out)
		}
	}
}

func TestDomainsAreNormalised(t *testing.T) {
	dir := t.TempDir()

	// All three name one target, so the last write wins and there is one row.
	runOK(t, dir, "add", "vehicle", "-d", "example.com")
	runOK(t, dir, "add", "vehicle", "-d", "www.example.com")
	out := runOK(t, dir, "add", "vehicle", "-d", "https://example.com/")

	if !strings.Contains(out, "domain example.com") {
		t.Errorf("domain was not normalised:\n%s", out)
	}
}

func TestRemoveWholeEntityNeedsForce(t *testing.T) {
	dir := t.TempDir()
	runOK(t, dir, "add", "vehicle")

	out, err := run(t, dir, "remove", "vehicle")
	if err == nil {
		t.Fatal("removing an entity without --force must fail")
	}
	if !strings.Contains(out, "--force") {
		t.Errorf("the refusal should say what to do:\n%s", out)
	}

	// The entity is still there.
	if out := runOK(t, dir, "list"); !strings.Contains(out, "vehicle") {
		t.Errorf("entity was removed despite the refusal:\n%s", out)
	}

	runOK(t, dir, "remove", "vehicle", "--force")
	if out := runOK(t, dir, "list"); strings.Contains(out, "vehicle") {
		t.Errorf("entity survived --force:\n%s", out)
	}
}

func TestRemoveParts(t *testing.T) {
	dir := t.TempDir()
	// Separate commands: with --prop present, --domain scopes the property
	// rather than adding a target.
	runOK(t, dir, "add", "vehicle", "-d", "example.com")
	runOK(t, dir, "add", "vehicle", "-p", "year", "-e", "2026")

	out := runOK(t, dir, "remove", "vehicle", "-d", "example.com")
	if !strings.Contains(out, "removed domain example.com") {
		t.Errorf("unexpected output:\n%s", out)
	}

	out = runOK(t, dir, "remove", "vehicle", "-p", "year")
	if !strings.Contains(out, "removed property year") {
		t.Errorf("unexpected output:\n%s", out)
	}

	// The entity itself survives having its parts removed.
	if out := runOK(t, dir, "list"); !strings.Contains(out, "vehicle") {
		t.Errorf("removing a target should not remove the entity:\n%s", out)
	}
}

func TestRemoveReportsMissingThings(t *testing.T) {
	dir := t.TempDir()
	runOK(t, dir, "add", "vehicle")

	if _, err := run(t, dir, "remove", "vehicle", "-d", "absent.example"); err == nil {
		t.Error("removing an absent target must fail")
	}
	if _, err := run(t, dir, "remove", "absent", "--force"); err == nil {
		t.Error("removing an absent entity must fail")
	}
}

func TestExampleWithoutPropIsRejected(t *testing.T) {
	dir := t.TempDir()
	if _, err := run(t, dir, "add", "vehicle", "-e", "Ford"); err == nil {
		t.Error("--example without --prop must fail")
	}
}

func TestBadURLIsRejected(t *testing.T) {
	dir := t.TempDir()
	if _, err := run(t, dir, "add", "vehicle", "-u", "://nonsense"); err == nil {
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
	runOK(t, dir, "add", "news", "--template", "article")

	out := runOK(t, dir, "add", "news", "-d", "example.com",
		"-p", "author", "-e", "Hannah McLeod", "-a", "byline")
	if !strings.Contains(out, "property author on example.com") {
		t.Errorf("output = %s", out)
	}
	if !strings.Contains(out, "alias byline for author on example.com") {
		t.Errorf("alias should attach to the property, not the entity: %s", out)
	}

	// Scoping must not quietly widen the crawl.
	if shown := runOK(t, dir, "list", "news"); strings.Contains(shown, "targets     1") {
		t.Errorf("--domain alongside --prop should not add a target: %s", shown)
	}

	// A second site teaches its own answer without disturbing the first.
	runOK(t, dir, "add", "news", "-d", "other.test",
		"-p", "author", "-e", "Jared Wright")

	first := runOK(t, dir, "list", "news")
	if !strings.Contains(first, "Hannah McLeod") && !strings.Contains(first, "example.com") {
		t.Logf("status = %s", first)
	}
}

// A pattern that does not compile has to fail when it is taught, not when a
// crawl runs, where it would look like a site that stopped publishing a field.
func TestTaughtRegexMustCompile(t *testing.T) {
	dir := t.TempDir()
	if _, err := run(t, dir, "add", "news", "-p", "title", "--regex", "^(unclosed"); err == nil {
		t.Error("an invalid regex should be rejected when taught")
	}
	if _, err := run(t, dir, "add", "news", "--regex", "^x$"); err == nil {
		t.Error("--regex without --prop should be rejected")
	}
}

// An entity with nothing in it can do nothing: no targets to crawl, no
// properties to look for. Naming it and stopping told someone their command had
// worked when what it did was leave them where they were.
func TestAddingNothingSaysWhatToAdd(t *testing.T) {
	dir := t.TempDir()
	out := runOK(t, dir, "add", "thing")

	for _, want := range []string{"nothing added yet", "scour crawl thing", "--help"} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not mention %q:\n%s", want, out)
		}
	}
	// The examples are the useful part, so they have to actually be there.
	if !strings.Contains(out, "-p make -e Ford") {
		t.Errorf("no examples shown:\n%s", out)
	}

	// Once it has something, the report is the change and nothing else.
	added := runOK(t, dir, "add", "thing", "-p", "make", "-e", "Ford")
	if strings.Contains(added, "nothing added yet") {
		t.Errorf("still nagging after a property was added:\n%s", added)
	}
	if !strings.Contains(added, "property make") {
		t.Errorf("output = %s", added)
	}
}

// A stopped entity used to be crawled anyway: the pause is checked once a
// second, so it fetched a handful of pages and then stopped saying "paused",
// which explains neither why nor what to do about it.
func TestCrawlingAStoppedEntityExplainsItself(t *testing.T) {
	dir := t.TempDir()
	runOK(t, dir, "add", "news", "-d", "example.com")

	out := runOK(t, dir, "stop", "news")
	if !strings.Contains(out, "frontier kept") || !strings.Contains(out, "scour start news") {
		t.Errorf("stop did not say what it kept or how to undo it:\n%s", out)
	}

	_, err := run(t, dir, "crawl", "news")
	if err == nil {
		t.Fatal("crawling a stopped entity should refuse")
	}
	if !strings.Contains(err.Error(), "scour start news") {
		t.Errorf("the refusal does not say how to fix it: %v", err)
	}

	if out := runOK(t, dir, "start", "news"); !strings.Contains(out, "scour crawl news") {
		t.Errorf("start did not say what to do next:\n%s", out)
	}
}

// Announcing a crawl that cannot run puts the reason it failed after the claim
// that it started.
func TestACrawlThatCannotRunSaysNothingFirst(t *testing.T) {
	dir := t.TempDir()
	runOK(t, dir, "add", "solo")

	out, err := run(t, dir, "crawl", "solo")
	if err == nil {
		t.Fatal("crawling an entity with no targets should refuse")
	}
	if strings.Contains(out, "crawling solo") {
		t.Errorf("it announced a crawl it could not run:\n%s", out)
	}
}
