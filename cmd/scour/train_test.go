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
)

// `scour job train` had no end-to-end test at all, and it is the one command that
// writes to a file somebody wrote by hand.
//
// Everything under it was covered: induction has its own tests, and so does the
// line editing that puts a locator back. What nothing exercised was the command
// itself, which reads the document twice, once as text and once through the
// parser, and has to make those two views agree. That is exactly where its
// worst defect lived: a locator induced from one job was written into another
// job's item of the same name.
//
// It also has a rule that is easy to state and easy to break: without --write
// it prints and changes nothing. A tool that edited somebody's file when they
// only asked what it would do is a tool people run once.

// trainSite serves a set of pages with a headline in a class nothing declares,
// which is what induction is for: the class is the same everywhere and nothing
// says so.
func trainSite(t *testing.T) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")

		if r.URL.Path == "/" {
			fmt.Fprint(w, `<html><head><title>Index</title></head><body>
			  <a href="/story/1">one</a><a href="/story/2">two</a><a href="/story/3">three</a>
			</body></html>`)
			return
		}
		n := strings.TrimPrefix(r.URL.Path, "/story/")
		fmt.Fprintf(w, `<html><head>
		  <meta property="og:title" content="Story %s">
		</head><body>
		  <article><h1 class="hed">Story %s</h1><p class="sub">A summary of it.</p></article>
		</body></html>`, n, n)
	}))
	t.Cleanup(server.Close)
	return server
}

// trainDoc writes a document with a property that has no locator, and fills the
// cache by crawling, which is where train reads its corpus from.
func trainDoc(t *testing.T, server *httptest.Server, jobs string) (dir, path string) {
	t.Helper()

	dir = t.TempDir()
	path = filepath.Join(dir, "job.hcl")
	if err := os.WriteFile(path, []byte(jobs), 0o600); err != nil {
		t.Fatal(err)
	}

	if got := scour(t, dir, "crawl", path); got.code != 0 {
		t.Fatalf("seeding the corpus: exit %d\n%s%s", got.code, got.stdout, got.stderr)
	}
	return dir, path
}

func oneJobDoc(server *httptest.Server) string {
	host := strings.TrimPrefix(server.URL, "http://")
	return fmt.Sprintf(`
job "news" {
  domains = ["%s"]
  start   = ["%s/"]

  scheduler {
    rate = "1ms"
  }

  item "article" {
    property "title" {
      type = str
    }

    property "summary" {
      type = str
    }
  }
}
`, host, server.URL)
}

// TestTrainPrintsAndChangesNothingWithoutWrite.
//
// The rule the command leads with. A tool that edited a file when somebody
// asked what it would do is a tool people run once, and this is the only test
// that holds the whole command to it rather than the function underneath.
func TestTrainPrintsAndChangesNothingWithoutWrite(t *testing.T) {
	server := trainSite(t)
	dir, path := trainDoc(t, server, oneJobDoc(server))

	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	got := scour(t, dir, "job", "train", "--file", path)
	if got.code != 0 {
		t.Fatalf("exit %d\n%s%s", got.code, got.stdout, got.stderr)
	}
	// It names the property and the selector it would write, because a
	// proposal somebody cannot see is one they cannot review, and reviewing is
	// the entire point of printing by default.
	if !strings.Contains(got.stdout, "article.title") {
		t.Errorf("it did not say which property it would write:\n%s", got.stdout)
	}
	if !strings.Contains(got.stdout, "meta[") && !strings.Contains(got.stdout, ".") {
		t.Errorf("it did not say what selector it would write:\n%s", got.stdout)
	}
	if !strings.Contains(got.stderr, "--write") {
		t.Errorf("it did not say how to apply the proposals:\n%s", got.stderr)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Errorf("the document changed without --write:\n%s", after)
	}
}

