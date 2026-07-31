// SPDX-License-Identifier: GPL-3.0-or-later

package export

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/rangertaha/scour/internal/store"
)

func rows() []store.RecordRow {
	return []store.RecordRow{
		{
			Record: store.Record{ID: 1, Confidence: 0.91, Format: "html", Label: store.Valid},
			URL:    "http://a.example.com/cars/1/",
			Values: map[string]string{"make": "Ford", "price": "$42,000"},
		},
		{
			Record: store.Record{ID: 2, Confidence: 0.72, Format: "html", Label: store.Unlabelled},
			URL:    "http://a.example.com/cars/2/",
			// Carries a field the first record lacks, which is the ordinary
			// case: extraction is per page.
			Values: map[string]string{"make": "Ram", "price": "$39,500", "year": "2026"},
		},
		{
			Record: store.Record{ID: 3, Confidence: 0.55, Format: "pdf", Label: store.Invalid},
			URL:    "http://b.example.com/listing.pdf",
			Values: map[string]string{"make": "Chevrolet"},
		},
	}
}

func exportTo(t *testing.T, format string) (*Result, string) {
	t.Helper()

	dir := t.TempDir()
	e, err := New(format, Config{Dir: dir, Timestamp: "2026-03-14"})
	if err != nil {
		t.Fatal(err)
	}

	result, err := e.Export(context.Background(), "vehicle", rows())
	if err != nil {
		t.Fatal(err)
	}
	return result, dir
}

// One file per site is what makes an export diffable: a site that changed is a
// changed file rather than a diff across everything ever crawled.
func TestRecordsAreGroupedByDomain(t *testing.T) {
	result, dir := exportTo(t, "csv")

	if result.Records != 3 {
		t.Errorf("exported %d records, want 3", result.Records)
	}
	if len(result.Destinations) != 2 {
		t.Fatalf("wrote %d files, want one per domain: %v", len(result.Destinations), result.Destinations)
	}

	for _, want := range []string{
		filepath.Join(dir, "vehicle", "a.example.com", "2026-03-14.csv"),
		filepath.Join(dir, "vehicle", "b.example.com", "2026-03-14.csv"),
	} {
		if _, err := os.Stat(want); err != nil {
			t.Errorf("missing %s: %v", want, err)
		}
	}
}

func TestCSVColumns(t *testing.T) {
	_, dir := exportTo(t, "csv")

	f, err := os.Open(filepath.Join(dir, "vehicle", "a.example.com", "2026-03-14.csv"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	records, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 3 {
		t.Fatalf("read %d lines, want a header and two records", len(records))
	}

	header := records[0]
	// The union of every record's fields, not the first record's: "year"
	// appears only on the second row and must still get a column.
	want := []string{"id", "url", "confidence", "format", "label", "make", "price", "year"}
	if strings.Join(header, ",") != strings.Join(want, ",") {
		t.Errorf("header = %v,\n    want %v", header, want)
	}

	// Every row has to be the width of the header, or the file is not a CSV.
	for i, row := range records {
		if len(row) != len(header) {
			t.Errorf("row %d has %d cells, header has %d", i, len(row), len(header))
		}
	}

	// A record missing a field gets an empty cell rather than a shifted row.
	if records[1][7] != "" {
		t.Errorf("the record without a year has %q in the year column", records[1][7])
	}
	if records[2][7] != "2026" {
		t.Errorf("year = %q", records[2][7])
	}

	// The label travels with the record, because an export is also how a
	// person corrects records outside scour.
	if records[1][4] != string(store.Valid) {
		t.Errorf("label = %q", records[1][4])
	}
}

func TestJSONShape(t *testing.T) {
	_, dir := exportTo(t, "json")

	raw, err := os.ReadFile(filepath.Join(dir, "vehicle", "b.example.com", "2026-03-14.json"))
	if err != nil {
		t.Fatal(err)
	}

	var out []jsonRecord
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("not valid json: %v\n%s", err, raw)
	}
	if len(out) != 1 {
		t.Fatalf("got %d records", len(out))
	}
	if out[0].ID != 3 || out[0].Values["make"] != "Chevrolet" || out[0].Label != string(store.Invalid) {
		t.Errorf("record = %+v", out[0])
	}
}

func TestValuesAreNeverNull(t *testing.T) {
	dir := t.TempDir()
	e, err := New("json", Config{Dir: dir, Timestamp: "d"})
	if err != nil {
		t.Fatal(err)
	}

	_, err = e.Export(context.Background(), "vehicle", []store.RecordRow{
		{Record: store.Record{ID: 1}, URL: "http://x.example.com/"},
	})
	if err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "vehicle", "x.example.com", "d.json"))
	if err != nil {
		t.Fatal(err)
	}
	// A consumer should be able to index values without a nil check.
	if strings.Contains(string(raw), "null") {
		t.Errorf("null in the output:\n%s", raw)
	}
}

