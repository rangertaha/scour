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

// The whole loop, through the built binary: crawl to fill a cache, propose
// labels from seed terms, train from them, and use what comes out.
//
// Nothing else covers it. bayes has its own tests and so does the store, but
// until now nothing created a topic at all: Train and Put had no callers outside
// tests, which is the class this repository keeps finding.

// twoSubjects serves pages about two clearly different things, so a classifier
// trained on them has something real to separate.
func twoSubjects(t *testing.T) *httptest.Server {
	t.Helper()

	climate := []string{
		"Emissions from shipping must fall to meet the carbon budget agreed last year.",
		"The climate committee criticised the pace of decarbonisation across the sector.",
		"Net zero requires carbon capture at scale, the emissions report said today.",
		"Ministers delayed the decarbonisation plan despite rising emissions from coal.",
	}
	sport := []string{
		"The manager named an unchanged squad for the cup fixture on Saturday.",
		"The transfer window closed with the squad unchanged and no signings at all.",
		"A late goal settled the match after the visiting side dominated possession.",
		"The league confirmed the fixture would be replayed after the floodlight failure.",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")

		if r.URL.Path == "/" {
			fmt.Fprint(w, `<html><head><title>Index</title></head><body>`)
			for i := range climate {
				fmt.Fprintf(w, `<a href="/climate/%d">c%d</a>`, i, i)
			}
			for i := range sport {
				fmt.Fprintf(w, `<a href="/sport/%d">s%d</a>`, i, i)
			}
			fmt.Fprint(w, `</body></html>`)
			return
		}

		body, which := climate, "climate"
		if strings.HasPrefix(r.URL.Path, "/sport/") {
			body, which = sport, "sport"
		}
		var n int
		fmt.Sscanf(strings.TrimPrefix(r.URL.Path, "/"+which+"/"), "%d", &n)
		if n < 0 || n >= len(body) {
			http.NotFound(w, r)
			return
		}
		fmt.Fprintf(w, `<html><head><meta property="og:title" content="%s %d"></head>
		  <body><article>%s</article></body></html>`, which, n, body[n])
	}))
	t.Cleanup(server.Close)
	return server
}

// corpusFor crawls the site so there is a cache to label.
func corpusFor(t *testing.T, server *httptest.Server) (dir string) {
	t.Helper()

	dir = t.TempDir()
	host := strings.TrimPrefix(server.URL, "http://")
	job := filepath.Join(dir, "job.hcl")
	if err := os.WriteFile(job, []byte(fmt.Sprintf(`
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
  }
}
`, host, server.URL)), 0o600); err != nil {
		t.Fatal(err)
	}

	if got := scour(t, dir, "crawl", job); got.code != 0 {
		t.Fatalf("filling the cache: exit %d\n%s%s", got.code, got.stdout, got.stderr)
	}
	return dir
}

// TestTheWholeTopicLoop: propose, correct, train, use.
func TestTheWholeTopicLoop(t *testing.T) {
	server := twoSubjects(t)
	dir := corpusFor(t, server)

	labels := filepath.Join(dir, "labels.hcl")
	if err := os.WriteFile(labels, []byte(`
topic "climate" {
  terms = ["emissions", "decarbonisation", "carbon"]
}
`), 0o600); err != nil {
		t.Fatal(err)
	}

	// Nothing is written without --write, which is the rule the whole loop
	// rests on: the file is the person's.
	before, err := os.ReadFile(labels)
	if err != nil {
		t.Fatal(err)
	}
	if got := scour(t, dir, "topic", "propose", labels); got.code != 0 {
		t.Fatalf("propose: exit %d\n%s%s", got.code, got.stdout, got.stderr)
	}
	after, err := os.ReadFile(labels)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("propose edited the document without --write")
	}

	// With --write it proposes, and what it proposed is readable URLs rather
	// than cache keys, because a label nobody can read is one they cannot check.
	got := scour(t, dir, "topic", "propose", labels, "--write")
	if got.code != 0 {
		t.Fatalf("propose --write: exit %d\n%s%s", got.code, got.stdout, got.stderr)
	}

	proposed, err := os.ReadFile(labels)
	if err != nil {
		t.Fatal(err)
	}
	text := string(proposed)
	if !strings.Contains(text, "about = [") || !strings.Contains(text, "not = [") {
		t.Fatalf("nothing was proposed:\n%s", text)
	}
	if !strings.Contains(text, server.URL+"/climate/0") {
		t.Errorf("the labels do not name pages by URL:\n%s", text)
	}
	if strings.Contains(text, "terms = [") == false {
		t.Errorf("the seed terms were lost:\n%s", text)
	}

	// The bootstrap should have put the climate pages on one side and the sport
	// pages on the other. It is a weak classifier, so this asserts the split
	// happened rather than that it was perfect.
	about, _, _ := strings.Cut(strings.SplitN(text, "about = [", 2)[1], "]")
	if !strings.Contains(about, "/climate/") {
		t.Errorf("no climate page was proposed as the subject:\n%s", about)
	}

	// Train from what the file now says.
	trained := scour(t, dir, "topic", "train", labels)
	if trained.code != 0 {
		t.Fatalf("train: exit %d\n%s%s", trained.code, trained.stdout, trained.stderr)
	}
	if !strings.Contains(trained.stdout, "climate@1") {
		t.Errorf("training did not say what it produced:\n%s", trained.stdout)
	}
	if !strings.Contains(trained.stderr, `subject = "climate@1"`) {
		t.Errorf("it did not say how to use it:\n%s", trained.stderr)
	}

	// It is listed, and it can be read back.
	list := scour(t, dir, "topic", "list")
	if !strings.Contains(list.stdout, "climate@1") {
		t.Errorf("ls does not show it:\n%s", list.stdout)
	}

	show := scour(t, dir, "topic", "show", "climate@1")
	if show.code != 0 {
		t.Fatalf("show: exit %d\n%s%s", show.code, show.stdout, show.stderr)
	}
	if !strings.Contains(show.stdout, "bayes") || !strings.Contains(show.stdout, "strongest") {
		t.Errorf("show does not print what it learned:\n%s", show.stdout)
	}

	// Training again writes the next version rather than replacing one, because
	// a job may have pinned the first.
	second := scour(t, dir, "topic", "train", labels)
	if second.code != 0 {
		t.Fatalf("second train: exit %d\n%s%s", second.code, second.stdout, second.stderr)
	}
	if !strings.Contains(second.stdout, "climate@2") {
		t.Errorf("retraining did not write the next version:\n%s", second.stdout)
	}

	// And rm takes one version, leaving the other.
	if got := scour(t, dir, "topic", "delete", "climate@1"); got.code != 0 {
		t.Fatalf("rm: exit %d\n%s%s", got.code, got.stdout, got.stderr)
	}
	list = scour(t, dir, "topic", "list")
	if strings.Contains(list.stdout, "climate@1") {
		t.Errorf("rm left it behind:\n%s", list.stdout)
	}
	if !strings.Contains(list.stdout, "climate@2") {
		t.Errorf("rm took the wrong version:\n%s", list.stdout)
	}
}

