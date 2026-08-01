// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// write puts a config in the test's directory and returns its path.
func write(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// The sample is the first thing anyone runs, so it has to be a config that
// actually applies rather than a shape to fill in.
func TestTheSampleConfigIsValid(t *testing.T) {
	dir := t.TempDir()

	out := runOK(t, dir, "job", "config")
	path := write(t, dir, "sample.toml", out)

	if got := runOK(t, dir, "job", "validate", "-f", path); !strings.Contains(got, "ok") {
		t.Errorf("the printed sample does not validate:\n%s", got)
	}
}

// A job built from flags and one built from a file are the same job, which is
// only true if the file can be written back out and applied again.
func TestJobConfigRoundTrip(t *testing.T) {
	dir := t.TempDir()

	runOK(t, dir, "item", "add", "vehicle", "-p", "make", "-e", "Ford")
	runOK(t, dir, "job", "add", "uk", "-i", "vehicle", "-d", "example.co.uk", "--subdomains")
	runOK(t, dir, "job", "add", "uk", "-u", "https://www.example.co.uk/used/")
	runOK(t, dir, "job", "add", "uk", "-t", "html", "-t", "feed")
	runOK(t, dir, "job", "set", "uk", "--depth", "3", "--max-pages", "500", "--max-time", "30m")

	exported := runOK(t, dir, "job", "show", "uk", "--toml")
	t.Logf("job show uk --toml:\n%s", exported)
	for _, want := range []string{
		`name = "uk"`, `item = "vehicle"`, "depth     = 3", "max_pages = 500",
		`max_time  = "30m0s"`, `"feed"`, `"html"`, "example.co.uk", "subdomains = true",
		"https://www.example.co.uk/used/",
	} {
		if !strings.Contains(exported, want) {
			t.Errorf("the exported config is missing %q:\n%s", want, exported)
		}
	}

	// It has to come back in, on a machine that has never seen the job.
	fresh := t.TempDir()
	runOK(t, fresh, "item", "add", "vehicle", "-p", "make", "-e", "Ford")
	path := write(t, fresh, "uk.toml", exported)
	runOK(t, fresh, "job", "validate", "-f", path)
	runOK(t, fresh, "job", "add", "-f", path)

	again := runOK(t, fresh, "job", "show", "uk", "--toml")
	if again != exported {
		t.Errorf("the job did not survive the round trip:\nwrote:\n%s\nread back:\n%s", exported, again)
	}
}

// A config checker that stops at the first fault turns fixing a file into as
// many runs as it has mistakes.
func TestValidateReportsEveryProblemAtOnce(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, "bad.toml", `
name = ""
item = ""
depth = -1
max_pages = -5
max_time = "soon"
types = ["nonsense"]
`)

	out, err := run(t, dir, "job", "validate", "-f", path)
	if err == nil {
		t.Fatalf("a config with six faults validated:\n%s", out)
	}
	for _, want := range []string{"name is required", "item is required", "depth", "max_pages", "max_time", "types", "no targets"} {
		if !strings.Contains(out, want) {
			t.Errorf("the report never mentions %q:\n%s", want, out)
		}
	}
}

// A misspelled key is the whole failure mode of a config file: it parses, it
// applies, and it does nothing the author asked for.
func TestAnUnknownKeyIsRejected(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, "typo.toml", `
name = "uk"
item = "vehicle"
max_page = 500

[[domain]]
value = "example.co.uk"
`)

	out, err := run(t, dir, "job", "validate", "-f", path)
	if err == nil {
		t.Fatalf("a misspelled key was accepted:\n%s", out)
	}
	// A key nothing reads is worth naming: "unknown key" alone leaves the
	// author reading the whole file for it.
	if !strings.Contains(err.Error()+out, "max_page") {
		t.Errorf("the error does not name the key: %v\n%s", err, out)
	}
}

// Validating is safe to do against a config written for another machine, so it
// must not require the item to exist here.
func TestValidateDoesNotNeedTheItem(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, "elsewhere.toml", `
name = "uk"
item = "an-item-this-machine-has-never-heard-of"

[[domain]]
value = "example.co.uk"
`)

	if out := runOK(t, dir, "job", "validate", "-f", path); !strings.Contains(out, "ok") {
		t.Errorf("validate wanted a database:\n%s", out)
	}
}

// Applying one does need it, and should say so rather than making an empty one.
func TestApplyingAConfigNeedsTheItem(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, "uk.toml", `
name = "uk"
item = "missing"

[[domain]]
value = "example.co.uk"
`)

	if out, err := run(t, dir, "job", "add", "-f", path); err == nil {
		t.Errorf("a config naming an unknown item was applied:\n%s", out)
	}
}

// The file carries the name, so a positional one is a second answer to a
// question already answered.
func TestAFileAndANameTogetherIsRejected(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, "uk.toml", "name = \"uk\"\nitem = \"vehicle\"\n\n[[domain]]\nvalue = \"example.co.uk\"\n")

	if out, err := run(t, dir, "job", "add", "other", "-f", path); err == nil {
		t.Errorf("a name and a file together were accepted:\n%s", out)
	}
}
