// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rangertaha/scour/internal/cli"
	"github.com/rangertaha/scour/internal/engine"
)

// run drives the whole command line, arguments and all, and returns what it
// printed and the exit code the process would have used.
func run(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()

	var out, errs bytes.Buffer
	a := &cli.App{Out: &out, Err: &errs, In: strings.NewReader("")}

	code = cli.Run(context.Background(), a, root(a), append([]string{"scour"}, args...))
	return out.String(), errs.String(), code
}

func write(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestInitProducesSomethingThatValidates is the promise init makes: what it
// prints is a job, not a sketch of one.
func TestInitProducesSomethingThatValidates(t *testing.T) {
	sample, _, code := run(t, "init", "news")
	if code != cli.ExitOK {
		t.Fatalf("init exited %d", code)
	}
	if !strings.Contains(sample, `job "news"`) {
		t.Errorf("the sample is not named after the argument:\n%s", sample)
	}

	path := write(t, "news.hcl", sample)
	stdout, stderr, code := run(t, "validate", path)
	if code != cli.ExitOK {
		t.Fatalf("the sample does not validate: %d\n%s%s", code, stdout, stderr)
	}
}

func TestInitDefaultsItsName(t *testing.T) {
	sample, _, code := run(t, "init")
	if code != cli.ExitOK {
		t.Fatalf("exited %d", code)
	}
	if !strings.Contains(sample, `job "example"`) {
		t.Error("a sample with no name given is not named example")
	}
}

func TestInitWritesAFileAndRefusesToClobberIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "job.hcl")

	if _, _, code := run(t, "init", "-o", path); code != cli.ExitOK {
		t.Fatalf("exited %d", code)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("nothing was written: %v", err)
	}

	// Somebody running this twice in a directory they have been working in
	// must not lose what they wrote the first time.
	if _, _, code := run(t, "init", "-o", path); code != cli.ExitFailed {
		t.Errorf("overwriting exited %d, want it refused", code)
	}
	if _, _, code := run(t, "init", "-o", path, "--force"); code != cli.ExitOK {
		t.Errorf("--force exited %d", code)
	}
}

func TestValidateReportsEveryProblemAtOnce(t *testing.T) {
	path := write(t, "broken.hcl", `
job "news" {
  start = ["file:///etc/passwd"]

  item "article" {
    property "title" {
      type = str
      property "child" {}
    }
  }

  scheduler {
    concurrency = 999
  }
}
`)

	_, stderr, code := run(t, "validate", path)
	if code != cli.ExitInvalid {
		t.Fatalf("exited %d, want %d", code, cli.ExitInvalid)
	}
	for _, want := range []string{"http and https", "concurrency", "object"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("the report does not mention %q:\n%s", want, stderr)
		}
	}
}

// TestExitCodesTellTheCasesApart is the property a script depends on: a wrong
// document and a broken tool must not look the same.
func TestExitCodesTellTheCasesApart(t *testing.T) {
	good := write(t, "good.hcl", string(mustInit(t)))

	if _, _, code := run(t, "validate", good); code != cli.ExitOK {
		t.Errorf("a good document exited %d", code)
	}

	bad := write(t, "bad.hcl", "job \"j\" {\n}\n")
	if _, _, code := run(t, "validate", bad); code != cli.ExitInvalid {
		t.Errorf("a refused document exited %d, want %d", code, cli.ExitInvalid)
	}

	if _, _, code := run(t, "validate", filepath.Join(t.TempDir(), "absent.hcl")); code != cli.ExitFailed {
		t.Errorf("a missing file exited %d, want %d", code, cli.ExitFailed)
	}

	if _, _, code := run(t, "validate"); code != cli.ExitUsage {
		t.Errorf("no argument exited %d, want %d", code, cli.ExitUsage)
	}
	if _, _, code := run(t, "validate", good, good); code != cli.ExitUsage {
		t.Errorf("two documents exited %d, want %d", code, cli.ExitUsage)
	}
}

