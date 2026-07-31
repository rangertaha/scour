// SPDX-License-Identifier: GPL-3.0-or-later

// Package transport is how a request actually reaches the network.
//
// The extension point is [http.RoundTripper] rather than an interface of
// scour's own, because that is what colly already accepts: a plugin that
// satisfies the standard library satisfies scour. It is also why a
// browser-rendered page needs no second code path. The browser is a transport,
// so a rendered response travels the same callbacks, the same queue and the
// same metrics as any other.
package transport

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// Config is what a transport is built from.
type Config struct {
	// UserAgent is sent with every request.
	UserAgent string
	// Timeout bounds a single round trip.
	Timeout time.Duration
	// Proxy, when set, is the proxy URL to route through.
	Proxy string
	// Browser holds the settings only a rendering transport uses. It is
	// carried here rather than passed separately so that a plugin stays a
	// blank import: whoever builds the transport never has to know which
	// implementation it is.
	Browser BrowserConfig
}

// BrowserConfig configures transports that drive a real browser. Transports
// that do not render ignore it.
type BrowserConfig struct {
	// Pool caps how many pages render at once.
	Pool int
	// Timeout bounds one render. Zero falls back to the transport's own
	// default, which is longer than an HTTP timeout because a render includes
	// every request the page makes.
	Timeout time.Duration
	// ExecPath overrides the browser binary.
	ExecPath string
}

// Factory builds a transport.
type Factory func(Config) (http.RoundTripper, error)

var (
	mu       sync.RWMutex
	registry = map[string]Factory{}
)

// Register adds an implementation, from init, so a plugin is a blank import.
func Register(name string, f Factory) {
	mu.Lock()
	defer mu.Unlock()
	registry[name] = f
}

// New builds a registered transport by name. An empty name is the plain HTTP
// one, which is what a host with no policy of its own gets.
func New(name string, cfg Config) (http.RoundTripper, error) {
	if name == "" {
		name = "http"
	}

	mu.RLock()
	f, ok := registry[name]
	mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown transport %q, have %s", name, strings.Join(Names(), ", "))
	}
	return f(cfg)
}

// Names lists the registered transports.
func Names() []string {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]string, 0, len(registry))
	for name := range registry {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Has reports whether a transport is registered, so a caller can fall back
// rather than fail when an optional one was left out of the build.
func Has(name string) bool {
	mu.RLock()
	defer mu.RUnlock()
	_, ok := registry[name]
	return ok
}
