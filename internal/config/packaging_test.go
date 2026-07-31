// SPDX-License-Identifier: GPL-3.0-or-later

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The config we ship has to load, or the first thing a new operator does is
// hit a parse error in a file they did not write.
func TestPackagedConfigLoads(t *testing.T) {
	path := filepath.Join("..", "..", "packaging", "etc", "config.toml")
	if _, err := os.Stat(path); err != nil {
		t.Skipf("packaging not present: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("the shipped config does not load: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("the shipped config does not validate: %v", err)
	}

	// Every value in it is meant to be the built-in default, so that commenting
	// a line out changes nothing. A drift here means the file is documenting
	// behaviour scour does not have.
	def := Default()
	if cfg.Server.Listen != def.Server.Listen {
		t.Errorf("listen = %q, default is %q", cfg.Server.Listen, def.Server.Listen)
	}
	if cfg.Crawl.Concurrency != def.Crawl.Concurrency {
		t.Errorf("concurrency = %d, default is %d", cfg.Crawl.Concurrency, def.Crawl.Concurrency)
	}
	if cfg.Crawl.Depth != def.Crawl.Depth {
		t.Errorf("depth = %d, default is %d", cfg.Crawl.Depth, def.Crawl.Depth)
	}
	if cfg.Browser.Policy != def.Browser.Policy || cfg.Browser.Pool != def.Browser.Pool {
		t.Errorf("browser = %+v, default is %+v", cfg.Browser, def.Browser)
	}
	if cfg.Model.Scorer != def.Model.Scorer || cfg.Model.Matcher != def.Model.Matcher {
		t.Errorf("model = %+v, default is %+v", cfg.Model, def.Model)
	}

	// Auth off by default is a deliberate choice paired with a loopback bind.
	// Shipping a token file path that does not exist would stop the service
	// from starting at all.
	if cfg.Server.TokenFile != "" {
		t.Errorf("the shipped config sets token_file = %q", cfg.Server.TokenFile)
	}
}

// The unit is what actually starts scour on a server, so a typo in it is a
// production outage rather than a cosmetic problem.
func TestPackagedUnit(t *testing.T) {
	path := filepath.Join("..", "..", "packaging", "systemd", "scour.service")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("packaging not present: %v", err)
	}
	unit := string(raw)

	for _, want := range []string{
		"[Unit]", "[Service]", "[Install]",
		"ExecStart=", "WantedBy=multi-user.target",
		// These two create and own the directories the config expects, which
		// is what lets the unit run without a preinstall script.
		"StateDirectory=scour", "CacheDirectory=scour",
		// A crawl needs to be asked to stop, not shot.
		"KillSignal=SIGTERM",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("the unit is missing %q", want)
		}
	}

	// The config path in the unit has to be the one we actually ship.
	if !strings.Contains(unit, "/etc/scour/config.toml") {
		t.Error("the unit does not point at /etc/scour/config.toml")
	}

	// Hardening that would break scour if it were wrong: it fetches over the
	// network and resolves names.
	if !strings.Contains(unit, "AF_INET") || !strings.Contains(unit, "AF_UNIX") {
		t.Error("the address family restriction would stop scour reaching the network or a local resolver")
	}
}
