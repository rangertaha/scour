// SPDX-License-Identifier: GPL-3.0-or-later

// Package jobs is the cluster's job manager: what a job is, and what it is
// doing.
//
// # What it owns
//
// Two things that cannot be owned separately. It is the only writer of the job
// bucket, so a submission is parsed, validated and reviewed against the
// revision already running before it is stored; and it drives the crawls, so
// starting one and asking how far it has got are questions to the same process.
//
// Splitting them was the arrangement that did not work. A control plane that
// only wrote "running" into a bucket describes a crawl it has no handle on: it
// cannot say how far along that crawl is, cannot stop it except by writing
// again and hoping, and cannot tell a crawl that died from one that never
// started.
//
// # What it does not own
//
// The fetching and the reading. The driver reaches those through
// [bus.Conn.NewDownloader] and [bus.Conn.NewSpider], so the nodes do the work
// and this owns only the order it happens in. That is the same seam
// [run.Options] already had for tests, used in earnest.
//
// One driver per job, and that asymmetry is the politeness rule rather than a
// simplification: two schedulers handing out the same host cannot honour a
// crawl delay between them, so the frontier has one owner.
//
// # Pausing
//
// A paused job is one whose loop was stopped with its frontier left alone.
// There is no gate inside the crawl loop holding workers still, because a
// frontier that survives a restart makes one unnecessary: stopping and starting
// again is resuming. What pause adds over stop is the recorded intention, which
// is what makes `resume` mean "carry on" rather than "start over".
//
// Both drain rather than cancel. Cancelling aborts the fetches in flight, and
// an aborted fetch tells the frontier nothing on purpose so that an interrupted
// URL is not charged an attempt; its lease then has to expire before anybody
// can have it again. That is right for ctrl-c and wrong here, because a job
// paused and resumed a second later would find nothing due for five minutes
// while reporting itself as running. See [run.Run.Drain].
package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/hcl/v2"

	"github.com/rangertaha/scour/internal/bus"
	"github.com/rangertaha/scour/internal/cache"
	"github.com/rangertaha/scour/internal/engine"
	"github.com/rangertaha/scour/internal/frontier"
	"github.com/rangertaha/scour/internal/frontier/sqlite"
	"github.com/rangertaha/scour/internal/run"
)

// errNotRunning is asking a job that is not running to stop.
//
// A sentinel because one caller has to tell it from a real failure: a delete
// races with a crawl ending on its own, and that race is the ordinary case
// rather than a problem.
var errNotRunning = errors.New("is not running")

// Report is how often a running crawl says where it has got to.
//
// Often enough that `job watch` looks alive, rarely enough that a hundred jobs
// reporting are not themselves a load. Progress is published, so nobody
// watching costs nothing.
const Report = 2 * time.Second

// Settle is how long a stop waits for the loop to finish what it is holding.
//
// A crawl asked to stop finishes the pages already in flight, because
// abandoning them means fetching them again, so this is longer than a page
// fetch rather than an instant.
const Settle = 3 * time.Minute

// Options are how a manager is set up.
type Options struct {
	// Dir is where the frontiers live. Required: a frontier in a temporary
	// directory is a crawl that cannot be resumed, which is most of what the
	// phases above are for.
	Dir string

	// Bodies is how a fetched page reaches the driver from the node that
	// fetched it. Required, and it has to be storage both can see: a directory
	// works on one machine, and a cluster wants the same object store every
	// node has.
	Bodies cache.Store

	// Name is this manager, for the record it writes of who is driving what.
	Name string

	// Log is where a crawl's progress goes. Nil is silent.
	Log *slog.Logger

	// Eval resolves `secret()` in plugin configuration. Nil means a job asking
	// for a secret is refused by name when its stages are built.
	Eval *hcl.EvalContext

	// Wait bounds one request to a stage. Zero means [bus.Timeout].
	Wait time.Duration

	// Report overrides how often a running crawl publishes progress. Zero
	// means [Report].
	Report time.Duration
}

