// SPDX-License-Identifier: GPL-3.0-or-later

package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/rangertaha/scour/internal/content"
	"github.com/rangertaha/scour/internal/crawl"
	"github.com/rangertaha/scour/internal/defaults"
	"github.com/rangertaha/scour/internal/store"
	"github.com/rangertaha/scour/internal/train"
)

// crawlRequest mirrors the flags of `scour crawl`.
type crawlRequest struct {
	Depth       int      `json:"depth,omitempty"`
	MaxPages    int      `json:"max_pages,omitempty"`
	MaxTime     string   `json:"max_time,omitempty"`
	Types       []string `json:"types,omitempty"`
	ExcludeType []string `json:"exclude_types,omitempty"`
	Browser     string   `json:"browser,omitempty"`
	Reset       bool     `json:"reset,omitempty"`
}

// ErrInvalid is a request the caller can fix, as opposed to a failure on this
// side. HTTP turns it into a 400 and MCP reports it to the agent; both need to
// tell those apart.
type ErrInvalid struct{ msg string }

func (e ErrInvalid) Error() string { return e.msg }

func invalid(format string, args ...any) error {
	return ErrInvalid{msg: fmt.Sprintf(format, args...)}
}

// crawlJob validates a crawl and starts it.
//
// Everything that can be rejected is rejected before the job exists, so a bad
// request comes back on the call that made it rather than as a job that fails
// a minute later somewhere the caller has to go looking.
func (s *Server) crawlJob(ctx context.Context, name string, req crawlRequest) (*Job, error) {
	entity, err := s.store.EntityFull(ctx, name)
	if err != nil {
		return nil, err
	}
	if len(entity.Targets) == 0 {
		return nil, invalid("entity %s has no targets: add a domain or url first", name)
	}

	allow := req.Types
	if len(allow) == 0 {
		for _, t := range entity.ContentTypes {
			allow = append(allow, t.Type)
		}
	}
	if len(allow) == 0 {
		allow = s.cfg.Crawl.ContentTypes
	}
	types, err := content.New(allow, req.ExcludeType)
	if err != nil {
		return nil, ErrInvalid{msg: err.Error()}
	}

	scorer, _, err := train.Scorer(s.cfg, entity)
	if err != nil {
		return nil, ErrInvalid{msg: err.Error()}
	}
	scorer, _, err = train.ChainScorer(ctx, s.store, entity, scorer)
	if err != nil {
		return nil, err
	}

	if req.Reset {
		if err := s.store.ResetFrontier(ctx, entity.ID); err != nil {
			return nil, err
		}
	}

	var maxTime time.Duration
	if req.MaxTime != "" {
		maxTime, err = time.ParseDuration(req.MaxTime)
		if err != nil {
			return nil, invalid("max_time %q is not a duration such as \"30m\"", req.MaxTime)
		}
	}

	depth := req.Depth
	if depth <= 0 {
		depth = s.cfg.Crawl.Depth
	}

	return s.jobs.Start("crawl", entity.Name, func(jobCtx context.Context) (any, error) {
		crawler := crawl.New(s.cfg, s.store, s.pages)
		return crawler.Run(jobCtx, crawl.Options{
			Entity:  entity,
			Targets: entity.Targets,
			Types:   types,
			Depth:   depth,
			Limit:   req.MaxPages,
			MaxTime: maxTime,
			Browser: req.Browser,
			Scorer:  scorer,
		})
	})
}

// trainRequest mirrors the flags of `scour train`.
type trainRequest struct {
	Limit   int      `json:"limit,omitempty"`
	Types   []string `json:"types,omitempty"`
	NoChain bool     `json:"no_chain,omitempty"`
}

// trainJob validates a training run and starts it.
func (s *Server) trainJob(ctx context.Context, name string, req trainRequest) (*Job, error) {
	entity, err := s.store.EntityFull(ctx, name)
	if err != nil {
		return nil, err
	}

	var types *content.Set
	if len(req.Types) > 0 {
		types, err = content.New(req.Types, nil)
		if err != nil {
			return nil, ErrInvalid{msg: err.Error()}
		}
	}

	return s.jobs.Start("train", entity.Name, func(jobCtx context.Context) (any, error) {
		trainer := train.New(s.cfg, s.store, s.pages)
		return trainer.Run(jobCtx, entity, train.Options{
			Limit:   req.Limit,
			Types:   types,
			NoChain: req.NoChain,
		})
	})
}

func (s *Server) startCrawl(w http.ResponseWriter, r *http.Request) {
	var req crawlRequest
	if r.ContentLength > 0 && !decode(w, r, &req) {
		return
	}
	job, err := s.crawlJob(r.Context(), r.PathValue("name"), req)
	s.accepted(w, r, job, err)
}

func (s *Server) startTrain(w http.ResponseWriter, r *http.Request) {
	var req trainRequest
	if r.ContentLength > 0 && !decode(w, r, &req) {
		return
	}
	job, err := s.trainJob(r.Context(), r.PathValue("name"), req)
	s.accepted(w, r, job, err)
}

// accepted writes the response for a job that was asked for.
func (s *Server) accepted(w http.ResponseWriter, r *http.Request, job *Job, err error) {
	var busy ErrBusy
	var bad ErrInvalid

	switch {
	case errors.As(err, &busy):
		// Conflict rather than a failure: the caller asked for something
		// reasonable that is already happening, and the id lets them watch the
		// one that is.
		writeJSON(w, http.StatusConflict, map[string]any{"error": busy.Error(), "job": busy.ID})
	case errors.As(err, &bad):
		s.badRequest(w, bad.Error())
	case err != nil:
		s.fail(w, r, err)
	default:
		writeJSON(w, http.StatusAccepted, job)
	}
}

func (s *Server) listJobs(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"jobs": s.jobs.List()})
}

func (s *Server) getJob(w http.ResponseWriter, r *http.Request) {
	job, ok := s.jobs.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "no such job")
		return
	}
	writeJSON(w, http.StatusOK, job)
}

// applyTemplate copies a shipped schema onto an entity, as `scour add
// --template` does.
func applyTemplate(ctx context.Context, s *store.Store, entityID uint, name string) error {
	schema, err := defaults.Schema(name)
	if err != nil {
		return ErrInvalid{msg: err.Error()}
	}

	fields := schema
	if len(schema) == 1 && len(schema[0].Props) > 0 {
		fields = schema[0].Props
	}

	for _, p := range fields {
		example := ""
		if len(p.Examples) > 0 {
			example = p.Examples[0]
		}
		if err := s.AddPropertyDetail(ctx, entityID, store.PropertyDetail{
			Name: p.Name, Type: string(p.Type), Example: example, Description: p.Description}); err != nil {
			return err
		}
		for _, alias := range p.Aliases {
			if err := s.AddPropertyAlias(ctx, entityID, "", p.Name, strings.TrimSpace(alias)); err != nil {
				return err
			}
		}
	}
	return nil
}
