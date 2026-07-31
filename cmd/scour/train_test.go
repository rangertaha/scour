// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// carSite serves a listing and a set of detail pages that share one markup
// shape, which is what induction needs to find a repeating record.
func carSite(t *testing.T) *httptest.Server {
	t.Helper()

	cars := map[string][3]string{
		"one":   {"Ford", "F-Series", "2026"},
		"two":   {"Chevrolet", "Silverado", "2025"},
		"three": {"Toyota", "Tacoma", "2026"},
		"four":  {"Ram", "1500", "2024"},
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		if r.URL.Path == "/" {
			fmt.Fprint(w, `<a href="/cars/">cars</a><a href="/careers/">careers</a>`)
			return
		}
		http.NotFound(w, r)
	})
	mux.HandleFunc("/careers/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<h1>Jobs</h1><p>We are hiring.</p>`)
	})
	mux.HandleFunc("/cars/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		if r.URL.Path == "/cars/" {
			for slug := range cars {
				fmt.Fprintf(w, `<a href="/cars/%s/">%s</a>`, slug, slug)
			}
			return
		}
		slug := strings.Trim(strings.TrimPrefix(r.URL.Path, "/cars/"), "/")
		c, ok := cars[slug]
		if !ok {
			http.NotFound(w, r)
			return
		}
		fmt.Fprintf(w, `<div class="vehicle"><dl>
<dt>Make</dt><dd class="make">%s</dd>
<dt>Model</dt><dd class="model">%s</dd>
<dt>Year</dt><dd class="year">%s</dd>
</dl></div>`, c[0], c[1], c[2])
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// trained sets up an item, crawls the site and trains, returning the dir.
func trained(t *testing.T) (string, *httptest.Server) {
	t.Helper()
	srv := carSite(t)
	dir := crawlDir(t)

	runOK(t, dir, "item", "add", "vehicle", "--alias", "car", "-u", srv.URL+"/")
	runOK(t, dir, "item", "add", "vehicle", "-p", "make", "-e", "Ford")
	runOK(t, dir, "item", "add", "vehicle", "-p", "model", "-e", "F-Series")
	runOK(t, dir, "item", "add", "vehicle", "-p", "year", "-e", "2026")
	runOK(t, dir, "start", "vehicle", "--depth", "5")
	return dir, srv
}

func TestTrainThenRulesThenSearch(t *testing.T) {
	dir, _ := trained(t)

	out := runOK(t, dir, "train", "vehicle")
	if !strings.Contains(out, "model written to") {
		t.Fatalf("train did not report writing a model:\n%s", out)
	}
	if strings.Contains(out, "no records extracted") {
		t.Fatalf("training extracted nothing:\n%s", out)
	}

	rules := runOK(t, dir, "rules", "vehicle")
	for _, want := range []string{"ID", "PID", "HIT", "PROP", "XPATH", "SELECTOR"} {
		if !strings.Contains(rules, want) {
			t.Errorf("rules table missing %s:\n%s", want, rules)
		}
	}
	if !strings.Contains(rules, "make") {
		t.Errorf("no rule was induced for make:\n%s", rules)
	}

	search := runOK(t, dir, "stream", "vehicle")
	for _, want := range []string{"ID", "CONF", "FORMAT", "MAKE", "MODEL", "YEAR"} {
		if !strings.Contains(search, want) {
			t.Errorf("search table missing %s:\n%s", want, search)
		}
	}
	// The makes must land under MAKE, not shuffled between columns.
	for _, want := range []string{"Ford", "Chevrolet", "Toyota", "Ram"} {
		if !strings.Contains(search, want) {
			t.Errorf("search output missing %s:\n%s", want, search)
		}
	}
}