// Manager owns the cluster's jobs. It satisfies [bus.Controller].
type Manager struct {
	conn   *bus.Conn
	opts   Options
	log    *slog.Logger
	jobs   *bus.Jobs
	states *bus.States

	// ctx is the manager's own lifetime, and every crawl runs under it. Held
	// rather than taken per call because a crawl must outlive the request that
	// started it: driving from the caller's context meant a crawl that ended
	// the moment `scour job start` returned.
	ctx  context.Context
	stop context.CancelFunc

	mu      sync.Mutex
	running map[string]*driver
	closed  bool

	// starting is the names somebody is in the middle of starting.
	//
	// A driver is only in `running` once its stages are built and its frontier
	// is open, which takes long enough for every concurrent caller to get past
	// the check that looks there. They all then built a crawl of their own and
	// threw it away, and building one opens the job's SQLite frontier: a loser
	// could fail with "database is locked" rather than being told plainly that
	// somebody else had started it.
	//
	// Reserving the name first makes the losers cheap and their message true.
	starting map[string]bool

	// wg counts the drivers, so Close can wait for the crawls rather than
	// merely cancelling them and returning while they are still writing.
	wg sync.WaitGroup

	// shut makes Close idempotent by making a second caller wait for the
	// first, rather than by making it return early. See [Manager.Close].
	shut sync.Once
}

// driver is one crawl being run.
type driver struct {
	name     string
	revision uint64
	crawl    *run.Run
	cancel   context.CancelFunc
	started  time.Time

	// intent is what cancelling means, and it is the whole difference between
	// stop and pause. Read and written under [Manager.mu].
	intent bus.Phase

	// done closes when the crawl has ended and its state has been recorded, so
	// a caller that asked it to stop can wait for that to be true rather than
	// for it to have been requested.
	done chan struct{}
}

// New opens the buckets and returns a manager. The context bounds every crawl
// it drives.
func New(ctx context.Context, conn *bus.Conn, opts Options) (*Manager, error) {
	if conn == nil {
		return nil, errors.New("jobs: no connection")
	}
	if opts.Dir == "" {
		return nil, errors.New("jobs: no directory for the frontiers")
	}
	if opts.Bodies == nil {
		return nil, errors.New("jobs: no cache, so a fetched page has no way back from the node that fetched it")
	}
	if opts.Log == nil {
		opts.Log = slog.New(slog.DiscardHandler)
	}
	if opts.Name == "" {
		opts.Name = "scour-jobs"
	}
	if opts.Report == 0 {
		opts.Report = Report
	}

	jobsKV, err := conn.OpenJobs(ctx)
	if err != nil {
		return nil, err
	}
	states, err := conn.OpenStates(ctx)
	if err != nil {
		return nil, err
	}

	// Detached from the caller's context on purpose, and cancelled by Close.
	// A crawl that ended when the request that started it returned is what
	// deriving from the caller gets you.
	own, stop := context.WithCancel(context.WithoutCancel(ctx))

	m := &Manager{
		conn:     conn,
		opts:     opts,
		log:      opts.Log.With("service", opts.Name),
		jobs:     jobsKV,
		states:   states,
		ctx:      own,
		stop:     stop,
		running:  map[string]*driver{},
		starting: map[string]bool{},
	}

	// The manager still dies with the process. Close is what a caller should
	// use, and this is what happens when the caller is a signal handler that
	// cancelled its context and went away.
	go func() {
		<-ctx.Done()
		_ = m.Close()
	}()
	return m, nil
}

// List is every job the cluster knows about and what it is doing.
func (m *Manager) List(ctx context.Context) ([]bus.JobStatus, error) {
	names, err := m.jobs.Names(ctx)
	if err != nil {
		return nil, err
	}
	sort.Strings(names)

	out := make([]bus.JobStatus, 0, len(names))
	for _, name := range names {
		status, err := m.Status(ctx, name)
		if errors.Is(err, bus.ErrNoJob) {
			// Deleted between listing the keys and reading one, which is a
			// race with an ordinary cause and not worth failing a list over.
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, status)
	}
	return out, nil
}

