// SPDX-License-Identifier: GPL-3.0-or-later

package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"
)

// requestIDKey carries the per-request id through the handler chain.
type requestIDKey struct{}

// RequestID returns the id given to this request, or "" outside the server.
//
// A handler that logs its own line uses this so that line and the access line
// can be tied together, which is the whole point of having an id at all.
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// newRequestID returns a short random id.
//
// Short because it is read by people correlating two lines in a log, not stored
// or indexed, and eight bytes is already far more than enough to keep the
// requests in flight at one moment distinguishable.
func newRequestID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Losing the id is not worth failing a request over: the access line is
		// still written, it just cannot be joined to a handler's own line.
		return ""
	}
	return hex.EncodeToString(b[:])
}

// Logging writes one line per request.
//
// The status code and the byte count come from the recorder the metrics
// middleware already uses, so this adds no second wrapper around the response
// writer: two wrappers would each think they owned the implicit 200 that
// writing without WriteHeader produces.
//
// Level follows the status, because that is what someone grepping a log is
// actually asking: a 5xx is scour's fault and belongs at error, a 4xx is the
// caller's and belongs at warn, and everything else is routine.
//
// Polling endpoints log at debug whatever their status. A readiness check every
// second and a Prometheus scrape every fifteen would otherwise be almost the
// entire log, and burying the one request that mattered is the same as not
// logging it.
func Logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		id := newRequestID()
		if id != "" {
			r = r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id))
			// Echoed so a caller reporting a problem can quote the id rather
			// than the time they think it happened.
			w.Header().Set("X-Request-Id", id)
		}

		rec, ok := w.(*recorder)
		if !ok {
			rec = &recorder{ResponseWriter: w}
			w = rec
		}
		next.ServeHTTP(w, r)

		status := rec.status
		if status == 0 {
			status = http.StatusOK
		}

		level := slog.LevelInfo
		switch {
		case polling(r.URL.Path):
			level = slog.LevelDebug
		case status >= 500:
			level = slog.LevelError
		case status >= 400:
			level = slog.LevelWarn
		}

		attrs := []any{
			"method", r.Method,
			"path", r.URL.Path,
			"status", status,
			"bytes", rec.written,
			"duration", time.Since(start).Round(time.Millisecond),
			"remote", clientIP(r),
		}
		if id != "" {
			attrs = append(attrs, "id", id)
		}
		if q := r.URL.RawQuery; q != "" {
			attrs = append(attrs, "query", q)
		}
		slog.Log(r.Context(), level, "request", attrs...)
	})
}

// polling reports whether a path is one an orchestrator or scraper hits on a
// timer rather than one a person or an agent asked for.
func polling(path string) bool {
	return path == "/healthz" || strings.HasSuffix(path, "/metrics")
}

// clientIP is the address to attribute a request to.
//
// X-Forwarded-For is honoured because scour is documented as belonging behind a
// reverse proxy, where every request otherwise appears to come from localhost
// and the field says nothing at all. It is a hint, not evidence: anyone talking
// to scour directly can set it, so it is worth logging and not worth trusting
// for a decision.
func clientIP(r *http.Request) string {
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		if first, _, found := strings.Cut(fwd, ","); found {
			return strings.TrimSpace(first)
		}
		return strings.TrimSpace(fwd)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
