// SPDX-License-Identifier: GPL-3.0-or-later

package server

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// syncBuffer is a buffer safe to write from another goroutine while the test
// reads it.
//
// The lock is not decoration. capture redirects the process-wide logger, and
// jobs started by other tests in this package outlive them and go on logging,
// so a plain bytes.Buffer here is written by a stray goroutine and read by the
// test at the same time.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// capture installs a logger writing into a buffer, at the given level, and
// restores the previous default when the test ends.
func capture(t *testing.T, level slog.Level) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: level})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

// serve runs one request through the middleware under test.
func serve(h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	Logging(h).ServeHTTP(w, r)
	return w
}

func TestLoggingLevelFollowsStatus(t *testing.T) {
	cases := []struct {
		status int
		want   string
	}{
		{http.StatusOK, "level=INFO"},
		{http.StatusNotFound, "level=WARN"},
		{http.StatusUnauthorized, "level=WARN"},
		{http.StatusInternalServerError, "level=ERROR"},
	}
	for _, tc := range cases {
		buf := capture(t, slog.LevelDebug)
		h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(tc.status)
		})
		serve(h, httptest.NewRequest("GET", "/v1/items", nil))

		got := buf.String()
		if !strings.Contains(got, tc.want) {
			t.Errorf("status %d logged at the wrong level:\n%s", tc.status, got)
		}
	}
}

// A readiness check every second and a scrape every fifteen would otherwise be
// almost the whole log, which buries the requests that matter.
func TestLoggingKeepsPollersOutOfTheLog(t *testing.T) {
	for _, path := range []string{"/healthz", "/metrics"} {
		buf := capture(t, slog.LevelInfo)
		h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
		serve(h, httptest.NewRequest("GET", path, nil))

		if strings.Contains(buf.String(), "msg=request") {
			t.Errorf("%s should not be logged at info:\n%s", path, buf.String())
		}
	}

	// Still reachable when someone asks for it.
	buf := capture(t, slog.LevelDebug)
	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	serve(h, httptest.NewRequest("GET", "/healthz", nil))
	if !strings.Contains(buf.String(), "/healthz") {
		t.Errorf("--verbose should reach the polling endpoints:\n%s", buf.String())
	}
}

// The id is what ties a handler's own line to the access line, so it has to be
// in the context the handler sees and in the response the caller keeps.
func TestLoggingRequestIDReachesHandlerAndCaller(t *testing.T) {
	capture(t, slog.LevelDebug)

	var seen string
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = RequestID(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	w := serve(h, httptest.NewRequest("GET", "/v1/items", nil))

	if seen == "" {
		t.Fatal("the handler could not read the request id")
	}
	if got := w.Header().Get("X-Request-Id"); got != seen {
		t.Errorf("the header carried %q where the handler saw %q", got, seen)
	}
}

func TestLoggingRecordsSizeAndImplicitStatus(t *testing.T) {
	buf := capture(t, slog.LevelDebug)

	// No WriteHeader, which is the implicit 200 net/http produces on first
	// write and the case a status recorder most easily gets wrong.
	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("hello world"))
	})
	serve(h, httptest.NewRequest("GET", "/v1/items", nil))

	got := buf.String()
	if !strings.Contains(got, "status=200") {
		t.Errorf("an unwritten status should log as 200:\n%s", got)
	}
	if !strings.Contains(got, "bytes=11") {
		t.Errorf("the response size was not recorded:\n%s", got)
	}
}

func TestClientIPPrefersForwardedFor(t *testing.T) {
	cases := []struct {
		name, forwarded, remote, want string
	}{
		{"direct", "", "192.0.2.5:41234", "192.0.2.5"},
		{"proxied", "203.0.113.9", "127.0.0.1:41234", "203.0.113.9"},
		{"chain", "203.0.113.9, 10.0.0.1", "127.0.0.1:41234", "203.0.113.9"},
		{"no port", "", "192.0.2.5", "192.0.2.5"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/v1/items", nil)
			r.RemoteAddr = tc.remote
			if tc.forwarded != "" {
				r.Header.Set("X-Forwarded-For", tc.forwarded)
			}
			if got := clientIP(r); got != tc.want {
				t.Errorf("clientIP = %q, want %q", got, tc.want)
			}
		})
	}
}

// A rejected request has to leave a line: a silent 401 is the one event in this
// log worth alerting on, and the access line alone cannot tell a wrong token
// from a missing one.
func TestUnauthorizedIsLogged(t *testing.T) {
	auth := &Auth{token: "s3cret"}
	handler := Logging(auth.Middleware(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })))

	for _, tc := range []struct{ name, header, want string }{
		{"missing", "", "presented=false"},
		{"wrong", "Bearer nope", "presented=true"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			buf := capture(t, slog.LevelDebug)
			r := httptest.NewRequest("GET", "/v1/items", nil)
			if tc.header != "" {
				r.Header.Set("Authorization", tc.header)
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)

			if w.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", w.Code)
			}
			got := buf.String()
			if !strings.Contains(got, "msg=unauthorized") {
				t.Errorf("the rejection was not logged:\n%s", got)
			}
			if !strings.Contains(got, tc.want) {
				t.Errorf("the log should record %s:\n%s", tc.want, got)
			}
			if strings.Contains(got, "s3cret") || strings.Contains(got, "nope") {
				t.Errorf("the log must not carry the token:\n%s", got)
			}
		})
	}
}
