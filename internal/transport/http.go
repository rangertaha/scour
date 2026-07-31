// SPDX-License-Identifier: GPL-3.0-or-later

package transport

import (
	"fmt"
	"net/http"
	"net/url"
	"time"
)

func init() {
	Register("http", NewHTTP)
}

// NewHTTP returns the ordinary network transport, which is what everything
// uses unless a host is configured otherwise.
func NewHTTP(cfg Config) (http.RoundTripper, error) {
	base := http.DefaultTransport.(*http.Transport).Clone()

	if cfg.Proxy != "" {
		u, err := url.Parse(cfg.Proxy)
		if err != nil {
			return nil, fmt.Errorf("parse proxy %q: %w", cfg.Proxy, err)
		}
		base.Proxy = http.ProxyURL(u)
	}

	if cfg.Timeout > 0 {
		base.ResponseHeaderTimeout = cfg.Timeout
	}
	base.MaxIdleConnsPerHost = 8

	return base, nil
}

// Timeout is the fallback used when a config carries none.
const Timeout = 30 * time.Second

func (c Config) timeout() time.Duration {
	if c.Timeout <= 0 {
		return Timeout
	}
	return c.Timeout
}