// Status is one job's phase.
func (m *Manager) Status(ctx context.Context, name string) (bus.JobStatus, error) {
	_, revision, err := m.jobs.Get(ctx, name)
	if err != nil {
		return bus.JobStatus{}, err
	}

	state, err := m.states.Get(ctx, name)
	if err != nil {
		return bus.JobStatus{}, err
	}

	// What is actually running wins over what was recorded. They agree unless
	// the manager died between starting a crawl and recording it, and in that
	// case the crawl in front of us is the truth.
	m.mu.Lock()
	if d := m.running[name]; d != nil {
		state.Phase = bus.PhaseRunning
		state.Revision = d.revision
		state.Since = d.started
		state.Driver = m.opts.Name
		state.Ending = ""
		state.Error = ""
	}
	m.mu.Unlock()

	return bus.JobStatus{Name: name, State: state, Revision: revision}, nil
}

// Stats is how far one job's crawl has got.
//
// A job that is not running reports what is left in its frontier and nothing
// else: the counters belong to a run, and the run is over. How much is left is
// the question somebody asks about a paused job, so it is answered.
func (m *Manager) Stats(ctx context.Context, name string) (bus.JobStats, error) {
	if _, _, err := m.jobs.Get(ctx, name); err != nil {
		return bus.JobStats{}, err
	}

	m.mu.Lock()
	d := m.running[name]
	m.mu.Unlock()

	if d != nil {
		return m.snapshot(ctx, d), nil
	}

	// What the last run did, plus what is left now. The counters are a record
	// and the frontier is current, which is why one is read back and the other
	// is asked for.
	state, err := m.states.Get(ctx, name)
	if err != nil {
		return bus.JobStats{}, err
	}

	var out bus.JobStats
	if state.Last != nil {
		out = *state.Last
	}

	waiting, err := m.waiting(ctx, name)
	if err != nil {
		return bus.JobStats{}, err
	}
	out.Waiting = waiting
	return out, nil
}

// Document is the job document as it was submitted.
func (m *Manager) Document(ctx context.Context, name string) ([]byte, error) {
	document, _, err := m.jobs.Get(ctx, name)
	return document, err
}

// Create submits a job that does not exist yet.
func (m *Manager) Create(ctx context.Context, document []byte) (bus.JobStatus, error) {
	job, err := only(document)
	if err != nil {
		return bus.JobStatus{}, err
	}

	// The store refuses a name already taken, rather than this reading first
	// and writing after. That gap is where a second client got the same answer:
	// eight simultaneous creates of one name all found nothing, all wrote, and
	// the last silently replaced the rest. The read is also what sends an
	// operator to Update, which is what reviews a change against a running job,
	// so a create landing in the gap replaced a running job's document with no
	// review at all.
	if _, err := m.jobs.Create(ctx, job.Name, document); err != nil {
		if errors.Is(err, bus.ErrJobExists) {
			return bus.JobStatus{}, fmt.Errorf(
				"jobs: %q already exists. Change it with update, or delete it first", job.Name)
		}
		return bus.JobStatus{}, err
	}
	if err := m.states.Put(ctx, job.Name, bus.JobState{
		Phase: bus.PhaseStopped,
		Since: time.Now().UTC(),
	}); err != nil {
		return bus.JobStatus{}, err
	}

	m.log.InfoContext(ctx, "job created", "job", job.Name)
	return m.Status(ctx, job.Name)
}

