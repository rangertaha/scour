// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// testSite serves a two-level site plus one image, which is enough to exercise
// link following, the content-type filter and the summary table.
func testSite(t *testing.T) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()
	html := func(pattern, body string) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, body)
		})
	}
	html("/", `<a href="/cars/">cars</a> <a href="/logo.png">logo</a>`)
	html("/cars/", `<a href="/cars/one/">one</a>`)
	html("/cars/one/", `<h1>Ford F-Series 2026</h1>`)
	mux.HandleFunc("/logo.png", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		fmt.Fprint(w, "not really a png")
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// crawlDir prepares a data directory whose config makes crawls fast and
// offline-safe: no delay between requests, and robots.txt ignored because
// httptest serves none.
func crawlDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	body := "[crawl]\nrate = \"0s\"\nrobots = false\nconcurrency = 4\n"
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestCrawlEndToEnd(t *testing.T) {
	srv := testSite(t)
	dir := crawlDir(t)

	runOK(t, dir, "item", "add", "vehicle", "-u", srv.URL+"/")
	out := runOK(t, dir, "start", "vehicle", "--depth", "5")

	for _, want := range []string{"PROBABILITY", "MATCHES", "SPEED", "LATENCY", "RATE", "URL"} {
		if !strings.Contains(out, want) {
			t.Errorf("summary table missing the %s column:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "fetched") {
		t.Errorf("no crawl summary line:\n%s", out)
	}
	if !strings.Contains(out, "/cars/") {
		t.Errorf("the crawl did not report reaching /cars/:\n%s", out)
	}
}

func TestCrawlThenStatus(t *testing.T) {
	srv := testSite(t)
	dir := crawlDir(t)

	runOK(t, dir, "item", "add", "vehicle", "-u", srv.URL+"/")
	runOK(t, dir, "start", "vehicle", "--depth", "5")

	out := runOK(t, dir, "item", "ls", "vehicle")
	for _, want := range []string{"item", "targets", "frontier", "visited", "cache", "matches", "model"} {
		if !strings.Contains(out, want) {
			t.Errorf("status missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "not trained yet") {
		t.Errorf("status should say the model is untrained:\n%s", out)
	}
	if !strings.Contains(out, "html") {
		t.Errorf("status should report the formats crawled:\n%s", out)
	}
}

func TestCrawlSkipsImagesByDefault(t *testing.T) {
	srv := testSite(t)
	dir := crawlDir(t)

	runOK(t, dir, "item", "add", "vehicle", "-u", srv.URL+"/")
	out := runOK(t, dir, "start", "vehicle", "--depth", "5")

	if !strings.Contains(out, "skipped") {
		t.Errorf("the image should have been reported as skipped:\n%s", out)
	}
	if strings.Contains(out, "logo.png") {
		t.Errorf("an image should not appear in the summary:\n%s", out)
	}
}

func TestCrawlMaxPages(t *testing.T) {
	srv := testSite(t)
	dir := crawlDir(t)

	runOK(t, dir, "item", "add", "vehicle", "-u", srv.URL+"/")
	out := runOK(t, dir, "start", "vehicle", "--depth", "5", "--max-pages", "1")

	if strings.Contains(out, "/cars/one/") {
		t.Errorf("a one-page budget should not have reached the third level:\n%s", out)
	}
}

func TestCrawlJSON(t *testing.T) {
	srv := testSite(t)
	dir := crawlDir(t)

	runOK(t, dir, "item", "add", "vehicle", "-u", srv.URL+"/")
	out := runOK(t, dir, "start", "vehicle", "--depth", "5", "--json")

	if !strings.Contains(out, `"URL"`) || !strings.Contains(out, `"Score"`) {
		t.Errorf("json output missing the frontier fields:\n%s", out)
	}
}

func TestCrawlWithoutTargetsExplainsItself(t *testing.T) {
	dir := crawlDir(t)
	runOK(t, dir, "item", "add", "vehicle")

	out, err := run(t, dir, "start", "vehicle")
	if err == nil {
		t.Fatal("crawling an item with no targets must fail")
	}
	if !strings.Contains(err.Error()+out, "scour item add") {
		t.Errorf("the error should say how to add a target: %v\n%s", err, out)
	}
}

func TestCrawlResumesAndResets(t *testing.T) {
	srv := testSite(t)
	dir := crawlDir(t)

	runOK(t, dir, "item", "add", "vehicle", "-u", srv.URL+"/")
	runOK(t, dir, "start", "vehicle", "--depth", "5")

	// A second crawl sees the same pages again; the frontier persists.
	before := runOK(t, dir, "item", "ls", "vehicle")
	runOK(t, dir, "start", "vehicle", "--depth", "5", "--reset")
	after := runOK(t, dir, "item", "ls", "vehicle")

	if !strings.Contains(before, "visited") || !strings.Contains(after, "visited") {
		t.Errorf("status should report visits before and after:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestStatusOnUnknownItem(t *testing.T) {
	dir := crawlDir(t)
	if _, err := run(t, dir, "item", "ls", "absent"); err == nil {
		t.Error("status on an unknown item must fail")
	}
}

// start on a paused item resumes it. Refusing would be answering "start this"
// with "it is paused", which is the thing being asked to change.
func TestStartResumesAPausedItem(t *testing.T) {
	srv := carSite(t)
	dir := crawlDir(t)
	runOK(t, dir, "item", "add", "vehicle", "-u", srv.URL+"/")

	runOK(t, dir, "job", "pause", "vehicle")
	if shown := runOK(t, dir, "item", "ls", "vehicle"); !strings.Contains(shown, "paused") {
		t.Fatalf("the item did not record the pause:\n%s", shown)
	}

	out := runOK(t, dir, "start", "vehicle", "--depth", "5")
	if !strings.Contains(out, "resuming a paused search") {
		t.Errorf("start did not say it was resuming:\n%s", out)
	}
	if shown := runOK(t, dir, "item", "ls", "vehicle"); strings.Contains(shown, "paused") {
		t.Errorf("start left the item paused:\n%s", shown)
	}
}

// stop discards the frontier, which is the whole difference from pause, and it
// says so rather than doing it quietly. What it must not discard is the pages
// already fetched: those are the item's corpus, and the design keeps them.
func TestStopNeedsForceAndThenDiscards(t *testing.T) {
	srv := carSite(t)
	dir := crawlDir(t)
	runOK(t, dir, "item", "add", "vehicle", "-u", srv.URL+"/")
	// Budgeted, so the crawl stops with work still queued. A crawl that drains
	// its frontier leaves stop nothing to lose and nothing to ask about.
	runOK(t, dir, "start", "vehicle", "--depth", "5", "--max-pages", "1")

	before := runOK(t, dir, "job", "ls", "-i", "vehicle")
	if strings.Contains(before, "no jobs") {
		t.Fatalf("nothing was crawled, so there is nothing to discard:\n%s", before)
	}

	// Naming a destructive default "stop" is how someone loses a frontier they
	// meant to keep, so it has to be asked for.
	_, err := run(t, dir, "job", "stop", "vehicle")
	if err == nil {
		t.Fatal("stop must not discard a frontier without --force")
	}
	if !strings.Contains(err.Error(), "scour job pause vehicle") {
		t.Errorf("the refusal should name the non-destructive verb: %v", err)
	}
	if after := runOK(t, dir, "job", "ls", "-i", "vehicle"); after != before {
		t.Errorf("a refused stop changed something:\n%s\n%s", before, after)
	}

	visitedBefore := visitedCount(t, dir, "vehicle")

	out := runOK(t, dir, "job", "stop", "vehicle", "--force")
	if !strings.Contains(out, "discarded") {
		t.Errorf("stop did not say what it threw away:\n%s", out)
	}
	if after := runOK(t, dir, "item", "show", "vehicle"); !strings.Contains(after, "0 queued") {
		t.Errorf("the frontier survived a forced stop:\n%s", after)
	}
	// The pages are the item's corpus, and stop is documented as keeping them.
	// Discarding them here is how a stop costs a refetch of the whole site.
	if got := visitedCount(t, dir, "vehicle"); got != visitedBefore {
		t.Errorf("stop discarded %d visited pages, want the corpus kept at %d",
			visitedBefore-got, visitedBefore)
	}
}

// visitedCount reads the pages fetched from `scour item show`, which reports
// them as "<queued> queued / <visited> visited".
func visitedCount(t *testing.T, dir string, item string) int {
	t.Helper()
	for _, line := range strings.Split(runOK(t, dir, "item", "show", item), "\n") {
		_, rest, ok := strings.Cut(line, " / ")
		if !ok || !strings.Contains(line, "queued") {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(rest), " visited")))
		if err != nil {
			t.Fatalf("could not read the visited count from %q: %v", line, err)
		}
		return n
	}
	t.Fatalf("no frontier line in item show:\n%s", runOK(t, dir, "item", "show", item))
	return 0
}

// A job with no history says so rather than showing an empty table, and a crawl
// writes one row that says how it ended.
func TestRunsAndLog(t *testing.T) {
	srv := carSite(t)
	dir := crawlDir(t)
	runOK(t, dir, "item", "add", "vehicle", "-u", srv.URL+"/")

	// Empty is not an error outside --strict, so this is about what it says.
	if out := runOK(t, dir, "job", "runs", "vehicle"); !strings.Contains(out, "has not run yet") {
		t.Errorf("runs on a job with no history said:\n%s", out)
	}

	// Budgeted, so it ends on its budget rather than on an empty frontier.
	runOK(t, dir, "start", "vehicle", "--depth", "5", "--max-pages", "2")

	runs := runOK(t, dir, "job", "runs", "vehicle")
	if !strings.Contains(runs, "budget") {
		t.Errorf("a run stopped by --max-pages did not say so:\n%s", runs)
	}
	// The distinction a page count hides: an exhausted frontier and a spent
	// budget both leave the same number of pages behind.
	if strings.Contains(runs, "running") {
		t.Errorf("the run was left open after the crawl returned:\n%s", runs)
	}

	log := runOK(t, dir, "job", "log", "vehicle")
	for _, want := range []string{"run", "how", "budget", "pages"} {
		if !strings.Contains(log, want) {
			t.Errorf("the log is missing %q:\n%s", want, log)
		}
	}
	// The log defaults to the last run, and the pages it fetched are in it.
	if !strings.Contains(log, srv.URL) {
		t.Errorf("the log names no page the run fetched:\n%s", log)
	}

	// A second run is a second row, so the history accumulates.
	runOK(t, dir, "start", "vehicle", "--depth", "5", "--max-pages", "2")
	if again := runOK(t, dir, "job", "runs", "vehicle"); strings.Count(again, "budget") < 2 {
		t.Errorf("the second run did not join the history:\n%s", again)
	}

	// And the listing stops claiming a crawled job never ran.
	if ls := runOK(t, dir, "job", "ls"); strings.Contains(ls, "never") {
		t.Errorf("job ls still says never for a job that has run:\n%s", ls)
	}
}

// Following a run that has already ended must say so rather than waiting for
// lines that will never come.
func TestLogWillNotFollowAFinishedRun(t *testing.T) {
	srv := carSite(t)
	dir := crawlDir(t)
	runOK(t, dir, "item", "add", "vehicle", "-u", srv.URL+"/")
	runOK(t, dir, "start", "vehicle", "--depth", "5", "--max-pages", "2")

	out := runOK(t, dir, "job", "log", "vehicle", "--follow")
	if !strings.Contains(out, "already ended") {
		t.Errorf("following a finished run did not say it was over:\n%s", out)
	}
}
