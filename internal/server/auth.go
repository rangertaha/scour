// SPDX-License-Identifier: GPL-3.0-or-later

package server

import (
	"crypto/subtle"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
)

// Auth checks a bearer token.
//
// It is deliberately the simplest thing that is actually safe rather than a
// user system: scour's threat model is a service on a private network or
// behind a reverse proxy, and inventing accounts and sessions here would be
// more surface than the problem needs. What it must get right is constant-time
// comparison and not being accidentally optional.
type Auth struct {
	token string
}

// NewAuth reads the token from a file.
//
// The token lives in a file rather than in config.toml so that the secret and
// the settings can have different permissions: a config file is often readable
// and checked into configuration management, and a token should be neither.
func NewAuth(path string) (*Auth, error) {
	if path == "" {
		return &Auth{}, nil
	}

	raw, err := os.ReadFile(path) //nolint:gosec // the path is operator supplied
	if err != nil {
		return nil, fmt.Errorf("read token file: %w", err)
	}

	token := strings.TrimSpace(string(raw))
	if token == "" {
		// An empty file almost certainly means a failed provisioning step, and
		// silently serving without auth would be the worst possible response
		// to it.
		return nil, fmt.Errorf("token file %s is empty: remove token_file to serve without auth", path)
	}
	return &Auth{token: token}, nil
}

// Enabled reports whether a token is required.
func (a *Auth) Enabled() bool { return a != nil && a.token != "" }

// Middleware rejects requests without the right token.
func (a *Auth) Middleware(next http.Handler) http.Handler {
	if !a.Enabled() {
		return next
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Liveness is exempt so an orchestrator can check the process without
		// holding a credential. It carries no data by design, precisely so
		// that exemption costs nothing.
		if r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}

		if !a.ok(r.Header.Get("Authorization")) {
			// Logged separately from the access line because a run of these is
			// the one thing in this log worth alerting on, and the access line
			// alone cannot tell a rejected token from a missing one.
			slog.Warn("unauthorized",
				"path", r.URL.Path,
				"remote", clientIP(r),
				"presented", r.Header.Get("Authorization") != "",
				"id", RequestID(r.Context()))
			w.Header().Set("WWW-Authenticate", `Bearer realm="scour"`)
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ok reports whether the header carries the configured token.
func (a *Auth) ok(header string) bool {
	const prefix = "Bearer "
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return false
	}

	// Constant time, so the comparison does not leak the token one byte at a
	// time to anyone able to measure the response.
	given := strings.TrimSpace(header[len(prefix):])
	return subtle.ConstantTimeCompare([]byte(given), []byte(a.token)) == 1
}