// Update resubmits a job that exists.
//
// A job that is running has its change reviewed first: the `mutation` block is
// the operator's statement about which changes may be applied to a crawl in
// progress, and applying a refused one is exactly what that block exists to
// prevent. A job that is not running is changed without review, because there
// is no work in progress for a costly change to cost anything.
func (m *Manager) Update(ctx context.Context, document []byte) (bus.JobStatus, error) {
	submitted, err := only(document)
	if err != nil {
		return bus.JobStatus{}, err
	}

	current, revision, err := m.jobs.Get(ctx, submitted.Name)
	if err != nil {
		if errors.Is(err, bus.ErrNoJob) {
			return bus.JobStatus{}, fmt.Errorf(
				"jobs: no job called %q. Submit it with create", submitted.Name)
		}
		return bus.JobStatus{}, err
	}

	m.mu.Lock()
	live := m.running[submitted.Name] != nil
	m.mu.Unlock()

	if live {
		running, err := only(current)
		if err != nil {
			return bus.JobStatus{}, fmt.Errorf("jobs: the revision now running cannot be read: %w", err)
		}
		if review := submitted.Review(running); !review.OK() {
			return bus.JobStatus{}, fmt.Errorf(
				"jobs: %q is running and this change is refused by its mutation policy:\n%s",
				submitted.Name, refusals(review))
		}
	}

	// Compare and swap rather than a plain write. Two clients updating one job
	// at once is the case a control plane has to survive: one wins and the
	// other is told, rather than one of them silently disappearing.
	if _, err := m.jobs.Update(ctx, submitted.Name, document, revision); err != nil {
		return bus.JobStatus{}, err
	}

	m.log.InfoContext(ctx, "job updated", "job", submitted.Name, "was", revision)
	return m.Status(ctx, submitted.Name)
}

// Delete removes a job, stopping it first if it is running.
func (m *Manager) Delete(ctx context.Context, name string) error {
	if _, _, err := m.jobs.Get(ctx, name); err != nil {
		return err
	}

	m.mu.Lock()
	live := m.running[name] != nil
	m.mu.Unlock()

	if live {
		// A crawl that ended between the check above and the stop below is not
		// a failure: it is the thing being asked for, arriving early. Reported
		// as one, `scour job delete` told the operator the job was not running,
		// which they had not claimed, and then deleted nothing. List already
		// treats this race as ordinary; this did not.
		if _, err := m.halt(ctx, name, bus.PhaseStopped); err != nil && !errors.Is(err, errNotRunning) {
			return err
		}
	}

	// The state goes first. A job whose document is gone and whose state is
	// not is a row nothing can ever clear, because every path that clears one
	// starts by reading the document.
	if err := m.states.Delete(ctx, name); err != nil {
		return err
	}
	if err := m.jobs.Delete(ctx, name); err != nil {
		return err
	}

	m.log.InfoContext(ctx, "job deleted", "job", name)
	return nil
}

// Start begins a crawl, seeding the frontier from the job's start URLs.
func (m *Manager) Start(ctx context.Context, name string, fresh bool) (bus.JobStatus, error) {
	return m.begin(ctx, name, fresh, true)
}

// Resume starts a paused job again without re-seeding it.
func (m *Manager) Resume(ctx context.Context, name string) (bus.JobStatus, error) {
	state, err := m.states.Get(ctx, name)
	if err != nil {
		return bus.JobStatus{}, err
	}
	if state.Phase != bus.PhasePaused {
		return bus.JobStatus{}, fmt.Errorf(
			"jobs: %q is %s, not paused. Use start", name, state.Phase)
	}
	return m.begin(ctx, name, false, false)
}

// Stop ends a crawl, keeping the frontier.
func (m *Manager) Stop(ctx context.Context, name string) (bus.JobStatus, error) {
	return m.halt(ctx, name, bus.PhaseStopped)
}

// Pause ends the loop and records that resuming should carry on.
func (m *Manager) Pause(ctx context.Context, name string) (bus.JobStatus, error) {
	return m.halt(ctx, name, bus.PhasePaused)
}

