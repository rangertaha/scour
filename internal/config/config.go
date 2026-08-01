// SPDX-License-Identifier: GPL-3.0-or-later

// Package config loads scour's configuration.
//
// Settings resolve in a fixed order, so a flag always wins and a packaged
// default never overwrites a local one:
//
//  1. command line flags
//  2. environment variables
//  3. /etc/scour/config.toml if it exists, otherwise the user config file
//  4. built-in defaults
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/rangertaha/scour/internal/schedule"

	"github.com/BurntSushi/toml"
)

// Config is the whole of scour's configuration.
type Config struct {
	Server  Server  `toml:"server"`
	Crawl   Crawl   `toml:"crawl"`
	Browser Browser `toml:"browser"`
	Model   Model   `toml:"model"`
	Store   Store   `toml:"store"`
	Bus     Bus     `toml:"bus"`
	Cache   Cache   `toml:"cache"`
	Paths   Paths   `toml:"paths"`
	AI      []AI    `toml:"ai"`
	Hosts   []Host  `toml:"host"`
}

// Server configures the HTTP and MCP endpoint served by `scour server`.
type Server struct {
	Listen    string `toml:"listen"`
	MCP       bool   `toml:"mcp"`
	Metrics   string `toml:"metrics"`
	TokenFile string `toml:"token_file"`
}

// Crawl holds the defaults applied to every host that has no override.
type Crawl struct {
	Concurrency  int      `toml:"concurrency"`
	Rate         Duration `toml:"rate"`
	Timeout      Duration `toml:"timeout"`
	MaxSize      Size     `toml:"max_size"`
	UserAgent    string   `toml:"user_agent"`
	Robots       bool     `toml:"robots"`
	ContentTypes []string `toml:"content_types"`
	Depth        int      `toml:"depth"`
	// Scheduler is the order the frontier is drained in: best, breadth, depth,
	// random or warmup. Empty is best, which is what makes a focused crawl
	// focused. See `scour start --help` for what each means.
	Scheduler string `toml:"scheduler"`
}

// Browser configures rendering pages in a real browser, for sites that build
// their content in JavaScript.
type Browser struct {
	// Enabled allows escalating to a browser at all. Turning it off is the
	// same as passing --browser never to every crawl.
	Enabled bool `toml:"enabled"`
	// Policy is when to render: never, auto or always. A --browser flag beats
	// it.
	Policy string `toml:"policy"`
	// Pool caps how many tabs render at once. A tab costs orders of magnitude
	// more than a socket, so this stays well below the crawl's concurrency.
	Pool int `toml:"pool"`
	// Timeout bounds one render.
	Timeout Duration `toml:"timeout"`
	// ExecPath overrides the browser binary, for a machine where it is not on
	// the path or several are installed.
	ExecPath string `toml:"exec_path"`
}

// Model selects the scoring and matching implementations.
type Model struct {
	Scorer  string `toml:"scorer"`
	Matcher string `toml:"matcher"`
	// Classifier reads a fetched page and says what it is, which is what lets
	// a first crawl label pages before any extraction rule exists. Empty or
	// "none" leaves labelling to the record count alone.
	Classifier string `toml:"classifier"`
	// AI names the [[ai]] block a model-backed matcher or classifier should
	// use. With a single block it can be left out.
	AI string `toml:"ai"`
	// Budget caps how many model calls one training run may make. Zero is the
	// matcher's own default; negative means no limit.
	Budget int `toml:"budget"`
	// Vectors is where a vector-based scorer loads its word vectors from.
	Vectors  string  `toml:"vectors"`
	Holdout  float64 `toml:"holdout"`
	MinScore float64 `toml:"min_score"`
}

// Bus configures how components talk to each other. An empty URL starts an
// embedded broker in-process, which is the single-process default.
type Bus struct {
	URL      string `toml:"url"`
	StoreDir string `toml:"store_dir"`
}

