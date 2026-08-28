// SPDX-License-Identifier: GPL-3.0-or-later

package bus

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
)

// Jobs as a service: submitting them, and starting and stopping the crawls.
//
// # Why this is a service and not a client writing to the bucket
//
// [JobsBucket] is a key-value store and any client with the address can write
// to it, which is exactly the arrangement this replaces. A submission is not a
// write: it has to be parsed, validated, compared against the revision already
// running, and refused when the change is one the job's own `mutation` policy
// says to refuse. Every client doing that for itself means every client doing
// it slightly differently, and a client that skips it silently applies a change
// the operator asked to be warned about.
//
// The same argument the entity graph makes, for the same reason: one process
// owns the decision, and everything else asks it.
//
// # Why the same service drives the crawl
//
// Because starting a job and running it cannot be two different owners. A
// control plane that only wrote "running" into a bucket would be describing a
// crawl it has no handle on: it could not report how far along it is, could not
// stop it except by asking it nicely through another write, and could not tell
// a crawl that had died from one that had never started. The service holds the
// frontier and the loop, so `job status` is an answer from the thing doing the
// work rather than a guess about it.
//
// The stages stay where they were. The driver fetches and reads through
// [Conn.NewDownloader] and [Conn.NewSpider], so the nodes do the crawling and
// this owns only the order it happens in. That asymmetry is the politeness
// rule: two schedulers handing out the same host cannot honour a crawl delay
// between them, so there is one per job.

// ControlQueue is the queue group the job service answers in.
const ControlQueue = "scour-jobs"

// ControlSubject is where one job operation is asked for: scour.jobs.<op>.
//
// Plural, unlike the stage subjects, which are scour.<job>.<stage>. A job
// called `jobs` is therefore not a collision: the third token is an operation
// here and a stage there, and no stage is called `list`.
func ControlSubject(op string) string { return Prefix + ".jobs." + op }

// JobEventSubject is where one job's execution is reported: scour.jobs.event.<job>.
//
// Published, not queued, and not part of the request/reply surface above. A
// watcher is an observer: two of them both see everything, and none of them
// slows the crawl down by existing.
func JobEventSubject(job string) string { return Prefix + ".jobs.event." + job }

// JobStatus is what a job is and what it is doing.
type JobStatus struct {
	// Name is the job, which is its identity in the cluster.
	Name string `json:"name"`

	// State is its phase and how it got there.
	State JobState `json:"state"`

	// Revision is the revision of the submitted document, which is not
	// necessarily [JobState.Revision]: a job resubmitted while it crawls has a
	// newer document than the one running.
	Revision uint64 `json:"revision"`
}

// Stale reports a job running an older revision than the one submitted, which
// is a thing an operator wants to see and cannot otherwise tell.
func (s JobStatus) Stale() bool {
	return s.State.Phase.Live() && s.State.Revision != 0 && s.State.Revision != s.Revision
}

// JobStats is what a crawl has done so far.
//
// Plain integers rather than the atomics [run.Stats] keeps, because these have
// crossed a network and are a snapshot: an atomic here would suggest a liveness
// it cannot have.
type JobStats struct {
	Fetched  int64 `json:"fetched"`
	Cached   int64 `json:"cached"`
	Dropped  int64 `json:"dropped"`
	Failed   int64 `json:"failed"`
	Items    int64 `json:"items"`
	Queued   int64 `json:"queued"`
	Lost     int64 `json:"lost"`
	Store    int64 `json:"store"`
	Exported int64 `json:"exported"`

	// Waiting is how many URLs are still in the frontier, which is the only
	// number here that says how much is left rather than how much was done.
	Waiting int `json:"waiting"`

	// Elapsed is how long the current run has been going, in nanoseconds.
	Elapsed time.Duration `json:"elapsed"`
}

// JobEvent is one thing happening to a job, as a watcher sees it.
type JobEvent struct {
	Name  string    `json:"name"`
	At    time.Time `json:"at"`
	Phase Phase     `json:"phase"`

	// Message says what happened, for a transition. Empty on the periodic
	// reports, which carry only numbers.
	Message string `json:"message,omitempty"`

	// Stats is where the crawl had got to when this was sent.
	Stats JobStats `json:"stats"`
}