// begin builds a crawl and drives it.
func (m *Manager) begin(ctx context.Context, name string, fresh, seed bool) (bus.JobStatus, error) {
	job, revision, err := m.jobs.Job(ctx, name)
	if err != nil {
		return bus.JobStatus{}, err
	}

	m.mu.Lock()
	switch {
	case m.closed:
		m.mu.Unlock()
		return bus.JobStatus{}, errors.New("jobs: the manager is closing")

	case m.running[name] != nil, m.starting[name]:
		m.mu.Unlock()
		return bus.JobStatus{}, fmt.Errorf("jobs: %q is already running", name)
	}

	// Reserved before anything is built, so a caller that has lost stops here
	// rather than opening a frontier it is about to discard.
	m.starting[name] = true
	m.mu.Unlock()

	defer func() {
		m.mu.Lock()
		delete(m.starting, name)
		m.mu.Unlock()
	}()

	dir := m.dir(name)
	if fresh {
		if err := m.forget(ctx, name); err != nil {
			return bus.JobStatus{}, err
		}
	}

	// The stages are somewhere else, always, and that is what makes this a
	// cluster rather than a `scour crawl` with extra steps. Nothing serving
	// them fails fast with [bus.ErrNoStage] rather than hanging, because NATS
	// answers "no responders" immediately.
	crawl, err := run.New(m.ctx, job, run.Options{
		Dir:   dir,
		Log:   m.log,
		Eval:  m.opts.Eval,
		Open:  func(cfg frontier.Config) (frontier.Frontier, error) { return sqlite.Open(cfg) },
		Fetch: m.conn.NewDownloader(job.Name, m.opts.Bodies, m.opts.Wait),
		Read:  m.conn.NewSpider(job.Name, m.opts.Bodies, m.opts.Wait),
	})
	if err != nil {
		return bus.JobStatus{}, fmt.Errorf("jobs: %q: %w", name, err)
	}

	if seed {
		if _, err := crawl.Seed(ctx); err != nil {
			_ = crawl.Close()
			return bus.JobStatus{}, fmt.Errorf("jobs: %q: %w", name, err)
		}
	}

	work, cancel := context.WithCancel(m.ctx)
	d := &driver{
		name:     name,
		revision: revision,
		crawl:    crawl,
		cancel:   cancel,
		started:  time.Now().UTC(),
		intent:   bus.PhaseStopped,
		done:     make(chan struct{}),
	}

	// Registered before the state is written and before the goroutine starts,
	// so there is no window in which a crawl is running and nothing says so.
	//
	// The check for a driver already running is made again here, and it is not
	// belt and braces. The one at the top of this function releases the lock
	// before building the stages and seeding, which takes long enough for
	// every concurrent caller to walk through it: eight simultaneous starts
	// produced eight drivers on one job, seven of them unreachable because
	// only the last reached the map. Eight schedulers on one frontier is the
	// politeness rule broken exactly as the single-driver design exists to
	// prevent, and the site would have been the first to notice.
	//
	// The control service answers each request on its own goroutine, so two
	// `scour job start` at the same moment is all it took.
	// Counted before it is published, and that ordering is load-bearing. The
	// wait group is what Close waits on, and the driver goes into the map here
	// while the goroutine that increments it only starts after a round trip to
	// the cluster below. A Close landing in that window found the driver,
	// cancelled it, and then waited on a counter still at zero: it returned
	// saying every crawl had stopped, its caller carried on down the shutdown
	// list closing the shared cache and the bus, and drive then started and
	// flushed its exporters into a closed bucket.
	//
	// Every path out of here from this point either starts drive or calls Done.
	m.wg.Add(1)

	m.mu.Lock()
	switch {
	case m.closed:
		m.mu.Unlock()
		m.wg.Done()
		cancel()
		_ = crawl.Close()
		return bus.JobStatus{}, errors.New("jobs: the manager is closing")

	case m.running[name] != nil:
		// Somebody else won the race while this one was building. What it
		// built is closed rather than left running, which is the whole point:
		// the loser must not be a second crawl nobody can stop.
		m.mu.Unlock()
		m.wg.Done()
		cancel()
		_ = crawl.Close()
		return bus.JobStatus{}, fmt.Errorf("jobs: %q is already running", name)
	}
	m.running[name] = d
	m.mu.Unlock()

	if err := m.states.Put(ctx, name, bus.JobState{
		Phase:    bus.PhaseRunning,
		Since:    d.started,
		Revision: revision,
		Driver:   m.opts.Name,
	}); err != nil {
		m.mu.Lock()
		if m.running[name] == d {
			delete(m.running, name)
		}
		m.mu.Unlock()

		// Released, because something may already be waiting on it. This
		// driver was in the map for as long as the write above took, which is
		// a round trip to the cluster, and a stop or a delete arriving in that
		// window takes a handle to it and waits for done. Only drive closes
		// that channel, and drive is never going to run: the waiter sat until
		// its own deadline and then reported that a job which had never
		// started could not be stopped.
		close(d.done)

		m.wg.Done()
		cancel()
		_ = crawl.Close()
		return bus.JobStatus{}, err
	}

	go m.drive(work, d)

	m.log.InfoContext(ctx, "job started", "job", name, "revision", revision, "seeded", seed)
	m.event(bus.JobEvent{
		Name:    name,
		At:      d.started,
		Phase:   bus.PhaseRunning,
		Message: started(seed, fresh),
	})
	return m.Status(ctx, name)
}

