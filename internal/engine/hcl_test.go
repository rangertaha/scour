// SPDX-License-Identifier: GPL-3.0-or-later

package engine_test

import (
	"strings"
	"testing"
	"time"

	_ "github.com/rangertaha/scour/internal/cache/local"
	"github.com/rangertaha/scour/internal/engine"
)

func TestParseConfig(t *testing.T) {
	src := `
cache {
  backend = "s3"
  bucket  = "pages"
  prefix  = "news"
  region  = "eu-west-1"
}

limits {
  max_pages = 500
  max_depth = 4
  max_time  = "90m"
}

politeness {
  rate        = "2s"
  concurrency = 4
  robots      = false
  user_agent  = "mine"
}

components {
  external = ["spider"]
  timeout  = "10m"
}
`

	cfg, err := engine.ParseConfig([]byte(src), "job.hcl")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if cfg.Cache.Backend != "s3" || cfg.Cache.Bucket != "pages" {
		t.Errorf("cache = %+v", cfg.Cache)
	}
	if cfg.Limits.MaxPages != 500 || cfg.Limits.MaxDepth != 4 {
		t.Errorf("limits = %+v", cfg.Limits)
	}
	if cfg.Limits.MaxTime.Duration() != 90*time.Minute {
		t.Errorf("max_time = %s, want 90m", cfg.Limits.MaxTime)
	}
	if cfg.Politeness.Rate.Duration() != 2*time.Second {
		t.Errorf("rate = %s, want 2s", cfg.Politeness.Rate)
	}
	if cfg.Politeness.ObeysRobots() {
		t.Error("robots = false did not survive parsing")
	}
	if !cfg.Components.IsExternal(engine.StageSpider) {
		t.Error("external spider did not survive parsing")
	}
	if cfg.Components.Timeout.Duration() != 10*time.Minute {
		t.Errorf("timeout = %s, want 10m", cfg.Components.Timeout)
	}
}

// TestParseEmptyConfig covers the fragment case: a file that sets nothing is
// legal, and defaults do the rest.
func TestParseEmptyConfig(t *testing.T) {
	cfg, err := engine.ParseConfig(nil, "empty.hcl")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := cfg.WithDefaults().Validate(); err != nil {
		t.Fatalf("an empty config did not survive defaults and validation: %v", err)
	}
}

func TestParsePartialConfig(t *testing.T) {
	cfg, err := engine.ParseConfig([]byte("limits {\n  max_pages = 10\n}\n"), "partial.hcl")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.Limits.MaxPages != 10 {
		t.Errorf("max_pages = %d, want 10", cfg.Limits.MaxPages)
	}
	if cfg.Cache.Backend != "" {
		t.Errorf("a block that was absent was filled in: %q", cfg.Cache.Backend)
	}
}

// TestParseReportsTheLine is why HCL is worth a dependency: a client who
// mistyped something gets told where.
func TestParseReportsTheLine(t *testing.T) {
	src := "limits {\n  max_pages = \n}\n"

	_, err := engine.ParseConfig([]byte(src), "broken.hcl")
	if err == nil {
		t.Fatal("accepted a syntax error")
	}
	if !strings.Contains(err.Error(), "broken.hcl") {
		t.Errorf("error does not name the file: %v", err)
	}
}

func TestParseRejectsAnUnknownBlock(t *testing.T) {
	_, err := engine.ParseConfig([]byte("wizardry {\n  spells = 3\n}\n"), "job.hcl")
	if err == nil {
		t.Fatal("accepted a block that is not part of the schema")
	}
}

func TestParseRejectsABadDuration(t *testing.T) {
	_, err := engine.ParseConfig([]byte("politeness {\n  rate = \"soon\"\n}\n"), "job.hcl")
	if err == nil {
		t.Fatal("accepted a duration that is not one")
	}
	if !strings.Contains(err.Error(), "politeness.rate") {
		t.Errorf("error does not name the field: %v", err)
	}
}

// TestParsedConfigValidates joins the two halves: what HCL accepts still has
// to be a configuration that can run.
func TestParsedConfigValidates(t *testing.T) {
	src := `
cache {
  backend = "local"
  bucket  = "wrong"
}
limits {
  max_depth = -1
}
`
	cfg, err := engine.ParseConfig([]byte(src), "job.hcl")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("a parseable but impossible config was accepted")
	}
}