// TestProposeKeepsACorrection.
//
// The loop only works if editing the file means something. A proposal that
// overwrote a decision somebody made would make correcting it pointless, and
// they would have to correct it again after every run.
func TestProposeKeepsACorrection(t *testing.T) {
	server := twoSubjects(t)
	dir := corpusFor(t, server)

	// A page the terms would have proposed as not the subject, corrected by
	// hand to say it is.
	labels := filepath.Join(dir, "labels.hcl")
	if err := os.WriteFile(labels, []byte(fmt.Sprintf(`
topic "climate" {
  terms = ["emissions", "decarbonisation", "carbon"]

  about = [
    "%s/sport/0",
  ]
}
`, server.URL)), 0o600); err != nil {
		t.Fatal(err)
	}

	if got := scour(t, dir, "topic", "propose", labels, "--write"); got.code != 0 {
		t.Fatalf("propose: exit %d\n%s%s", got.code, got.stdout, got.stderr)
	}

	text, err := os.ReadFile(labels)
	if err != nil {
		t.Fatal(err)
	}

	about, _, _ := strings.Cut(strings.SplitN(string(text), "about = [", 2)[1], "]")
	if !strings.Contains(about, server.URL+"/sport/0") {
		t.Errorf("the correction was dropped:\n%s", text)
	}

	not := ""
	if _, rest, found := strings.Cut(string(text), "not = ["); found {
		not, _, _ = strings.Cut(rest, "]")
	}
	if strings.Contains(not, server.URL+"/sport/0") {
		t.Errorf("the corrected page was proposed on the other side as well:\n%s", text)
	}
}

// TestTrainingNeedsBothSides.
//
// A classifier trained on nothing but positives has learned that everything is
// the subject, and would say so confidently. The refusal names the subject.
func TestTrainingNeedsBothSides(t *testing.T) {
	server := twoSubjects(t)
	dir := corpusFor(t, server)

	labels := filepath.Join(dir, "labels.hcl")
	if err := os.WriteFile(labels, []byte(fmt.Sprintf(`
topic "climate" {
  about = [
    "%s/climate/0",
    "%s/climate/1",
  ]
}
`, server.URL, server.URL)), 0o600); err != nil {
		t.Fatal(err)
	}

	got := scour(t, dir, "topic", "train", labels)
	if got.code == 0 {
		t.Fatalf("training on one side only succeeded:\n%s%s", got.stdout, got.stderr)
	}
	if !strings.Contains(got.stderr, "climate") {
		t.Errorf("the refusal does not name the subject: %s", got.stderr)
	}
}

// TestALabelsFileSurvivesAnAwkwardURL.
//
// An HCL quoted string is a template: `${` opens an interpolation, and the
// escape for a literal one is `$${`. Go's %q knows nothing about that, so a
// crawled URL carrying `${` was written unescaped and the labels file stopped
// parsing, with a diagnostic about an unknown variable rather than an error
// naming the URL. Since propose rewrites the file in place, the corrections in
// it were then recoverable only by hand.
func TestALabelsFileSurvivesAnAwkwardURL(t *testing.T) {
	server := twoSubjects(t)
	dir := corpusFor(t, server)

	awkward := server.URL + "/r?u=${next}"
	escaped := strings.ReplaceAll(awkward, "${", "$${")

	labels := filepath.Join(dir, "labels.hcl")
	if err := os.WriteFile(labels, []byte(
		"\ntopic \"climate\" {\n  terms = [\"emissions\", \"carbon\"]\n\n  about = [\n    \""+
			escaped+"\",\n  ]\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if got := scour(t, dir, "topic", "propose", labels, "--write"); got.code != 0 {
		t.Fatalf("propose: exit %d\n%s%s", got.code, got.stdout, got.stderr)
	}

	// The file it wrote is still a labels document.
	again := scour(t, dir, "topic", "propose", labels)
	if again.code != 0 {
		t.Fatalf("the rewritten document no longer parses: exit %d\n%s", again.code, again.stderr)
	}

	text, err := os.ReadFile(labels)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(text), "$${next}") {
		t.Errorf("the interpolation was not kept escaped for HCL:\n%s", text)
	}
	if strings.Contains(string(text), "\""+awkward+"\"") {
		t.Errorf("the URL was written as a live template:\n%s", text)
	}
}
