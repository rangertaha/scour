// SPDX-License-Identifier: GPL-3.0-or-later

package server

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rangertaha/scour/internal/export"
	"github.com/rangertaha/scour/internal/store"
)

// onRecords runs one request against the record routes alone.
//
// They are wired into the same mux as everything else in the running server,
// but server.go is not this file's to edit, so the tests build a mux of their
// own out of the one registration function. What that costs is the middleware,
// which the tests in server_test.go already cover, and what it buys is that a
// route these tests pass against is a route registerRecords really declares
// rather than one a test helper invented.
func onRecords(t *testing.T, srv *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()

	mux := http.NewServeMux()
	srv.registerRecords(mux)

	var r *http.Request
	if body == "" {
		r = httptest.NewRequestWithContext(t.Context(), method, path, nil)
	} else {
		r = httptest.NewRequestWithContext(t.Context(), method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

// corpus is an item with records in it, built through the store.
//
// Through the store rather than by crawling, because these tests are about what
// the routes do with records rather than about where records come from, and a
// fixture that had to run an extraction would fail for reasons that have
// nothing to do with the API.
//
// Every record is attached to a page, since that is what a real one has: the
// url is what the export writes and what ?job= filters through, and a fixture
// without one would pass tests that a real record fails.
type corpus struct {
	item *store.Item
	uk   *store.Job
	us   *store.Job
}

func newCorpus(t *testing.T, srv *Server) corpus {
	t.Helper()
	ctx := t.Context()

	item, err := srv.store.CreateItem(ctx, "vehicle")
	if err != nil {
		t.Fatal(err)
	}
	for _, prop := range []string{"make", "model"} {
		if err := srv.store.AddPropertyDetail(ctx, item.ID, store.PropertyDetail{Name: prop}); err != nil {
			t.Fatal(err)
		}
	}

	uk, err := srv.store.CreateJob(ctx, "uk", item.ID)
	if err != nil {
		t.Fatal(err)
	}
	us, err := srv.store.CreateJob(ctx, "us", item.ID)
	if err != nil {
		t.Fatal(err)
	}

	rows := []struct {
		job    *store.Job
		url    string
		conf   float64
		format string
		values map[string]string
	}{
		{uk, "https://example.com/cars/1", 0.91, "text/html", map[string]string{"make": "Ford", "model": "F-150 crew cab"}},
		{uk, "https://example.com/cars/2", 0.42, "text/html", map[string]string{"make": "Vauxhall", "model": "Corsa"}},
		{us, "https://example.com/cars/3", 0.77, "application/pdf", map[string]string{"make": "Ford", "model": "Ranger"}},
	}

	extracted := make([]store.Extracted, 0, len(rows))
	for _, row := range rows {
		if err := srv.store.RecordFetch(ctx, store.Fetched{
			ItemID: item.ID, JobID: row.job.ID, URL: row.url,
			Status: store.URLFetched, StatusCode: 200, ContentType: row.format,
		}); err != nil {
			t.Fatal(err)
		}
		extracted = append(extracted, store.Extracted{
			URL: row.url, Confidence: row.conf, Format: row.format, Values: row.values,
		})
	}
	if _, err := srv.store.SaveRecords(ctx, item.ID, extracted); err != nil {
		t.Fatal(err)
	}
	return corpus{item: item, uk: uk, us: us}
}

// join writes ids the way a request carries them, which is also the way a path
// carries one.
func join(ids []uint) string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, strconv.FormatUint(uint64(id), 10))
	}
	return strings.Join(out, ",")
}