func TestLabelThenRetrainKeepsIDs(t *testing.T) {
	dir, _ := trained(t)
	runOK(t, dir, "train", "vehicle")

	before := runOK(t, dir, "stream", "vehicle", "--json")

	out := runOK(t, dir, "invalid", "vehicle", "1")
	if !strings.Contains(out, "marked invalid") {
		t.Fatalf("labelling did not report success:\n%s", out)
	}

	runOK(t, dir, "train", "vehicle")
	after := runOK(t, dir, "stream", "vehicle", "--json")

	if !strings.Contains(after, `"ID": 1`) {
		t.Errorf("record 1 did not survive retraining:\n%s", after)
	}
	if !strings.Contains(after, `"Label": "invalid"`) {
		t.Errorf("the label was lost by retraining:\n%s", after)
	}
	if strings.Count(before, `"ID"`) != strings.Count(after, `"ID"`) {
		t.Errorf("record count changed across retraining")
	}

	only := runOK(t, dir, "stream", "vehicle", "--label", "invalid")
	if !strings.Contains(only, "showing 1 of 1") {
		t.Errorf("filtering by label did not find the labelled record:\n%s", only)
	}
}

func TestLabelUnknownRecord(t *testing.T) {
	dir, _ := trained(t)
	runOK(t, dir, "train", "vehicle")

	if _, err := run(t, dir, "invalid", "vehicle", "9999"); err == nil {
		t.Error("labelling an unknown record must fail rather than report success")
	}
	if _, err := run(t, dir, "invalid", "vehicle", "not-a-number"); err == nil {
		t.Error("a non-numeric id must fail")
	}
}

func TestSearchConfidenceFilter(t *testing.T) {
	dir, _ := trained(t)
	runOK(t, dir, "train", "vehicle")

	// Nothing scores 1.0, so this filters everything out, and the message has
	// to say so rather than suggesting a model has not been trained.
	out := runOK(t, dir, "stream", "vehicle", "--confidence", "1")
	if !strings.Contains(out, "no records matched") {
		t.Errorf("expected an empty filtered result:\n%s", out)
	}

	if _, err := run(t, dir, "stream", "vehicle", "--confidence", "50"); err == nil {
		t.Error("--confidence is a probability, so 50 must be rejected")
	}
}

func TestTrainBeforeCrawl(t *testing.T) {
	dir := crawlDir(t)
	runOK(t, dir, "item", "add", "vehicle", "-p", "make", "-e", "Ford")

	if _, err := run(t, dir, "train", "vehicle"); err == nil {
		t.Error("training with nothing cached must fail")
	}
}

func TestTrainWithoutProperties(t *testing.T) {
	dir, srv := trained(t)
	runOK(t, dir, "item", "add", "other", "-u", srv.URL+"/")

	if _, err := run(t, dir, "train", "other"); err == nil {
		t.Error("training an item with no properties must fail")
	}
}

func TestRulesBeforeTraining(t *testing.T) {
	dir := crawlDir(t)
	runOK(t, dir, "item", "add", "vehicle")

	out := runOK(t, dir, "rules", "vehicle")
	if !strings.Contains(out, "scour train") {
		t.Errorf("rules should say how to produce some:\n%s", out)
	}
}

func TestSearchBeforeTraining(t *testing.T) {
	dir := crawlDir(t)
	runOK(t, dir, "item", "add", "vehicle")

	out := runOK(t, dir, "stream", "vehicle")
	if !strings.Contains(out, "scour train") {
		t.Errorf("search should say how to produce records:\n%s", out)
	}
}

func TestStatusAfterTraining(t *testing.T) {
	dir, _ := trained(t)
	runOK(t, dir, "train", "vehicle")

	out := runOK(t, dir, "item", "ls", "vehicle")
	if strings.Contains(out, "not trained yet") {
		t.Errorf("status still says untrained after training:\n%s", out)
	}
	if !strings.Contains(out, "rules") {
		t.Errorf("status should report the rule count:\n%s", out)
	}
}

