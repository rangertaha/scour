// SPDX-License-Identifier: GPL-3.0-or-later

package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/rangertaha/scour/internal/jobfile"
)

// jobs runs one request against the job routes.
//
// It builds its own mux rather than going through Handler, because the job
// routes are registered by the caller that owns Handler and these tests are
// about the resource rather than about who wired it. Everything the middleware
// does is covered by the tests that do go through Handler.
func jobs(t *testing.T, srv *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()

	mux := http.NewServeMux()
	srv.registerJobs(mux)

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

// itemWithJob creates an item through the API, which is what these tests want:
// POST /v1/items makes the job named after the item on the way, and a job route
// with no job to act on tests nothing. The items tests use a helper of their
// own that goes through the store, because they are about an item's parts and
// must not depend on a route creating anything else.
func itemWithJob(t *testing.T, srv *Server, name string) {
	t.Helper()
	if w := do(t, srv, http.MethodPost, "/v1/items", `{"name":"`+name+`"}`); w.Code != http.StatusOK {
		t.Fatalf("creating item %s: %s", name, w.Body)
	}
}

// A job assembled over the wire has to come back out as the config that would
// rebuild it, or a job could only ever live in the database it was made in.
func TestAJobRoundTripsThroughTOML(t *testing.T) {
	srv := newServer(t, nil)
	itemWithJob(t, srv, "vehicle")

	w := jobs(t, srv, http.MethodPost, "/v1/jobs", `{
		"name":"uk","item":"vehicle","depth":3,"max_pages":500,"max_time":"30m",
		"types":["html","feed"],
		"domains":[{"value":"https://www.example.co.uk/","subdomains":true}],
		"urls":[{"value":"https://www.example.co.uk/used/"}]}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create = %d, want 201: %s", w.Code, w.Body)
	}
	if loc := w.Header().Get("Location"); loc != "/v1/jobs/uk" {
		t.Errorf("Location = %q, want the job's address", loc)
	}

	w = jobs(t, srv, http.MethodGet, "/v1/jobs/uk?format=toml", "")
	if w.Code != http.StatusOK {
		t.Fatalf("toml = %d: %s", w.Code, w.Body)
	}
	if got := w.Header().Get("Content-Type"); !strings.Contains(got, "toml") {
		t.Errorf("Content-Type = %q, want a document rather than JSON", got)
	}

	config := w.Body.String()
	for _, want := range []string{
		`name = "uk"`, `item = "vehicle"`, "depth     = 3", "max_pages = 500",
		`max_time  = "30m0s"`, `"feed"`, `"html"`, "subdomains = true",
		"https://www.example.co.uk/used/",
		// The domain was given as a URL and has to come back as the bare host,
		// or the API would store a second target for a site the CLI already has.
		`value      = "example.co.uk"`,
	} {
		if !strings.Contains(config, want) {
			t.Errorf("the config is missing %q:\n%s", want, config)
		}
	}
}

// Validation has to run the code the create runs, so the two cannot disagree
// about which configs are acceptable, and it has to leave nothing behind.
func TestValidatingCreatesNothing(t *testing.T) {
	srv := newServer(t, nil)
	itemWithJob(t, srv, "vehicle")

	body := `{"name":"uk","item":"vehicle","domains":[{"value":"example.co.uk"}]}`
	w := jobs(t, srv, http.MethodPost, "/v1/jobs?validate=true", body)
	if w.Code != http.StatusOK {
		t.Fatalf("validate = %d, want 200: %s", w.Code, w.Body)
	}
	if decodeBody(t, w)["valid"] != true {
		t.Errorf("a good config was not called valid: %s", w.Body)
	}

	if w := jobs(t, srv, http.MethodGet, "/v1/jobs/uk", ""); w.Code != http.StatusNotFound {
		t.Errorf("validating created the job: %d %s", w.Code, w.Body)
	}
	// And the same body without the flag does create it, which is what makes
	// the check worth anything.
	if w := jobs(t, srv, http.MethodPost, "/v1/jobs", body); w.Code != http.StatusCreated {
		t.Fatalf("create after validate = %d: %s", w.Code, w.Body)
	}
}

// Validating is safe to do against a config written for another machine, so it
// must not require the item to exist here.
func TestValidatingDoesNotNeedTheItem(t *testing.T) {
	srv := newServer(t, nil)

	body := `{"name":"uk","item":"an-item-this-machine-has-never-heard-of",
		"domains":[{"value":"example.co.uk"}]}`
	if w := jobs(t, srv, http.MethodPost, "/v1/jobs?validate=true", body); w.Code != http.StatusOK {
		t.Errorf("validate = %d, want 200: %s", w.Code, w.Body)
	}
	// Applying one does need it, and says so rather than making an empty item.
	if w := jobs(t, srv, http.MethodPost, "/v1/jobs", body); w.Code != http.StatusNotFound {
		t.Errorf("create = %d, want 404: %s", w.Code, w.Body)
	}
}

// A checker that stops at the first fault turns fixing a config into as many
// round trips as it has mistakes.
func TestEveryProblemIsReportedAtOnce(t *testing.T) {
	srv := newServer(t, nil)

	w := jobs(t, srv, http.MethodPost, "/v1/jobs?validate=true", `{
		"name":"","item":"","depth":-1,"max_pages":-5,"max_time":"soon",
		"types":["nonsense"],"domains":[{"value":"not a hostname"}]}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", w.Code, w.Body)
	}

	var body struct {
		Problems []string `json:"problems"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(body.Problems, "\n")
	for _, want := range []string{"name is required", "item is required", "depth", "max_pages", "max_time", "types", "domain 1"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the report never mentions %q:\n%s", want, joined)
		}
	}
}

// A job with nowhere to look is refused when it is started rather than when it
// is created, because targets are a sub-resource here and adding them is the
// next request.
func TestAJobMayBeCreatedBeforeItHasTargets(t *testing.T) {
	srv := newServer(t, nil)
	itemWithJob(t, srv, "vehicle")

	if w := jobs(t, srv, http.MethodPost, "/v1/jobs", `{"name":"uk","item":"vehicle"}`); w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", w.Code, w.Body)
	}
	if w := jobs(t, srv, http.MethodGet, "/v1/jobs/uk", ""); w.Code != http.StatusOK {
		t.Errorf("reading it back = %d: %s", w.Code, w.Body)
	}
}

// The listing answers "what is there", and filtering by item is what makes it
// usable on a database with a job per site.
func TestJobListing(t *testing.T) {
	srv := newServer(t, nil)
	itemWithJob(t, srv, "vehicle")
	itemWithJob(t, srv, "boat")

	if w := jobs(t, srv, http.MethodPost, "/v1/jobs", `{"name":"uk","item":"vehicle"}`); w.Code != http.StatusCreated {
		t.Fatal(w.Body.String())
	}

	names := func(path string) []string {
		t.Helper()
		w := jobs(t, srv, http.MethodGet, path, "")
		if w.Code != http.StatusOK {
			t.Fatalf("%s = %d: %s", path, w.Code, w.Body)
		}
		var body struct {
			Jobs []struct {
				Name string
				Item string `json:"item"`
			} `json:"jobs"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		out := make([]string, 0, len(body.Jobs))
		for _, j := range body.Jobs {
			out = append(out, j.Name+"/"+j.Item)
		}
		return out
	}

	// Creating an item makes the job named after it, so all three are here.
	all := names("/v1/jobs")
	if len(all) != 3 {
		t.Errorf("listing = %v, want the two items' jobs and uk", all)
	}
	// The item name has to be on the row: a caller holding an item id would
	// otherwise need a request per job to turn it into a name.
	if !strings.Contains(strings.Join(all, " "), "uk/vehicle") {
		t.Errorf("the rows do not name the item: %v", all)
	}

	if got := names("/v1/jobs?item=boat"); len(got) != 1 || got[0] != "boat/boat" {
		t.Errorf("filtered listing = %v, want only boat's job", got)
	}
	if w := jobs(t, srv, http.MethodGet, "/v1/jobs?item=nosuchitem", ""); w.Code != http.StatusNotFound {
		t.Errorf("filtering by an unknown item = %d, want 404", w.Code)
	}
}

// Only the bounds given are written, so setting a depth leaves the budgets
// alone. Absent and zero mean different things, and a PATCH that could not tell
// them apart would make removing a budget impossible.
func TestPatchWritesOnlyWhatIsGiven(t *testing.T) {
	srv := newServer(t, nil)
	itemWithJob(t, srv, "vehicle")

	if w := jobs(t, srv, http.MethodPost, "/v1/jobs",
		`{"name":"uk","item":"vehicle","depth":3,"max_pages":500,"max_time":"30m"}`); w.Code != http.StatusCreated {
		t.Fatal(w.Body.String())
	}

	bounds := func(w *httptest.ResponseRecorder) (depth, maxPages float64, maxTime float64) {
		t.Helper()
		var body struct {
			Job struct {
				Depth    float64
				MaxPages float64
				MaxTime  float64
			}
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode %q: %v", w.Body, err)
		}
		return body.Job.Depth, body.Job.MaxPages, body.Job.MaxTime
	}

	w := jobs(t, srv, http.MethodPatch, "/v1/jobs/uk", `{"depth":12}`)
	if w.Code != http.StatusOK {
		t.Fatalf("patch = %d: %s", w.Code, w.Body)
	}
	depth, maxPages, maxTime := bounds(w)
	if depth != 12 {
		t.Errorf("depth = %v, want 12", depth)
	}
	if maxPages != 500 || maxTime == 0 {
		t.Errorf("setting the depth removed the budgets: max_pages %v, max_time %v", maxPages, maxTime)
	}

	// Zero is a value, not an omission: it removes the budget.
	w = jobs(t, srv, http.MethodPatch, "/v1/jobs/uk", `{"max_pages":0}`)
	if _, maxPages, _ = bounds(w); maxPages != 0 {
		t.Errorf("max_pages = %v, want the budget removed", maxPages)
	}
	if depth, _, _ = bounds(w); depth != 12 {
		t.Errorf("removing the page budget changed the depth to %v", depth)
	}
}

func TestPatchRejectsWhatItCannotWrite(t *testing.T) {
	srv := newServer(t, nil)
	itemWithJob(t, srv, "vehicle")
	if w := jobs(t, srv, http.MethodPost, "/v1/jobs", `{"name":"uk","item":"vehicle"}`); w.Code != http.StatusCreated {
		t.Fatal(w.Body.String())
	}

	tests := []struct{ name, body string }{
		{"nothing to set", `{}`},
		{"max_time is not a duration", `{"max_time":"soon"}`},
		{"a bound that cannot be negative", `{"depth":-1}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if w := jobs(t, srv, http.MethodPatch, "/v1/jobs/uk", tt.body); w.Code == http.StatusOK {
				t.Errorf("status = %d, want a rejection: %s", w.Code, w.Body)
			}
		})
	}
}

// A domain and a url are both targets, so both are one POST with a different
// body field, and both come back out of one GET.
func TestTargetsAreOneResource(t *testing.T) {
	srv := newServer(t, nil)
	itemWithJob(t, srv, "vehicle")
	if w := jobs(t, srv, http.MethodPost, "/v1/jobs", `{"name":"uk","item":"vehicle"}`); w.Code != http.StatusCreated {
		t.Fatal(w.Body.String())
	}

	if w := jobs(t, srv, http.MethodPost, "/v1/jobs/uk/targets",
		`{"domain":"https://www.example.co.uk/","subdomains":true}`); w.Code != http.StatusCreated {
		t.Fatalf("adding a domain = %d: %s", w.Code, w.Body)
	}
	// The list form is what an import sends, rather than one request per line.
	if w := jobs(t, srv, http.MethodPost, "/v1/jobs/uk/targets",
		`{"urls":["https://www.example.co.uk/used/","https://www.example.co.uk/new/"]}`); w.Code != http.StatusCreated {
		t.Fatalf("adding urls = %d: %s", w.Code, w.Body)
	}

	type target struct {
		ID    uint
		Kind  string
		Value string
	}
	export := func() (rows []target, domains, urls []string) {
		t.Helper()
		w := jobs(t, srv, http.MethodGet, "/v1/jobs/uk/targets", "")
		if w.Code != http.StatusOK {
			t.Fatalf("export = %d: %s", w.Code, w.Body)
		}
		var body struct {
			Targets []target `json:"targets"`
			Domains []string `json:"domains"`
			URLs    []string `json:"urls"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body.Targets, body.Domains, body.URLs
	}

	rows, domains, urls := export()
	if len(rows) != 3 {
		t.Fatalf("targets = %v, want three", rows)
	}
	// A subdomain target has to be written the way import reads it back, or it
	// quietly narrows to the bare host on the way through.
	if len(domains) != 1 || domains[0] != "*.example.co.uk" {
		t.Errorf("domains = %v, want the subdomain marker kept", domains)
	}
	if len(urls) != 2 {
		t.Errorf("urls = %v, want two", urls)
	}

	// The id from the listing is an address that answers.
	var aURL target
	for _, row := range rows {
		if row.Kind == "url" {
			aURL = row
			break
		}
	}
	if aURL.ID == 0 {
		t.Fatalf("no url target to delete: %v", rows)
	}
	if w := jobs(t, srv, http.MethodDelete, "/v1/jobs/uk/targets/"+itoa(int(aURL.ID)), ""); w.Code != http.StatusNoContent {
		t.Errorf("delete by id = %d: %s", w.Code, w.Body)
	}

	// And so is the value, for a caller mirroring `job rm uk -d example.co.uk`
	// who has only the domain they typed and would otherwise have to list the
	// targets first just to translate it.
	if w := jobs(t, srv, http.MethodDelete, "/v1/jobs/uk/targets/example.co.uk", ""); w.Code != http.StatusNoContent {
		t.Errorf("delete by value = %d: %s", w.Code, w.Body)
	}

	rows, domains, _ = export()
	if len(rows) != 1 {
		t.Errorf("targets = %v, want the one that was not deleted", rows)
	}
	if len(domains) != 0 {
		t.Errorf("domains = %v, want the domain gone", domains)
	}
	if w := jobs(t, srv, http.MethodDelete, "/v1/jobs/uk/targets/999999", ""); w.Code != http.StatusNotFound {
		t.Errorf("deleting a target that is not there = %d, want 404", w.Code)
	}
}

// A url target holds slashes, so deleting one by value only works if an escaped
// path segment survives routing. It is worth a test rather than an assumption,
// because the failure would be a 404 on a target that is plainly there.
func TestAURLTargetIsDeletedByItsEscapedValue(t *testing.T) {
	srv := newServer(t, nil)
	itemWithJob(t, srv, "vehicle")
	if w := jobs(t, srv, http.MethodPost, "/v1/jobs",
		`{"name":"uk","item":"vehicle","urls":[{"value":"https://www.example.co.uk/used/"}]}`); w.Code != http.StatusCreated {
		t.Fatal(w.Body.String())
	}

	escaped := url.PathEscape("https://www.example.co.uk/used/")
	if w := jobs(t, srv, http.MethodDelete, "/v1/jobs/uk/targets/"+escaped, ""); w.Code != http.StatusNoContent {
		t.Fatalf("delete = %d, want 204: %s", w.Code, w.Body)
	}

	w := jobs(t, srv, http.MethodGet, "/v1/jobs/uk/targets", "")
	if strings.Contains(w.Body.String(), "used") {
		t.Errorf("the target survived: %s", w.Body)
	}
}

// A bad target is the caller's mistake, and has to come back on the request that
// made it rather than as a crawl that fetches nothing.
func TestABadTargetIsRejected(t *testing.T) {
	srv := newServer(t, nil)
	itemWithJob(t, srv, "vehicle")
	if w := jobs(t, srv, http.MethodPost, "/v1/jobs", `{"name":"uk","item":"vehicle"}`); w.Code != http.StatusCreated {
		t.Fatal(w.Body.String())
	}

	for _, body := range []string{`{"domain":""}`, `{"url":"not a url"}`, `{}`} {
		if w := jobs(t, srv, http.MethodPost, "/v1/jobs/uk/targets", body); w.Code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400: %s", body, w.Code, w.Body)
		}
	}
}

// Allowed types are a set on the job. Excluding one is a property of a run, and
// accepting the request and then dropping it would show up as a crawl fetching
// exactly what it was told not to.
func TestTypesAreOneResource(t *testing.T) {
	srv := newServer(t, nil)
	itemWithJob(t, srv, "vehicle")
	if w := jobs(t, srv, http.MethodPost, "/v1/jobs", `{"name":"uk","item":"vehicle"}`); w.Code != http.StatusCreated {
		t.Fatal(w.Body.String())
	}

	w := jobs(t, srv, http.MethodPost, "/v1/jobs/uk/types", `{"types":["html","PDF"]}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", w.Code, w.Body)
	}
	// Stored lowercased, so "PDF" and "pdf" are not two restrictions.
	if body := w.Body.String(); !strings.Contains(body, `"pdf"`) || strings.Contains(body, `"PDF"`) {
		t.Errorf("types = %s, want them lowercased", body)
	}

	w = jobs(t, srv, http.MethodPost, "/v1/jobs/uk/types", `{"type":"pdf","exclude":true}`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("excluding = %d, want 400: %s", w.Code, w.Body)
	}
	// The error has to say where exclusion does work, or the caller is left
	// guessing at a capability that exists one route away.
	if !strings.Contains(w.Body.String(), "exclude_types") {
		t.Errorf("the error does not name the alternative: %s", w.Body)
	}

	if w := jobs(t, srv, http.MethodDelete, "/v1/jobs/uk/types/pdf", ""); w.Code != http.StatusNoContent {
		t.Errorf("delete = %d: %s", w.Code, w.Body)
	}
	if w := jobs(t, srv, http.MethodDelete, "/v1/jobs/uk/types/pdf", ""); w.Code != http.StatusNotFound {
		t.Errorf("deleting it twice = %d, want 404", w.Code)
	}
}

// Removing a job takes its frontier and leaves the item's corpus, because the
// next job over the same site should not have to refetch it.
func TestDeletingAJobKeepsThePages(t *testing.T) {
	srv := newServer(t, nil)
	itemWithJob(t, srv, "vehicle")
	if w := jobs(t, srv, http.MethodPost, "/v1/jobs", `{"name":"uk","item":"vehicle"}`); w.Code != http.StatusCreated {
		t.Fatal(w.Body.String())
	}

	if w := jobs(t, srv, http.MethodDelete, "/v1/jobs/uk", ""); w.Code != http.StatusNoContent {
		t.Fatalf("delete = %d: %s", w.Code, w.Body)
	}
	if w := jobs(t, srv, http.MethodGet, "/v1/jobs/uk", ""); w.Code != http.StatusNotFound {
		t.Errorf("the job survived its delete: %d", w.Code)
	}
	// The item is untouched, which is the whole point of the job being its own
	// noun.
	if w := do(t, srv, http.MethodGet, "/v1/items/vehicle", ""); w.Code != http.StatusOK {
		t.Errorf("deleting the job took the item: %d", w.Code)
	}

	// Asking for the pages is answered rather than silently ignored, since
	// scour deliberately keeps them.
	if w := jobs(t, srv, http.MethodPost, "/v1/jobs", `{"name":"uk","item":"vehicle"}`); w.Code != http.StatusCreated {
		t.Fatal(w.Body.String())
	}
	w := jobs(t, srv, http.MethodDelete, "/v1/jobs/uk?pages=true", "")
	if w.Code != http.StatusOK {
		t.Fatalf("delete with pages = %d, want an answer: %s", w.Code, w.Body)
	}
	if !strings.Contains(w.Body.String(), "kept") {
		t.Errorf("the answer does not say the pages were kept: %s", w.Body)
	}
}

// The frontier is the one read with no command behind it: a remote client has no
// crawl in front of it, so it gets a URL instead.
func TestTheFrontierIsRead(t *testing.T) {
	srv := newServer(t, nil)
	itemWithJob(t, srv, "vehicle")
	if w := jobs(t, srv, http.MethodPost, "/v1/jobs", `{"name":"uk","item":"vehicle"}`); w.Code != http.StatusCreated {
		t.Fatal(w.Body.String())
	}

	w := jobs(t, srv, http.MethodGet, "/v1/jobs/uk/frontier", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body)
	}
	body := decodeBody(t, w)
	// What is waiting is the job's own, which is exactly what does not carry
	// between two jobs of one item.
	if _, ok := body["queued"]; !ok {
		t.Errorf("the frontier does not say how much is waiting: %s", w.Body)
	}
	if body["item"] != "vehicle" {
		t.Errorf("item = %v, want the item whose corpus the urls are in", body["item"])
	}
}

// The sample is the first thing anyone fetches, so it has to be a config that
// actually applies rather than a shape to fill in.
func TestTheSampleConfigIsServedAndApplies(t *testing.T) {
	srv := newServer(t, nil)

	w := jobs(t, srv, http.MethodGet, "/v1/schema/job", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body)
	}
	if got := w.Header().Get("Content-Type"); !strings.Contains(got, "toml") {
		t.Errorf("Content-Type = %q", got)
	}
	if w.Body.String() != jobfile.Sample().Render() {
		t.Error("the served schema is not the sample the CLI prints")
	}

	// The same sample, as the JSON body this API takes, has to validate: a
	// starting point that the create path rejects teaches the wrong thing.
	body, err := json.Marshal(jobfile.Sample())
	if err != nil {
		t.Fatal(err)
	}
	if w := jobs(t, srv, http.MethodPost, "/v1/jobs?validate=true", string(body)); w.Code != http.StatusOK {
		t.Errorf("the sample does not validate: %d %s", w.Code, w.Body)
	}
}

// A name that is not there is the caller's business rather than the server's
// failure, on every route that takes one.
func TestMissingJobIsNotFound(t *testing.T) {
	srv := newServer(t, nil)

	tests := []struct{ method, path, body string }{
		{http.MethodGet, "/v1/jobs/nosuchjob", ""},
		{http.MethodGet, "/v1/jobs/nosuchjob?format=toml", ""},
		{http.MethodPatch, "/v1/jobs/nosuchjob", `{"depth":3}`},
		{http.MethodDelete, "/v1/jobs/nosuchjob", ""},
		{http.MethodGet, "/v1/jobs/nosuchjob/targets", ""},
		{http.MethodPost, "/v1/jobs/nosuchjob/targets", `{"domain":"example.com"}`},
		{http.MethodDelete, "/v1/jobs/nosuchjob/targets/1", ""},
		{http.MethodPost, "/v1/jobs/nosuchjob/types", `{"type":"html"}`},
		{http.MethodDelete, "/v1/jobs/nosuchjob/types/html", ""},
		{http.MethodGet, "/v1/jobs/nosuchjob/frontier", ""},
	}
	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			if w := jobs(t, srv, tt.method, tt.path, tt.body); w.Code != http.StatusNotFound {
				t.Errorf("status = %d, want 404: %s", w.Code, w.Body)
			}
		})
	}
}

// A body that is not the shape the route takes is rejected on the request that
// sent it, including a key nothing reads: a misspelled field that applies and
// does nothing is the whole failure mode of a config.
func TestJobBadRequests(t *testing.T) {
	srv := newServer(t, nil)
	itemWithJob(t, srv, "vehicle")

	tests := []struct{ name, method, path, body string }{
		{"malformed json", http.MethodPost, "/v1/jobs", `{"name":`},
		{"unknown field", http.MethodPost, "/v1/jobs", `{"name":"uk","item":"vehicle","max_page":5}`},
		{"unknown field on a target", http.MethodPost, "/v1/jobs/vehicle/targets", `{"domian":"example.com"}`},
		{"nothing to add", http.MethodPost, "/v1/jobs/vehicle/types", `{}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if w := jobs(t, srv, tt.method, tt.path, tt.body); w.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400: %s", w.Code, w.Body)
			}
		})
	}
}
