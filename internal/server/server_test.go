// SPDX-License-Identifier: GPL-3.0-or-later

package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rangertaha/scour/internal/cache"
	"github.com/rangertaha/scour/internal/config"
	"github.com/rangertaha/scour/internal/store"
)

func newServer(t *testing.T, mutate func(*config.Config)) *Server {
	t.Helper()

	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "scour.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	cfg := config.Default()
	cfg.Paths.Data = dir
	cfg.Paths.Cache = dir
	if mutate != nil {
		mutate(&cfg)
	}

	srv, err := New(cfg, db, cache.Local(filepath.Join(dir, "pages")))
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

// do runs one request against the full handler, middleware included.
func do(t *testing.T, srv *Server, method, path, body string, headers ...string) *httptest.ResponseRecorder {
	t.Helper()

	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		r.Header.Set(headers[i], headers[i+1])
	}

	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, r)
	return w
}

func decodeBody(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("body %q: %v", w.Body.String(), err)
	}
	return out
}

func TestHealthNeedsNothing(t *testing.T) {
	srv := newServer(t, nil)

	w := do(t, srv, http.MethodGet, "/healthz", "")
	if w.Code != http.StatusOK {
		t.Errorf("status = %d", w.Code)
	}
	if decodeBody(t, w)["status"] != "ok" {
		t.Errorf("body = %s", w.Body.String())
	}
}

func TestItemLifecycle(t *testing.T) {
	srv := newServer(t, nil)

	w := do(t, srv, http.MethodPost, "/v1/items",
		`{"name":"vehicle","aliases":["car"],"urls":["http://example.com/cars/"],
		  "properties":[{"name":"make","example":"Ford","aliases":["manufacturer"]}]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("create = %d: %s", w.Code, w.Body)
	}

	w = do(t, srv, http.MethodGet, "/v1/items/vehicle", "")
	if w.Code != http.StatusOK {
		t.Fatalf("get = %d: %s", w.Code, w.Body)
	}
	// The property alias has to survive the round trip, or the API would be
	// quietly less capable than the CLI it mirrors.
	if !strings.Contains(w.Body.String(), "manufacturer") {
		t.Errorf("property aliases were lost: %s", w.Body)
	}

	w = do(t, srv, http.MethodGet, "/v1/items", "")
	if !strings.Contains(w.Body.String(), "vehicle") {
		t.Errorf("list = %s", w.Body)
	}

	if w := do(t, srv, http.MethodDelete, "/v1/items/vehicle", ""); w.Code != http.StatusNoContent {
		t.Errorf("delete = %d: %s", w.Code, w.Body)
	}
	if w := do(t, srv, http.MethodGet, "/v1/items/vehicle", ""); w.Code != http.StatusNotFound {
		t.Errorf("get after delete = %d", w.Code)
	}
}

// Creating the same item twice must be safe, because the API mirrors a CLI
// whose every form is idempotent.
func TestCreatingTwiceIsSafe(t *testing.T) {
	srv := newServer(t, nil)
	body := `{"name":"vehicle","aliases":["car"],"urls":["http://example.com/"]}`

	for i := range 2 {
		if w := do(t, srv, http.MethodPost, "/v1/items", body); w.Code != http.StatusOK {
			t.Fatalf("create %d = %d: %s", i, w.Code, w.Body)
		}
	}

	w := do(t, srv, http.MethodGet, "/v1/items/vehicle", "")
	var item store.Item
	if err := json.Unmarshal(w.Body.Bytes(), &item); err != nil {
		t.Fatal(err)
	}
	if len(item.Aliases) != 1 || len(item.AllTargets()) != 1 {
		t.Errorf("repeating the request duplicated things: %d aliases, %d targets",
			len(item.Aliases), len(item.AllTargets()))
	}
}

func TestMissingItemIsNotFound(t *testing.T) {
	srv := newServer(t, nil)

	for _, path := range []string{
		"/v1/items/nope",
		"/v1/items/nope/frontier",
		"/v1/items/nope/rules",
		"/v1/items/nope/records",
	} {
		if w := do(t, srv, http.MethodGet, path, ""); w.Code != http.StatusNotFound {
			t.Errorf("%s = %d, want 404", path, w.Code)
		}
	}
}

func TestBadRequests(t *testing.T) {
	srv := newServer(t, nil)

	tests := []struct{ name, method, path, body string }{
		{"no name", http.MethodPost, "/v1/items", `{"aliases":["car"]}`},
		{"malformed json", http.MethodPost, "/v1/items", `{"name":`},
		{"unknown field", http.MethodPost, "/v1/items", `{"name":"x","nonsense":1}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if w := do(t, srv, tt.method, tt.path, tt.body); w.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400: %s", w.Code, w.Body)
			}
		})
	}
}

// An item with no targets cannot be crawled, and saying so on the request
// beats a job that fails a minute later somewhere else.
func TestCrawlingWithoutTargetsFailsImmediately(t *testing.T) {
	srv := newServer(t, nil)

	if w := do(t, srv, http.MethodPost, "/v1/items", `{"name":"vehicle"}`); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}

	w := do(t, srv, http.MethodPost, "/v1/items/vehicle/crawl", `{}`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400: %s", w.Code, w.Body)
	}
	if !strings.Contains(w.Body.String(), "targets") {
		t.Errorf("the error does not say what is missing: %s", w.Body)
	}
}

