// SPDX-License-Identifier: GPL-3.0-or-later

package engine_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/rangertaha/scour/internal/cache"
	_ "github.com/rangertaha/scour/internal/cache/local"
	"github.com/rangertaha/scour/internal/engine"
)

func TestDefaultsFillWhatWasLeftOut(t *testing.T) {
	got := engine.Config{}.WithDefaults()

	if got.Cache.Backend != cache.DefaultBackend {
		t.Errorf("cache backend = %q, want %q", got.Cache.Backend, cache.DefaultBackend)
	}
	if got.Cache.Dir == "" {
		t.Error("local backend was left without a directory")
	}
	if got.Limits.MaxDepth != engine.DefaultMaxDepth {
		t.Errorf("max depth = %d, want %d", got.Limits.MaxDepth, engine.DefaultMaxDepth)
	}
	if !got.Politeness.ObeysRobots() {
		t.Error("robots defaulted to off")
	}
	if got.Politeness.UserAgent == "" {
		t.Error("no user agent")
	}
}

// TestDefaultsDoNotOverrideTheClient is the whole point of applying them at
// submission: what the client asked for survives.
func TestDefaultsDoNotOverrideTheClient(t *testing.T) {
	off := false
	submitted := engine.Config{
		Limits:     engine.Limits{MaxDepth: 99},
		Politeness: engine.Politeness{Robots: &off, UserAgent: "mine"},
	}

	got := submitted.WithDefaults()

	if got.Limits.MaxDepth != 99 {
		t.Errorf("max depth = %d, want the submitted 99", got.Limits.MaxDepth)
	}
	if got.Politeness.ObeysRobots() {
		t.Error("robots was turned back on over an explicit false")
	}
	if got.Politeness.UserAgent != "mine" {
		t.Errorf("user agent = %q, want the submitted one", got.Politeness.UserAgent)
	}
}

// TestWithDefaultsDoesNotMutate keeps "what they sent" and "what it will do"
// as two separate things.
func TestWithDefaultsDoesNotMutate(t *testing.T) {
	submitted := engine.Config{}
	_ = submitted.WithDefaults()

	if submitted.Cache.Backend != "" || submitted.Limits.MaxDepth != 0 {
		t.Error("WithDefaults changed the configuration it was called on")
	}
}

func TestValidateAcceptsAGoodConfig(t *testing.T) {
	cfg := engine.Config{
		Cache:  engine.Cache{Backend: "local", Dir: t.TempDir()},
		Limits: engine.Limits{MaxPages: 100, MaxDepth: 3},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("rejected a good config: %v", err)
	}
}

func TestValidateRejectsAnUnknownBackend(t *testing.T) {
	cfg := engine.Config{Cache: engine.Cache{Backend: "carrier-pigeon"}}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("accepted a backend that does not exist")
	}
	// The message has to say what is available, or the client is guessing.
	if !strings.Contains(err.Error(), "local") {
		t.Errorf("error does not list the available backends: %v", err)
	}
}

func TestValidateRequiresABucketForCloudBackends(t *testing.T) {
	for _, backend := range []string{"s3", "gcs"} {
		t.Run(backend, func(t *testing.T) {
			// Registered only in their own packages, which this test does not
			// import, so validation should complain about the backend itself.
			// What matters is that it complains rather than accepting silently.
			cfg := engine.Config{Cache: engine.Cache{Backend: backend}}
			if err := cfg.Validate(); err == nil {
				t.Fatal("accepted a cloud backend with no bucket")
			}
		})
	}
}

func TestValidateRejectsABucketOnTheLocalBackend(t *testing.T) {
	cfg := engine.Config{Cache: engine.Cache{Backend: "local", Bucket: "oops"}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("accepted a bucket on a directory backend")
	}
}

// TestValidateReportsEverythingAtOnce is a usability property with teeth: a
// client fixing one error per round trip gives up.
func TestValidateReportsEverythingAtOnce(t *testing.T) {
	cfg := engine.Config{
		Cache:      engine.Cache{Backend: "nope"},
		Limits:     engine.Limits{MaxPages: -1, MaxDepth: -2},
		Politeness: engine.Politeness{Concurrency: 1000},
		Components: engine.Components{External: []engine.Stage{"wizard"}},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("accepted a config with five problems")
	}

	for _, want := range []string{"cache.backend", "max_pages", "max_depth", "concurrency", "external"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %s:\n%v", want, err)
		}
	}
}

func TestExternalStages(t *testing.T) {
	cfg := engine.Config{
		Components: engine.Components{External: []engine.Stage{engine.StageSpider}},
	}.WithDefaults()

	if !cfg.Components.IsExternal(engine.StageSpider) {
		t.Error("spider was not marked external")
	}
	if cfg.Components.IsExternal(engine.StageDownloader) {
		t.Error("downloader was marked external without being asked for")
	}
	if cfg.Components.Timeout == 0 {
		t.Error("an external stage was left with no timeout")
	}
}

func TestValidateRejectsAnUnknownStage(t *testing.T) {
	cfg := engine.Config{Components: engine.Components{External: []engine.Stage{"scheduler"}}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("accepted a stage that cannot be replaced")
	}
}

func TestValidateRejectsADuplicateStage(t *testing.T) {
	cfg := engine.Config{Components: engine.Components{
		External: []engine.Stage{engine.StageSpider, engine.StageSpider},
	}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("accepted the same stage twice")
	}
}

// TestConfigRoundTripsAsJSON covers the wire form, since this is what a client
// submits.
func TestConfigRoundTripsAsJSON(t *testing.T) {
	want := engine.Config{
		Cache:      engine.Cache{Backend: "s3", Bucket: "pages", Prefix: "news", Region: "eu-west-1"},
		Limits:     engine.Limits{MaxPages: 500, MaxDepth: 4, MaxTime: engine.Duration(90 * time.Minute)},
		Politeness: engine.Politeness{Rate: engine.Duration(2 * time.Second), Concurrency: 4},
		Components: engine.Components{External: []engine.Stage{engine.StageSpider}},
	}

	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// Durations must be readable, because a job is a document people edit.
	if !strings.Contains(string(raw), `"1h30m0s"`) {
		t.Errorf("max_time is not human readable: %s", raw)
	}

	var got engine.Config
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Limits.MaxTime != want.Limits.MaxTime {
		t.Errorf("max time = %s, want %s", got.Limits.MaxTime, want.Limits.MaxTime)
	}
	if got.Cache.Bucket != want.Cache.Bucket {
		t.Errorf("bucket = %q, want %q", got.Cache.Bucket, want.Cache.Bucket)
	}
	if !got.Components.IsExternal(engine.StageSpider) {
		t.Error("external spider did not survive the round trip")
	}
}

// TestDurationAcceptsSeconds guards the reading a client most plausibly means
// by a bare number.
func TestDurationAcceptsSeconds(t *testing.T) {
	var d engine.Duration
	if err := json.Unmarshal([]byte("30"), &d); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if d.Duration() != 30*time.Second {
		t.Errorf("got %s, want 30s", d)
	}
}
