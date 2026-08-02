// SPDX-License-Identifier: GPL-3.0-or-later

package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/rangertaha/scour/internal/jobfile"
	"github.com/rangertaha/scour/internal/normalise"
	"github.com/rangertaha/scour/internal/store"
)

// The envelope fields the job routes answer in, named for the same reason
// runKey is: a dozen handlers have to agree on them, and a client keying on
// "job" or "targets" would not notice one handler quietly saying something else.
// Spelling them once is what makes that a compile error rather than a field that
// silently stops appearing.
const (
	jobKey     = "job"
	itemKey    = "item"
	targetsKey = "targets"
	urlsKey    = "urls"
	errorKey   = "error"
)

// registerJobs adds the job resource to a mux.
//
// The routes are gathered here rather than in Handler because a job is the
// largest noun in the API and its dozen routes would be most of that function.
// Anything reachable under /v1/jobs belongs in this file, with two deliberate
// exceptions: a job's runs live in runs.go, because a run is its own resource
// with its own listing, and only its address hangs off the job.
func (s *Server) registerJobs(mux *http.ServeMux) {
	// Create and validate are one route because validation has to run the code
	// the create runs. A second implementation of the checks drifts from the
	// first, and the drift shows up as a config that validates and then fails to
	// apply, which is worse than having no validator.
	mux.HandleFunc("POST /v1/jobs", s.createJob)
	mux.HandleFunc("GET /v1/jobs", s.listJobs)
	mux.HandleFunc("GET /v1/jobs/{name}", s.getJob)
	mux.HandleFunc("PATCH /v1/jobs/{name}", s.patchJob)
	mux.HandleFunc("DELETE /v1/jobs/{name}", s.deleteJob)

	// A domain and a url are both targets, so both are one POST with a different
	// body field. The command line splits them into -d and -u to have two short
	// flags, which is a fact about typing rather than about the model.
	mux.HandleFunc("POST /v1/jobs/{name}/targets", s.addJobTargets)
	mux.HandleFunc("GET /v1/jobs/{name}/targets", s.exportJobTargets)
	mux.HandleFunc("DELETE /v1/jobs/{name}/targets/{id}", s.deleteJobTarget)

	mux.HandleFunc("POST /v1/jobs/{name}/types", s.addJobTypes)
	mux.HandleFunc("DELETE /v1/jobs/{name}/types/{type}", s.deleteJobType)

	mux.HandleFunc("GET /v1/jobs/{name}/frontier", s.jobFrontier)

	// The sample config, under /v1/schema rather than /v1/jobs, because it
	// describes the shape of a job rather than being one. Asking for it does not
	// need a job to exist, and it answers the same thing on an empty database.
	mux.HandleFunc("GET /v1/schema/job", s.jobSchema)
}

// createJob applies a whole job document, or checks one without applying it.
//
// The body is a [jobfile.File], the same document `scour job add -f` reads, so a
// config kept under version control can be applied over the wire without being
// translated on the way. That is also why there is no separate create-from-flags
// shape: `job add uk -i vehicle` is this document with only two fields set.
func (s *Server) createJob(w http.ResponseWriter, r *http.Request) {
	var f jobfile.File
	if !decode(w, r, &f) {
		return
	}

	// Everything a job document can be wrong about is reported at once rather
	// than one fault per request, because a caller fixing a config should not
	// have to make as many round trips as the file has mistakes.
	if problems := f.ValidateFields(); len(problems) > 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			errorKey:   fmt.Sprintf("%d %s with this job", len(problems), jobfile.Plural("problem", len(problems))),
			"problems": problems,
		})
		return
	}

	// ?validate=true stops here, before the store is touched at all. Not opening
	// a database is the point rather than an optimisation: a config is often
	// written for a machine that is not this one, so checking it must not depend
	// on the item existing here.
	if boolParam(r, "validate") {
		writeJSON(w, http.StatusOK, map[string]any{
			"valid": true, jobKey: f, targetsKey: len(f.Domains) + len(f.URLs),
		})
		return
	}

	ctx := r.Context()
	item, err := s.store.Item(ctx, f.Item)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	job, err := s.store.CreateJob(ctx, f.Name, item.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	// The whole document is applied, bounds included, which is why an absent
	// depth writes zero rather than being skipped: a config that dropped its
	// page budget has to be able to drop it again on the next machine. Editing
	// one bound without restating the rest is what PATCH is for.
	maxTime, err := f.TimeBound()
	if err != nil {
		s.badRequest(w, err.Error())
		return
	}
	policy := store.JobPolicy{Depth: &f.Depth, MaxPages: &f.MaxPages, MaxTime: &maxTime}
	if err := s.store.SetJobPolicy(ctx, job.ID, policy); err != nil {
		s.fail(w, r, err)
		return
	}

	if err := s.applyTargets(ctx, job.ID, f.Domains, f.URLs); err != nil {
		s.answer(w, r, err)
		return
	}
	for _, t := range f.Types {
		if err := s.store.AddContentType(ctx, job.ID, strings.ToLower(t)); err != nil {
			s.fail(w, r, err)
			return
		}
	}

	// Read back rather than returned from the create, so the caller sees the job
	// with its targets and types attached rather than the bare row the insert
	// made.
	full, err := s.store.Job(ctx, job.Name)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("Location", "/v1/jobs/"+full.Name)
	writeJSON(w, http.StatusCreated, map[string]any{jobKey: full, itemKey: item.Name})
}

