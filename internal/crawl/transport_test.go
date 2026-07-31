// SPDX-License-Identifier: GPL-3.0-or-later

package crawl

import (
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/rangertaha/scour/internal/config"
	"github.com/rangertaha/scour/internal/transport"
)

// stubBrowser stands in for the real webdriver, so these tests depend on
// neither a browser being installed nor the plugin being imported here. The
// crawl package never imports webdriver, which is what makes the name free to
// register.
type stubBrowser struct{}

func (stubBrowser) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("stub browser")
}

var built struct {
	sync.Mutex
	cfg transport.Config
}

func init() {
	transport.Register("webdriver", func(cfg transport.Config) (http.RoundTripper, error) {
		built.Lock()
		built.cfg = cfg
		built.Unlock()
		return stubBrowser{}, nil
	})
}

// escalatingPolicy reports the policy the crawler settled on, or "" when it
// built a plain transport with no escalation at all.
func escalatingPolicy(t *testing.T, cfg config.Config, flag string) transport.Policy {
	t.Helper()
	c := New(cfg, nil, nil)
	rt, err := c.transport(Options{Browser: flag})
	if err != nil {
		t.Fatalf("transport: %v", err)
	}
	if e, ok := rt.(*transport.Escalating); ok {
		return e.Policy
	}
	return ""
}

func TestBrowserPolicyResolution(t *testing.T) {
	tests := []struct {
		name   string
		policy string
		on     bool
		flag   string
		want   transport.Policy
	}{
		{"config decides when no flag is given", "always", true, "", transport.Always},
		{"a flag beats the config", "always", true, "never", ""},
		{"and beats it the other way too", "never", true, "always", transport.Always},
		{"disabled beats both", "always", false, "always", ""},
		{"an empty policy is auto", "", true, "", transport.Auto},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Default()
			cfg.Browser.Policy = tt.policy
			cfg.Browser.Enabled = tt.on
			cfg.Crawl.Timeout = config.Duration(5 * time.Second)

			if got := escalatingPolicy(t, cfg, tt.flag); got != tt.want {
				t.Errorf("policy = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestKnownBrowserHostsComeFromConfig(t *testing.T) {
	cfg := config.Default()
	cfg.Hosts = []config.Host{
		{Host: "app.example.com", Transport: "webdriver"},
		{Host: "plain.example.com"},
		{Host: "other.example.com", Transport: "http"},
		// A pattern cannot be matched against a request host, so it must not
		// be primed as though it were a literal one.
		{Host: "*.spa.example.com", Transport: "webdriver"},
	}

	got := New(cfg, nil, nil).knownBrowserHosts()
	if len(got) != 1 || got[0] != "app.example.com" {
		t.Errorf("known hosts = %v, want just the exact webdriver host", got)
	}
}

// The browser settings have to reach the transport that uses them, or the
// config block would be decoration.
func TestBrowserSettingsReachTheTransport(t *testing.T) {
	cfg := config.Default()
	cfg.Browser.Pool = 5
	cfg.Browser.Timeout = config.Duration(90 * time.Second)
	cfg.Browser.ExecPath = "/opt/chrome"
	cfg.Crawl.UserAgent = "scour/test"
	cfg.Crawl.Timeout = config.Duration(7 * time.Second)

	c := New(cfg, nil, nil)
	if _, err := c.transport(Options{Browser: "always"}); err != nil {
		t.Fatal(err)
	}

	built.Lock()
	got := built.cfg
	built.Unlock()

	if got.Browser.Pool != 5 {
		t.Errorf("pool = %d, want 5", got.Browser.Pool)
	}
	// The render timeout is its own setting: a render includes every request
	// the page makes, so borrowing the HTTP timeout would cut pages short.
	if got.Browser.Timeout != 90*time.Second {
		t.Errorf("browser timeout = %v, want 90s", got.Browser.Timeout)
	}
	if got.Timeout != 7*time.Second {
		t.Errorf("http timeout = %v, want 7s", got.Timeout)
	}
	if got.Browser.ExecPath != "/opt/chrome" {
		t.Errorf("exec path = %q", got.Browser.ExecPath)
	}
	if got.UserAgent != "scour/test" {
		t.Errorf("user agent = %q", got.UserAgent)
	}
}