// Controller is what the job service can do, here or somewhere else.
//
// Both the local manager and [ControlClient] satisfy it, which is what lets
// the command line be written once against a cluster that may be in this
// process or on another machine.
type Controller interface {
	// List is every job the cluster knows about and what it is doing.
	List(ctx context.Context) ([]JobStatus, error)

	// Create submits a job that does not exist yet. A name already taken is
	// refused rather than replaced, because overwriting somebody else's job by
	// picking their name is not something to do silently. Use Update.
	Create(ctx context.Context, document []byte) (JobStatus, error)

	// Update resubmits a job that exists. When the job is running, the change
	// is reviewed against its `mutation` policy first and a refused change
	// leaves the running revision alone.
	Update(ctx context.Context, document []byte) (JobStatus, error)

	// Delete removes a job, stopping it first if it is running.
	Delete(ctx context.Context, name string) error

	// Status is one job's phase.
	Status(ctx context.Context, name string) (JobStatus, error)

	// Stats is how far one job's crawl has got.
	Stats(ctx context.Context, name string) (JobStats, error)

	// Start begins a crawl, seeding the frontier from the job's start URLs.
	// Fresh forgets what a previous run queued.
	Start(ctx context.Context, name string, fresh bool) (JobStatus, error)

	// Stop ends a crawl. The frontier is kept, so starting it again resumes
	// unless it is started fresh.
	Stop(ctx context.Context, name string) (JobStatus, error)

	// Pause ends the loop and keeps the frontier, which is Stop with the
	// intention recorded. Resume is what tells them apart.
	Pause(ctx context.Context, name string) (JobStatus, error)

	// Resume starts a paused job again without re-seeding it.
	Resume(ctx context.Context, name string) (JobStatus, error)

	// Document is the job document as it was submitted, which is what `spec`,
	// `show` and `train` work from.
	Document(ctx context.Context, name string) ([]byte, error)
}

type (
	jobNameAsk struct {
		Name string `json:"name"`
	}
	jobDocumentAsk struct {
		Document []byte `json:"document"`
	}
	jobStartAsk struct {
		Name  string `json:"name"`
		Fresh bool   `json:"fresh"`
	}
)

// ServeControl answers for a job manager until the returned service is closed.
// The wait bounds one request. Zero means [Timeout].
func (c *Conn) ServeControl(jobs Controller, wait time.Duration) (*Service, error) {
	s := &Service{wait: wait}

	serving(c, s, ControlSubject("list"), ControlQueue,
		func(ctx context.Context, _ nothingAsk) ([]JobStatus, error) {
			return jobs.List(ctx)
		})
	serving(c, s, ControlSubject("create"), ControlQueue,
		func(ctx context.Context, a jobDocumentAsk) (JobStatus, error) {
			return jobs.Create(ctx, a.Document)
		})
	serving(c, s, ControlSubject("update"), ControlQueue,
		func(ctx context.Context, a jobDocumentAsk) (JobStatus, error) {
			return jobs.Update(ctx, a.Document)
		})
	serving(c, s, ControlSubject("delete"), ControlQueue,
		func(ctx context.Context, a jobNameAsk) (none, error) {
			return none{}, jobs.Delete(ctx, a.Name)
		})
	serving(c, s, ControlSubject("status"), ControlQueue,
		func(ctx context.Context, a jobNameAsk) (JobStatus, error) {
			return jobs.Status(ctx, a.Name)
		})
	serving(c, s, ControlSubject("stats"), ControlQueue,
		func(ctx context.Context, a jobNameAsk) (JobStats, error) {
			return jobs.Stats(ctx, a.Name)
		})
	serving(c, s, ControlSubject("start"), ControlQueue,
		func(ctx context.Context, a jobStartAsk) (JobStatus, error) {
			return jobs.Start(ctx, a.Name, a.Fresh)
		})
	serving(c, s, ControlSubject("stop"), ControlQueue,
		func(ctx context.Context, a jobNameAsk) (JobStatus, error) {
			return jobs.Stop(ctx, a.Name)
		})
	serving(c, s, ControlSubject("pause"), ControlQueue,
		func(ctx context.Context, a jobNameAsk) (JobStatus, error) {
			return jobs.Pause(ctx, a.Name)
		})
	serving(c, s, ControlSubject("resume"), ControlQueue,
		func(ctx context.Context, a jobNameAsk) (JobStatus, error) {
			return jobs.Resume(ctx, a.Name)
		})
	serving(c, s, ControlSubject("document"), ControlQueue,
		func(ctx context.Context, a jobNameAsk) ([]byte, error) {
			return jobs.Document(ctx, a.Name)
		})

	if err := s.ready(c); err != nil {
		return nil, err
	}
	return s, nil
}

// ControlClient is a job service that is somewhere else. It satisfies
// [Controller].
type ControlClient struct {
	conn *Conn
	wait time.Duration
}

// NewControl returns a client for the job service. Zero means [Timeout].
func (c *Conn) NewControl(wait time.Duration) *ControlClient {
	return &ControlClient{conn: c, wait: wait}
}

func (j *ControlClient) List(ctx context.Context) ([]JobStatus, error) {
	return call[nothingAsk, []JobStatus](ctx, j.conn, ControlSubject("list"), j.wait, nothingAsk{})
}

func (j *ControlClient) Create(ctx context.Context, document []byte) (JobStatus, error) {
	return call[jobDocumentAsk, JobStatus](ctx, j.conn, ControlSubject("create"), j.wait,
		jobDocumentAsk{Document: document})
}

