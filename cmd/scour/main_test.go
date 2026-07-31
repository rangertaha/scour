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
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(append(args, "--config", filepath.Join(dir, "config.toml")))

	err := cmd.ExecuteContext(context.Background())
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
	if !strings.Contains(out, "NAME") || !strings.Contains(out, "MATCHES") {
		t.Errorf("list did not print the table header:\n%s", out)
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
	if strings.Count(out, `"Name": "vehicle"`) != 1 {
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
	if !strings.Contains(out, `"Matches": 0`) {
		t.Errorf("json output missing the match count:\n%s", out)
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
	runOK(t, dir, "add", "vehicle", "-d", "example.com", "-p", "year", "-e", "2026")

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