func TestTrainingTheScorerChangesWhatIsCrawled(t *testing.T) {
	// A site whose records live in one small section, surrounded by pages that
	// hold none. Focusing is only observable when there is something to avoid.
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		p := r.URL.Path
		switch {
		case p == "/":
			fmt.Fprint(w, `<a href="/cars/">cars</a><a href="/legal/">legal</a><a href="/blog/">blog</a>`)
		case p == "/cars/":
			for i := range 8 {
				fmt.Fprintf(w, `<a href="/cars/%d/">car %d</a>`, i, i)
			}
		case strings.HasPrefix(p, "/cars/"):
			id := strings.Trim(strings.TrimPrefix(p, "/cars/"), "/")
			fmt.Fprintf(w, `<div class="vehicle"><dl>
<dt>Make</dt><dd class="make">Ford</dd>
<dt>Model</dt><dd class="model">M%s</dd>
<dt>Year</dt><dd class="year">20%02s</dd></dl></div>`, id, id)
		case p == "/legal/" || p == "/blog/":
			section := strings.Trim(p, "/")
			for i := range 8 {
				fmt.Fprintf(w, `<a href="/%s/%d/">%s %d</a>`, section, i, section, i)
			}
		default:
			fmt.Fprint(w, `<h1>Boilerplate</h1><p>No records here.</p>`)
		}
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	dir := crawlDir(t)
	runOK(t, dir, "item", "add", "vehicle", "--alias", "car", "-u", srv.URL+"/")
	runOK(t, dir, "item", "add", "vehicle", "-p", "make", "-e", "Ford")
	runOK(t, dir, "item", "add", "vehicle", "-p", "model", "-e", "M1")
	runOK(t, dir, "item", "add", "vehicle", "-p", "year", "-e", "2001")

	cold := runOK(t, dir, "start", "vehicle", "--depth", "4")
	if !strings.Contains(cold, "seeded from aliases and examples") {
		t.Errorf("the first crawl should say it is running on a seeded model:\n%s", cold)
	}

	out := runOK(t, dir, "train", "vehicle")
	if !strings.Contains(out, "examples") {
		t.Fatalf("training did not report what the scorer learned from:\n%s", out)
	}
	if !strings.Contains(out, "scorer written to") {
		t.Fatalf("no scorer was saved:\n%s", out)
	}
	if !strings.Contains(out, "path:cars") {
		t.Errorf("the scorer did not learn the section holding the records:\n%s", out)
	}

	// The trained crawl must say so, and must spend its budget on /cars/.
	warm := runOK(t, dir, "start", "vehicle", "--reset", "--depth", "4", "--max-pages", "12")
	if !strings.Contains(warm, "scoring trained model") {
		t.Errorf("the second crawl did not pick up the trained model:\n%s", warm)
	}

	rows := runOK(t, dir, "start", "vehicle", "--json")
	var cars, noise int
	for _, line := range strings.Split(rows, "\n") {
		if !strings.Contains(line, `"URL"`) {
			continue
		}
		switch {
		case strings.Contains(line, "/cars"):
			cars++
		case strings.Contains(line, "/legal"), strings.Contains(line, "/blog"):
			noise++
		}
	}
	if cars == 0 {
		t.Fatalf("the focused crawl never reached the records:\n%s", rows)
	}
	if noise > cars {
		t.Errorf("the focused crawl preferred the noise: %d relevant, %d not", cars, noise)
	}
}

func TestResetReallyStartsOver(t *testing.T) {
	dir, _ := trained(t)

	before := runOK(t, dir, "item", "ls", "vehicle")
	if !strings.Contains(before, "visited") {
		t.Fatalf("nothing was crawled:\n%s", before)
	}

	// A reset that left the visited set behind would fetch nothing at all,
	// which looks like success and produces an empty corpus to train on.
	out := runOK(t, dir, "start", "vehicle", "--reset", "--depth", "5")
	if strings.Contains(out, "\n0 fetched") {
		t.Errorf("--reset fetched nothing, so the visited set survived it:\n%s", out)
	}
}
