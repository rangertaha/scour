// SPDX-License-Identifier: GPL-3.0-or-later

package engine_test

import (
	"strings"
	"testing"
	"time"

	_ "github.com/rangertaha/scour/internal/cache/local"
	"github.com/rangertaha/scour/internal/engine"
)

const twoJobs = `
job "news" {
  targets = ["https://example.com/", "https://other.example/"]

  cache {
    backend = "s3"
    bucket  = "pages"
    prefix  = "news"
  }

  limits {
    max_pages = 500
    max_time  = "90m"
  }

  components {
    external = ["spider"]
  }
}

job "products" {
  targets = ["https://shop.example/"]

  politeness {
    rate        = "5s"
    concurrency = 1
  }
}
`

func TestParseSubmissionReadsEveryJob(t *testing.T) {
	subs, err := engine.ParseSubmission([]byte(twoJobs), "jobs.hcl")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if len(subs) != 2 {
		t.Fatalf("got %d jobs, want 2", len(subs))
	}
	if got := subs.Names(); got[0] != "news" || got[1] != "products" {
		t.Errorf("names = %v, want them in document order", got)
	}
}

// TestParseSubmissionKeepsConfigPerJob is the requirement itself: nested
// blocks belong to the job they are nested in, and must not leak sideways.
func TestParseSubmissionKeepsConfigPerJob(t *testing.T) {
	subs, err := engine.ParseSubmission([]byte(twoJobs), "jobs.hcl")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	news, products := subs[0], subs[1]

	if news.Config.Cache.Backend != "s3" || news.Config.Cache.Bucket != "pages" {
		t.Errorf("news cache = %+v", news.Config.Cache)
	}
	if products.Config.Cache.Backend != "" {
		t.Errorf("products inherited news's cache: %+v", products.Config.Cache)
	}

	if news.Config.Limits.MaxTime.Duration() != 90*time.Minute {
		t.Errorf("news max_time = %s", news.Config.Limits.MaxTime)
	}
	if products.Config.Limits.MaxPages != 0 {
		t.Errorf("products inherited news's limits: %+v", products.Config.Limits)
	}

	if !news.Config.Components.IsExternal(engine.StageSpider) {
		t.Error("news lost its external spider")
	}
	if products.Config.Components.IsExternal(engine.StageSpider) {
		t.Error("products inherited news's external spider")
	}

	if products.Config.Politeness.Rate.Duration() != 5*time.Second {
		t.Errorf("products rate = %s, want 5s", products.Config.Politeness.Rate)
	}
	if news.Config.Politeness.Rate != 0 {
		t.Errorf("news inherited products's rate: %s", news.Config.Politeness.Rate)
	}
}

func TestSubmissionDefaultsApplyPerJob(t *testing.T) {
	subs, err := engine.ParseSubmission([]byte(twoJobs), "jobs.hcl")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	done := subs.WithDefaults()

	if done[0].Config.Cache.Backend != "s3" {
		t.Error("defaults overwrote an explicit backend")
	}
	if done[1].Config.Cache.Backend != "local" {
		t.Errorf("second job did not get the default backend: %q", done[1].Config.Cache.Backend)
	}
	if done[1].Config.Limits.MaxDepth != engine.DefaultMaxDepth {
		t.Error("second job did not get the default depth")
	}
}

func TestSubmissionValidates(t *testing.T) {
	subs, err := engine.ParseSubmission([]byte(twoJobs), "jobs.hcl")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	// s3 is not imported here, so the news job's backend is unknown, which is
	// exactly what validation should catch before anything runs.
	if err := subs.WithDefaults().Validate(); err == nil {
		t.Fatal("accepted a job naming a backend that is not registered")
	}
}

func TestSubmissionAcceptsAGoodDocument(t *testing.T) {
	src := `
job "news" {
  targets = ["https://example.com/"]

  cache {
    backend = "local"
    dir     = "` + t.TempDir() + `"
  }
}
`
	subs, err := engine.ParseSubmission([]byte(src), "jobs.hcl")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := subs.WithDefaults().Validate(); err != nil {
		t.Fatalf("rejected a good submission: %v", err)
	}
}

func TestSubmissionRejectsDuplicateNames(t *testing.T) {
	src := `
job "news" {
  targets = ["https://example.com/"]
}

job "news" {
  targets = ["https://other.example/"]
}
`
	subs, err := engine.ParseSubmission([]byte(src), "jobs.hcl")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	err = subs.WithDefaults().Validate()
	if err == nil {
		t.Fatal("accepted the same job name twice")
	}
	if !strings.Contains(err.Error(), "twice") {
		t.Errorf("error does not explain the duplicate: %v", err)
	}
}

func TestJobRejectsBadTargets(t *testing.T) {
	cases := map[string]string{
		"no targets": ``,
		"not http":   `targets = ["file:///etc/passwd"]`,
		"no host":    `targets = ["https://"]`,
		"not a url":  `targets = ["://nonsense"]`,
	}

	for name, line := range cases {
		t.Run(name, func(t *testing.T) {
			src := "job \"j\" {\n  " + line + "\n}\n"
			subs, err := engine.ParseSubmission([]byte(src), "jobs.hcl")
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if err := subs.WithDefaults().Validate(); err == nil {
				t.Fatal("accepted it")
			}
		})
	}
}

// TestSubmissionReportsEveryJobsProblems keeps the all-at-once property across
// a document, not just within one job.
func TestSubmissionReportsEveryJobsProblems(t *testing.T) {
	src := `
job "first" {
  limits {
    max_depth = -1
  }
}

job "second" {
  targets = ["https://ok.example/"]

  politeness {
    concurrency = 999
  }
}
`
	subs, err := engine.ParseSubmission([]byte(src), "jobs.hcl")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	err = subs.WithDefaults().Validate()
	if err == nil {
		t.Fatal("accepted a document with problems in both jobs")
	}
	for _, want := range []string{"first", "second", "max_depth", "concurrency"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %s:\n%v", want, err)
		}
	}
}

// TestParseSubmissionNamesTheJobInErrors matters once a document holds ten of
// them.
func TestParseSubmissionNamesTheJobInErrors(t *testing.T) {
	src := `
job "broken" {
  politeness {
    rate = "whenever"
  }
}
`
	_, err := engine.ParseSubmission([]byte(src), "jobs.hcl")
	if err == nil {
		t.Fatal("accepted a duration that is not one")
	}
	if !strings.Contains(err.Error(), "broken") {
		t.Errorf("error does not name the job: %v", err)
	}
}

func TestEmptyDocument(t *testing.T) {
	subs, err := engine.ParseSubmission(nil, "empty.hcl")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := subs.Validate(); err == nil {
		t.Fatal("accepted a document with no jobs")
	}
}