func TestShowFillsInTheDefaults(t *testing.T) {
	path := write(t, "job.hcl", `
job "news" {
  start = ["https://example.com/"]

  item "article" {
    property "title" {
      type = str
    }
  }

  downloader {
    plugin "cache" {}
    plugin "offsite" {}
  }

  pipeline {
    step "clean" "article" {}

    step "rank" "article" {
      requires = [clean.article]
    }

    step "score" "article" {
      requires = [clean.article]
    }
  }
}
`)

	stdout, _, code := run(t, "show", path)
	if code != cli.ExitOK {
		t.Fatalf("exited %d", code)
	}

	// Defaults the document never mentioned.
	for _, want := range []string{"robots", "user_agent", "max_depth", "priority"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("show does not report %q:\n%s", want, stdout)
		}
	}
	// The chain in the order it runs, which is the reason order numbers exist.
	if !strings.Contains(stdout, "offsite(500) -> cache(900)") {
		t.Errorf("show does not report the chain in run order:\n%s", stdout)
	}
	// The graph as waves, because a flat list hides the concurrency.
	if !strings.Contains(stdout, "2 wave(s)") || !strings.Contains(stdout, "2 at once") {
		t.Errorf("show does not report the waves:\n%s", stdout)
	}
}

func TestShowAsJSON(t *testing.T) {
	path := write(t, "job.hcl", string(mustInit(t)))

	stdout, _, code := run(t, "show", "--json", path)
	if code != cli.ExitOK {
		t.Fatalf("exited %d", code)
	}
	if !strings.HasPrefix(strings.TrimSpace(stdout), "{") {
		t.Errorf("--json did not print JSON:\n%s", stdout)
	}
}

// TestSpecGoesToStdoutAlone is what makes `scour spec job.hcl > spec.hcl`
// produce a spec rather than a spec with a note in the middle of it.
func TestSpecGoesToStdoutAlone(t *testing.T) {
	path := write(t, "job.hcl", string(mustInit(t)))

	stdout, stderr, code := run(t, "spec", path)
	if code != cli.ExitOK {
		t.Fatalf("exited %d", code)
	}
	// The real property: what stdout carries is a spec, on its own, parseable
	// by whatever it was piped into.
	spec, err := engine.ParseSpec([]byte(stdout), "spec.hcl")
	if err != nil {
		t.Fatalf("stdout is not a spec:\n%s\n%v", stdout, err)
	}
	if err := spec.Validate(); err != nil {
		t.Fatalf("the spec on stdout does not validate: %v", err)
	}

	// The fingerprint is reported to the person, on stderr, as well as being
	// in the document's own header for whatever reads it later.
	if !strings.Contains(stderr, spec.Fingerprint()) {
		t.Errorf("stderr does not report the fingerprint:\n%s", stderr)
	}
}

func TestAmbiguousJobIsRefused(t *testing.T) {
	path := write(t, "two.hcl", `
job "news" {
  start = ["https://example.com/"]
  item "a" {
    property "p" {
      type = str
    }
  }
}

job "products" {
  start = ["https://shop.example/"]
  item "b" {
    property "q" {
      type = str
    }
  }
}
`)

	_, stderr, code := run(t, "show", path)
	if code != cli.ExitUsage {
		t.Fatalf("exited %d, want a usage error", code)
	}
	if !strings.Contains(stderr, "news") || !strings.Contains(stderr, "products") {
		t.Errorf("the error does not say what there is to choose from: %s", stderr)
	}

	if _, _, code := run(t, "show", "--job", "products", path); code != cli.ExitOK {
		t.Errorf("naming a job exited %d", code)
	}
	if _, _, code := run(t, "show", "--job", "absent", path); code != cli.ExitUsage {
		t.Errorf("naming a job that is not there exited %d", code)
	}
}

func TestDefaults(t *testing.T) {
	stdout, _, code := run(t, "defaults")
	if code != cli.ExitOK {
		t.Fatalf("exited %d", code)
	}
	for _, want := range []string{"scheduler.max_depth", "downloader.robots", "mutation.costly"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("defaults does not list %q", want)
		}
	}

	asJSON, _, code := run(t, "defaults", "--json")
	if code != cli.ExitOK || !strings.HasPrefix(strings.TrimSpace(asJSON), "{") {
		t.Errorf("--json did not print JSON: %d\n%s", code, asJSON)
	}
}

