// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// The tests in this file run the built binary as a process.
//
// # Why, when everything else calls the command tree in-process
//
// Because main.go does three things that only exist once there is a process,
// and all three had no test at all: it turns an error into an exit code the
// shell can see, it points the command tree at the real streams, and it cancels
// a running crawl on an interrupt so that the crawl stops where it is and stays
// resumable. `main` was 0% covered, and the interrupt path is the one people
// use every day, by pressing ctrl-c on a crawl that is taking too long.
//
// Calling cli.Run with a bytes.Buffer cannot reach any of it. The signal in
// particular is the whole reason main.go is not empty: everything else it does
// lives under internal/cli precisely so that it can be tested without a
// process, and the note at the top of main.go says so.
//
// The binary is built once for the package and removed afterwards, so the cost
// is one build rather than one per test.
//
// # Counting what these cover
//
// A child process is invisible to the test binary's coverage instrumentation,
// so `go tool cover` reports 0% for everything only these tests reach: main,
// runTrain, and the corpus reader under it all read as untouched. That number
// is worse than no number, because somebody trimming dead code by coverage
// would delete tested code.
//
// So the binary is built with -cover and each run writes into a shared
// directory. Set SCOUR_COVERDIR to keep it, then:
//
//	SCOUR_COVERDIR=/tmp/e2e go test ./cmd/scour/
//	go tool covdata percent -i=/tmp/e2e

var (
	binary   string
	coverDir string
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "scour-binary-test")
	if err != nil {
		fmt.Fprintf(os.Stderr, "binary test: %v\n", err)
		os.Exit(1)
	}

	// Kept when the environment asks, so the counts can be read afterwards.
	coverDir = os.Getenv("SCOUR_COVERDIR")
	if coverDir == "" {
		coverDir = filepath.Join(dir, "cover")
	}
	if err := os.MkdirAll(coverDir, 0o750); err != nil {
		fmt.Fprintf(os.Stderr, "binary test: %v\n", err)
		os.Exit(1)
	}

	binary = filepath.Join(dir, "scour")
	build := exec.Command("go", "build",
		"-cover", "-coverpkg=github.com/rangertaha/scour/...", "-o", binary, ".")
	if out, err := build.CombinedOutput(); err != nil {
		// A failure here is a real failure and not a reason to skip: a build
		// that does not build is the first thing a test suite owes anybody.
		fmt.Fprintf(os.Stderr, "binary test: building scour: %v\n%s", err, out)
		os.RemoveAll(dir)
		os.Exit(1)
	}

	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// outcome is what a run of the binary produced.
type outcome struct {
	stdout string
	stderr string
	code   int
}

// scour runs the binary in a directory, to completion.
func scour(t *testing.T, dir string, args ...string) outcome {
	t.Helper()

	cmd := exec.Command(binary, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOCOVERDIR="+coverDir)

	var out, errs strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errs

	err := cmd.Run()
	code := 0
	if exit, ok := err.(*exec.ExitError); ok {
		code = exit.ExitCode()
	} else if err != nil {
		t.Fatalf("running scour %v: %v", args, err)
	}
	return outcome{stdout: out.String(), stderr: errs.String(), code: code}
}

// TestTheProcessExitsWithTheDocumentedCode.
//
// The codes are what a script reads, and the whole point of having four of them
// is that "the document is wrong" and "the command line is wrong" and "scour
// could not do it" are different things a script may want to act on
// differently. Everything else asserts them against a returned int; this
// asserts what the shell actually sees.
func TestTheProcessExitsWithTheDocumentedCode(t *testing.T) {
	dir := t.TempDir()

	good := filepath.Join(dir, "good.hcl")
	if err := os.WriteFile(good, []byte(`
job "news" {
  domains = ["example.com"]
  start   = ["https://example.com/"]

  item "article" {
    property "title" {
      type = str
    }
  }
}
`), 0o600); err != nil {
		t.Fatal(err)
	}

	bad := filepath.Join(dir, "bad.hcl")
	if err := os.WriteFile(bad, []byte("job \"news\" {\n  start = [\"not a url\"]\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		name string
		args []string
		want int
	}{
		{"a document that validates", []string{"validate", good}, 0},
		{"a document that does not", []string{"validate", bad}, 1},
		{"a command that is not one", []string{"nonesuch"}, 2},
		// Three and not one: a file that is not there is not a document that
		// was read and refused, and the table draws that line deliberately so
		// a script can tell "fix your file" from "the tool could not do it".
		{"a file that is not there", []string{"validate", filepath.Join(dir, "missing.hcl")}, 3},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := scour(t, dir, c.args...)
			if got.code != c.want {
				t.Errorf("exit %d, want %d\nstdout: %s\nstderr: %s", got.code, c.want, got.stdout, got.stderr)
			}
		})
	}
}

