// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The job service, through the binary.
//
// Everything under it has its own tests: the manager crawls a real site through
// a real node, and the control service is checked operation by operation. What
// nothing else covers is the thing an operator actually does, which is start a
// server in one process and drive it from another with no shared memory between
// them. A control plane that works in-process and not across one is a control
// plane that does not work.

// pages is a small site for a submitted job to crawl.
func pages(t *testing.T) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")

		if r.URL.Path == "/" {
			fmt.Fprint(w, `<html><head><title>Index</title></head><body>
			  <a href="/a">a</a><a href="/b">b</a>
			</body></html>`)
			return
		}
		fmt.Fprintf(w,
			`<html><head><meta property="og:title" content="Page %s"></head><body></body></html>`,
			strings.TrimPrefix(r.URL.Path, "/"))
	}))
	t.Cleanup(server.Close)
	return server
}

// jobFile writes a document that crawls the server, and returns its path.
func jobFile(t *testing.T, dir string, server *httptest.Server) string {
	t.Helper()

	path := filepath.Join(dir, "news.hcl")
	body := fmt.Sprintf(`
job "news" {
  domains = ["%s"]
  start   = ["%s/"]

  item "article" {
    property "title" {
      type     = str
      required = true
    }
  }

  scheduler {
    rate        = "1ms"
    concurrency = 2
  }
}
`, strings.TrimPrefix(server.URL, "http://"), server.URL)

	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// cluster starts a server and returns its directory and address.
func cluster(t *testing.T) (dir, address string) {
	t.Helper()

	dir = t.TempDir()
	server := start(t, dir, "server", "--name", "driver")
	return dir, waitFor(t, server, "join it with: scour server --join ")
}

// TestAJobIsSubmittedAndRunOnACluster is the whole workflow, end to end.
//
// Create, start, and read back what happened, each as a separate process
// talking to a server that is a third. This is the path the documentation
// describes and the one nothing tested before the job service existed, because
// before it there was no way to submit a job at all.
func TestAJobIsSubmittedAndRunOnACluster(t *testing.T) {
	site := pages(t)
	dir, address := cluster(t)
	path := jobFile(t, dir, site)

	if got := scour(t, dir, "job", "create", "--join", address, path); got.code != 0 {
		t.Fatalf("create: exit %d\n%s%s", got.code, got.stdout, got.stderr)
	}

	// Created is not started, and a control plane that ran a job the moment it
	// was submitted would give somebody no chance to look at it first.
	listed := scour(t, dir, "job", "list", "--join", address)
	if listed.code != 0 {
		t.Fatalf("list: exit %d\n%s%s", listed.code, listed.stdout, listed.stderr)
	}
	if !strings.Contains(listed.stdout, "news") {
		t.Errorf("the submitted job is not listed:\n%s", listed.stdout)
	}
	if !strings.Contains(listed.stdout, "stopped") {
		t.Errorf("a job that was created is not stopped:\n%s", listed.stdout)
	}

	if got := scour(t, dir, "job", "start", "--join", address, "news"); got.code != 0 {
		t.Fatalf("start: exit %d\n%s%s", got.code, got.stdout, got.stderr)
	}

	status := until(t, dir, address, "news", "done", "failed")
	if strings.Contains(status, "failed") {
		t.Fatalf("the crawl failed:\n%s", status)
	}

	stats := scour(t, dir, "job", "stats", "--join", address, "news")
	if stats.code != 0 {
		t.Fatalf("stats: exit %d\n%s%s", stats.code, stats.stdout, stats.stderr)
	}
	// The counters of a run that has ended, which is when somebody asks. They
	// live in the driver and the driver is gone, so they are only here because
	// the service wrote them down.
	if strings.Contains(stats.stdout, "fetched   0") {
		t.Errorf("a finished crawl reports having fetched nothing:\n%s", stats.stdout)
	}
}

// until polls `job status` until the job reaches one of the phases.
func until(t *testing.T, dir, address, name string, phases ...string) string {
	t.Helper()

	deadline := time.Now().Add(60 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		got := scour(t, dir, "job", "status", "--join", address, name)
		last = got.stdout + got.stderr
		for _, phase := range phases {
			if strings.Contains(got.stdout, "is "+phase) {
				return last
			}
		}
		time.Sleep(100 * time.Millisecond)
	}

	t.Fatalf("%q never reached %v:\n%s", name, phases, last)
	return last
}

// TestCreateRefusesTwiceAndUpdateRefusesWhatIsNotThere.
//
// The exit codes are the point. A refusal is the cluster answering, which is
// exit 1, and a script that treated it as exit 3 would retry a submission the
// cluster will never accept.
func TestCreateRefusesTwiceAndUpdateRefusesWhatIsNotThere(t *testing.T) {
	site := pages(t)
	dir, address := cluster(t)
	path := jobFile(t, dir, site)

	if got := scour(t, dir, "job", "create", "--join", address, path); got.code != 0 {
		t.Fatalf("create: exit %d\n%s%s", got.code, got.stdout, got.stderr)
	}

	again := scour(t, dir, "job", "create", "--join", address, path)
	if again.code != 1 {
		t.Errorf("creating twice exited %d, want 1: it is a refusal, not a failure\n%s", again.code, again.stderr)
	}

	// A document for a job nobody submitted, updated rather than created.
	other := filepath.Join(dir, "other.hcl")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, []byte(strings.Replace(string(body), `job "news"`, `job "sport"`, 1)), 0o600); err != nil {
		t.Fatal(err)
	}

	missing := scour(t, dir, "job", "update", "--join", address, other)
	if missing.code != 1 {
		t.Errorf("updating a job nobody submitted exited %d, want 1\n%s", missing.code, missing.stderr)
	}
	if !strings.Contains(missing.stderr, "create") {
		t.Errorf("the refusal does not say what to do instead: %s", missing.stderr)
	}
}

