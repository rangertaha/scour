// SPDX-License-Identifier: GPL-3.0-or-later

package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// Environment variables recognised by [Load] and [Resolve].
const (
	EnvConfig   = "SCOUR_CONFIG"
	EnvListen   = "SCOUR_LISTEN"
	EnvDataDir  = "SCOUR_DATA_DIR"
	EnvCacheDir = "SCOUR_CACHE_DIR"
)

// System paths, used when scour runs as a service.
const (
	SystemConfigFile = "/etc/scour/config.toml"
	SystemDataDir    = "/var/lib/scour"
	SystemCacheDir   = "/var/cache/scour"
)

// DiscoverFile returns the configuration file scour would read when none is
// named: the system file if it exists, otherwise the per-user one. It returns
// an empty string when neither exists, which is not an error since the
// built-in defaults are a complete configuration.
func DiscoverFile() string {
	if _, err := os.Stat(SystemConfigFile); err == nil {
		return SystemConfigFile
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "scour", "config.toml")
}

// Resolve fills in any path left empty by the file and the environment.
//
// Running as a service, with /etc/scour/config.toml present, data and cache
// live under /var. Otherwise they follow the user's XDG directories, which
// keeps working data out of the config directory so it can be cleared without
// losing the setup.
func Resolve(cfg *Config) error {
	system := false
	if _, err := os.Stat(SystemConfigFile); err == nil {
		system = true
	}

	if cfg.Paths.Data == "" {
		switch {
		case system:
			cfg.Paths.Data = SystemDataDir
		default:
			dir, err := userDataDir()
			if err != nil {
				return err
			}
			cfg.Paths.Data = dir
		}
	}

	if cfg.Paths.Cache == "" {
		switch {
		case system:
			cfg.Paths.Cache = SystemCacheDir
		default:
			dir, err := os.UserCacheDir()
			if err != nil {
				return fmt.Errorf("resolve cache directory: %w", err)
			}
			cfg.Paths.Cache = filepath.Join(dir, "scour")
		}
	}

	if cfg.Store.DSN == "" {
		cfg.Store.DSN = filepath.Join(cfg.Paths.Data, "scour.db")
	}
	return nil
}

// userDataDir returns $XDG_DATA_HOME/scour, falling back to
// $HOME/.local/share/scour. os.UserConfigDir has no data-directory twin, so
// this implements the same rule by hand.
func userDataDir() (string, error) {
	if dir := os.Getenv("XDG_DATA_HOME"); dir != "" {
		return filepath.Join(dir, "scour"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve data directory: %w", err)
	}
	return filepath.Join(home, ".local", "share", "scour"), nil
}

// ModelsDir is where per-entity models are written.
func (c Config) ModelsDir() string { return filepath.Join(c.Paths.Data, "models") }

// ScoreModelPath is where an entity's URL scoring model lives. It decides what
// to crawl next.
func (c Config) ScoreModelPath(entity string) string {
	return filepath.Join(c.ModelsDir(), entity+".score.json")
}

// ExtractModelPath is where an entity's extraction model lives. It decides
// what to pull out of a page once crawled.
//
// The two are separate files because they are separate models with different
// lifetimes: scoring is retrained from crawl outcomes, extraction is induced
// from page structure, and either can be discarded without the other.
func (c Config) ExtractModelPath(entity string) string {
	return filepath.Join(c.ModelsDir(), entity+".extract.json")
}

// PagesDir is where fetched page bodies are cached.
func (c Config) PagesDir() string { return filepath.Join(c.Paths.Cache, "pages") }

// ExportsDir is where extracted records are written.
func (c Config) ExportsDir() string { return filepath.Join(c.Paths.Data, "exports") }

// applyEnv overlays the environment onto cfg, between the file and any flags.
func applyEnv(cfg *Config) {
	if v := os.Getenv(EnvListen); v != "" {
		cfg.Server.Listen = v
	}
	if v := os.Getenv(EnvDataDir); v != "" {
		cfg.Paths.Data = v
	}
	if v := os.Getenv(EnvCacheDir); v != "" {
		cfg.Paths.Cache = v
	}
}

// MkdirAll creates the directories scour writes to. It is called once at
// startup rather than lazily, so a permission problem surfaces immediately
// instead of halfway through a crawl.
func (c Config) MkdirAll() error {
	for _, dir := range []string{c.Paths.Data, c.ModelsDir(), c.Paths.Cache, c.PagesDir()} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}
	return nil
}
