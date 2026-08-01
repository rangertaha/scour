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
	"net/http"
	"time"

	"github.com/rangertaha/scour/internal/registry"
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

// reg holds the implementations. See internal/registry for the shape every
// extension point in scour shares, and for how to add one.
var reg = registry.New[Config, http.RoundTripper]("transport").Default("http")

// Register adds an implementation, from init.
func Register(name string, f registry.Factory[Config, http.RoundTripper]) { reg.Register(name, f) }

// New builds a registered implementation.
func New(name string, cfg Config) (http.RoundTripper, error) { return reg.New(name, cfg) }

// Names lists what is registered.
func Names() []string { return reg.Names() }

// Has reports whether a name is registered.
func Has(name string) bool { return reg.Has(name) }
