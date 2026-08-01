// SPDX-License-Identifier: GPL-3.0-or-later

package server

import (
	"net/http"
	"strconv"

	"github.com/rangertaha/scour/internal/store"
)

// runKey is the envelope field a run comes back in. Named because three
// handlers and the start response have to agree on it, and a reader keying on
// "run" would not notice one of them saying something else.
const runKey = "run"

// defaultRunLimit bounds a listing nobody asked to bound. A history grows
// without limit and almost every reader wants the recent end of it.
const defaultRunLimit = 50

// listRuns is every recent run, of every kind, newest first.
func (s *Server) listRuns(w http.ResponseWriter, r *http.Request) {
	runs, err := s.store.RecentRuns(r.Context(), limitOf(r, defaultRunLimit))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
}

// getRun is one run, whatever it belongs to. The id is the whole address, which
// is why this read is not scoped to a job the way `job log` is.
func (s *Server) getRun(w http.ResponseWriter, r *http.Request) {
	run, ok := s.run(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{runKey: run})
}

// runLog is the pages a run fetched.
//
// It is read from the pages rather than from a written log, for the reason the
// CLI's is: every question asked of a bad run is about pages, and a page
// belongs to the run that fetched it last.
func (s *Server) runLog(w http.ResponseWriter, r *http.Request) {
	run, ok := s.run(w, r)
	if !ok {
		return
	}
	pages, err := s.store.RunPages(r.Context(), run.ID, limitOf(r, defaultRunLimit))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	total, err := s.store.RunPageCount(r.Context(), run.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// Saying how many of the run's pages are still attributed to it, rather
	// than implying it fetched that many: an old run's log thins out as the
	// site is recrawled.
	writeJSON(w, http.StatusOK, map[string]any{
		runKey: run, "pages": pages, "attributed": total,
	})
}

// jobRuns is one job's history.
func (s *Server) jobRuns(w http.ResponseWriter, r *http.Request) {
	job, err := s.store.Job(r.Context(), r.PathValue("name"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	runs, err := s.store.Runs(r.Context(), job.ID, limitOf(r, defaultRunLimit))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
}

// startJobRun starts a crawl of one job, by the job's own name.
//
// The old route named an item and let the store find its job, which works only
// while an item has one. A job is what holds a frontier, so a job is what a run
// is a run of.
func (s *Server) startJobRun(w http.ResponseWriter, r *http.Request) {
	var req crawlRequest
	if r.ContentLength > 0 && !decode(w, r, &req) {
		return
	}
	job, err := s.store.Job(r.Context(), r.PathValue("name"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	named, err := s.store.ItemByID(r.Context(), job.ItemID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// Loaded whole, because the scorer is built from the item's properties and
	// aliases rather than from its row.
	item, err := s.store.ItemFull(r.Context(), named.Name)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	run, err := s.crawlRun(r.Context(), item, job, req)
	s.accepted(w, r, run, err)
}

// run reads the run named by the path, answering the request itself when there
// is not one.
func (s *Server) run(w http.ResponseWriter, r *http.Request) (*store.Run, bool) {
	id, err := strconv.ParseUint(r.PathValue("id"), 10, 64)
	if err != nil {
		s.badRequest(w, "run id must be a number")
		return nil, false
	}
	run, err := s.store.Run(r.Context(), uint(id))
	if err != nil {
		s.fail(w, r, err)
		return nil, false
	}
	return run, true
}

// limitOf reads ?limit=, falling back to a default. Zero is "no limit", which
// is what a caller dumping a history asks for deliberately.
func limitOf(r *http.Request, fallback int) int {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return fallback
	}
	return n
}
