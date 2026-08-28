// SPDX-License-Identifier: GPL-3.0-or-later

package bus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

// What a job is doing, as opposed to what it says.
//
// # Why this is a bucket of its own and not a field on the job
//
// Because [JobsBucket] holds desired state, which is the document somebody
// submitted, and reformatting or extending that document to record that a crawl
// is halfway through would make the diff between two submissions unreadable.
// The diff is the whole of what a resubmission is reviewed by, so what a job
// says and what it is currently doing are kept apart.
//
// It also means the two have different lifetimes in the way they should: a
// submitted job outlives every run of it, and a phase is only true until the
// next transition.
//
// # Why it is durable, unlike the node registry
//
// [NodesBucket] has a TTL because a row outliving its process is a lie. A phase
// is the opposite: a job paused on Friday is still paused on Monday, whether or
// not anything has been running in between, and a service that forgot its
// phases on restart would resume nothing and say every job was stopped.
//
// # Why it does not churn
//
// A phase changes on a transition and not continuously: started, paused,
// finished. Progress is published to whoever is watching rather than written
// here, which is what keeps a crawl fetching a thousand pages from being a
// thousand writes to a key-value store.
//
// That is also why a shallow history is affordable, and it earns its keep:
// "what was this doing before it failed" is the first question anybody asks,
// and one entry cannot answer it.
const StatesBucket = "SCOUR_RUNS"

// Phase is what a job is doing now.
type Phase string

// The phases. A job moves between them only through the job service, which is
// the whole reason that service exists.
const (
	// PhaseStopped is a job the cluster knows about and is not running. It is
	// also what a job that has never been started reports, because "submitted
	// and not running" and "stopped" are the same situation to act on.
	PhaseStopped Phase = "stopped"

	// PhaseRunning is a job being driven right now.
	PhaseRunning Phase = "running"

	// PhasePaused is a job whose loop was stopped with its frontier kept, so
	// resuming continues rather than starting again.
	PhasePaused Phase = "paused"

	// PhaseDone is a crawl that ended on its own: the frontier ran dry, or a
	// budget was reached. [JobState.Ending] says which, because those are
	// different endings that look identical from here.
	PhaseDone Phase = "done"

	// PhaseFailed is a crawl that stopped because something went wrong.
	// [JobState.Error] is what.
	PhaseFailed Phase = "failed"
)

// Live reports whether a phase is one a driver is currently working on.
func (p Phase) Live() bool { return p == PhaseRunning }

// JobState is what the cluster remembers about one job's execution.
type JobState struct {
	// Phase is what it is doing.
	Phase Phase `json:"phase"`

	// Since is when it started doing that.
	Since time.Time `json:"since"`

	// Revision is the job revision being run, so a status can say that a job
	// was resubmitted while an older revision is still crawling.
	Revision uint64 `json:"revision,omitempty"`

	// Ending is why a finished crawl stopped, in the words [run.Ending] uses.
	Ending string `json:"ending,omitempty"`

	// Error is why a failed crawl stopped.
	Error string `json:"error,omitempty"`

	// Driver names the service that is running it, so an operator looking at a
	// cluster can tell which machine to go and look at.
	Driver string `json:"driver,omitempty"`

	// Last is what the run that just ended did.
	//
	// Kept because the counters live in the driver, and the driver is gone the
	// moment the crawl is: without this, "how did that go" is a question that
	// can only be asked while it is still running, which is the one time
	// nobody needs to ask it. `scour job stats` on a finished crawl reported
	// zeros, and zeros are indistinguishable from a crawl that did nothing.
	Last *JobStats `json:"last,omitempty"`
}

// States is the cluster's record of what its jobs are doing.
type States struct {
	kv jetstream.KeyValue
}

// OpenStates returns the job state store, creating the bucket if it is not
// there.
//
// History is kept, shallowly, for the reason [StatesBucket] gives.
func (c *Conn) OpenStates(ctx context.Context) (*States, error) {
	js, err := jetstream.New(c.Conn)
	if err != nil {
		return nil, fmt.Errorf("bus: jetstream: %w", err)
	}

	kv, err := js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket:      StatesBucket,
		Description: "what scour's jobs are doing",
		History:     10,
	})
	if err != nil {
		return nil, fmt.Errorf("bus: %s: %w", StatesBucket, err)
	}
	return &States{kv: kv}, nil
}

// Put records what a job is doing.
func (s *States) Put(ctx context.Context, name string, state JobState) error {
	if err := checkName(name); err != nil {
		return err
	}

	payload, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("bus: state of %q: %w", name, err)
	}
	if _, err := s.kv.Put(ctx, name, payload); err != nil {
		return fmt.Errorf("bus: record state of %q: %w", name, err)
	}
	return nil
}

// Get reads what a job is doing.
//
// A job with no row has never been started, which is reported as
// [PhaseStopped] rather than as an error: a caller asking what a job is doing
// wants an answer, and "submitted, not running" is one. A job that does not
// exist at all is [Jobs]'s to refuse, and it does.
func (s *States) Get(ctx context.Context, name string) (JobState, error) {
	entry, err := s.kv.Get(ctx, name)
	switch {
	case errors.Is(err, jetstream.ErrKeyNotFound):
		return JobState{Phase: PhaseStopped}, nil
	case err != nil:
		return JobState{}, fmt.Errorf("bus: read state of %q: %w", name, err)
	}

	var state JobState
	if err := json.Unmarshal(entry.Value(), &state); err != nil {
		return JobState{}, fmt.Errorf("bus: state of %q: %w", name, err)
	}
	return state, nil
}

// Delete forgets a job's state, which is what deleting the job means.
//
// A missing row is not an error. Deleting a job that was never started is the
// ordinary case, and reporting it would make every delete of a fresh job look
// like a failure.
func (s *States) Delete(ctx context.Context, name string) error {
	if err := s.kv.Delete(ctx, name); err != nil && !errors.Is(err, jetstream.ErrKeyNotFound) {
		return fmt.Errorf("bus: forget state of %q: %w", name, err)
	}
	return nil
}
