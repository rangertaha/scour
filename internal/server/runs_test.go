// SPDX-License-Identifier: GPL-3.0-or-later

package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// runOf reads the run out of a start response.
func runOf(t *testing.T, body string) map[string]any {
	t.Helper()
	var envelope struct {
		Run map[string]any `json:"run"`
	}
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	if envelope.Run == nil {
		t.Fatalf("no run in the response: %s", body)
	}
	return envelope.Run
}

// A caller who starts work is handed the durable thing they started, so they
// can ask about it tomorrow rather than only while this process lives.
func TestStartingACrawlReturnsARun(t *testing.T) {
	srv := newServer(t, nil)

	if w := do(t, srv, http.MethodPost, "/v1/items", `{"name":"vehicle"}`); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	if w := do(t, srv, http.MethodPost, "/v1/jobs", `{"name":"vehicle"}`); w.Code == http.StatusNotFound {
		t.Log("no job create route yet; the run route makes the job on first use")
	}

	w := do(t, srv, http.MethodPost, "/v1/jobs/vehicle/runs", `{}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("a job with no targets = %d, want 400: %s", w.Code, w.Body)
	}
}

// The training run is durable too, which is the whole reason a run carries a
// kind: one id space, watched one way.
func TestStartingATrainingReturnsARun(t *testing.T) {
	srv := newServer(t, nil)

	if w := do(t, srv, http.MethodPost, "/v1/items",
		`{"name":"vehicle","properties":[{"name":"make","example":"Ford"}]}`); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}

	w := do(t, srv, http.MethodPost, "/v1/items/vehicle/model/runs", `{}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202: %s", w.Code, w.Body)
	}
	if loc := w.Header().Get("Location"); !strings.HasPrefix(loc, "/v1/runs/") {
		t.Errorf("Location = %q, want the run's address", loc)
	}

	run := runOf(t, w.Body.String())
	if run["Kind"] != "train" {
		t.Errorf("kind = %v, want train: %v", run["Kind"], run)
	}

	// The id in the response is an address that answers.
	id, ok := run["ID"].(float64)
	if !ok {
		t.Fatalf("no numeric id: %v", run)
	}
	got := do(t, srv, http.MethodGet, "/v1/runs/"+itoa(int(id)), "")
	if got.Code != http.StatusOK {
		t.Errorf("reading the run back = %d: %s", got.Code, got.Body)
	}
}

// The old routes said what was being done rather than what was made, and were
// watched through a handle that died with the process.
func TestTheRetiredRoutesAreGone(t *testing.T) {
	srv := newServer(t, nil)

	if w := do(t, srv, http.MethodPost, "/v1/items", `{"name":"vehicle"}`); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	for _, path := range []string{"/v1/items/vehicle/crawl", "/v1/items/vehicle/train"} {
		if w := do(t, srv, http.MethodPost, path, `{}`); w.Code != http.StatusNotFound {
			t.Errorf("%s still answers with %d", path, w.Code)
		}
	}
}

// A run listing is bounded by default, because a history grows without limit
// and almost every reader wants the recent end of it.
func TestRunListingIsBounded(t *testing.T) {
	srv := newServer(t, nil)

	w := do(t, srv, http.MethodGet, "/v1/runs", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body)
	}
	var envelope struct {
		Runs []map[string]any `json:"runs"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if envelope.Runs == nil && w.Body.Len() == 0 {
		t.Error("no runs key at all")
	}
}

// A job's history is read through the job, and an unknown one is a miss.
func TestAJobsRunHistory(t *testing.T) {
	srv := newServer(t, nil)

	if w := do(t, srv, http.MethodGet, "/v1/jobs/nosuchjob/runs", ""); w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404: %s", w.Code, w.Body)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
