// SPDX-License-Identifier: GPL-3.0-or-later

package engine

import (
	"fmt"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/hashicorp/hcl/v2/hclparse"
)

// An engine configuration in HCL:
//
//	cache {
//	  backend = "s3"
//	  bucket  = "pages"
//	  prefix  = "news"
//	  region  = "eu-west-1"
//	}
//
//	limits {
//	  max_pages = 500
//	  max_depth = 4
//	  max_time  = "90m"
//	}
//
//	politeness {
//	  rate        = "2s"
//	  concurrency = 4
//	  robots      = true
//	}
//
//	components {
//	  external = ["spider"]
//	  timeout  = "10m"
//	}
//
// Every block is optional, and so is every attribute in it. What is left out
// is filled by [Config.WithDefaults].
//
// # Why the schema is separate from [Config]
//
// HCL wants shapes the domain model should not have to adopt: an optional
// block has to be a pointer, and a duration has to arrive as a string because
// HCL numbers have no units. Decoding into a schema of its own and converting
// keeps those constraints where they belong, so [Config] stays the plain
// struct the API submits and the tests read.
type file struct {
	Cache      *cacheBlock      `hcl:"cache,block"`
	Limits     *limitsBlock     `hcl:"limits,block"`
	Politeness *politenessBlock `hcl:"politeness,block"`
	Components *componentsBlock `hcl:"components,block"`
}

// document is a submission: one or more job blocks, each with the engine
// configuration nested inside it.
//
//	job "news" {
//	  targets = ["https://example.com/"]
//
//	  cache {
//	    backend = "s3"
//	    bucket  = "pages"
//	  }
//
//	  limits {
//	    max_pages = 500
//	  }
//	}
//
//	job "products" {
//	  targets = ["https://shop.example/"]
//	}
//
// Several jobs in one document are submitted together and accepted or refused
// together, so a document that names a backend that does not exist in its
// third job does not leave the first two running.
type document struct {
	Jobs []jobBlock `hcl:"job,block"`
}

type jobBlock struct {
	Name    string   `hcl:"name,label"`
	Targets []string `hcl:"targets,optional"`

	Cache      *cacheBlock      `hcl:"cache,block"`
	Limits     *limitsBlock     `hcl:"limits,block"`
	Politeness *politenessBlock `hcl:"politeness,block"`
	Components *componentsBlock `hcl:"components,block"`
}

// file is the same block set without the job wrapper, which is what the job
// block's own contents are converted through.
func (b jobBlock) file() file {
	return file{
		Cache:      b.Cache,
		Limits:     b.Limits,
		Politeness: b.Politeness,
		Components: b.Components,
	}
}

type cacheBlock struct {
	Backend  string `hcl:"backend,optional"`
	Dir      string `hcl:"dir,optional"`
	Bucket   string `hcl:"bucket,optional"`
	Prefix   string `hcl:"prefix,optional"`
	Region   string `hcl:"region,optional"`
	Endpoint string `hcl:"endpoint,optional"`
	Profile  string `hcl:"profile,optional"`
}

type limitsBlock struct {
	MaxPages     int    `hcl:"max_pages,optional"`
	MaxDepth     int    `hcl:"max_depth,optional"`
	MaxTime      string `hcl:"max_time,optional"`
	MaxBodyBytes int64  `hcl:"max_body_bytes,optional"`
}

type politenessBlock struct {
	Rate        string `hcl:"rate,optional"`
	Concurrency int    `hcl:"concurrency,optional"`
	Robots      *bool  `hcl:"robots,optional"`
	UserAgent   string `hcl:"user_agent,optional"`
}

type componentsBlock struct {
	External []string `hcl:"external,optional"`
	Timeout  string   `hcl:"timeout,optional"`
}

// ParseConfig reads an engine configuration from HCL.
//
// The filename is used only for error messages, so a caller holding the source
// in memory can still produce a diagnostic that points at a line.
//
// Defaults are not applied and the result is not validated. Both are the
// caller's to do, because reading a file and accepting a job are different
// decisions: a file may legitimately be a fragment, while a job about to run
// may not.
func ParseConfig(src []byte, filename string) (Config, error) {
	parser := hclparse.NewParser()

	parsed, diags := parser.ParseHCL(src, filename)
	if diags.HasErrors() {
		return Config{}, diagError(diags)
	}

	var f file
	if diags := gohcl.DecodeBody(parsed.Body, nil, &f); diags.HasErrors() {
		return Config{}, diagError(diags)
	}
	return f.config()
}

// ParseSubmission reads one or more jobs from an HCL document.
//
// Defaults are not applied and nothing is validated, for the same reason
// [ParseConfig] does neither: reading a document and accepting a submission
// are different decisions, and only the caller knows which one it is making.
func ParseSubmission(src []byte, filename string) (Submission, error) {
	parser := hclparse.NewParser()

	parsed, diags := parser.ParseHCL(src, filename)
	if diags.HasErrors() {
		return nil, diagError(diags)
	}

	var doc document
	if diags := gohcl.DecodeBody(parsed.Body, nil, &doc); diags.HasErrors() {
		return nil, diagError(diags)
	}

	out := make(Submission, 0, len(doc.Jobs))
	for _, block := range doc.Jobs {
		cfg, err := block.file().config()
		if err != nil {
			// Name the job, because a document holding ten of them makes
			// "components.timeout is not a duration" useless on its own.
			return nil, fmt.Errorf("job %q: %w", block.Name, err)
		}
		out = append(out, Job{
			Name:    block.Name,
			Targets: block.Targets,
			Config:  cfg,
		})
	}
	return out, nil
}

// config converts the decoded schema into the domain configuration.
func (f file) config() (Config, error) {
	var cfg Config

	if b := f.Cache; b != nil {
		cfg.Cache = Cache{
			Backend:  b.Backend,
			Dir:      b.Dir,
			Bucket:   b.Bucket,
			Prefix:   b.Prefix,
			Region:   b.Region,
			Endpoint: b.Endpoint,
			Profile:  b.Profile,
		}
	}

	if b := f.Limits; b != nil {
		maxTime, err := parseDuration("limits.max_time", b.MaxTime)
		if err != nil {
			return Config{}, err
		}
		cfg.Limits = Limits{
			MaxPages:     b.MaxPages,
			MaxDepth:     b.MaxDepth,
			MaxTime:      maxTime,
			MaxBodyBytes: b.MaxBodyBytes,
		}
	}

	if b := f.Politeness; b != nil {
		rate, err := parseDuration("politeness.rate", b.Rate)
		if err != nil {
			return Config{}, err
		}
		cfg.Politeness = Politeness{
			Rate:        rate,
			Concurrency: b.Concurrency,
			Robots:      b.Robots,
			UserAgent:   b.UserAgent,
		}
	}

	if b := f.Components; b != nil {
		timeout, err := parseDuration("components.timeout", b.Timeout)
		if err != nil {
			return Config{}, err
		}
		external := make([]Stage, 0, len(b.External))
		for _, name := range b.External {
			external = append(external, Stage(name))
		}
		cfg.Components = Components{External: external, Timeout: timeout}
	}

	return cfg, nil
}

func parseDuration(field, value string) (Duration, error) {
	if value == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", field, err)
	}
	return Duration(d), nil
}

// diagError turns HCL's diagnostics into one error.
//
// All of them, not the first. HCL reports every problem it found with a line
// and a column, and discarding the rest to return one would throw away the
// best thing about it.
func diagError(diags hcl.Diagnostics) error {
	return fmt.Errorf("config: %w", diags)
}