// Losing records because a URL is odd would be the worst trade an exporter
// could make.
func TestRecordsWithUnparseableURLsAreKept(t *testing.T) {
	dir := t.TempDir()
	e, err := New("csv", Config{Dir: dir, Timestamp: "d"})
	if err != nil {
		t.Fatal(err)
	}

	result, err := e.Export(context.Background(), "vehicle", []store.RecordRow{
		{Record: store.Record{ID: 1}, URL: ""},
		{Record: store.Record{ID: 2}, URL: "://nonsense"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Records != 2 {
		t.Errorf("exported %d of 2 records", result.Records)
	}
	if _, err := os.Stat(filepath.Join(dir, "vehicle", unknownDomain, "d.csv")); err != nil {
		t.Errorf("nothing under %s: %v", unknownDomain, err)
	}
}

// An entity name reaches this from user input, so a separator in it must not
// write outside the export directory.
func TestNamesCannotEscapeTheDirectory(t *testing.T) {
	// The property that matters is that the result stays inside the export
	// directory, not that it equals any particular string.
	base := t.TempDir()
	for _, in := range []string{
		"../../etc", "a/b", "..", ".", "....", "", "ok-name_1.2",
		`..\..\windows`, "a/../../b", "\u0000null", "  spaces  ",
	} {
		got := safe(in)
		if strings.ContainsAny(got, `/\`) {
			t.Errorf("safe(%q) = %q, which still contains a separator", in, got)
		}
		if got == "" {
			t.Errorf("safe(%q) is empty, which is not a usable path segment", in)
		}

		joined := filepath.Clean(filepath.Join(base, got))
		if !strings.HasPrefix(joined, base+string(filepath.Separator)) {
			t.Errorf("safe(%q) = %q escapes to %q", in, got, joined)
		}
	}

	// A name that is already fine should survive unchanged, or every export
	// path would be mangled for no reason.
	if got := safe("ok-name_1.2"); got != "ok-name_1.2" {
		t.Errorf("safe mangled an already-safe name: %q", got)
	}
}

func TestRerunningOverwrites(t *testing.T) {
	dir := t.TempDir()
	e, err := New("csv", Config{Dir: dir, Timestamp: "2026-03-14"})
	if err != nil {
		t.Fatal(err)
	}

	for range 3 {
		if _, err := e.Export(context.Background(), "vehicle", rows()); err != nil {
			t.Fatal(err)
		}
	}

	entries, err := os.ReadDir(filepath.Join(dir, "vehicle", "a.example.com"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("re-running left %d files, want one", len(entries))
	}
}

func TestWebhookPostsRecords(t *testing.T) {
	var mu sync.Mutex
	var got []payload

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body payload
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		got = append(got, body)
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	e, err := New("webhook", Config{URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}

	result, err := e.Export(context.Background(), "vehicle", rows())
	if err != nil {
		t.Fatal(err)
	}
	if result.Records != 3 {
		t.Errorf("posted %d records", result.Records)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("got %d requests, want one per domain", len(got))
	}
	for _, body := range got {
		if body.Entity != "vehicle" || body.Domain == "" || body.Batches < 1 {
			t.Errorf("payload = %+v", body)
		}
	}
}

// A receiver that rejects a batch usually says why, and discarding that would
// leave the operator guessing.
func TestWebhookReportsRejection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		w.Write([]byte("missing tenant header"))
	}))
	defer srv.Close()

	e, err := New("webhook", Config{URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}

	_, err = e.Export(context.Background(), "vehicle", rows())
	if err == nil {
		t.Fatal("a rejected batch was reported as success")
	}
	if !strings.Contains(err.Error(), "missing tenant header") {
		t.Errorf("the receiver's reason was dropped: %v", err)
	}
}

func TestWebhookSendsItsToken(t *testing.T) {
	var seen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	t.Setenv("SCOUR_TEST_WEBHOOK_TOKEN", "s3cret")
	e, err := New("webhook", Config{URL: srv.URL, TokenEnv: "SCOUR_TEST_WEBHOOK_TOKEN"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Export(context.Background(), "vehicle", rows()); err != nil {
		t.Fatal(err)
	}

	if seen != "Bearer s3cret" {
		t.Errorf("Authorization = %q", seen)
	}
}

// Naming a variable and leaving it unset is a deployment mistake, and posting
// unauthenticated is a worse answer than refusing.
func TestWebhookRefusesAnEmptyToken(t *testing.T) {
	t.Setenv("SCOUR_TEST_WEBHOOK_TOKEN", "")
	if _, err := New("webhook", Config{URL: "http://example.com", TokenEnv: "SCOUR_TEST_WEBHOOK_TOKEN"}); err == nil {
		t.Error("an empty token variable should be refused")
	}
}

func TestConfigurationIsChecked(t *testing.T) {
	tests := []struct {
		name   string
		format string
		cfg    Config
	}{
		{"csv without a directory", "csv", Config{Timestamp: "d"}},
		{"csv without a timestamp", "csv", Config{Dir: "/tmp"}},
		{"webhook without a url", "webhook", Config{}},
		{"webhook with a non-http url", "webhook", Config{URL: "ftp://example.com"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := New(tt.format, tt.cfg); err == nil {
				t.Error("want an error")
			}
		})
	}
}

func TestRegistry(t *testing.T) {
	for _, name := range []string{"csv", "json", "webhook"} {
		if !Has(name) {
			t.Errorf("%s is not registered: %v", name, Names())
		}
	}
	if _, err := New("nonsense", Config{Dir: "/tmp", Timestamp: "d"}); err == nil {
		t.Error("an unknown format must be an error")
	}
	// An empty name is the documented default rather than a failure.
	if _, err := New("", Config{Dir: t.TempDir(), Timestamp: "d"}); err != nil {
		t.Errorf("the default format failed: %v", err)
	}
}
