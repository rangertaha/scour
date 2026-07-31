// SPDX-License-Identifier: GPL-3.0-or-later

// Package server exposes scour over HTTP.
//
// It is the same scour the command line drives, over a socket: one database,
// one set of models, one cache. An entity defined through the API is the entity
// the CLI sees, because both go through the same store rather than through each
// other.
//
// The API is deliberately small. Everything that reads is a plain GET that
// answers immediately; everything that crawls or trains is a job, because those
// run for minutes and an HTTP request that blocks for minutes is a request that
// times out somewhere in the middle and leaves the caller unable to find out
// what happened.
package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/rangertaha/scour/internal/cache"
	"github.com/rangertaha/scour/internal/config"
	"github.com/rangertaha/scour/internal/store"
)

// Server holds what the handlers need.
type Server struct {
	cfg   config.Config
	store *store.Store
	pages cache.Store
	jobs  *Jobs
	auth  *Auth
	stats *Metrics
}

// New builds a server.
func New(cfg config.Config, s *store.Store, pages cache.Store) (*Server, error) {
	auth, err := NewAuth(cfg.Server.TokenFile)
	if err != nil {
		return nil, err
	}

	return &Server{
		cfg:   cfg,
		store: s,
		pages: pages,
		jobs:  NewJobs(),
		auth:  auth,
		stats: NewMetrics(),
	}, nil
}

// Jobs exposes the job manager, so a caller can wait for work to finish before
// shutting down.
func (s *Server) Jobs() *Jobs { return s.jobs }

// Handler returns the routed, authenticated handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Liveness carries no data and answers before auth, so a load balancer or
	// a systemd readiness check does not need a credential to ask whether the
	// process is up.
	mux.HandleFunc("GET /healthz", s.health)

	mux.HandleFunc("GET /v1/entities", s.listEntities)
	mux.HandleFunc("POST /v1/entities", s.createEntity)
	mux.HandleFunc("GET /v1/entities/{name}", s.getEntity)
	mux.HandleFunc("DELETE /v1/entities/{name}", s.deleteEntity)

	mux.HandleFunc("GET /v1/entities/{name}/frontier", s.frontier)
	mux.HandleFunc("GET /v1/entities/{name}/rules", s.rules)
	mux.HandleFunc("GET /v1/entities/{name}/records", s.records)
	mux.HandleFunc("POST /v1/entities/{name}/records/{id}/label", s.label)

	mux.HandleFunc("POST /v1/entities/{name}/crawl", s.startCrawl)
	mux.HandleFunc("POST /v1/entities/{name}/train", s.startTrain)

	mux.HandleFunc("GET /v1/jobs", s.listJobs)
	mux.HandleFunc("GET /v1/jobs/{id}", s.getJob)

	if path := s.cfg.Server.Metrics; path != "" {
		mux.HandleFunc("GET "+path, s.metrics)
	}

	// The same tools the stdio server exposes, so an agent can attach to a
	// running service instead of spawning its own process. Both views share one
	// database, so they are the same scour.
	if s.cfg.Server.MCP {
		mux.Handle("/mcp", s.MCPHandler())
		mux.Handle("/mcp/", s.MCPHandler())
	}

	// Order matters. Metrics is outermost so it owns the recorder and counts
	// every request, including the ones auth rejects and the ones that 404.
	// Logging sits inside it, reusing that recorder, and outside auth so a
	// rejected request is still a line in the log rather than a silent 401.
	return s.stats.Middleware(Logging(s.auth.Middleware(mux)))
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

func (s *Server) listEntities(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.Entities(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"entities": rows})
}

// entityRequest is what creating or extending an entity accepts. Every field is
// optional and additive, mirroring `scour add`: the same request run twice
// changes nothing the second time.
type entityRequest struct {
	Name       string   `json:"name"`
	Aliases    []string `json:"aliases,omitempty"`
	Domains    []string `json:"domains,omitempty"`
	URLs       []string `json:"urls,omitempty"`
	Types      []string `json:"types,omitempty"`
	Template   string   `json:"template,omitempty"`
	Subdomains bool     `json:"subdomains,omitempty"`
	Depth      int      `json:"depth,omitempty"`
	Properties []struct {
		Name        string   `json:"name"`
		Type        string   `json:"type,omitempty"`
		Example     string   `json:"example,omitempty"`
		Description string   `json:"description,omitempty"`
		Aliases     []string `json:"aliases,omitempty"`
	} `json:"properties,omitempty"`
}