// jobRow is one line of `scour job ls`, over the wire.
//
// It embeds the stored job rather than restating it, so a field added to a job
// appears here without anyone remembering to copy it. What is added on top is
// the four numbers the listing shows that the row itself cannot answer: which
// item it hunts, and how far it has got.
type jobRow struct {
	store.Job
	Item    string     `json:"item"`
	Queued  int        `json:"queued"`
	Visited int64      `json:"visited"`
	Records int64      `json:"records"`
	LastRun *time.Time `json:"last_run,omitempty"`
}

// listJobs is every job, or every job of one item.
func (s *Server) listJobs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var itemID uint
	if name := r.URL.Query().Get("item"); name != "" {
		item, err := s.store.Item(ctx, name)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		itemID = item.ID
	}

	jobs, err := s.store.Jobs(ctx, itemID)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	// One query for every job's last run rather than one per row, which is the
	// difference between two queries and two hundred on a database with a job
	// per site.
	ids := make([]uint, 0, len(jobs))
	for _, j := range jobs {
		ids = append(ids, j.ID)
	}
	last, err := s.store.LastRuns(ctx, ids)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	// Visited and records are the item's rather than the job's, and deliberately
	// so: both jobs of an item draw on one corpus, and a page already fetched is
	// not fetched again for the second job. Counting them per job would report
	// work that was never repeated as though it had not happened. Cached per
	// item because several jobs hunting one item is the reason jobs exist, and
	// the status query is the expensive one on this page.
	status := map[uint]*store.Status{}

	rows := make([]jobRow, 0, len(jobs))
	for _, j := range jobs {
		item, err := s.store.ItemByID(ctx, j.ItemID)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		// Queued is this job's own frontier: what it has left to hand out, which
		// is the number that differs between two jobs of one item.
		queued, err := s.store.QueueSize(ctx, j.ID)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		st, ok := status[j.ItemID]
		if !ok {
			if st, err = s.store.Status(ctx, j.ItemID); err != nil {
				s.fail(w, r, err)
				return
			}
			status[j.ItemID] = st
		}

		row := jobRow{Job: j, Item: item.Name, Queued: queued, Visited: st.Visited, Records: st.Matches}
		// From the run history rather than from a column on the job: a run says
		// when it started, and the column was never written to.
		if run, ok := last[j.ID]; ok {
			started := run.StartedAt
			row.LastRun = &started
		}
		rows = append(rows, row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": rows})
}

// getJob is everything about one job, as JSON or as the config file that would
// recreate it.
//
// The TOML form is what closes the round trip: a job assembled by a dozen
// requests over weeks can be fetched as a document, committed, and applied to a
// fresh machine with one POST to this same resource.
func (s *Server) getJob(w http.ResponseWriter, r *http.Request) {
	job, item, ok := s.job(w, r)
	if !ok {
		return
	}

	if r.URL.Query().Get("format") == "toml" {
		// Served as text rather than as a JSON string holding TOML, so `curl ...
		// > uk.toml` produces a file that applies. Wrapping it would make the
		// caller unescape a document before they could use it.
		w.Header().Set("Content-Type", "application/toml; charset=utf-8")
		writeText(w, jobfile.Of(job, item.Name).Render())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{jobKey: job, itemKey: item.Name})
}

// jobPatch is what a bound edit accepts.
//
// Every field is a pointer because absent and zero mean different things here:
// leaving out max_pages keeps the budget that was there, and sending zero
// removes it. That distinction is the whole of `scour job set`, and a plain int
// cannot carry it.
type jobPatch struct {
	Depth    *int    `json:"depth,omitempty"`
	MaxPages *int    `json:"max_pages,omitempty"`
	MaxTime  *string `json:"max_time,omitempty"`
}

// patchJob overwrites the bounds given and leaves the rest.
func (s *Server) patchJob(w http.ResponseWriter, r *http.Request) {
	var body jobPatch
	if !decode(w, r, &body) {
		return
	}
	if body.Depth == nil && body.MaxPages == nil && body.MaxTime == nil {
		s.badRequest(w, "nothing to set: name a bound, such as depth, max_pages or max_time")
		return
	}

	// The store refuses a negative bound too, but as a plain error, which would
	// reach the caller as a 500 and read as scour having broken. A bound below
	// zero is a request the caller can fix, so it is caught here and named.
	if body.Depth != nil && *body.Depth < 0 {
		s.badRequest(w, fmt.Sprintf("depth is %d: it counts links out from a target, so it cannot be negative", *body.Depth))
		return
	}
	if body.MaxPages != nil && *body.MaxPages < 0 {
		s.badRequest(w, fmt.Sprintf("max_pages is %d: use 0 for no bound", *body.MaxPages))
		return
	}

	policy := store.JobPolicy{Depth: body.Depth, MaxPages: body.MaxPages}
	if body.MaxTime != nil {
		// A string rather than a number of nanoseconds, matching the run request
		// and the config file: "30m" is what a person writes, and a caller that
		// has to convert it first will convert it wrongly at least once.
		d, err := time.ParseDuration(*body.MaxTime)
		if err != nil {
			s.badRequest(w, fmt.Sprintf("max_time %q is not a duration such as \"30m\"", *body.MaxTime))
			return
		}
		if d < 0 {
			s.badRequest(w, fmt.Sprintf("max_time %q is negative: use 0 for no bound", *body.MaxTime))
			return
		}
		policy.MaxTime = &d
	}

	job, _, ok := s.job(w, r)
	if !ok {
		return
	}
	if err := s.store.SetJobPolicy(r.Context(), job.ID, policy); err != nil {
		s.fail(w, r, err)
		return
	}

	after, err := s.store.Job(r.Context(), job.Name)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{jobKey: after})
}