// drive runs one crawl to its end and records how it ended.
func (m *Manager) drive(ctx context.Context, d *driver) {
	defer m.wg.Done()
	defer close(d.done)

	// The crawl's own context is released here whatever ended it. Stopping and
	// pausing drain rather than cancel, so nothing else ever calls this: a
	// manager left running while jobs were started and stopped accumulated one
	// live context per cycle, each holding a timer and a parent's child list
	// until the process ended.
	defer d.cancel()

	// Progress is reported from a goroutine of its own rather than from the
	// loop, because the loop is in [run.Run.Do] and this package does not get
	// to put a callback in it. It stops before the crawl is closed, so nothing
	// reads a run that is being torn down.
	ticking := make(chan struct{})
	reported := make(chan struct{})
	go func() {
		defer close(reported)
		m.reporting(ctx, d, ticking)
	}()

	ending, err := d.crawl.Do(ctx)

	// Told to stop, then waited for. Closing the channel is not a join, and the
	// goroutine it stops may be inside a snapshot, reading the frontier that
	// the close below is about to shut: the read then fails, the snapshot
	// reports nothing queued, and the last progress line of a budget-stopped
	// crawl announced "queued 0" to everyone watching. That is the same
	// misreport the closing event was fixed for, arriving one line earlier.
	close(ticking)
	<-reported

	// A context of its own, because the crawl's is very often the reason there
	// is something to record: the state of a job stopped by ctrl-c would be
	// written with the context that stopping cancelled, and so would not be
	// written at all.
	after, done := context.WithTimeout(context.WithoutCancel(ctx), Settle)
	defer done()

	// The counters are read before the crawl is closed, and that ordering is
	// the whole of whether the last number is true. Closing shuts the frontier,
	// so asking afterwards how much is left fails and the snapshot reports
	// zero: a crawl that stopped at its page budget with forty-one URLs still
	// queued announced "queued 0", telling every watcher the site was finished.
	// That is the confusion Ending exists to prevent, arriving by another route.
	//
	// Reading them here loses nothing, because [run.Run.Do] flushes its
	// exporters before it returns. Close flushes what the exporters themselves
	// buffer and does not change these counts.
	final := m.snapshot(after, d)

	// Closed before the state is written, for the reason `scour crawl` closes
	// before printing its summary: a flush that failed must not be contradicted
	// by a phase saying the job is done.
	closeErr := d.crawl.Close()

	m.mu.Lock()
	intent := d.intent
	// Only this driver's own entry, never whatever is in the map under its
	// name. They are the same thing now that starting twice is refused, and
	// this is what stops them silently diverging again: a driver that deleted
	// its successor's entry would leave a crawl running that nothing could
	// find, report or stop.
	if m.running[d.name] == d {
		delete(m.running, d.name)
	}
	m.mu.Unlock()

	state := bus.JobState{
		Since:    time.Now().UTC(),
		Revision: d.revision,
		Driver:   m.opts.Name,
		Ending:   string(ending),
		Last:     &final,
	}
	switch {
	case err != nil:
		state.Phase, state.Error = bus.PhaseFailed, err.Error()
	case closeErr != nil:
		state.Phase, state.Error = bus.PhaseFailed, closeErr.Error()
	case ending == run.Stopped:
		// Somebody asked, and what they asked for is the phase. Only the
		// intention tells a pause from a stop: the loop ends the same way.
		state.Phase = intent
	default:
		state.Phase = bus.PhaseDone
	}

	if err := m.states.Put(after, d.name, state); err != nil {
		m.log.ErrorContext(after, "the job ended and its state could not be recorded",
			"job", d.name, "phase", state.Phase, "error", err)
	}

	m.log.InfoContext(after, "job ended", "job", d.name,
		"phase", state.Phase, "ending", state.Ending, "error", state.Error)

	m.event(bus.JobEvent{
		Name:    d.name,
		At:      state.Since,
		Phase:   state.Phase,
		Message: ended(state),
		Stats:   final,
	})
}