// listed reads the ids out of a listing, in the order they came back.
func listed(t *testing.T, w *httptest.ResponseRecorder) []uint {
	t.Helper()

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body)
	}
	var envelope struct {
		Records []store.RecordRow `json:"records"`
		Total   int64             `json:"total"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode %q: %v", w.Body.String(), err)
	}
	out := make([]uint, 0, len(envelope.Records))
	for _, row := range envelope.Records {
		out = append(out, row.ID)
	}
	return out
}

// The query parameters are the command line's flags spelled the same way, so
// each one has to narrow the collection the way its flag narrows a listing.
func TestEveryFilterNarrowsTheCollection(t *testing.T) {
	srv := newServer(t, nil)
	newCorpus(t, srv)

	for _, tc := range []struct {
		name  string
		query string
		want  int
	}{
		{"everything", "", 3},
		{"a confidence floor", "?confidence=0.5", 2},
		{"a confidence ceiling", "?confidence=..0.5", 1},
		{"a confidence band", "?confidence=0.4..0.8", 2},
		{"one content type", "?type=text/html", 2},
		{"an excluded type", "?exclude_type=application/pdf", 2},
		{"two types at once", "?type=text/html&type=application/pdf", 3},
		{"one job", "?job=uk", 2},
		{"a limit", "?limit=1", 1},
		{"nothing marked yet", "?verdict=none", 3},
		{"marked valid", "?verdict=valid", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := listed(t, onRecords(t, srv, http.MethodGet, "/v1/items/vehicle/records"+tc.query, ""))
			if len(got) != tc.want {
				t.Errorf("%s returned %d records, want %d", tc.query, len(got), tc.want)
			}
		})
	}
}

// A ceiling is what makes review possible: the records worth a person's
// attention are the ones the model was least sure of, and a filter with only a
// floor cannot ask for them.
func TestTheDoubtfulRecordsCanBeSelected(t *testing.T) {
	srv := newServer(t, nil)
	newCorpus(t, srv)

	w := onRecords(t, srv, http.MethodGet, "/v1/items/vehicle/records?confidence=..0.5&verdict=none", "")
	if len(listed(t, w)) != 1 {
		t.Fatalf("the doubtful half = %s", w.Body)
	}

	// A band that cannot match anything is the caller's mistake, and one that
	// answered with the whole table would look like the filter had worked.
	for _, bad := range []string{"?confidence=high", "?confidence=..0", "?confidence=0.8..0.2", "?confidence=1.4"} {
		if w := onRecords(t, srv, http.MethodGet, "/v1/items/vehicle/records"+bad, ""); w.Code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400: %s", bad, w.Code, w.Body)
		}
	}
}

// A reader that stopped says where, and gets what has landed since in the order
// it landed. Ranking a resumption by confidence would print the tail out of
// sequence, which is the one order a follower cannot use.
func TestSinceIDResumesWhereAReaderStopped(t *testing.T) {
	srv := newServer(t, nil)
	newCorpus(t, srv)

	all := listed(t, onRecords(t, srv, http.MethodGet, "/v1/items/vehicle/records", ""))
	var lowest uint
	for _, id := range all {
		if lowest == 0 || id < lowest {
			lowest = id
		}
	}

	got := listed(t, onRecords(t, srv, http.MethodGet,
		"/v1/items/vehicle/records?since_id="+join([]uint{lowest}), ""))
	if len(got) != len(all)-1 {
		t.Fatalf("%d records after %d, want the %d written since", len(got), lowest, len(all)-1)
	}
	for i := 1; i < len(got); i++ {
		if got[i] < got[i-1] {
			t.Errorf("ids came back as %v, want them in the order they landed", got)
		}
	}
}

// Searching records is reading the collection with a query on it, which is why
// there is no /search path.
func TestASearchIsTheCollectionWithAQuery(t *testing.T) {
	srv := newServer(t, nil)
	newCorpus(t, srv)

	ask := func(q string) *httptest.ResponseRecorder {
		return onRecords(t, srv, http.MethodGet,
			"/v1/items/vehicle/records?"+url.Values{"q": {q}}.Encode(), "")
	}

	if got := listed(t, ask("make:Ford")); len(got) != 2 {
		t.Errorf("make:Ford matched %d, want the two Fords", len(got))
	}
	// Two terms narrow rather than widen, which is what repeated typing means
	// everywhere else a search box exists.
	if got := listed(t, ask("make:Ford model:Ranger")); len(got) != 1 {
		t.Errorf("two terms matched %d, want the one record answering both", len(got))
	}
	// A quoted phrase is one term. Split into two it would match anything
	// carrying both words anywhere, which is a much wider question.
	if got := listed(t, ask(`"crew cab"`)); len(got) != 1 {
		t.Errorf("a phrase matched %d, want the one record carrying it", len(got))
	}
	// A url is full of colons, so the field before one is decided by what the
	// item defines rather than by punctuation.
	if got := listed(t, ask("url:example.com/cars/3")); len(got) != 1 {
		t.Errorf("a url term matched %d, want the record read off that page", len(got))
	}

	// A field the item does not define is reported rather than searched for
	// literally, which would answer with nothing and read as an empty database.
	if w := ask("manufacturer:Ford"); w.Code != http.StatusBadRequest {
		t.Errorf("an unknown field = %d, want 400: %s", w.Code, w.Body)
	}
}

// `scour record write` and ?format=csv are one row of the parity table, so a
// script that reads the file has to be able to read the response.
func TestAnExportIsTheBytesRecordWriteWrites(t *testing.T) {
	srv := newServer(t, nil)
	corpus := newCorpus(t, srv)

	for _, document := range export.Documents() {
		t.Run(document, func(t *testing.T) {
			w := onRecords(t, srv, http.MethodGet, "/v1/items/vehicle/records?format="+document, "")
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d: %s", w.Code, w.Body)
			}

			// The same rows through the exporter the command line uses. Every
			// fixture record came off one site, so the file it writes for that
			// site holds all of them and the two are comparable whole.
			rows, _, err := srv.store.SearchRecords(t.Context(), corpus.item.ID, store.RecordQuery{})
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			exporter, err := export.New(document, export.Config{Dir: dir, Timestamp: "2026-03-14"})
			if err != nil {
				t.Fatal(err)
			}
			result, err := exporter.Export(t.Context(), corpus.item.Name, rows)
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Destinations) != 1 {
				t.Fatalf("the fixture wrote %d files, so there is nothing to compare against",
					len(result.Destinations))
			}
			written, err := os.ReadFile(filepath.Clean(result.Destinations[0]))
			if err != nil {
				t.Fatal(err)
			}
			if w.Body.String() != string(written) {
				t.Errorf("the response and the file disagree:\n%s\n%s", w.Body.String(), written)
			}
		})
	}

	// A format nobody implements is refused rather than answered as json, so a
	// pipeline asking for the wrong thing finds out on the request.
	if w := onRecords(t, srv, http.MethodGet, "/v1/items/vehicle/records?format=xlsx", ""); w.Code != http.StatusBadRequest {
		t.Errorf("an unknown format = %d, want 400: %s", w.Code, w.Body)
	}
	// A webhook is a destination rather than an encoding, and a GET cannot mean
	// "post these somewhere and tell me nothing".
	if w := onRecords(t, srv, http.MethodGet, "/v1/items/vehicle/records?format=webhook", ""); w.Code != http.StatusBadRequest {
		t.Errorf("format=webhook = %d, want 400: %s", w.Code, w.Body)
	}
}

// A stream is what lands after the request. Replaying what was already there
// would make a client that took a snapshot first see everything twice, and the
// envelope's total would be a lie about a thing that has no pages.
func TestFollowingSendsWhatLandsAfterTheRequest(t *testing.T) {
	srv := newServer(t, nil)
	corpus := newCorpus(t, srv)

	mux := http.NewServeMux()
	srv.registerRecords(mux)
	// A real connection rather than a recorder, because a recorder's body can
	// only be read once the handler has returned and a stream's whole point is
	// that it has not.
	listening := httptest.NewServer(mux)
	defer listening.Close()

	ctx, stop := context.WithCancel(t.Context())
	defer stop()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		listening.URL+"/v1/items/vehicle/records?follow=true", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if got := resp.Header.Get("Content-Type"); got != "application/x-ndjson" {
		t.Errorf("Content-Type = %q, want ndjson whatever else was asked for", got)
	}

	// Written after the stream is open, so it is unambiguously the record the
	// follower is waiting for rather than one of the three already there.
	landed := make(chan error, 1)
	go func() {
		time.Sleep(recordPoll / 2)
		_, err := srv.store.SaveRecords(t.Context(), corpus.item.ID, []store.Extracted{
			{URL: "https://example.com/cars/1", Confidence: 0.91, Format: "text/html",
				Values: map[string]string{"make": "Ford", "model": "F-150 crew cab"}},
			{URL: "https://example.com/cars/2", Confidence: 0.42, Format: "text/html",
				Values: map[string]string{"make": "Vauxhall", "model": "Corsa"}},
			{URL: "https://example.com/cars/3", Confidence: 0.77, Format: "application/pdf",
				Values: map[string]string{"make": "Ford", "model": "Ranger"}},
			{URL: "https://example.com/cars/4", Confidence: 0.88, Format: "text/html",
				Values: map[string]string{"make": "Tesla", "model": "Model 3"}},
		})
		landed <- err
	}()

	lines := bufio.NewScanner(resp.Body)
	if !lines.Scan() {
		t.Fatalf("the stream ended without a record: %v", lines.Err())
	}
	if err := <-landed; err != nil {
		t.Fatal(err)
	}

	var first store.RecordRow
	if err := json.Unmarshal(lines.Bytes(), &first); err != nil {
		t.Fatalf("decode %q: %v", lines.Text(), err)
	}
	if first.Values["make"] != "Tesla" {
		t.Errorf("the first line is %v, want the record that landed after the request",
			first.Values)
	}

	// The client hanging up is how a stream ends, and it has to end rather than
	// polling a database nobody is listening to.
	stop()
	done := make(chan struct{})
	go func() {
		for lines.Scan() { //nolint:revive // draining until the connection closes is the point
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * recordPoll):
		t.Error("the stream is still open after the client went away")
	}
}

// A reviewer marks a screenful, so the verdict is a PATCH on the collection
// carrying the ids rather than one request per record.
func TestMarkingAScreenfulIsOneRequest(t *testing.T) {
	srv := newServer(t, nil)
	corpus := newCorpus(t, srv)

	ids := listed(t, onRecords(t, srv, http.MethodGet, "/v1/items/vehicle/records", ""))
	if len(ids) != 3 {
		t.Fatalf("the fixture has %d records", len(ids))
	}

	w := onRecords(t, srv, http.MethodPatch, "/v1/items/vehicle/records",
		`{"ids":[`+join(ids[:2])+`],"verdict":"invalid"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body)
	}
	if got := decodeBody(t, w)["marked"]; got != float64(2) {
		t.Errorf("marked = %v, want both: %s", got, w.Body)
	}

	rows, _, err := srv.store.SearchRecords(t.Context(), corpus.item.ID,
		store.RecordQuery{Label: store.Invalid})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Errorf("%d records carry the verdict, want the two that were sent", len(rows))
	}

	// The API says none where the column says unlabelled, and taking a verdict
	// back has to be possible or a mistake is permanent.
	back := onRecords(t, srv, http.MethodPatch, "/v1/items/vehicle/records",
		`{"ids":[`+join(ids[:1])+`],"verdict":"none"}`)
	if back.Code != http.StatusOK {
		t.Fatalf("unmarking = %d: %s", back.Code, back.Body)
	}
	if got := decodeBody(t, back)["verdict"]; got != "none" {
		t.Errorf("verdict = %v, want the word that was sent back", got)
	}
}