func (s *Server) createEntity(w http.ResponseWriter, r *http.Request) {
	var req entityRequest
	if !decode(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		s.badRequest(w, "name is required")
		return
	}

	ctx := r.Context()
	entity, err := s.store.CreateEntity(ctx, req.Name)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	if req.Template != "" {
		if err := applyTemplate(ctx, s.store, entity.ID, req.Template); err != nil {
			s.fail(w, r, err)
			return
		}
	}

	for _, alias := range req.Aliases {
		if err := s.store.AddAlias(ctx, entity.ID, alias); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	for _, d := range req.Domains {
		if err := s.store.AddTarget(ctx, entity.ID, store.TargetDomain, d, req.Subdomains, req.Depth); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	for _, u := range req.URLs {
		if err := s.store.AddTarget(ctx, entity.ID, store.TargetURL, u, req.Subdomains, req.Depth); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	for _, t := range req.Types {
		if err := s.store.AddContentType(ctx, entity.ID, strings.ToLower(t)); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	for _, p := range req.Properties {
		if err := s.store.AddPropertyDetail(ctx, entity.ID, store.PropertyDetail{
			Name: p.Name, Type: p.Type, Example: p.Example, Description: p.Description}); err != nil {
			s.fail(w, r, err)
			return
		}
		for _, alias := range p.Aliases {
			if err := s.store.AddPropertyAlias(ctx, entity.ID, "", p.Name, alias); err != nil {
				s.fail(w, r, err)
				return
			}
		}
	}

	full, err := s.store.EntityFull(ctx, entity.Name)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, full)
}

func (s *Server) getEntity(w http.ResponseWriter, r *http.Request) {
	entity, err := s.store.EntityFull(r.Context(), r.PathValue("name"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, entity)
}

func (s *Server) deleteEntity(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteEntity(r.Context(), r.PathValue("name")); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) frontier(w http.ResponseWriter, r *http.Request) {
	entity, err := s.store.Entity(r.Context(), r.PathValue("name"))
	if err != nil {
		s.fail(w, r, err)
		return
	}

	rows, err := s.store.FetchedURLs(r.Context(), entity.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if limit := intParam(r, "limit"); limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	writeJSON(w, http.StatusOK, map[string]any{"urls": rows})
}

func (s *Server) rules(w http.ResponseWriter, r *http.Request) {
	entity, err := s.store.Entity(r.Context(), r.PathValue("name"))
	if err != nil {
		s.fail(w, r, err)
		return
	}

	rows, err := s.store.Rules(r.Context(), entity.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rules": rows})
}

func (s *Server) records(w http.ResponseWriter, r *http.Request) {
	entity, err := s.store.Entity(r.Context(), r.PathValue("name"))
	if err != nil {
		s.fail(w, r, err)
		return
	}

	query := store.RecordQuery{
		Limit:         intParam(r, "limit"),
		Formats:       r.URL.Query()["type"],
		ExcludeFormat: r.URL.Query()["exclude_type"],
	}
	if v := r.URL.Query().Get("confidence"); v != "" {
		c, err := strconv.ParseFloat(v, 64)
		if err != nil {
			s.badRequest(w, fmt.Sprintf("confidence %q is not a number", v))
			return
		}
		query.MinConfidence = c
	}
	if v := r.URL.Query().Get("label"); v != "" {
		query.Label = store.Label(v)
	}

	rows, total, err := s.store.SearchRecords(r.Context(), entity.ID, query)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"records": rows, "total": total})
}

func (s *Server) label(w http.ResponseWriter, r *http.Request) {
	entity, err := s.store.Entity(r.Context(), r.PathValue("name"))
	if err != nil {
		s.fail(w, r, err)
		return
	}

	id, err := strconv.ParseUint(r.PathValue("id"), 10, 64)
	if err != nil {
		s.badRequest(w, "record id must be a number")
		return
	}

	var body struct {
		Label string `json:"label"`
	}
	if !decode(w, r, &body) {
		return
	}

	label := store.Label(strings.ToLower(strings.TrimSpace(body.Label)))
	switch label {
	case store.Valid, store.Invalid, store.Unlabelled:
	default:
		s.badRequest(w, fmt.Sprintf("label must be valid, invalid or unlabelled, got %q", body.Label))
		return
	}

	n, err := s.store.LabelRecords(r.Context(), entity.ID, []uint{uint(id)}, label)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if n == 0 {
		writeError(w, http.StatusNotFound, "no such record for this entity")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "label": label})
}

// fail turns a store error into the right status.
//
// A missing entity is a 404 rather than a 500, because the caller asked for
// something that is not there, which is their business rather than the
// server's failure.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case r.Context().Err() != nil:
		// The client gave up. Nothing can be written to a closed connection,
		// and logging it as a server error would be misleading.
		slog.Debug("request cancelled", "path", r.URL.Path)
	default:
		slog.Error("request failed", "path", r.URL.Path, "err", err)
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}

func (s *Server) badRequest(w http.ResponseWriter, msg string) {
	writeError(w, http.StatusBadRequest, msg)
}

// decode reads a JSON body, reporting a malformed one to the caller.
func decode(w http.ResponseWriter, r *http.Request, into any) bool {
	// Bounded because an unbounded body is a way to exhaust memory with a
	// single request.
	const maxBody = 4 << 20

	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		slog.Debug("could not write response", "err", err)
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": msg})
}

func intParam(r *http.Request, name string) int {
	v, err := strconv.Atoi(r.URL.Query().Get(name))
	if err != nil || v < 0 {
		return 0
	}
	return v
}

// Timeouts are set on the server rather than left to the caller: a socket that
// opens and never sends is a slow way to run a process out of file
// descriptors.
const (
	readHeaderTimeout = 10 * time.Second
	idleTimeout       = 2 * time.Minute
)

// HTTPServer wraps the handler in a configured [http.Server].
func (s *Server) HTTPServer(handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              s.cfg.Server.Listen,
		Handler:           handler,
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       idleTimeout,
	}
}
