// SPDX-License-Identifier: GPL-3.0-or-later

package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// RunState is how a run ended, or that it has not.
type RunState string

const (
	// RunRunning is a run that has started and not reported an ending. A
	// process killed mid-crawl leaves one of these behind, which is why the
	// listing says how long ago it started rather than trusting it.
	RunRunning RunState = "running"
	// RunDone is a crawl that ran out of frontier: the site is exhausted for
	// the scope it was given.
	RunDone RunState = "done"
	// RunBudget is a crawl that stopped on its page or time budget, so there
	// is more waiting for the next one.
	RunBudget RunState = "budget"
	// RunStopped is a crawl someone paused or interrupted.
	RunStopped RunState = "stopped"
	// RunFailed is a crawl that ended on an error.
	RunFailed RunState = "failed"
)

// Run is one execution of a job.
//
// The job says what to crawl and holds the frontier; a run is one occasion of
// working through it. Kept separate because the interesting questions are all
// about a particular occasion: what did last night's run fetch, why did it stop
// early, which one started producing the bad records.
//
// The counters are copied in rather than derived. A run's pages can be
// recounted from the urls table only until one of them is fetched again, and a
// history that changes when you re-crawl is not a history.
type Run struct {
	ID    uint `gorm:"primaryKey"`
	JobID uint `gorm:"index:idx_run_job_started;not null"`
	// StartedAt is indexed with the job because every listing of runs is that
	// job's, newest first.
	StartedAt time.Time `gorm:"index:idx_run_job_started"`
	EndedAt   *time.Time
	State     RunState `gorm:"index"`

	Fetched int
	Failed  int
	Skipped int
	Bytes   int64
	// Records is how many the run's pages yielded, filled in by training
	// rather than by the crawl, since extraction happens afterwards.
	Records int

	// Budget names what ended it: "pages", "time", "pause", or empty when the
	// frontier simply ran out.
	Budget string
	// Statuses is the response code histogram, stored as JSON because it is
	// read back whole and never queried by code.
	Statuses string
	// Error is why a failed run failed, in the words it failed with.
	Error string
}

// Elapsed is how long the run took, or has been going.
func (r *Run) Elapsed() time.Duration {
	if r.EndedAt != nil {
		return r.EndedAt.Sub(r.StartedAt)
	}
	return time.Since(r.StartedAt)
}

// StatusCounts decodes the histogram. A run written before the column, or one
// that recorded none, reads as empty rather than as an error: a missing
// histogram is not worth failing a listing over.
func (r *Run) StatusCounts() map[int]int {
	if r.Statuses == "" {
		return nil
	}
	var out map[int]int
	if err := json.Unmarshal([]byte(r.Statuses), &out); err != nil {
		return nil
	}
	return out
}

// StartRun opens a run for a job and returns it.
func (s *Store) StartRun(ctx context.Context, jobID uint) (*Run, error) {
	run := &Run{JobID: jobID, StartedAt: time.Now().UTC(), State: RunRunning}
	if err := s.db.WithContext(ctx).Create(run).Error; err != nil {
		return nil, fmt.Errorf("start run: %w", err)
	}
	return run, nil
}

// Finished is what a crawl reports when it ends.
type Finished struct {
	State    RunState
	Fetched  int
	Failed   int
	Skipped  int
	Bytes    int64
	Budget   string
	Statuses map[int]int
	Err      error
}

// FinishRun closes a run with what it did.
//
// Never fails the crawl it is recording: a run that fetched ten thousand pages
// and could not write its own history row still fetched them, and the pages are
// the thing worth keeping. The error is returned so a caller can log it.
func (s *Store) FinishRun(ctx context.Context, runID uint, f Finished) error {
	now := time.Now().UTC()
	fields := map[string]any{
		"ended_at": now,
		"state":    f.State,
		"fetched":  f.Fetched,
		"failed":   f.Failed,
		"skipped":  f.Skipped,
		"bytes":    f.Bytes,
		"budget":   f.Budget,
	}
	if len(f.Statuses) > 0 {
		if encoded, err := json.Marshal(f.Statuses); err == nil {
			fields["statuses"] = string(encoded)
		}
	}
	if f.Err != nil {
		fields["error"] = f.Err.Error()
	}
	err := s.db.WithContext(ctx).Model(&Run{}).Where("id = ?", runID).Updates(fields).Error
	if err != nil {
		return fmt.Errorf("finish run %d: %w", runID, err)
	}
	return nil
}

