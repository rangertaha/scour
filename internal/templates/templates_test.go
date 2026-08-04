// SPDX-License-Identifier: GPL-3.0-or-later

package templates

import (
	"sort"
	"strings"
	"testing"

	"github.com/rangertaha/scour/internal/engine"
)

// TestEveryTemplateIsAJobThatWorks is the promise a shipped template makes.
//
// A sample that does not validate is worse than none, because the first thing
// anybody does with one is assume it does.
func TestEveryTemplateIsAJobThatWorks(t *testing.T) {
	for _, tmpl := range All() {
		t.Run(tmpl.Name, func(t *testing.T) {
			src, err := Render(tmpl.Name, "test")
			if err != nil {
				t.Fatalf("render: %v", err)
			}

			doc, err := engine.Parse(src, tmpl.Name+".hcl")
			if err != nil {
				t.Fatalf("does not parse:\n%s\n%v", src, err)
			}
			if err := doc.Validate(); err != nil {
				t.Fatalf("does not validate:\n%v", err)
			}

			if len(doc.Jobs) != 1 {
				t.Fatalf("holds %d jobs, want 1", len(doc.Jobs))
			}
			if doc.Jobs[0].Name != "test" {
				t.Errorf("job is named %q, want the name it was rendered with", doc.Jobs[0].Name)
			}
		})
	}
}

// TestEveryTemplateExtractsSomething catches a template that parses but asks
// for nothing, which would be a working document and a useless one.
func TestEveryTemplateExtractsSomething(t *testing.T) {
	for _, tmpl := range All() {
		t.Run(tmpl.Name, func(t *testing.T) {
			src, _ := Render(tmpl.Name, "test")
			doc, err := engine.Parse(src, "t.hcl")
			if err != nil {
				t.Fatal(err)
			}
			job := doc.Jobs[0]

			if len(job.Items) == 0 {
				t.Fatal("extracts nothing")
			}
			for _, item := range job.Items {
				if len(item.Properties) < 2 {
					t.Errorf("item %q has %d properties, which is not a starting point",
						item.Name, len(item.Properties))
				}
			}
			if len(job.Start) == 0 {
				t.Error("has nowhere to start")
			}
			if len(job.Exporters) == 0 {
				t.Error("has nowhere to put what it finds")
			}
		})
	}
}

// TestTemplatesArePolite: a starting point is copied without being read, so
// its defaults are what most crawls will actually run at.
func TestTemplatesArePolite(t *testing.T) {
	for _, tmpl := range All() {
		t.Run(tmpl.Name, func(t *testing.T) {
			src, _ := Render(tmpl.Name, "test")
			doc, _ := engine.Parse(src, "t.hcl")
			job := doc.Jobs[0].Resolved()

			if !job.Downloader.ObeysRobots() {
				t.Error("does not obey robots.txt")
			}
			rate, err := job.Scheduler.RateDuration()
			if err != nil {
				t.Fatal(err)
			}
			if rate < engine.DefaultRate {
				t.Errorf("rate is %s, faster than the default %s", rate, engine.DefaultRate)
			}
			if job.Scheduler.Parallelism() > 4 {
				t.Errorf("concurrency is %d against one host", job.Scheduler.Parallelism())
			}
			// A starting point with no ceiling is one somebody leaves running.
			if job.Scheduler.Pages() == 0 {
				t.Error("has no page budget")
			}
		})
	}
}

// TestSummariesAndFilesAgree stops a template being added without a summary,
// or a summary outliving its file.
func TestSummariesAndFilesAgree(t *testing.T) {
	onDisk, err := fileNames()
	if err != nil {
		t.Fatal(err)
	}

	described := Names()
	sort.Strings(described)

	if strings.Join(onDisk, ",") != strings.Join(described, ",") {
		t.Errorf("files are %v, summaries describe %v", onDisk, described)
	}
	for _, tmpl := range All() {
		if tmpl.Summary == "" {
			t.Errorf("%s has no summary", tmpl.Name)
		}
	}
}

func TestDefaultIsFirst(t *testing.T) {
	if got := Names()[0]; got != Default {
		t.Errorf("first is %q, want the default %q", got, Default)
	}
	if !Has(Default) {
		t.Fatalf("the default template %q does not exist", Default)
	}
}

func TestUnknownTemplateListsWhatThereIs(t *testing.T) {
	_, err := Render("carrier-pigeon", "j")
	if err == nil {
		t.Fatal("rendered a template that does not exist")
	}
	for _, name := range Names() {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("the error does not mention %q: %v", name, err)
		}
	}
}

func TestEmptyNamesUseTheDefaults(t *testing.T) {
	src, err := Render("", "")
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(string(src), `job "example"`) {
		t.Error("an unnamed job is not called example")
	}
}

// TestJobNameCannotBreakOut is why the name is quoted by the template function
// rather than written into the file with quotes around it.
func TestJobNameCannotBreakOut(t *testing.T) {
	src, err := Render(Default, `evil" { } job "sneaky`)
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	doc, err := engine.Parse(src, "t.hcl")
	if err != nil {
		return // refusing it outright is also fine
	}
	if len(doc.Jobs) != 1 {
		t.Fatalf("a name with a quote in it produced %d jobs", len(doc.Jobs))
	}
}