func (j *ControlClient) Update(ctx context.Context, document []byte) (JobStatus, error) {
	return call[jobDocumentAsk, JobStatus](ctx, j.conn, ControlSubject("update"), j.wait,
		jobDocumentAsk{Document: document})
}

func (j *ControlClient) Delete(ctx context.Context, name string) error {
	_, err := call[jobNameAsk, none](ctx, j.conn, ControlSubject("delete"), j.wait,
		jobNameAsk{Name: name})
	return err
}

func (j *ControlClient) Status(ctx context.Context, name string) (JobStatus, error) {
	return call[jobNameAsk, JobStatus](ctx, j.conn, ControlSubject("status"), j.wait,
		jobNameAsk{Name: name})
}

func (j *ControlClient) Stats(ctx context.Context, name string) (JobStats, error) {
	return call[jobNameAsk, JobStats](ctx, j.conn, ControlSubject("stats"), j.wait,
		jobNameAsk{Name: name})
}

func (j *ControlClient) Start(ctx context.Context, name string, fresh bool) (JobStatus, error) {
	return call[jobStartAsk, JobStatus](ctx, j.conn, ControlSubject("start"), j.wait,
		jobStartAsk{Name: name, Fresh: fresh})
}

func (j *ControlClient) Stop(ctx context.Context, name string) (JobStatus, error) {
	return call[jobNameAsk, JobStatus](ctx, j.conn, ControlSubject("stop"), j.wait,
		jobNameAsk{Name: name})
}

func (j *ControlClient) Pause(ctx context.Context, name string) (JobStatus, error) {
	return call[jobNameAsk, JobStatus](ctx, j.conn, ControlSubject("pause"), j.wait,
		jobNameAsk{Name: name})
}

func (j *ControlClient) Resume(ctx context.Context, name string) (JobStatus, error) {
	return call[jobNameAsk, JobStatus](ctx, j.conn, ControlSubject("resume"), j.wait,
		jobNameAsk{Name: name})
}

func (j *ControlClient) Document(ctx context.Context, name string) ([]byte, error) {
	return call[jobNameAsk, []byte](ctx, j.conn, ControlSubject("document"), j.wait,
		jobNameAsk{Name: name})
}

// Announce publishes one job event to whoever is watching.
//
// A failure to publish is returned rather than logged, because the driver has a
// logger and this does not, and because a caller that wants to ignore it should
// have to say so.
func (c *Conn) Announce(event JobEvent) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("bus: job event: %w", err)
	}
	if err := c.Publish(JobEventSubject(event.Name), payload); err != nil {
		return fmt.Errorf("bus: job event: %w", err)
	}
	return nil
}

// WatchJob reports what happens to one job until the context ends.
//
// Every job, when the name is empty. A watcher joining halfway sees what
// happens from then on and not what already did: this is a live feed, and the
// history is [States]'s to keep.
func (c *Conn) WatchJob(ctx context.Context, name string) (<-chan JobEvent, func() error, error) {
	subject := JobEventSubject(name)
	if name == "" {
		subject = JobEventSubject("*")
	}

	// Two channels rather than one, and the reason is a panic rather than a
	// preference. The subscription's callback runs on a NATS goroutine that
	// can still be delivering a message after Unsubscribe returns, so a
	// callback writing straight to the channel a watcher ranges over races
	// with closing it, and a send on a closed channel takes the process down.
	//
	// So the callback writes to a channel nothing ever closes, and the pump
	// below owns the one the caller sees. One writer, one closer, no race.
	//
	// Both are buffered and dropped when full rather than blocking. A watcher
	// is an observer: a slow terminal on the other end of one must not be able
	// to hold up the crawl it is watching.
	raw := make(chan JobEvent, 64)
	out := make(chan JobEvent, 64)

	sub, err := c.Subscribe(subject, func(msg *nats.Msg) {
		var event JobEvent
		if err := json.Unmarshal(msg.Data, &event); err != nil {
			return
		}
		select {
		case raw <- event:
		default:
		}
	})
	if err != nil {
		return nil, nil, fmt.Errorf("bus: watch %s: %w", subject, err)
	}

	// Flushed before returning, for the reason [Service.ready] gives: a
	// subscription is not live until the server has processed it, so a watcher
	// that returned before flushing missed whatever happened in between and
	// looked like a cluster that had gone quiet.
	if err := c.Flush(); err != nil {
		_ = sub.Unsubscribe()
		return nil, nil, fmt.Errorf("bus: watch %s: %w", subject, err)
	}

	go func() {
		defer close(out)
		defer func() { _ = sub.Unsubscribe() }()

		for {
			select {
			case <-ctx.Done():
				return
			case event := <-raw:
				select {
				case out <- event:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out, sub.Unsubscribe, nil
}