// deleteJob removes a job and its frontier, leaving the item, its records and
// its model alone.
func (s *Server) deleteJob(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.store.DeleteJob(r.Context(), name); err != nil {
		s.fail(w, r, err)
		return
	}

	// The cached pages stay, whatever ?pages= asked for. They belong to the
	// item's corpus, so the next job over the same site should not refetch them,
	// and removing a job is a common thing to do where refetching a site is an
	// expensive one. A caller who asked for them is told they were kept and
	// where to go instead, which a 204 has nowhere to put; a caller who did not
	// ask gets the same empty answer the item route gives.
	if boolParam(r, "pages") {
		writeJSON(w, http.StatusOK, map[string]any{
			jobKey:  name,
			"pages": "kept: the cached pages belong to the item's corpus, and go with DELETE /v1/items/{name}",
		})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// targetRequest is what adding targets accepts.
//
// Singular and plural both, because one endpoint serves two commands: `job add
// uk -d example.com` sends one, and `job import uk --domains domains.txt` sends
// the file. A list of a thousand domains as a thousand requests would be a
// thousand transactions to do one import.
type targetRequest struct {
	Domain     string   `json:"domain,omitempty"`
	URL        string   `json:"url,omitempty"`
	Domains    []string `json:"domains,omitempty"`
	URLs       []string `json:"urls,omitempty"`
	Subdomains bool     `json:"subdomains,omitempty"`
	Depth      int      `json:"depth,omitempty"`
}

// addJobTargets adds domains, urls, or both.
func (s *Server) addJobTargets(w http.ResponseWriter, r *http.Request) {
	var body targetRequest
	if !decode(w, r, &body) {
		return
	}

	domains := body.Domains
	if body.Domain != "" {
		domains = append(domains, body.Domain)
	}
	urls := body.URLs
	if body.URL != "" {
		urls = append(urls, body.URL)
	}
	if len(domains) == 0 && len(urls) == 0 {
		s.badRequest(w, "nothing to add: send a domain or a url")
		return
	}

	job, _, ok := s.job(w, r)
	if !ok {
		return
	}

	wanted := make([]jobfile.DomainTarget, 0, len(domains))
	for _, d := range domains {
		wanted = append(wanted, jobfile.DomainTarget{Value: d, Subdomains: body.Subdomains, Depth: body.Depth})
	}
	pages := make([]jobfile.URLTarget, 0, len(urls))
	for _, u := range urls {
		pages = append(pages, jobfile.URLTarget{Value: u, Depth: body.Depth})
	}
	if err := s.applyTargets(r.Context(), job.ID, wanted, pages); err != nil {
		s.answer(w, r, err)
		return
	}

	// The job's whole target list rather than the additions, because adding is
	// idempotent: a caller who sends the same domain twice needs to see what the
	// job has, not that their second request also "added" one.
	after, err := s.store.TargetsFor(r.Context(), job.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{targetsKey: after})
}

// exportJobTargets writes a job's targets out.
//
// Two shapes, because they answer different questions. The rows carry the ids
// that DELETE takes, and the two lists are the form `scour import` reads and
// this endpoint's own POST accepts, so what comes out here goes back in
// somewhere else without being reshaped.
func (s *Server) exportJobTargets(w http.ResponseWriter, r *http.Request) {
	job, _, ok := s.job(w, r)
	if !ok {
		return
	}
	targets, err := s.store.TargetsFor(r.Context(), job.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	domains, urls := []string{}, []string{}
	for _, t := range targets {
		switch t.Kind {
		case store.TargetURL:
			urls = append(urls, t.Value)
		case store.TargetDomain:
			// Written the way import reads it back, so a subdomain target does
			// not quietly narrow to the bare host on the way through.
			if t.Subdomains {
				domains = append(domains, "*."+t.Value)
				continue
			}
			domains = append(domains, t.Value)
		}
	}
	sort.Strings(domains)
	sort.Strings(urls)

	writeJSON(w, http.StatusOK, map[string]any{
		targetsKey: targets, "domains": domains, urlsKey: urls,
	})
}

// deleteJobTarget removes one target.
//
// The id may be the target's number or its value, because the two callers know
// different things. A client that listed the targets has the ids; a person
// mirroring `scour job rm uk -d example.co.uk` has only the domain they typed,
// and making them list the targets first to translate it would be a round trip
// spent on bookkeeping. A number is always the id: no domain or url normalises
// to bare digits, so there is nothing for the two forms to collide over.
func (s *Server) deleteJobTarget(w http.ResponseWriter, r *http.Request) {
	job, _, ok := s.job(w, r)
	if !ok {
		return
	}
	raw := r.PathValue("id")

	value := raw
	if id, err := strconv.ParseUint(raw, 10, 64); err == nil {
		targets, err := s.store.TargetsFor(r.Context(), job.ID)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		value = ""
		for _, t := range targets {
			if uint64(t.ID) == id {
				value = t.Value
				break
			}
		}
		if value == "" {
			writeError(w, http.StatusNotFound, fmt.Sprintf("job %s has no target %d", job.Name, id))
			return
		}
	}

	if err := s.store.DeleteTarget(r.Context(), job.ID, value); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// typeRequest is what allowing a content type accepts.
type typeRequest struct {
	Type  string   `json:"type,omitempty"`
	Types []string `json:"types,omitempty"`
	// Exclude asks for the type to be forbidden rather than allowed. See
	// addJobTypes for why it is accepted and then refused.
	Exclude bool `json:"exclude,omitempty"`
}

// addJobTypes restricts a job to the content types given.
func (s *Server) addJobTypes(w http.ResponseWriter, r *http.Request) {
	var body typeRequest
	if !decode(w, r, &body) {
		return
	}

	types := body.Types
	if body.Type != "" {
		types = append(types, body.Type)
	}
	if len(types) == 0 {
		s.badRequest(w, "nothing to add: send a type such as html or pdf")
		return
	}

	// A job stores the types it allows and has nowhere to record one it forbids:
	// exclusion is a property of a run, set with exclude_types when the run is
	// started, and it is not stored between runs. The request is refused rather
	// than accepted and dropped, and it names where the thing the caller wanted
	// does work, because an exclusion silently ignored would show up as a crawl
	// fetching exactly what it was told not to.
	if body.Exclude {
		s.badRequest(w, "exclude is not stored on a job: pass exclude_types to POST /v1/jobs/{name}/runs to exclude a type from one run")
		return
	}

	job, _, ok := s.job(w, r)
	if !ok {
		return
	}
	for _, t := range types {
		if err := s.store.AddContentType(r.Context(), job.ID, strings.ToLower(t)); err != nil {
			s.fail(w, r, err)
			return
		}
	}

	after, err := s.store.ContentTypesFor(r.Context(), job.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"types": after})
}

// deleteJobType stops a job allowing a content type.
func (s *Server) deleteJobType(w http.ResponseWriter, r *http.Request) {
	job, _, ok := s.job(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteContentType(r.Context(), job.ID, r.PathValue("type")); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// jobFrontier is what a job has fetched, ranked, and how much it has left.
//
// There is no command for this. The command line shows the frontier as the
// ranked output of a crawl while it happens, and a remote client has no crawl in
// front of it, so it gets a URL instead.
//
// The URLs are the item's, because that is where a fetch is recorded and two
// jobs of one item share the corpus they build. The count of what is waiting is
// the job's own, because that is exactly what does not carry across: a job holds
// its own frontier, and one of them can be paused while the other runs.
func (s *Server) jobFrontier(w http.ResponseWriter, r *http.Request) {
	job, item, ok := s.job(w, r)
	if !ok {
		return
	}

	rows, err := s.store.FetchedURLs(r.Context(), item.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	fetched := len(rows)
	if limit := limitOf(r, 0); limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}

	queued, err := s.store.QueueSize(r.Context(), job.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// Both totals are reported next to a possibly truncated list, so a caller
	// that asked for ten urls can still tell whether it is looking at the whole
	// crawl or the top of it.
	writeJSON(w, http.StatusOK, map[string]any{
		urlsKey: rows, "fetched": fetched, "queued": queued, itemKey: item.Name,
	})
}

// jobSchema is the commented sample config, the same one `scour job config`
// prints.
//
// It is a real config rather than an empty shape, so it can be saved, edited and
// POSTed back without anything in between, and it is the one job route that
// answers on a database with nothing in it.
func (s *Server) jobSchema(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/toml; charset=utf-8")
	writeText(w, jobfile.Sample().Render())
}

// job reads the job named by the path and the item it hunts, answering the
// request itself when either is missing.
//
// Both together because almost every job handler needs both: a job row carries
// an item id, and a caller holding a name should not have to make a second
// request to turn it into one.
func (s *Server) job(w http.ResponseWriter, r *http.Request) (*store.Job, *store.Item, bool) {
	job, err := s.store.Job(r.Context(), r.PathValue("name"))
	if err != nil {
		s.fail(w, r, err)
		return nil, nil, false
	}
	item, err := s.store.ItemByID(r.Context(), job.ItemID)
	if err != nil {
		s.fail(w, r, err)
		return nil, nil, false
	}
	return job, item, true
}

// applyTargets normalises and stores targets, rejecting the whole request if any
// of them is unusable.
//
// Normalising here rather than in the store is what keeps the API and the
// command line agreeing on what counts as the same target: example.com,
// www.example.com and https://example.com/ are one domain, and a target added
// over HTTP that skipped this would sit beside the CLI's as a second row that
// crawls the same site twice.
func (s *Server) applyTargets(ctx context.Context, jobID uint, domains []jobfile.DomainTarget, urls []jobfile.URLTarget) error {
	for _, d := range domains {
		host, err := normalise.Domain(d.Value)
		if err != nil {
			return ErrInvalid{msg: err.Error()}
		}
		if err := s.store.AddTarget(ctx, jobID, store.TargetDomain, host, d.Subdomains, d.Depth); err != nil {
			return err
		}
	}
	for _, u := range urls {
		normalised, err := normalise.URL(u.Value)
		if err != nil {
			return ErrInvalid{msg: err.Error()}
		}
		if err := s.store.AddTarget(ctx, jobID, store.TargetURL, normalised, false, u.Depth); err != nil {
			return err
		}
	}
	return nil
}

// answer writes an error that may be the caller's fault or the server's.
//
// accepted does this for work that returns a run; this is the same split for the
// handlers that return nothing, so a bad domain is a 400 in both places rather
// than a 400 on one route and a 500 on the next.
func (s *Server) answer(w http.ResponseWriter, r *http.Request, err error) {
	var bad ErrInvalid
	if errors.As(err, &bad) {
		s.badRequest(w, bad.Error())
		return
	}
	s.fail(w, r, err)
}

// writeText writes a document that is not JSON.
//
// The error is logged rather than returned because there is nowhere left to
// report it: the status and the headers have gone, and a caller whose connection
// died is not listening for a 500 either.
func writeText(w http.ResponseWriter, body string) {
	if _, err := io.WriteString(w, body); err != nil {
		slog.Debug("could not write response", "err", err)
	}
}
