// SPDX-License-Identifier: GPL-3.0-or-later

package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDefaultIsValid(t *testing.T) {
	if err := Default().Validate(); err != nil {
		t.Fatalf("the built-in defaults must be valid: %v", err)
	}
}

func TestLoadFileOverridesDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	body := `
[crawl]
concurrency = 32
rate = "250ms"
max_size = "2MB"
content_types = ["html", "pdf"]

[model]
scorer = "hmm"

[[ai]]
name = "claude"
provider = "anthropic"
model = "claude-opus-5"

[[host]]
host = "example.com"
rate = "5s"
concurrency = 2
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Crawl.Concurrency != 32 {
		t.Errorf("concurrency = %d, want 32", cfg.Crawl.Concurrency)
	}
	if got, want := cfg.Crawl.Rate.Duration(), 250*time.Millisecond; got != want {
		t.Errorf("rate = %v, want %v", got, want)
	}
	if got, want := int64(cfg.Crawl.MaxSize), int64(2<<20); got != want {
		t.Errorf("max_size = %d, want %d", got, want)
	}
	if got := cfg.Crawl.ContentTypes; len(got) != 2 || got[0] != "html" || got[1] != "pdf" {
		t.Errorf("content_types = %v", got)
	}
	if cfg.Model.Scorer != "hmm" {
		t.Errorf("scorer = %q, want hmm", cfg.Model.Scorer)
	}
	// Untouched settings must keep their defaults rather than zeroing.
	if cfg.Crawl.Depth != Default().Crawl.Depth {
		t.Errorf("depth = %d, want the default %d", cfg.Crawl.Depth, Default().Crawl.Depth)
	}
	if !cfg.Crawl.Robots {
		t.Error("robots should still default to true")
	}
	if len(cfg.Hosts) != 1 || cfg.Hosts[0].Host != "example.com" {
		t.Fatalf("hosts = %+v", cfg.Hosts)
	}
	if got, want := cfg.Hosts[0].Rate.Duration(), 5*time.Second; got != want {
		t.Errorf("host rate = %v, want %v", got, want)
	}
}

func TestBrowserBlock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	body := `
[browser]
policy  = "always"
pool    = 4
timeout = "90s"
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Browser.Policy != "always" {
		t.Errorf("policy = %q", cfg.Browser.Policy)
	}
	if cfg.Browser.Pool != 4 {
		t.Errorf("pool = %d, want 4", cfg.Browser.Pool)
	}
	if got, want := cfg.Browser.Timeout.Duration(), 90*time.Second; got != want {
		t.Errorf("timeout = %v, want %v", got, want)
	}
	// Setting one field must not zero the rest of the block, which would
	// silently disable the browser for anyone who only wanted a bigger pool.
	if !cfg.Browser.Enabled {
		t.Error("enabled should still default to true")
	}
}

func TestBrowserDefaultsAreSane(t *testing.T) {
	cfg := Default()
	if !cfg.Browser.Enabled || cfg.Browser.Policy != "auto" {
		t.Errorf("browser defaults = %+v, want enabled and auto", cfg.Browser)
	}
	// A pool at or above the crawl's concurrency would let a wide crawl open a
	// tab per worker, which is the failure this bound exists to prevent.
	if cfg.Browser.Pool >= cfg.Crawl.Concurrency {
		t.Errorf("browser pool %d is not below crawl concurrency %d",
			cfg.Browser.Pool, cfg.Crawl.Concurrency)
	}
}

func TestLoadMissingExplicitFileIsAnError(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "absent.toml")); err == nil {
		t.Fatal("a named config file that does not exist must be an error")
	}
}

func TestEnvBeatsFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("[server]\nlisten = \"127.0.0.1:1\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvListen, "0.0.0.0:9999")
	t.Setenv(EnvDataDir, filepath.Join(dir, "data"))
	t.Setenv(EnvCacheDir, filepath.Join(dir, "cache"))

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.Listen != "0.0.0.0:9999" {
		t.Errorf("listen = %q, want the environment's value", cfg.Server.Listen)
	}
	if want := filepath.Join(dir, "data", "scour.db"); cfg.Store.DSN != want {
		t.Errorf("dsn = %q, want %q", cfg.Store.DSN, want)
	}
}

func TestValidateRejectsBadValues(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{"zero concurrency", func(c *Config) { c.Crawl.Concurrency = 0 }},
		{"negative depth", func(c *Config) { c.Crawl.Depth = -1 }},
		{"holdout of one", func(c *Config) { c.Model.Holdout = 1 }},
		{"min score above one", func(c *Config) { c.Model.MinScore = 1.5 }},
		{"negative browser pool", func(c *Config) { c.Browser.Pool = -1 }},
		{"unknown browser policy", func(c *Config) { c.Browser.Policy = "sometimes" }},
		{"unnamed ai block", func(c *Config) { c.AI = []AI{{Provider: "anthropic"}} }},
		{"duplicate ai name", func(c *Config) { c.AI = []AI{{Name: "x"}, {Name: "x"}} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			tt.mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Error("want an error")
			}
		})
	}
}

func TestSizeRoundTrip(t *testing.T) {
	tests := map[string]int64{
		"512B": 512,
		"10KB": 10 << 10,
		"10MB": 10 << 20,
		"2GB":  2 << 30,
		"1024": 1024,
	}
	for in, want := range tests {
		var s Size
		if err := s.UnmarshalText([]byte(in)); err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		if int64(s) != want {
			t.Errorf("%s = %d, want %d", in, int64(s), want)
		}
	}

	var bad Size
	if err := bad.UnmarshalText([]byte("ten megabytes")); err == nil {
		t.Error("want an error for an unparseable size")
	}
}

func TestPathsAreDerivedFromData(t *testing.T) {
	cfg := Default()
	cfg.Paths.Data = "/tmp/scour-data"
	cfg.Paths.Cache = "/tmp/scour-cache"

	if got, want := cfg.ModelsDir(), filepath.Join("/tmp/scour-data", "models"); got != want {
		t.Errorf("ModelsDir = %q, want %q", got, want)
	}
	if got, want := cfg.PagesDir(), filepath.Join("/tmp/scour-cache", "pages"); got != want {
		t.Errorf("PagesDir = %q, want %q", got, want)
	}
}