// TestAskingAClusterThatIsNotThereFailsRatherThanRefuses.
//
// The other half of the exit-code contract. Nothing listening is exit 3: the
// tool could not do what it was asked, and the document had nothing to do with
// it. NATS answers "no responders" immediately, so this does not hang.
func TestAskingAClusterThatIsNotThereFailsRatherThanRefuses(t *testing.T) {
	dir := t.TempDir()

	got := scour(t, dir, "job", "list", "--join", "nats://127.0.0.1:1")
	if got.code != 3 {
		t.Errorf("listing jobs on a cluster that is not there exited %d, want 3\n%s%s",
			got.code, got.stdout, got.stderr)
	}
}

// TestDeleteRemovesAJobFromTheCluster.
func TestDeleteRemovesAJobFromTheCluster(t *testing.T) {
	site := pages(t)
	dir, address := cluster(t)
	path := jobFile(t, dir, site)

	if got := scour(t, dir, "job", "create", "--join", address, path); got.code != 0 {
		t.Fatalf("create: exit %d\n%s%s", got.code, got.stdout, got.stderr)
	}
	if got := scour(t, dir, "job", "delete", "--join", address, "news"); got.code != 0 {
		t.Fatalf("delete: exit %d\n%s%s", got.code, got.stdout, got.stderr)
	}

	listed := scour(t, dir, "job", "list", "--join", address)
	if strings.Contains(listed.stdout, "news") {
		t.Errorf("a deleted job is still listed:\n%s", listed.stdout)
	}

	// And asking about it is a refusal rather than an empty success, which is
	// the difference between "there is no such job" and "it is doing nothing".
	status := scour(t, dir, "job", "status", "--join", address, "news")
	if status.code == 0 {
		t.Errorf("asking about a deleted job succeeded:\n%s", status.stdout)
	}
}

// TestTheClusterListsTheServerThatJoinedIt.
func TestTheClusterListsTheServerThatJoinedIt(t *testing.T) {
	dir, address := cluster(t)

	got := scour(t, dir, "cluster", "list", "--join", address)
	if got.code != 0 {
		t.Fatalf("cluster list: exit %d\n%s%s", got.code, got.stdout, got.stderr)
	}
	if !strings.Contains(got.stdout, "driver") {
		t.Errorf("the server that is running is not listed:\n%s", got.stdout)
	}
	// A node announces which stages it serves, and an operator reading this is
	// usually asking exactly that: whether there is anything here to fetch.
	if !strings.Contains(got.stdout, "download") {
		t.Errorf("the listing does not say what the node serves:\n%s", got.stdout)
	}
}