func TestUnknownTemplateIsRejected(t *testing.T) {
	srv := newServer(t, nil)

	w := do(t, srv, http.MethodPost, "/v1/items", `{"name":"x","template":"nonexistent"}`)
	if w.Code != http.StatusBadRequest && w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "nonexistent") {
		t.Errorf("the error does not name the template: %s", w.Body)
	}
}

func TestTemplateFillsProperties(t *testing.T) {
	srv := newServer(t, nil)

	w := do(t, srv, http.MethodPost, "/v1/items", `{"name":"cars","template":"vehicle"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body)
	}

	var item store.Item
	if err := json.Unmarshal(w.Body.Bytes(), &item); err != nil {
		t.Fatal(err)
	}
	if len(item.Properties) < 5 {
		t.Errorf("template gave %d properties", len(item.Properties))
	}
}

func TestJobNotFound(t *testing.T) {
	srv := newServer(t, nil)

	if w := do(t, srv, http.MethodGet, "/v1/jobs/crawl-999", ""); w.Code != http.StatusNotFound {
		t.Errorf("status = %d", w.Code)
	}
	if w := do(t, srv, http.MethodGet, "/v1/jobs", ""); w.Code != http.StatusOK {
		t.Errorf("list = %d", w.Code)
	}
}

func TestMetricsAreServed(t *testing.T) {
	srv := newServer(t, nil)

	do(t, srv, http.MethodGet, "/healthz", "")
	do(t, srv, http.MethodGet, "/v1/items/nope", "")

	w := do(t, srv, http.MethodGet, "/metrics", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}

	body := w.Body.String()
	for _, want := range []string{
		"scour_http_requests_total",
		"scour_http_requests_in_flight",
		"scour_jobs",
		"scour_uptime_seconds",
		`status="404"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics do not mention %q:\n%s", want, body)
		}
	}
}

func TestMetricsCanBeDisabled(t *testing.T) {
	srv := newServer(t, func(c *config.Config) { c.Server.Metrics = "" })

	if w := do(t, srv, http.MethodGet, "/metrics", ""); w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want the route absent", w.Code)
	}
}

func tokenFile(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAuth(t *testing.T) {
	path := tokenFile(t, "s3cret\n")
	srv := newServer(t, func(c *config.Config) { c.Server.TokenFile = path })

	tests := []struct {
		name   string
		header string
		want   int
	}{
		{"no header", "", http.StatusUnauthorized},
		{"wrong token", "Bearer wrong", http.StatusUnauthorized},
		{"not bearer", "Basic s3cret", http.StatusUnauthorized},
		{"bare token", "s3cret", http.StatusUnauthorized},
		{"empty bearer", "Bearer ", http.StatusUnauthorized},
		{"right token", "Bearer s3cret", http.StatusOK},
		// A trailing newline in the file must not become part of the secret,
		// since every editor adds one.
		{"lowercase scheme", "bearer s3cret", http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := do(t, srv, http.MethodGet, "/v1/items", "", "Authorization", tt.header)
			if w.Code != tt.want {
				t.Errorf("status = %d, want %d", w.Code, tt.want)
			}
		})
	}
}

// Liveness must answer without a credential, so an orchestrator can check the
// process without holding one.
func TestHealthIsExemptFromAuth(t *testing.T) {
	path := tokenFile(t, "s3cret")
	srv := newServer(t, func(c *config.Config) { c.Server.TokenFile = path })

	if w := do(t, srv, http.MethodGet, "/healthz", ""); w.Code != http.StatusOK {
		t.Errorf("status = %d", w.Code)
	}
	// Everything else is not exempt, including metrics, which describe the
	// service to anyone who can read them.
	if w := do(t, srv, http.MethodGet, "/metrics", ""); w.Code != http.StatusUnauthorized {
		t.Errorf("metrics = %d, want 401", w.Code)
	}
}

func TestUnauthorizedAdvertisesTheScheme(t *testing.T) {
	path := tokenFile(t, "s3cret")
	srv := newServer(t, func(c *config.Config) { c.Server.TokenFile = path })

	w := do(t, srv, http.MethodGet, "/v1/items", "")
	if got := w.Header().Get("WWW-Authenticate"); !strings.Contains(got, "Bearer") {
		t.Errorf("WWW-Authenticate = %q", got)
	}
}

// An empty token file is almost always a failed provisioning step, and serving
// without auth would be the worst possible response to it.
func TestEmptyTokenFileIsRefused(t *testing.T) {
	path := tokenFile(t, "   \n")

	cfg := config.Default()
	cfg.Server.TokenFile = path
	if _, err := New(cfg, nil, nil); err == nil {
		t.Error("an empty token file should be an error, not auth silently disabled")
	}
}

func TestMissingTokenFileIsRefused(t *testing.T) {
	cfg := config.Default()
	cfg.Server.TokenFile = filepath.Join(t.TempDir(), "absent")
	if _, err := New(cfg, nil, nil); err == nil {
		t.Error("a missing token file should be reported rather than ignored")
	}
}

func TestNoTokenFileMeansNoAuth(t *testing.T) {
	srv := newServer(t, nil)
	if srv.auth.Enabled() {
		t.Error("auth should be off when no token file is configured")
	}
	if w := do(t, srv, http.MethodGet, "/v1/items", ""); w.Code != http.StatusOK {
		t.Errorf("status = %d", w.Code)
	}
}