// Cache configures where fetched bodies are kept.
//
// Separate from Paths because the bodies are the only part of scour's state
// that does not have to be local. On one machine a directory is right; with
// crawlers on several, each writes to its own disk and the trainer reads an
// empty cache, so the location has to be somewhere they all share.
type Cache struct {
	// Driver names the implementation: local, or one registered by a build
	// that includes it. Empty means local.
	Driver string `toml:"driver"`
	// URL says where bodies go, in the driver's dialect. Empty means the
	// default pages directory.
	URL string `toml:"url"`
	// Options carries whatever a driver needs beyond the location: a region,
	// an endpoint, a credentials profile.
	Options map[string]string `toml:"options"`
}

// Store configures the database backing every item.
type Store struct {
	Driver string `toml:"driver"`
	DSN    string `toml:"dsn"`
}

// Paths overrides where scour keeps its data. Empty fields fall back to the
// values derived in [Resolve].
type Paths struct {
	Data  string `toml:"data"`
	Cache string `toml:"cache"`
}

// AI describes one model provider, referenced by name from [Model].
type AI struct {
	Name     string `toml:"name"`
	Provider string `toml:"provider"`
	Model    string `toml:"model"`
	Effort   string `toml:"effort"`
	// Endpoint overrides where the provider is reached, which is how a local
	// model on another machine is used.
	Endpoint  string   `toml:"endpoint"`
	Path      string   `toml:"path"`
	APIKeyEnv string   `toml:"api_key_env"`
	Timeout   Duration `toml:"timeout"`
}

// Host overrides the crawl defaults for hosts matching Host, which may be a
// glob such as "*.example.com". The first matching block wins.
type Host struct {
	Host        string   `toml:"host"`
	Rate        Duration `toml:"rate"`
	Concurrency int      `toml:"concurrency"`
	Robots      *bool    `toml:"robots"`
	Transport   string   `toml:"transport"`
}

// Default returns the built-in configuration. An empty config file behaves
// identically to this.
func Default() Config {
	return Config{
		Server: Server{
			Listen:  "127.0.0.1:8080",
			MCP:     true,
			Metrics: "/metrics",
		},
		Crawl: Crawl{
			Concurrency:  8,
			Rate:         Duration(time.Second),
			Timeout:      Duration(30 * time.Second),
			MaxSize:      Size(10 << 20),
			UserAgent:    "scour/" + "0.1" + " (+https://github.com/Rangertaha/scour)",
			Robots:       true,
			ContentTypes: []string{"html"},
			Depth:        10,
		},
		Browser: Browser{
			Enabled: true,
			Policy:  "auto",
			Pool:    2,
			Timeout: Duration(45 * time.Second),
		},
		Model: Model{
			Scorer:   "bayes",
			Matcher:  "heuristic",
			Holdout:  0.2,
			MinScore: 0.1,
		},
		Store: Store{Driver: "sqlite"},
	}
}

// Load reads the configuration, applying file, environment and the built-in
// defaults in precedence order. Flags are applied by the caller afterwards,
// since only the command knows which of its flags were set.
//
// An explicit path must exist; a discovered one may be absent, in which case
// the defaults stand.
func Load(path string) (Config, error) {
	cfg := Default()

	explicit := path != ""
	if !explicit {
		path = os.Getenv(EnvConfig)
		explicit = path != ""
	}
	if !explicit {
		path = DiscoverFile()
	}

	if path != "" {
		if err := loadFile(path, &cfg); err != nil {
			if explicit || !errors.Is(err, fs.ErrNotExist) {
				return cfg, err
			}
		}
	}

	applyEnv(&cfg)

	if err := Resolve(&cfg); err != nil {
		return cfg, err
	}
	return cfg, cfg.Validate()
}

func loadFile(path string, cfg *Config) error {
	f, err := os.Open(path) //nolint:gosec // the path is operator supplied
	if err != nil {
		return fmt.Errorf("open config %s: %w", path, err)
	}
	defer f.Close()

	if _, err := toml.NewDecoder(f).Decode(cfg); err != nil {
		return fmt.Errorf("parse config %s: %w", path, err)
	}
	return nil
}