func TestUnknownCommandIsAUsageError(t *testing.T) {
	_, stderr, code := run(t, "wizardry")
	if code == cli.ExitOK {
		t.Fatal("an unknown command succeeded")
	}
	if !strings.Contains(stderr, "wizardry") {
		t.Errorf("the error does not name what was typed: %s", stderr)
	}
}

func TestVersion(t *testing.T) {
	stdout, _, code := run(t, "--version")
	if code != cli.ExitOK {
		t.Fatalf("exited %d", code)
	}
	if strings.TrimSpace(stdout) == "" {
		t.Error("no version was printed")
	}
}

func mustInit(t *testing.T) []byte {
	t.Helper()
	out, _, code := run(t, "init")
	if code != cli.ExitOK {
		t.Fatalf("init exited %d", code)
	}
	return []byte(out)
}

// Templates.

func TestEveryTemplateInitsAndValidates(t *testing.T) {
	listed, _, code := run(t, "init", "--list")
	if code != cli.ExitOK {
		t.Fatalf("--list exited %d", code)
	}
	if strings.TrimSpace(listed) == "" {
		t.Fatal("--list printed nothing")
	}

	for _, line := range strings.Split(strings.TrimSpace(listed), "\n") {
		name := strings.Fields(line)[0]

		t.Run(name, func(t *testing.T) {
			sample, _, code := run(t, "init", "crawl", "--template", name)
			if code != cli.ExitOK {
				t.Fatalf("init exited %d", code)
			}
			if !strings.Contains(sample, `job "crawl"`) {
				t.Errorf("the template is not named after the argument:\n%s", sample)
			}

			path := write(t, name+".hcl", sample)
			stdout, stderr, code := run(t, "validate", path)
			if code != cli.ExitOK {
				t.Fatalf("the %s template does not validate: %d\n%s%s", name, code, stdout, stderr)
			}
			// And it is a job somebody can immediately look at.
			if _, _, code := run(t, "show", path); code != cli.ExitOK {
				t.Errorf("show exited %d on the %s template", code, name)
			}
		})
	}
}

func TestUnknownTemplateIsAUsageError(t *testing.T) {
	_, stderr, code := run(t, "init", "--template", "carrier-pigeon")
	if code != cli.ExitUsage {
		t.Fatalf("exited %d, want a usage error", code)
	}
	// Guessing twice is worse than being told once.
	if !strings.Contains(stderr, "basic") {
		t.Errorf("the error does not list what there is: %s", stderr)
	}
}

func TestInitDefaultsToTheBasicTemplate(t *testing.T) {
	byDefault, _, _ := run(t, "init", "j")
	explicit, _, _ := run(t, "init", "j", "--template", "basic")

	if byDefault != explicit {
		t.Error("init with no template is not the basic template")
	}
}

// TestAWrongCommandLineExitsTwoWhoeverNoticed.
//
// A wrong flag is a wrong command line, not a broken scour, and urfave/cli
// returns a plain error for one. Without recognising it the process exited 3,
// and a build script that retries on 3 and gives up on 2 retries a typo
// forever: the conflation the exit codes exist to prevent.
func TestAWrongCommandLineExitsTwoWhoeverNoticed(t *testing.T) {
	path := write(t, "job.hcl", `
job "news" {
  start = ["https://example.com/"]

  item "article" {
    property "title" {
      type = str
    }
  }
}
`)

	for name, args := range map[string][]string{
		"a flag that does not exist": {"validate", "--nope", path},
		"a flag with no value":       {"spec", "--job"},
		"too many arguments":         {"validate", path, path},
	} {
		t.Run(name, func(t *testing.T) {
			out, errOut, code := run(t, args...)
			if code != cli.ExitUsage {
				t.Errorf("exit %d, want %d\n%s%s", code, cli.ExitUsage, out, errOut)
			}
		})
	}

	// And a document that is merely wrong is still 1, so the fix did not turn
	// every failure into a usage error.
	bad := write(t, "bad.hcl", "job \"news\" {}\n")
	if _, _, code := run(t, "validate", bad); code != cli.ExitInvalid {
		t.Errorf("a refused document exited %d, want %d", code, cli.ExitInvalid)
	}
}
