// SPDX-License-Identifier: GPL-3.0-or-later

package server

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"
)

// State is where a job has got to.
type State string

// The states a job moves through.
const (
	Running State = "running"
	Done    State = "done"
	Failed  State = "failed"
)

// Job is one crawl or training run.
//
// Crawls and trainings take minutes. Holding an HTTP request open for that long
// means the caller's timeout, or any proxy between them, decides when the work
// is abandoned, and leaves no way to ask afterwards what happened. A job id
// separates starting the work from watching it.
type Job struct {
	ID       string    `json:"id"`
	Kind     string    `json:"kind"`
	Item     string    `json:"item"`
	State    State     `json:"state"`
	Started  time.Time `json:"started"`
	Finished time.Time `json:"finished,omitzero"`
	// Error is the failure message, if it failed.
	Error string `json:"error,omitempty"`
	// Result is whatever the work produced, for a caller that polled too late
	// to watch it happen.
	Result any `json:"result,omitempty"`
}

// Elapsed is how long the job ran, or has been running.
func (j Job) Elapsed() time.Duration {
	if j.Finished.IsZero() {
		return time.Since(j.Started)
	}
	return j.Finished.Sub(j.Started)
}

// Jobs tracks background work.
type Jobs struct {
	mu      sync.Mutex
	jobs    map[string]*Job
	next    int
	running sync.WaitGroup

	// active is one entry per item and kind, so the same item cannot be
	// crawled twice at once. Two crawls of one site would race on the frontier
	// and double the load on someone else's server.
	active map[string]string
}

// NewJobs returns an empty manager.
func NewJobs() *Jobs {
	return &Jobs{jobs: map[string]*Job{}, active: map[string]string{}}
}

// ErrBusy is returned when the same work is already running.
type ErrBusy struct {
	Kind, Item, ID string
}

func (e ErrBusy) Error() string {
	return fmt.Sprintf("%s of %s is already running as job %s", e.Kind, e.Item, e.ID)
}

// Start runs work in the background and returns the job watching it.
//
// The work is given a context that outlives the request: a caller who starts a
// crawl and hangs up has still started a crawl, and cancelling it because they
// stopped listening would make the API unusable from anything that does not
// hold the connection open.
func (j *Jobs) Start(kind, item string, work func(context.Context) (any, error)) (*Job, error) {
	key := kind + ":" + item

	j.mu.Lock()
	if id, busy := j.active[key]; busy {
		j.mu.Unlock()
		return nil, ErrBusy{Kind: kind, Item: item, ID: id}
	}

	j.next++
	job := &Job{
		ID:      fmt.Sprintf("%s-%d", kind, j.next),
		Kind:    kind,
		Item:    item,
		State:   Running,
		Started: time.Now().UTC(),
	}
	j.jobs[job.ID] = job
	j.active[key] = job.ID
	j.prune()

	// Copied while the lock is still held. Once the goroutine below starts it
	// may finish and write the outcome at any moment, so reading the job after
	// unlocking would race it.
	snapshot := *job
	j.mu.Unlock()

	j.running.Add(1)
	go func() {
		defer j.running.Done()

		result, err := work(context.Background())

		j.mu.Lock()
		defer j.mu.Unlock()

		job.Finished = time.Now().UTC()
		delete(j.active, key)
		if err != nil {
			job.State = Failed
			job.Error = err.Error()
			slog.Error("job failed", "id", job.ID, "err", err)
			return
		}
		job.State = Done
		job.Result = result
		slog.Info("job finished", "id", job.ID, "elapsed", job.Elapsed().Round(time.Millisecond))
	}()

	return &snapshot, nil
}

// Get returns a job by id.
func (j *Jobs) Get(id string) (Job, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()

	job, ok := j.jobs[id]
	if !ok {
		return Job{}, false
	}
	return *job, true
}

// List returns every tracked job, newest first.
func (j *Jobs) List() []Job {
	j.mu.Lock()
	defer j.mu.Unlock()

	out := make([]Job, 0, len(j.jobs))
	for _, job := range j.jobs {
		out = append(out, *job)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Started.After(out[b].Started) })
	return out
}

// Wait blocks until every running job has finished, so a shutdown does not
// abandon a crawl halfway through writing its results.
func (j *Jobs) Wait() { j.running.Wait() }

// maxFinished is how many completed jobs are kept. A long-lived service would
// otherwise accumulate one record per crawl forever.
const maxFinished = 100

// prune drops the oldest finished jobs. The caller holds the lock.
func (j *Jobs) prune() {
	var finished []*Job
	for _, job := range j.jobs {
		if job.State != Running {
			finished = append(finished, job)
		}
	}
	if len(finished) <= maxFinished {
		return
	}

	sort.Slice(finished, func(a, b int) bool { return finished[a].Finished.Before(finished[b].Finished) })
	for _, job := range finished[:len(finished)-maxFinished] {
		delete(j.jobs, job.ID)
	}
}