// Validate reports configuration that would fail later in a less obvious place.
func (c Config) Validate() error {
	if c.Crawl.Concurrency < 1 {
		return fmt.Errorf("crawl.concurrency must be at least 1, got %d", c.Crawl.Concurrency)
	}
	if c.Crawl.Depth < 0 {
		return fmt.Errorf("crawl.depth must not be negative, got %d", c.Crawl.Depth)
	}
	// A misspelled scheduler must fail here, not silently fall back to the
	// default, where a crawl ordered by something other than what was asked
	// for looks exactly like one that worked.
	if name := strings.TrimSpace(c.Crawl.Scheduler); name != "" && !schedule.Has(name) {
		return fmt.Errorf("crawl.scheduler %q is not one of: %s",
			name, strings.Join(schedule.Names(), ", "))
	}
	if c.Browser.Pool < 0 {
		return fmt.Errorf("browser.pool must not be negative, got %d", c.Browser.Pool)
	}
	switch strings.ToLower(strings.TrimSpace(c.Browser.Policy)) {
	case "", "never", "auto", "always":
	default:
		return fmt.Errorf("browser.policy must be never, auto or always, got %q", c.Browser.Policy)
	}
	if c.Model.Holdout < 0 || c.Model.Holdout >= 1 {
		return fmt.Errorf("model.holdout must be in [0,1), got %v", c.Model.Holdout)
	}
	if c.Model.MinScore < 0 || c.Model.MinScore > 1 {
		return fmt.Errorf("model.min_score must be in [0,1], got %v", c.Model.MinScore)
	}
	seen := make(map[string]bool, len(c.AI))
	for _, ai := range c.AI {
		if ai.Name == "" {
			return errors.New("every [[ai]] block needs a name")
		}
		if seen[ai.Name] {
			return fmt.Errorf("duplicate [[ai]] name %q", ai.Name)
		}
		seen[ai.Name] = true
	}
	return nil
}

// Duration is a time.Duration that reads from TOML as a string such as "1s".
type Duration time.Duration

// UnmarshalText implements encoding.TextUnmarshaler.
func (d *Duration) UnmarshalText(text []byte) error {
	v, err := time.ParseDuration(string(text))
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", text, err)
	}
	*d = Duration(v)
	return nil
}

// MarshalText implements encoding.TextMarshaler.
func (d Duration) MarshalText() ([]byte, error) {
	return []byte(time.Duration(d).String()), nil
}

// Duration returns the value as a time.Duration.
func (d Duration) Duration() time.Duration { return time.Duration(d) }

// String implements fmt.Stringer.
func (d Duration) String() string { return time.Duration(d).String() }

// Size is a byte count that reads from TOML as a string such as "10MB".
type Size int64

var sizeUnits = []struct {
	suffix string
	factor int64
}{
	{"KB", 1 << 10}, {"MB", 1 << 20}, {"GB", 1 << 30},
	{"K", 1 << 10}, {"M", 1 << 20}, {"G", 1 << 30},
	{"B", 1},
}

// UnmarshalText implements encoding.TextUnmarshaler.
func (s *Size) UnmarshalText(text []byte) error {
	raw := strings.ToUpper(strings.TrimSpace(string(text)))
	for _, u := range sizeUnits {
		if !strings.HasSuffix(raw, u.suffix) {
			continue
		}
		n, err := strconv.ParseInt(strings.TrimSpace(strings.TrimSuffix(raw, u.suffix)), 10, 64)
		if err != nil {
			return fmt.Errorf("invalid size %q: %w", text, err)
		}
		*s = Size(n * u.factor)
		return nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid size %q: %w", text, err)
	}
	*s = Size(n)
	return nil
}

// MarshalText implements encoding.TextMarshaler.
func (s Size) MarshalText() ([]byte, error) { return []byte(s.String()), nil }

// String renders the size with the largest unit that divides it exactly.
func (s Size) String() string {
	n := int64(s)
	switch {
	case n >= 1<<30 && n%(1<<30) == 0:
		return strconv.FormatInt(n/(1<<30), 10) + "GB"
	case n >= 1<<20 && n%(1<<20) == 0:
		return strconv.FormatInt(n/(1<<20), 10) + "MB"
	case n >= 1<<10 && n%(1<<10) == 0:
		return strconv.FormatInt(n/(1<<10), 10) + "KB"
	default:
		return strconv.FormatInt(n, 10) + "B"
	}
}