// Runs is a job's history, newest first.
func (s *Store) Runs(ctx context.Context, jobID uint, limit int) ([]Run, error) {
	q := s.db.WithContext(ctx).Where("job_id = ?", jobID).Order("started_at DESC, id DESC")
	if limit > 0 {
		q = q.Limit(limit)
	}
	var runs []Run
	if err := q.Find(&runs).Error; err != nil {
		return nil, fmt.Errorf("runs of job %d: %w", jobID, err)
	}
	return runs, nil
}

// RunByID reads one run, scoped to its job so an id from another job's history
// is a miss rather than somebody else's run.
func (s *Store) RunByID(ctx context.Context, jobID, id uint) (*Run, error) {
	var run Run
	err := s.db.WithContext(ctx).Where("id = ? AND job_id = ?", id, jobID).First(&run).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, fmt.Errorf("run %d: %w", id, ErrNotFound)
		}
		return nil, fmt.Errorf("run %d: %w", id, err)
	}
	return &run, nil
}

// LastRun is the most recent run of a job, which is what `job log` shows when
// no run is named: the one that just ended is the one you are asking about.
func (s *Store) LastRun(ctx context.Context, jobID uint) (*Run, error) {
	runs, err := s.Runs(ctx, jobID, 1)
	if err != nil {
		return nil, err
	}
	if len(runs) == 0 {
		return nil, fmt.Errorf("job %d has no runs: %w", jobID, ErrNotFound)
	}
	return &runs[0], nil
}

// LastRuns is the most recent run of each of several jobs, for a listing that
// would otherwise ask once per row.
func (s *Store) LastRuns(ctx context.Context, jobIDs []uint) (map[uint]Run, error) {
	out := map[uint]Run{}
	if len(jobIDs) == 0 {
		return out, nil
	}
	var runs []Run
	err := s.db.WithContext(ctx).
		Where("job_id IN ?", jobIDs).
		Where("id IN (SELECT MAX(id) FROM runs WHERE job_id IN ? GROUP BY job_id)", jobIDs).
		Find(&runs).Error
	if err != nil {
		return nil, fmt.Errorf("last runs: %w", err)
	}
	for _, r := range runs {
		out[r.JobID] = r
	}
	return out, nil
}

// RunPages is what one run fetched, most recent first.
//
// This is a run's log. It is read from the pages rather than from a written
// log, because the questions asked of a run that ended badly are all about
// pages: which ones failed, what came back, how slow the site was. A separate
// log would be the same facts a second time.
//
// A page fetched again by a later run belongs to that run, so an old run's log
// thins out as the site is recrawled. That is the cost of not keeping a row per
// fetch, and it falls on the histories nobody reads twice.
func (s *Store) RunPages(ctx context.Context, runID uint, limit int) ([]URL, error) {
	q := s.db.WithContext(ctx).
		Where("run_id = ?", runID).
		Order("fetched_at DESC, id DESC")
	if limit > 0 {
		q = q.Limit(limit)
	}
	var out []URL
	if err := q.Find(&out).Error; err != nil {
		return nil, fmt.Errorf("pages of run %d: %w", runID, err)
	}
	return out, nil
}

// RunPageCount is how many of a run's pages are still attributed to it, which
// is not the same as how many it fetched once a later run has been over them.
func (s *Store) RunPageCount(ctx context.Context, runID uint) (int64, error) {
	var n int64
	err := s.db.WithContext(ctx).Model(&URL{}).Where("run_id = ?", runID).Count(&n).Error
	if err != nil {
		return 0, fmt.Errorf("count pages of run %d: %w", runID, err)
	}
	return n, nil
}