// TestTheStreamsAreSeparate.
//
// `scour spec` is meant to be piped, so what it prints has to be on stdout with
// nothing else mixed in, and the human commentary has to be on stderr. In
// process that is two buffers somebody wired up; here it is the two file
// descriptors a pipe actually reads.
func TestTheStreamsAreSeparate(t *testing.T) {
	dir := t.TempDir()

	path := filepath.Join(dir, "job.hcl")
	if err := os.WriteFile(path, []byte(`
job "news" {
  domains = ["example.com"]
  start   = ["https://example.com/"]

  item "article" {
    property "title" {
      type = str
    }
  }
}
`), 0o600); err != nil {
		t.Fatal(err)
	}

	got := scour(t, dir, "spec", path)
	if got.code != 0 {
		t.Fatalf("exit %d: %s", got.code, got.stderr)
	}
	// The spec, and only the spec. Every line on stdout is either a comment,
	// blank, or part of the item blocks: anything addressed to a person would
	// land in the file somebody redirected this into.
	if !strings.Contains(got.stdout, `item "article"`) {
		t.Errorf("stdout is not the spec:\n%s", got.stdout)
	}
	for _, line := range strings.Split(got.stdout, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if !strings.ContainsAny(trimmed, "{}=\"") {
			t.Errorf("a line on stdout is not part of the spec: %q", line)
		}
	}
	if got.stderr != "" && strings.Contains(got.stdout, got.stderr) {
		t.Errorf("what was written to stderr also reached stdout:\n%s", got.stdout)
	}
}

// TestAnInterruptStopsTheCrawlAndLeavesItResumable.
//
// This is what main.go exists for. The first interrupt cancels the context the
// whole engine runs under, so the crawl stops where it is, reports that it was
// stopped rather than that it finished, and leaves the frontier on disk with
// its remaining URLs. A crawl that was interrupted and one that ran out of
// pages look identical afterwards unless the ending says which, and a crawl
// that lost its queue on ctrl-c would make the whole resumption promise
// worthless.
//
// Nothing could test this before, because a signal needs a process.
func TestAnInterruptStopsTheCrawlAndLeavesItResumable(t *testing.T) {
	var served atomic.Int32

	// Slow enough that the crawl is still going when the interrupt arrives,
	// and wide enough that it has plenty left to do.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			http.NotFound(w, r)
			return
		}
		served.Add(1)
		time.Sleep(150 * time.Millisecond)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")

		fmt.Fprint(w, `<html><head><meta property="og:title" content="A story"></head><body>`)
		for i := range 40 {
			fmt.Fprintf(w, `<a href="/story/%d">%d</a>`, i, i)
		}
		fmt.Fprint(w, `</body></html>`)
	}))
	defer server.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "job.hcl")
	host := strings.TrimPrefix(server.URL, "http://")
	if err := os.WriteFile(path, []byte(fmt.Sprintf(`
job "news" {
  domains = ["%s"]
  start   = ["%s/"]

  scheduler {
    // Fast, because what is under test is the interrupt and not politeness,
    // and a rate the test has to wait out would make this a slow test of the
    // wrong thing. The pages are slow instead, which is what gives the
    // interrupt something to land in the middle of.
    rate = "1ms"
  }

  item "article" {
    property "title" {
      type = str
    }
  }
}
`, host, server.URL)), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(binary, "run", path)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOCOVERDIR="+coverDir)

	var out, errs strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errs

	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	// Interrupt once the crawl is demonstrably under way, so this is not a
	// test of what happens to a process that has not started working.
	deadline := time.Now().Add(20 * time.Second)
	for served.Load() < 2 {
		if time.Now().After(deadline) {
			cmd.Process.Kill()
			t.Fatalf("the crawl never reached the site\nstdout: %s\nstderr: %s", out.String(), errs.String())
		}
		time.Sleep(20 * time.Millisecond)
	}

	if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		cmd.Process.Kill()
		t.Fatalf("the interrupt was ignored\nstdout: %s\nstderr: %s", out.String(), errs.String())
	}

	// It says it was stopped, rather than that it finished. The two mean
	// opposite things and a script cannot tell them apart any other way.
	summary := out.String() + errs.String()
	if !strings.Contains(summary, "stopped") {
		t.Errorf("the summary does not say the crawl was stopped:\n%s", summary)
	}
	if strings.Contains(summary, "finished") {
		t.Errorf("an interrupted crawl reported that it finished:\n%s", summary)
	}

	// And what it had queued is still there, so the next run continues rather
	// than starting over.
	before := served.Load()
	again := scour(t, dir, "run", path)
	if again.code != 0 {
		t.Fatalf("the second run exited %d\n%s%s", again.code, again.stdout, again.stderr)
	}
	if served.Load() <= before {
		t.Error("the second run fetched nothing, so the interrupt lost the queue")
	}
}

// TestVersionIsPrintedByTheBuiltBinary, which is the only build that has one.
func TestVersionIsPrintedByTheBuiltBinary(t *testing.T) {
	got := scour(t, t.TempDir(), "--version")
	if got.code != 0 {
		t.Fatalf("exit %d: %s", got.code, got.stderr)
	}
	if strings.TrimSpace(got.stdout+got.stderr) == "" {
		t.Error("--version printed nothing")
	}
}
