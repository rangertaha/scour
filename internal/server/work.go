// SPDX-License-Identifier: GPL-3.0-or-later

package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/rangertaha/scour/internal/content"
	"github.com/rangertaha/scour/internal/crawl"
	"github.com/rangertaha/scour/internal/defaults"
	"github.com/rangertaha/scour/internal/store"
	"github.com/rangertaha/scour/internal/train"
)

// crawlRequest mirrors the flags of `scour run`.
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
func (s *Server) crawlJob(ctx context.Context, name string, req crawlRequest) (*store.Run, error) {
	item, err := s.store.ItemFull(ctx, name)
	if err != nil {
		return nil, err
	}
	// An unnamed crawl of an item means the job named after it, created on the
	// first crawl so a caller never has to name one.
	job, err := s.store.JobForItem(ctx, item)
	if err != nil {
		return nil, err
	}
	return s.crawlRun(ctx, item, job, req)
}

// crawlRun starts a crawl of one named job.
//
// The job is passed in rather than found, because an item can have several and
// finding one by its item would start whichever the store happened to return.
// That is the whole reason a run hangs off a job in the API: the job is what
// holds the frontier, so the job is what a run is a run of.
func (s *Server) crawlRun(ctx context.Context, item *store.Item, job *store.Job, req crawlRequest) (*store.Run, error) {
	if len(job.Targets) == 0 {
		return nil, invalid("job %s has no targets: add a domain or url first", job.Name)
	}

	allow := req.Types
	if len(allow) == 0 {
		for _, t := range job.ContentTypes {
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

	scorer, _, err := train.Scorer(s.cfg, item)
	if err != nil {
		return nil, ErrInvalid{msg: err.Error()}
	}
	scorer, _, err = train.ChainScorer(ctx, s.store, item, scorer)
	if err != nil {
		return nil, err
	}

	if req.Reset {
		if err := s.store.RecrawlJob(ctx, item.ID, job.ID); err != nil {
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

	// The run opens before the work so its id can be handed back: a caller who
	// started a crawl needs the address of the thing they started, and waiting
	// for the goroutine to open one would mean answering with nothing to poll.
	run, err := s.store.StartRun(ctx, job.ID, item.ID)
	if err != nil {
		return nil, err
	}

	_, err = s.jobs.Start("crawl", item.Name, func(jobCtx context.Context) (any, error) {
		crawler := crawl.New(s.cfg, s.store, s.pages)
		result, err := crawler.Run(jobCtx, crawl.Options{
			Item:    item,
			Job:     job,
			RunID:   run.ID,
			Targets: job.Targets,
			Types:   types,
			Depth:   depth,
			Limit:   req.MaxPages,
			MaxTime: maxTime,
			Browser: req.Browser,
			Scorer:  scorer,
		})
		if finErr := s.store.FinishRun(jobCtx, run.ID, result.Finished(err)); finErr != nil {
			slog.Warn("could not record the run", "job", job.Name, "err", finErr)
		}
		return result, err
	})
	if err != nil {
		// The slot was taken, so this run never began.
		if derr := s.store.DeleteRun(ctx, run.ID); derr != nil {
			slog.Warn("could not remove a run that never started", "run", run.ID, "err", derr)
		}
		return nil, err
	}
	return run, nil
}

// trainRequest mirrors the flags of `scour model train`.
type trainRequest struct {
	Limit   int      `json:"limit,omitempty"`
	Types   []string `json:"types,omitempty"`
	NoChain bool     `json:"no_chain,omitempty"`
}

// trainJob validates a training run and starts it.
func (s *Server) trainJob(ctx context.Context, name string, req trainRequest) (*store.Run, error) {
	item, err := s.store.ItemFull(ctx, name)
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

	run, err := s.store.StartTrainingRun(ctx, item.ID)
	if err != nil {
		return nil, err
	}

	_, err = s.jobs.Start("train", item.Name, func(jobCtx context.Context) (any, error) {
		trainer := train.New(s.cfg, s.store, s.pages)
		return trainer.Run(jobCtx, item, train.Options{
			Limit:   req.Limit,
			Types:   types,
			NoChain: req.NoChain,
			RunID:   run.ID,
		})
	})
	if err != nil {
		if derr := s.store.DeleteRun(ctx, run.ID); derr != nil {
			slog.Warn("could not remove a run that never started", "run", run.ID, "err", derr)
		}
		return nil, err
	}
	return run, nil
}

func (s *Server) startTrain(w http.ResponseWriter, r *http.Request) {
	var req trainRequest
	if r.ContentLength > 0 && !decode(w, r, &req) {
		return
	}
	job, err := s.trainJob(r.Context(), r.PathValue("name"), req)
	s.accepted(w, r, job, err)
}

// accepted writes the response for work that was asked for.
//
// The body is the run, which is the durable thing: a caller who started a crawl
// gets the address of what they started, and can ask about it tomorrow rather
// than only for as long as this process happens to live.
func (s *Server) accepted(w http.ResponseWriter, r *http.Request, run *store.Run, err error) {
	var busy ErrBusy
	var bad ErrInvalid

	switch {
	case errors.As(err, &busy):
		// Conflict rather than a failure: the caller asked for something
		// reasonable that is already happening.
		writeJSON(w, http.StatusConflict, map[string]any{"error": busy.Error()})
	case errors.As(err, &bad):
		s.badRequest(w, bad.Error())
	case err != nil:
		s.fail(w, r, err)
	default:
		w.Header().Set("Location", fmt.Sprintf("/v1/runs/%d", run.ID))
		writeJSON(w, http.StatusAccepted, map[string]any{runKey: run})
	}
}

// applyTemplate copies a shipped schema onto an item, as `scour add
// --template` does.
func applyTemplate(ctx context.Context, s *store.Store, itemID uint, name string) error {
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
		if err := s.AddPropertyDetail(ctx, itemID, store.PropertyDetail{
			Name: p.Name, Type: string(p.Type), Example: example, Description: p.Description}); err != nil {
			return err
		}
		for _, alias := range p.Aliases {
			if err := s.AddPropertyAlias(ctx, itemID, "", p.Name, strings.TrimSpace(alias)); err != nil {
				return err
			}
		}
	}
	return nil
}