// reporting publishes where a crawl has got to, until it ends.
func (m *Manager) reporting(ctx context.Context, d *driver, until <-chan struct{}) {
	ticker := time.NewTicker(m.opts.Report)
	defer ticker.Stop()

	for {
		select {
		case <-until:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.event(bus.JobEvent{
				Name:  d.name,
				At:    time.Now().UTC(),
				Phase: bus.PhaseRunning,
				Stats: m.snapshot(ctx, d),
			})
		}
	}
}

// halt ends a crawl and waits for it to have ended.
func (m *Manager) halt(ctx context.Context, name string, intent bus.Phase) (bus.JobStatus, error) {
	m.mu.Lock()
	d := m.running[name]
	if d == nil {
		m.mu.Unlock()
		if _, _, err := m.jobs.Get(ctx, name); err != nil {
			return bus.JobStatus{}, err
		}
		return bus.JobStatus{}, fmt.Errorf("jobs: %q %w", name, errNotRunning)
	}
	d.intent = intent
	crawl, done := d.crawl, d.done
	m.mu.Unlock()

	// Drained rather than cancelled, which is the difference between a pause
	// somebody can resume from and one that looks like a hung job.
	//
	// Cancelling aborts the fetches in flight, and an aborted fetch reports
	// nothing to the frontier on purpose, so its URL stays leased for [run.Lease]
	// and a resume finds nothing due for five minutes while reporting itself as
	// running. Draining lets those pages finish and be recorded, so the frontier
	// is left holding nothing. See [run.Run.Drain].
	crawl.Drain()

	// Waited for rather than merely requested. A caller told a job had stopped
	// while its exporters were still flushing would be told something that is
	// not yet true, and the next thing such a caller does is read the output.
	select {
	case <-done:
	case <-ctx.Done():
		return bus.JobStatus{}, fmt.Errorf(
			"jobs: %q was asked to stop and had not finished within the wait", name)
	}
	return m.Status(ctx, name)
}

// snapshot is what a crawl has done, now.
func (m *Manager) snapshot(ctx context.Context, d *driver) bus.JobStats {
	stats := d.crawl.Stats()
	out := bus.JobStats{
		Fetched:  stats.Fetched.Load(),
		Cached:   stats.Cached.Load(),
		Dropped:  stats.Dropped.Load(),
		Failed:   stats.Failed.Load(),
		Items:    stats.Items.Load(),
		Queued:   stats.Queued.Load(),
		Lost:     stats.Lost.Load(),
		Store:    stats.Store.Load(),
		Exported: stats.Exported.Load(),
		Elapsed:  time.Since(d.started),
	}

	// The frontier is asked last and its failure is not the snapshot's. A
	// report that returned nothing because one number could not be read would
	// lose the eight that could.
	waiting, err := d.crawl.Waiting(ctx)
	if err != nil {
		m.log.DebugContext(ctx, "could not read what is left in the frontier",
			"job", d.name, "error", err)
		return out
	}
	out.Waiting = waiting
	return out
}

// waiting is how much a job that is not running has left.
func (m *Manager) waiting(ctx context.Context, name string) (int, error) {
	queue, err := sqlite.Open(frontier.Config{Dir: m.dir(name)})
	if err != nil {
		return 0, fmt.Errorf("jobs: %q: %w", name, err)
	}
	defer func() { _ = queue.Close() }()

	left, err := queue.Len(ctx, name)
	if err != nil {
		return 0, fmt.Errorf("jobs: %q: %w", name, err)
	}
	return left, nil
}

