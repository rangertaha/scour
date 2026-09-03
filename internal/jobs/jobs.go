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
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
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

	// nodes is who is in the cluster, with a TTL. Consulted only to tell a
	// state row that is true from one whose driver has died: see
	// [Manager.elsewhere].
	nodes *bus.Nodes

	// instance identifies this process, where opts.Name identifies the
	// machine. Announced in the registry for as long as this manager lives,
	// and written into every state row it starts, so another node can ask
	// whether the process that wrote a row is still there.
	//
	// A name cannot answer that: a supervisor restarting a node hands the new
	// process the old one's name, and two started by hand share it outright.
	// Deciding on the name alone made a restart strand a job, and then made a
	// second manager sharing a name clear the first one's live row and put two
	// drivers on one crawl.
	instance string

	// leave stops announcing this instance.
	leave func()

	// ctx is the manager's own lifetime, and every crawl runs under it. Held
	// rather than taken per call because a crawl must outlive the request that
	// started it: driving from the caller's context meant a crawl that ended
	// the moment `scour job start` returned.
	ctx  context.Context
	stop context.CancelFunc

	mu      sync.Mutex
	running map[string]*driver
	closed  bool

	// claimed is the names an operation currently owns, and what it is doing
	// with them: see [Manager.claim].
	claimed map[string]string

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
	nodes, err := conn.OpenNodes(ctx)
	if err != nil {
		return nil, err
	}

	// Detached from the caller's context on purpose, and cancelled by Close.
	// A crawl that ended when the request that started it returned is what
	// deriving from the caller gets you.
	own, stop := context.WithCancel(context.WithoutCancel(ctx))

	// This process's own identity, which is what another node asks about
	// before acting on a row this one wrote. See [Manager.instance].
	instance, err := newInstance()
	if err != nil {
		stop()
		return nil, err
	}

	m := &Manager{
		conn:     conn,
		opts:     opts,
		log:      opts.Log.With("service", opts.Name, "instance", instance),
		jobs:     jobsKV,
		states:   states,
		nodes:    nodes,
		instance: instance,
		ctx:      own,
		stop:     stop,
		running:  map[string]*driver{},
		claimed:  map[string]string{},
	}

	// Announced for as long as this manager lives, and gone when it is. The
	// registry's TTL is what makes "is the process that wrote this row still
	// there" a question any node can answer.
	leave, err := nodes.Announce(own, DriverKey(instance), []byte(opts.Name))
	if err != nil {
		stop()
		return nil, err
	}
	m.leave = leave

	// Any state row that says this manager is driving a job is a row from
	// before this manager existed, because it has not started anything yet.
	//
	// Without this a crash was permanent. [Manager.elsewhere] decides whether a
	// running row is true by asking whether the node it names is still in the
	// cluster, and a node that is SIGKILLed and restarted by its supervisor
	// re-announces under the same name well inside the registry's TTL. The row
	// then looked live to every other driver: start, stop and delete were all
	// refused, from every node except the one that had just come back and knew
	// it was not driving it - and control requests are queue-distributed, so
	// which node answered was NATS's choice, not the operator's. The doc on
	// elsewhere says "a driver that is gone is gone", and that is exactly the
	// case where the name outlives the process.
	//
	// Only the node holding the name may say this, which is what makes it safe:
	// nobody else can tell a restart from a peer that is still working.
	if err := m.reconcile(own); err != nil {
		return nil, err
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
		state.Instance = m.instance
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

	// Not this node's to answer. The counters live in the driver and the
	// frontier lives on its disk, so a node that is not driving this job has
	// neither: it reported the previous run's counters, or nothing at all,
	// plus whatever its own local frontier directory happened to hold.
	//
	// Control requests are queue-distributed, so `scour job stats` landed on
	// whichever node NATS picked and returned zeros for a crawl that was
	// fetching - while `scour job status` on that same node correctly said it
	// was running. Two contradictory answers from one node is what asking the
	// question in one place was meant to retire. See [Manager.elsewhere].
	if driver, err := m.elsewhere(ctx, name); err != nil {
		return bus.JobStats{}, err
	} else if driver != "" {
		return bus.JobStats{}, fmt.Errorf(
			"jobs: %q is running on %s, and this is %s. Ask that node", name, driver, m.opts.Name)
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

	// Claimed like every other operation that writes a job. The document and
	// the state row are two writes, and a delete answered between them left a
	// state row with no document behind - which is the row nothing can ever
	// clear, because every path that clears one starts by reading the
	// document. Create was the one mutating operation left outside the claim
	// when the claim was introduced, so the class it was meant to close was
	// still open on exactly this pair of writes.
	release, err := m.claim(job.Name, "creating")
	if err != nil {
		return bus.JobStatus{}, err
	}
	defer release()

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

	release, err := m.claim(submitted.Name, "updating")
	if err != nil {
		return bus.JobStatus{}, err
	}
	defer release()

	// Is this job running, anywhere.
	//
	// Not "is anything working on it": this holds the claim, and asking the
	// broader question meant asking about a reservation this call had just
	// taken, so the answer was always yes - every stopped job was reviewed as
	// if it were running, and the refusal said so when it was not.
	//
	// And not the local driver map either. Control requests are answered in a
	// queue group, so a second `scour server` is a standby that shares the
	// load and a job running on one node has its update answered by another.
	// The map is per-manager; a standby saw no driver, skipped the review, and
	// wrote the document - the `mutation` policy bypassed on exactly the crawl
	// it exists to protect, by which node NATS happened to pick.
	//
	// The recorded phase is the shared truth. The local driver still wins over
	// it, for the reason [Manager.Status] gives: they agree unless a manager
	// died between starting a crawl and recording it, and then the crawl in
	// front of us is the truth.
	driver, err := m.elsewhere(ctx, submitted.Name)
	if err != nil {
		return bus.JobStatus{}, err
	}
	live := driver != ""

	m.mu.Lock()
	if m.running[submitted.Name] != nil {
		live = true
	}
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

	// Claimed for the whole delete, not for the check alone. Draining a live
	// crawl takes as long as its pages in flight, and a start answered in that
	// window seeded the frontier that the forget below then emptied: the new
	// crawl ended "finished, fetched 0". See [Manager.claim].
	release, err := m.claim(name, "deleting")
	if err != nil {
		return err
	}
	defer release()

	// A crawl that ended between the check above and the stop below is not a
	// failure: it is the thing being asked for, arriving early. Reported as
	// one, `scour job delete` told the operator the job was not running, which
	// they had not claimed, and then deleted nothing. List already treats this
	// race as ordinary; this did not.
	if _, err := m.end(ctx, name, bus.PhaseStopped); err != nil && !errors.Is(err, errNotRunning) {
		return err
	}

	// The frontier too, because delete means delete.
	//
	// It used to be left, on the reasoning that a job recreated under the same
	// name should carry on. That reasoning is what `stop` is for. What it
	// actually produced was a job somebody deleted, rewrote and started, whose
	// every start URL was already recorded as finished: Seed added nothing, the
	// workers leased nothing, and the run ended "finished" with fetched 0 and
	// items 0 - indistinguishable from a site that had gone dark, and fixable
	// only by knowing to pass --fresh to a job that had never been run.
	//
	// Reported and not fatal. The document is what the cluster serves, and
	// refusing to delete it because a file could not be tidied would leave the
	// job running tomorrow.
	if err := m.forget(ctx, name); err != nil {
		m.log.WarnContext(ctx, "the job is deleted and its frontier was not emptied",
			"job", name, "error", err)
	}

	// The state goes before the document. A job whose document is gone and
	// whose state is not is a row nothing can ever clear, because every path
	// that clears one starts by reading the document.
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
//
// Claimed before the phase is read, not after. Reading first and deciding on
// what it said is the shape [Manager.claim] exists to stop: the answer can be
// stale by the time it is acted on, and a resume that reported "is running,
// not paused" about a job another request was in the middle of deleting told
// the operator something that was true a moment ago and useless now.
func (m *Manager) Resume(ctx context.Context, name string) (bus.JobStatus, error) {
	release, err := m.claim(name, "resuming")
	if err != nil {
		return bus.JobStatus{}, err
	}
	defer release()

	state, err := m.states.Get(ctx, name)
	if err != nil {
		return bus.JobStatus{}, err
	}
	if state.Phase != bus.PhasePaused {
		return bus.JobStatus{}, fmt.Errorf(
			"jobs: %q is %s, not paused. Use start", name, state.Phase)
	}

	// The queue is on the disk of the node that paused it, and resuming means
	// carrying on with it. A paused row is not running, so [Manager.elsewhere]
	// says nothing about it - and a resume answered by another node opened an
	// empty frontier, seeded nothing, finished immediately and wrote "done"
	// over the paused row. The operator was told the crawl had finished the
	// site, and the URLs it had queued could never be resumed, because the row
	// that said where they were was gone.
	if state.Driver != "" && state.Driver != m.opts.Name {
		return bus.JobStatus{}, fmt.Errorf(
			"jobs: %q was paused on %s and its queue is there, and this is %s. Ask that node, "+
				"or use start --fresh to begin again here", name, state.Driver, m.opts.Name)
	}

	return m.launch(ctx, name, false, false)
}

// Stop ends a crawl, keeping the frontier.
func (m *Manager) Stop(ctx context.Context, name string) (bus.JobStatus, error) {
	return m.halt(ctx, name, bus.PhaseStopped)
}

// Pause ends the loop and records that resuming should carry on.
func (m *Manager) Pause(ctx context.Context, name string) (bus.JobStatus, error) {
	return m.halt(ctx, name, bus.PhasePaused)
}

// reconcile clears the running rows this manager's own name is on.
//
// See the call site for why. A row that cannot be read is left alone and
// reported: refusing to start is better than starting a second driver on a
// guess.
func (m *Manager) reconcile(ctx context.Context) error {
	names, err := m.jobs.Names(ctx)
	if err != nil {
		return err
	}

	here, err := m.nodes.Here(ctx)
	if err != nil {
		return fmt.Errorf("jobs: cannot read the cluster to reconcile: %w", err)
	}

	for _, name := range names {
		state, err := m.states.Get(ctx, name)
		if err != nil {
			return err
		}
		if state.Phase != bus.PhaseRunning || state.Driver != m.opts.Name {
			continue
		}
		// And the process that wrote it is gone. Clearing on the name alone
		// cleared a live row whenever two managers shared a name - the default
		// is the hostname, so two `scour server --drive` on one machine
		// collide by default - and the standby was then free to start a second
		// driver on a job that was still being crawled, which is the one thing
		// the whole arrangement exists to prevent.
		if state.Instance != "" {
			if _, alive := here[DriverKey(state.Instance)]; alive {
				continue
			}
		}

		m.log.WarnContext(ctx, "a previous run of this driver did not finish; recording it as stopped",
			"job", name, "since", state.Since)

		state.Phase = bus.PhaseStopped
		state.Since = time.Now().UTC()
		state.Driver = ""
		state.Ending = "the driver stopped without recording an ending"
		if err := m.states.Put(ctx, name, state); err != nil {
			return err
		}
	}
	return nil
}

// DriverKey is how a driving manager appears in the node registry.
//
// Prefixed so it cannot collide with a node's own entry, and so that
// `scour cluster list` can leave it out: it is not a machine an operator would
// go and look at, it is a liveness token for one process.
func DriverKey(instance string) string { return "drive-" + instance }

// newInstance is a fresh identity for one manager process.
func newInstance() (string, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("jobs: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

// elsewhere names the node driving this job, when that node is not this one.
//
// Empty when this manager holds the driver, when nothing is driving the job,
// and when the node the state row names has left the cluster.
//
// # Why this exists
//
// Because "is this job running" was asked three times and answered from
// `m.running`, which is this process's own map. Control requests are answered
// in a NATS queue group, so a second `scour server --drive` is a standby that
// shares the load and any request for a job can land on the node that is not
// driving it. Each of the three then did something wrong:
//
//   - start built a second driver on its own frontier and crawled the same
//     site again. Two schedulers handing out one host cannot honour a crawl
//     delay between them, which is the politeness rule the single-driver
//     design exists for, and the site would have been the first to notice.
//   - stop and pause answered "is not running" while the crawl carried on,
//     and `scour job status` said running, so the operator was told two
//     contradictory things and had no command that worked.
//   - delete swallowed that same "is not running", removed the document and
//     the state row, and left the crawl running: when it ended it wrote a
//     state row for a document that no longer existed - the row nothing can
//     ever clear, reached from the cluster side instead of the concurrency
//     side.
//
// Update was fixed for this one pass earlier by reading the recorded phase.
// Fixing that one site did not retire the class, so the question is asked in
// one place now and the answer is the same wherever it is asked.
//
// # Why the node registry
//
// Because the phase alone cannot tell a crawl that is running from a manager
// that died holding one. That row outlives the process that wrote it, and
// refusing every operation on its say-so would strand the job for good.
// [bus.NodesBucket] has a TTL for exactly this: a driver that is gone is gone,
// and its state row is then a fact about the past.
func (m *Manager) elsewhere(ctx context.Context, name string) (string, error) {
	m.mu.Lock()
	mine := m.running[name] != nil
	m.mu.Unlock()
	if mine {
		return "", nil
	}

	state, err := m.states.Get(ctx, name)
	if err != nil {
		return "", err
	}
	// Mine by instance, not by name. Two managers can share a name - the
	// default is the hostname - and one that read a twin's row as its own
	// answered "nothing is driving it" and started a second crawl on a job the
	// twin was still fetching.
	//
	// A row with no instance came from an older build, and the name is the
	// best that can be said about it.
	ours := state.Instance == m.instance
	if state.Instance == "" {
		ours = state.Driver == m.opts.Name
	}
	if state.Phase != bus.PhaseRunning || state.Driver == "" || ours {
		// A running phase with no driver recorded is from a build old enough
		// not to have written one. Treated as nothing driving it, because the
		// alternative is refusing every operation on a job nobody can name a
		// driver for.
		return "", nil
	}

	here, err := m.nodes.Here(ctx)
	if err != nil {
		// The registry is not reachable. Reported rather than guessed: saying
		// "nothing is driving it" would start a second crawl and saying
		// "something is" would strand the job.
		return "", fmt.Errorf("jobs: %q: cannot tell whether %s is still driving it: %w",
			name, state.Driver, err)
	}

	// The process, not the machine. A name outlives the process that held it -
	// a supervisor hands the restarted node the old one's name, and two
	// started by hand share it outright - so asking about the name answered
	// "yes" for a driver that had died and "no" for nobody at all.
	//
	// A row with no instance was written by an older build, and the name is
	// the best that can be said about it.
	key, what := state.Driver, "driver"
	if state.Instance != "" {
		key, what = DriverKey(state.Instance), "instance"
	}
	if _, alive := here[key]; !alive {
		m.log.InfoContext(ctx, "the "+what+" recorded for this job has left the cluster",
			"job", name, "driver", state.Driver, "instance", state.Instance)
		return "", nil
	}
	return state.Driver, nil
}

// claim reserves a job name for one operation and returns what gives it back.
//
// # Why a claim and not a check
//
// Because every mutating operation here is "read this job's state, decide, act
// on it", the state is shared, and each of them used to hold the lock for the
// read alone. Three passes of this have now been fixed one window at a time and
// the fourth was the same shape again, so the reservation is now the whole
// operation rather than a flag one of them sets:
//
//   - Eight simultaneous starts built eight drivers on one job, seven of them
//     unreachable, because a driver reaches `running` only once its stages are
//     built and its frontier is open. Eight schedulers on one frontier is the
//     politeness rule broken exactly as the single-driver design prevents.
//   - A delete landing in that build window returned success and removed the
//     document while begin carried on and wrote a running phase for a job that
//     no longer existed: a crawl nothing can find, and a state row nothing can
//     ever clear, because every path that clears one begins by reading the
//     document.
//   - An update landing in it skipped the mutation review and applied a change
//     to the revision that was about to start.
//   - A delete that got as far as draining a live crawl released the lock to do
//     it. A start answered in that window seeded the frontier afresh, and the
//     delete then resumed and emptied it: the new crawl ended "finished,
//     fetched 0".
//
// The window is not the same one each time and patching them individually has
// not converged, which is what says the shape is wrong rather than the code.
//
// # Why it refuses rather than waits
//
// Because these are control requests, each answered on its own goroutine, and
// the thing being waited for can be a crawl draining its pages in flight. A
// caller told "busy, try again" can decide; a caller blocked for a minute
// inside `scour job delete` cannot tell that from a hung cluster.
func (m *Manager) claim(name, doing string) (func(), error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.closed {
		return nil, errors.New("jobs: the manager is closing")
	}
	if other := m.claimed[name]; other != "" {
		return nil, fmt.Errorf("jobs: %q is busy: another request is %s it", name, other)
	}
	m.claimed[name] = doing

	return func() {
		m.mu.Lock()
		delete(m.claimed, name)
		m.mu.Unlock()
	}, nil
}

// begin builds a crawl and drives it.
// begin claims a job and builds a crawl for it.
//
// Resume does the same thing without this wrapper, because it holds the claim
// already: the phase it decides on has to be read under the claim, not before
// it. See [Manager.claim].
func (m *Manager) begin(ctx context.Context, name string, fresh, seed bool) (bus.JobStatus, error) {
	// Claimed before anything is read or built, so a caller that has lost
	// stops here rather than opening a frontier it is about to discard, and so
	// that nothing else acts on the job while it is being built.
	release, err := m.claim(name, "starting")
	if err != nil {
		return bus.JobStatus{}, err
	}
	defer release()

	return m.launch(ctx, name, fresh, seed)
}

// launch builds a crawl and drives it. The caller holds the job's claim.
func (m *Manager) launch(ctx context.Context, name string, fresh, seed bool) (bus.JobStatus, error) {
	job, revision, err := m.jobs.Job(ctx, name)
	if err != nil {
		return bus.JobStatus{}, err
	}

	m.mu.Lock()
	running := m.running[name] != nil
	m.mu.Unlock()
	if running {
		return bus.JobStatus{}, fmt.Errorf("jobs: %q is already running", name)
	}

	// And not on another node either. See [Manager.elsewhere]: this used to
	// ask the local map alone, so a start answered by a standby built a second
	// driver on its own frontier and crawled the same site again.
	if driver, err := m.elsewhere(ctx, name); err != nil {
		return bus.JobStatus{}, err
	} else if driver != "" {
		return bus.JobStatus{}, fmt.Errorf("jobs: %q is already running on %s", name, driver)
	}

	dir := m.dir(name)
	if fresh {
		if err := m.forget(ctx, name); err != nil {
			return bus.JobStatus{}, err
		}
	}

	// How long each stage has to answer, from the document rather than from a
	// manager-wide default.
	//
	// This is the third time the class has been found: the document's
	// `external_timeout` was parsed, defaulted, validated and printed by
	// `scour job show`, and the request was bounded by bus.Timeout regardless.
	// Twice it was fixed by wiring the value somewhere that displays it. A job
	// saying `external_timeout = "10m"` had its pages failed at two minutes,
	// each failure spending a frontier attempt, until the URL was abandoned -
	// while the node answered every time.
	fetchWait, err := job.Downloader.ExternalWait()
	if err != nil {
		return bus.JobStatus{}, fmt.Errorf("jobs: %q: %w", name, err)
	}
	readWait, err := job.Spider.ExternalWait()
	if err != nil {
		return bus.JobStatus{}, fmt.Errorf("jobs: %q: %w", name, err)
	}
	// A manager-wide override still wins, which is what a test setting a short
	// wait is asking for.
	if m.opts.Wait > 0 {
		fetchWait, readWait = m.opts.Wait, m.opts.Wait
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
		Fetch: m.conn.NewDownloader(job.Name, m.opts.Bodies, fetchWait),
		Read:  m.conn.NewSpider(job.Name, m.opts.Bodies, readWait),
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
	m.mu.Lock()
	switch {
	case m.closed:
		m.mu.Unlock()
		cancel()
		_ = crawl.Close()
		return bus.JobStatus{}, errors.New("jobs: the manager is closing")

	case m.running[name] != nil:
		// Somebody else won the race while this one was building. What it
		// built is closed rather than left running, which is the whole point:
		// the loser must not be a second crawl nobody can stop.
		m.mu.Unlock()
		cancel()
		_ = crawl.Close()
		return bus.JobStatus{}, fmt.Errorf("jobs: %q is already running", name)
	}

	// Counted under the same lock that sets `closed`, so a positive Add can
	// never land while Close is waiting: either this gets the lock first and
	// Close then sees the driver and waits for it, or Close gets it first and
	// the case above returns. Lifting the counter off zero beside a live Wait
	// is a documented misuse that panics the process, and the window was real -
	// the last driver's Done takes it to zero while a start is in flight.
	//
	// Every path out of here from this point either starts drive or calls Done.
	m.wg.Add(1)
	m.running[name] = d
	m.mu.Unlock()

	if err := m.states.Put(ctx, name, bus.JobState{
		Phase:    bus.PhaseRunning,
		Since:    d.started,
		Revision: revision,
		Driver:   m.opts.Name,
		Instance: m.instance,
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
		Instance: m.instance,
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

// halt claims a job and ends its crawl.
//
// Delete does the same thing without this wrapper, because it holds the claim
// already and has more to do under it. See [Manager.claim].
func (m *Manager) halt(ctx context.Context, name string, intent bus.Phase) (bus.JobStatus, error) {
	release, err := m.claim(name, phrase(intent))
	if err != nil {
		return bus.JobStatus{}, err
	}
	defer release()

	return m.end(ctx, name, intent)
}

// phrase is what an intent is called in the message a losing caller gets.
func phrase(intent bus.Phase) string {
	if intent == bus.PhasePaused {
		return "pausing"
	}
	return "stopping"
}

// end ends a crawl and waits for it to have ended. The caller holds the job's
// claim, so nothing can start one underneath this.
func (m *Manager) end(ctx context.Context, name string, intent bus.Phase) (bus.JobStatus, error) {
	m.mu.Lock()
	d := m.running[name]
	if d == nil {
		m.mu.Unlock()

		if _, _, err := m.jobs.Get(ctx, name); err != nil {
			return bus.JobStatus{}, err
		}

		// A crawl this node cannot reach is not a crawl that is not running.
		// Saying so was worse than useless: `scour job stop` reported "is not
		// running" while the pages kept arriving and `scour job status` said
		// running, and delete swallowed the same answer and removed the
		// document out from under the crawl. See [Manager.elsewhere].
		driver, err := m.elsewhere(ctx, name)
		if err != nil {
			return bus.JobStatus{}, err
		}
		if driver != "" {
			return bus.JobStatus{}, fmt.Errorf(
				"jobs: %q is running on %s, and this is %s. Ask that node", name, driver, m.opts.Name)
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
	queue, ok, err := m.queue(name)
	if err != nil || !ok {
		// A job that has never run has nothing waiting, which is the true
		// answer and not an error.
		return 0, err
	}
	defer func() { _ = queue.Close() }()

	left, err := queue.Len(ctx, name)
	if err != nil {
		return 0, fmt.Errorf("jobs: %q: %w", name, err)
	}
	return left, nil
}

// queue opens a job's frontier if it has one, and says it has none rather than
// making one.
//
// [sqlite.Open] creates the directory and the database, which is what a crawl
// about to start wants and what every other caller here does not. Asking how
// many URLs a job has waiting created an empty frontier for a job that had
// never run, and deleting such a job created one on its way out and left the
// directory behind after the job was gone. Both looked like nothing had
// happened, because an empty frontier answers every question the same way a
// missing one should.
func (m *Manager) queue(name string) (*sqlite.Frontier, bool, error) {
	if _, err := os.Stat(filepath.Join(m.dir(name), sqlite.File)); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("jobs: %q: %w", name, err)
	}

	queue, err := sqlite.Open(frontier.Config{Dir: m.dir(name)})
	if err != nil {
		return nil, false, fmt.Errorf("jobs: %q: %w", name, err)
	}
	return queue, true, nil
}

// forget empties a job's frontier, which is what starting fresh means.
func (m *Manager) forget(ctx context.Context, name string) error {
	queue, ok, err := m.queue(name)
	if err != nil || !ok {
		// Nothing to forget, and making one to empty it is how a deleted job
		// left a frontier behind.
		return err
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

	// Gone from the registry before the context that keeps the announcement
	// alive is cancelled, so another node stops seeing this instance as soon
	// as it has actually stopped driving anything.
	if m.leave != nil {
		m.leave()
	}
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
