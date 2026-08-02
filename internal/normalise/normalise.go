// SPDX-License-Identifier: GPL-3.0-or-later

// Package normalise reduces a typed domain or URL to the one form scour stores.
//
// It is a leaf package on purpose. These two functions decide what counts as
// the same target, so the command line, the HTTP API and the job config file all
// have to agree on them, and they lived in internal/cli where only the command
// line could reach them. A server that reached for them there would depend on
// the CLI, which is backwards: the CLI is one caller of scour, not a layer
// underneath the others.
package normalise

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// Domain reduces a domain to its bare host, so example.com, www.example.com and
// https://example.com/ are one target.
func Domain(raw string) (string, error) {
	host := strings.TrimSpace(strings.ToLower(raw))
	if host == "" {
		return "", errors.New("domain must not be empty")
	}
	if strings.Contains(host, "://") {
		u, err := url.Parse(host)
		if err != nil {
			return "", fmt.Errorf("parse domain %q: %w", raw, err)
		}
		host = u.Host
	}
	host = strings.TrimSuffix(host, "/")
	if i := strings.IndexByte(host, '/'); i >= 0 {
		host = host[:i]
	}
	host = strings.TrimPrefix(host, "www.")
	if host == "" {
		return "", fmt.Errorf("domain %q has no host", raw)
	}
	if strings.ContainsAny(host, " \t#") {
		return "", fmt.Errorf("domain %q is not a hostname", raw)
	}
	return host, nil
}

// URL checks a URL is absolute and returns it with a scheme.
func URL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", errors.New("url must not be empty")
	}
	if !strings.Contains(trimmed, "://") {
		trimmed = "http://" + trimmed
	}
	u, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("parse url %q: %w", raw, err)
	}
	if u.Host == "" {
		return "", fmt.Errorf("url %q has no host", raw)
	}
	u.Fragment = ""
	return u.String(), nil
}