// forget empties a job's frontier, which is what starting fresh means.
func (m *Manager) forget(ctx context.Context, name string) error {
	queue, err := sqlite.Open(frontier.Config{Dir: m.dir(name)})
	if err != nil {
		return fmt.Errorf("jobs: %q: %w", name, err)
	}
	defer func() { _ = queue.Close() }()

	if err := queue.Remove(ctx, name); err != nil {
		return fmt.Errorf("jobs: %q: %w", name, err)
	}
	return nil
}

// dir is where one job's frontier lives.
//
// A directory each rather than one shared database, because a job started
// fresh empties its own and nothing else's, and because two crawls writing one
// SQLite file is the contention this design spent a service avoiding.
func (m *Manager) dir(name string) string {
	return filepath.Join(m.opts.Dir, "jobs", name)
}

// event publishes one job event, reporting a failure to publish rather than
// losing it silently.
func (m *Manager) event(event bus.JobEvent) {
	if err := m.conn.Announce(event); err != nil {
		m.log.Warn("a job event could not be published", "job", event.Name, "error", err)
	}
}

// Close stops every crawl and waits for them.
//
// # Closing twice waits twice, and that is the point
//
// A second caller blocks until the first has finished, rather than seeing the
// closed flag and returning at once. That is what [sync.Once] buys here and it
// is not a nicety: this manager has two closers on the ordinary path. [New]
// starts a watchdog that closes when its context ends, and `scour server` also
// closes it from its shutdown list, so a SIGTERM fires both.
//
// Returning early left the second one believing the crawls were over while the
// first was still waiting for them. Its caller then carried on down the
// shutdown list and closed the shared cache the drivers were still flushing
// their exporters into. [run.Run.Close] carries the same guarantee for the same
// reason, and its comment records what that failure looks like when it happens:
// an object-store backend answering "Bucket has been closed" on work the
// operator had already been told was finished.
func (m *Manager) Close() error {
	m.shut.Do(m.close)
	return nil
}

func (m *Manager) close() {
	m.mu.Lock()
	m.closed = true
	for _, d := range m.running {
		// Stopped rather than paused. A manager going away has not been told
		// anything about intent, and a job that comes back as stopped is
		// started again by hand, which is the safe way round: a job wrongly
		// marked paused would be resumed by somebody expecting it to carry on.
		d.intent = bus.PhaseStopped
		d.cancel()
	}
	m.mu.Unlock()

	// Waited for outside the lock, because the drivers take it on their way
	// out. Holding it here is a deadlock rather than a slow close.
	m.wg.Wait()
	m.stop()
}

// only is the single job a submitted document holds.
//
// One, because the bucket's key is the job's name: a document holding two jobs
// stored under one of their names is a job the cluster serves and a job it
// silently does not. Refused by name, with the names listed, so the fix is
// obvious.
func only(document []byte) (*engine.Job, error) {
	doc, err := engine.Parse(document, "job.hcl")
	if err != nil {
		return nil, fmt.Errorf("jobs: %w", err)
	}
	if err := doc.Validate(); err != nil {
		return nil, fmt.Errorf("jobs: %w", err)
	}

	switch len(doc.Jobs) {
	case 1:
		return doc.Jobs[0], nil
	case 0:
		return nil, errors.New("jobs: the document has no jobs")
	default:
		return nil, fmt.Errorf(
			"jobs: the document holds %d jobs (%s), and a submission is one job. "+
				"Submit them one at a time",
			len(doc.Jobs), strings.Join(doc.Names(), ", "))
	}
}

// refusals renders why a resubmission was refused, one per line.
func refusals(review engine.Review) string {
	lines := make([]string, 0, len(review.Refused))
	for _, change := range review.Refused {
		lines = append(lines, "  "+change.String())
	}
	return strings.Join(lines, "\n")
}

// started says what a start did, for whoever is watching.
func started(seed, fresh bool) string {
	switch {
	case fresh:
		return "started, with the frontier emptied first"
	case seed:
		return "started"
	default:
		return "resumed"
	}
}

// ended says how a crawl ended, in a line.
func ended(state bus.JobState) string {
	switch state.Phase {
	case bus.PhaseFailed:
		return "failed: " + state.Error
	case bus.PhasePaused:
		return "paused"
	case bus.PhaseStopped:
		return "stopped"
	default:
		return state.Ending
	}
}