// Reporting only what was marked would let a caller working from a stale
// listing believe every verdict it sent was recorded.
func TestAMarkSaysHowManyOfTheIdsItFound(t *testing.T) {
	srv := newServer(t, nil)
	newCorpus(t, srv)

	ids := listed(t, onRecords(t, srv, http.MethodGet, "/v1/items/vehicle/records", ""))
	w := onRecords(t, srv, http.MethodPatch, "/v1/items/vehicle/records",
		`{"ids":[`+join(ids[:1])+`,90210],"verdict":"valid"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body)
	}
	body := decodeBody(t, w)
	if body["marked"] != float64(1) || body["asked"] != float64(2) {
		t.Errorf("body = %s, want both counts", w.Body)
	}

	// None of them found is a miss rather than a success that did nothing.
	if w := onRecords(t, srv, http.MethodPatch, "/v1/items/vehicle/records",
		`{"ids":[90210],"verdict":"valid"}`); w.Code != http.StatusNotFound {
		t.Errorf("marking nothing = %d, want 404: %s", w.Code, w.Body)
	}
	// A body with no ids is a client that built its list wrong, and marking
	// nothing quietly is how that goes unnoticed.
	for _, body := range []string{`{"ids":[],"verdict":"valid"}`, `{"ids":[1],"verdict":"probably"}`} {
		if w := onRecords(t, srv, http.MethodPatch, "/v1/items/vehicle/records", body); w.Code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400: %s", body, w.Code, w.Body)
		}
	}
}

// There is no undo, and a bare DELETE on the collection is a plausible typo for
// the listing beside it.
func TestAnUnfilteredDeleteIsRefused(t *testing.T) {
	srv := newServer(t, nil)
	newCorpus(t, srv)

	if w := onRecords(t, srv, http.MethodDelete, "/v1/items/vehicle/records", ""); w.Code != http.StatusBadRequest {
		t.Fatalf("a bare delete = %d, want 400: %s", w.Code, w.Body)
	}
	// Present and false is a client deciding not to, which must not read as
	// permission because the key was there.
	if w := onRecords(t, srv, http.MethodDelete, "/v1/items/vehicle/records?all=false", ""); w.Code != http.StatusBadRequest {
		t.Errorf("all=false = %d, want 400: %s", w.Code, w.Body)
	}
	// A limit says how many rows to show, and honouring it here would remove an
	// arbitrary subset of what matched.
	if w := onRecords(t, srv, http.MethodDelete, "/v1/items/vehicle/records?job=uk&limit=1", ""); w.Code != http.StatusBadRequest {
		t.Errorf("a limited delete = %d, want 400: %s", w.Code, w.Body)
	}
	// Everything and a filter together is a contradiction rather than a
	// narrowing, since one of the two has to be ignored for it to run.
	if w := onRecords(t, srv, http.MethodDelete, "/v1/items/vehicle/records?all=true&job=uk", ""); w.Code != http.StatusBadRequest {
		t.Errorf("all with a filter = %d, want 400: %s", w.Code, w.Body)
	}

	if len(listed(t, onRecords(t, srv, http.MethodGet, "/v1/items/vehicle/records", ""))) != 3 {
		t.Error("a refused delete removed something")
	}
}

// The filters that pick a set out for reading pick the same set out for
// removal, which is what makes looking before deleting worth anything.
func TestADeleteTakesWhatTheFilterMatched(t *testing.T) {
	srv := newServer(t, nil)
	corpus := newCorpus(t, srv)

	w := onRecords(t, srv, http.MethodDelete, "/v1/items/vehicle/records?job=uk", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body)
	}
	if got := decodeBody(t, w)["deleted"]; got != float64(2) {
		t.Errorf("deleted = %v, want the two pages that job fetched: %s", got, w.Body)
	}
	if got := listed(t, onRecords(t, srv, http.MethodGet, "/v1/items/vehicle/records", "")); len(got) != 1 {
		t.Errorf("%d records left, want the other job's", len(got))
	}

	// The pages stay. A record is what was read out of a page, and a bad
	// reading is not a reason to pay to fetch the site again.
	urls, err := srv.store.FetchedURLs(t.Context(), corpus.item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(urls) != 3 {
		t.Errorf("%d pages left, want all three: removing records refetched the site", len(urls))
	}

	if w := onRecords(t, srv, http.MethodDelete, "/v1/items/vehicle/records?all=true", ""); w.Code != http.StatusOK {
		t.Fatalf("all=true = %d: %s", w.Code, w.Body)
	}
	if got := listed(t, onRecords(t, srv, http.MethodGet, "/v1/items/vehicle/records", "")); len(got) != 0 {
		t.Errorf("%d records left after ?all=true", len(got))
	}
}

// A record is read one at a time, and an id from another item's listing names
// nothing here rather than somebody else's row.
func TestARecordIsReadByItsOwnID(t *testing.T) {
	srv := newServer(t, nil)
	newCorpus(t, srv)

	ids := listed(t, onRecords(t, srv, http.MethodGet, "/v1/items/vehicle/records", ""))
	w := onRecords(t, srv, http.MethodGet, "/v1/items/vehicle/records/"+join(ids[:1]), "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body)
	}
	var envelope struct {
		Record store.RecordRow `json:"record"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Record.ID != ids[0] {
		t.Errorf("record = %d, want %d", envelope.Record.ID, ids[0])
	}
	// The page it came from is the next place to look when a value is wrong, so
	// it travels with the record.
	if envelope.Record.URL == "" {
		t.Error("the record does not say which page it was read out of")
	}

	if w := onRecords(t, srv, http.MethodGet, "/v1/items/vehicle/records/90210", ""); w.Code != http.StatusNotFound {
		t.Errorf("an id that is not there = %d, want 404: %s", w.Code, w.Body)
	}
	if w := onRecords(t, srv, http.MethodGet, "/v1/items/vehicle/records/newest", ""); w.Code != http.StatusBadRequest {
		t.Errorf("a word where an id goes = %d, want 400: %s", w.Code, w.Body)
	}
}

// The vocabulary moved, and a client still sending the old word is asking for a
// subset. Answering with every record would look like the item had no marks.
func TestTheOldLabelParameterIsRefused(t *testing.T) {
	srv := newServer(t, nil)
	newCorpus(t, srv)

	w := onRecords(t, srv, http.MethodGet, "/v1/items/vehicle/records?label=unlabelled", "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", w.Code, w.Body)
	}
	if !strings.Contains(w.Body.String(), "verdict") {
		t.Errorf("the refusal does not say what to send instead: %s", w.Body)
	}
}

// Every route here names an item, and one that is not there is the caller's
// business rather than the server's failure.
func TestAnUnknownItemIsAMissOnEveryRecordRoute(t *testing.T) {
	srv := newServer(t, nil)

	for _, probe := range []struct{ method, path, body string }{
		{http.MethodGet, "/v1/items/nosuch/records", ""},
		{http.MethodGet, "/v1/items/nosuch/records/1", ""},
		{http.MethodPatch, "/v1/items/nosuch/records", `{"ids":[1],"verdict":"valid"}`},
		{http.MethodDelete, "/v1/items/nosuch/records?all=true", ""},
	} {
		if w := onRecords(t, srv, probe.method, probe.path, probe.body); w.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404: %s", probe.method, probe.path, w.Code, w.Body)
		}
	}

	// An unknown job is a miss too, resolved before anything runs: an empty
	// answer would be indistinguishable from a job that found nothing.
	newCorpus(t, srv)
	if w := onRecords(t, srv, http.MethodGet, "/v1/items/vehicle/records?job=nosuch", ""); w.Code != http.StatusNotFound {
		t.Errorf("an unknown job = %d, want 404: %s", w.Code, w.Body)
	}
}