// TestTrainWritesALocatorAndTheDocumentStillWorks.
//
// The whole loop: induce from the cached corpus, edit the file, and have what
// comes out still be a document scour will run. An edit that produced something
// that no longer parses would be the worst possible outcome here, because the
// file is the person's own and may be the only copy.
func TestTrainWritesALocatorAndTheDocumentStillWorks(t *testing.T) {
	server := trainSite(t)
	dir, path := trainDoc(t, server, oneJobDoc(server))

	got := scour(t, dir, "job", "train", "--write", "--file", path)
	if got.code != 0 {
		t.Fatalf("exit %d\n%s%s", got.code, got.stdout, got.stderr)
	}

	edited, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(edited), "css") {
		t.Fatalf("nothing was written into the document:\n%s", edited)
	}
	if !strings.Contains(string(edited), "induced") {
		t.Errorf("the locator is not marked as one scour wrote, so a person cannot tell:\n%s", edited)
	}

	// It still parses, and it still runs.
	if v := scour(t, dir, "job", "valid", path); v.code != 0 {
		t.Fatalf("the edited document no longer validates: exit %d\n%s%s", v.code, v.stdout, v.stderr)
	}
	if r := scour(t, dir, "crawl", "--fresh", path); r.code != 0 {
		t.Fatalf("the edited document no longer runs: exit %d\n%s%s", r.code, r.stdout, r.stderr)
	}

	// And the comments and spacing a person wrote are still there: this edits
	// text rather than reprinting a parsed tree precisely so that a diff stays
	// reviewable, and that is the whole argument for the approach.
	if !strings.Contains(string(edited), `property "title"`) {
		t.Error("the edit lost part of the document")
	}
}

// TestTrainWritesIntoTheJobItLearnedFrom.
//
// Two jobs in one document, each with an item of the same name, is ordinary.
// The search for where a locator goes was line-oriented with no notion of a job
// block, so it took the first `item "article"` in the file and training the
// second job wrote its selector into the first. The unit test for that covers
// the function; this covers the command, which is where the two readings of the
// file, one as text and one through the parser, have to agree.
func TestTrainWritesIntoTheJobItLearnedFrom(t *testing.T) {
	server := trainSite(t)
	host := strings.TrimPrefix(server.URL, "http://")

	doc := fmt.Sprintf(`
job "first" {
  domains = ["%s"]
  start   = ["%s/"]

  scheduler {
    rate = "1ms"
  }

  item "article" {
    property "title" {
      type = str
    }
  }
}

job "second" {
  domains = ["%s"]
  start   = ["%s/"]

  scheduler {
    rate = "1ms"
  }

  item "article" {
    property "title" {
      type = str
    }
  }
}
`, host, server.URL, host, server.URL)

	dir := t.TempDir()
	path := filepath.Join(dir, "job.hcl")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}

	if got := scour(t, dir, "crawl", "--job", "second", path); got.code != 0 {
		t.Fatalf("seeding: exit %d\n%s%s", got.code, got.stdout, got.stderr)
	}

	got := scour(t, dir, "job", "train", "--write", "--file", path, "second")
	if got.code != 0 {
		t.Fatalf("exit %d\n%s%s", got.code, got.stdout, got.stderr)
	}

	edited, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	first, second, found := strings.Cut(string(edited), `job "second"`)
	if !found {
		t.Fatalf("the second job is gone from the document:\n%s", edited)
	}
	if strings.Contains(first, "css") {
		t.Errorf("the second job's locator was written into the first:\n%s", first)
	}
	if !strings.Contains(second, "css") {
		t.Errorf("the job that was trained got no locator:\n%s", second)
	}
}

// TestTrainWithNoCorpusSaysWhatToDo.
//
// Training reads the pages a crawl left behind, so the first thing anybody
// meets is having none. The message has to name the commands that make one,
// because "no pages" on its own is a dead end.
func TestTrainWithNoCorpusSaysWhatToDo(t *testing.T) {
	server := trainSite(t)

	dir := t.TempDir()
	path := filepath.Join(dir, "job.hcl")
	if err := os.WriteFile(path, []byte(oneJobDoc(server)), 0o600); err != nil {
		t.Fatal(err)
	}

	got := scour(t, dir, "job", "train", "--file", path)
	if got.code == 0 {
		t.Fatalf("training with nothing to learn from succeeded:\n%s%s", got.stdout, got.stderr)
	}
	if !strings.Contains(got.stderr, "scour crawl") && !strings.Contains(got.stderr, "scour scrape") {
		t.Errorf("the message does not say how to get a corpus: %s", got.stderr)
	}
}
