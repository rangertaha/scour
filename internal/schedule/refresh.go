// SPDX-License-Identifier: GPL-3.0-or-later

package schedule

import (
	"context"
	"errors"
	"time"

	"github.com/rangertaha/scour/internal/registry"
)

// A crawl asks two scheduling questions and they are not the same one.
//
// Policy answers what comes out of the frontier next, among the URLs already
// waiting. Refresh answers when a URL goes back in: a front page carries
// something different every hour, an archived article never changes again, and
// crawling them at one rate is either wasteful at one end or stale at the other.
//
// Splitting them is what lets a site be crawled once and then kept current
// cheaply, which is a different job from crawling it in the first place.

// ErrNoSchedule is returned by a refresh policy that is registered but not
// written yet. A caller treats it as "nothing is due", not as a failure: an
// unwritten schedule is not a reason to abandon a crawl.
var ErrNoSchedule = errors.New("schedule: refresh policy not implemented yet")

// Node is a node of the crawl graph, as a refresh policy sees it.
//
// It carries what the decision is made from and nothing else. Whether the last
// fetch changed anything is the strongest signal there is, and it is the one a
// naive implementation forgets.
type Node struct {
	// URL is the node's identity.
	URL string
	// Role is what the crawl graph decided this URL is, when anything has:
	// a hub and a detail page earn different rates.
	Role string
	// FetchedAt is when it was last retrieved, zero when it never was.
	FetchedAt time.Time
	// ChangedAt is when its body was last seen to differ. Zero when it has
	// never changed, or has only been fetched once.
	ChangedAt time.Time
	// Fetches is how many times it has been retrieved.
	Fetches int
	// Changes is how many of those returned something new. A page fetched
	// forty times and changed once wants a slower rate than one that changes
	// every time, and no fixed interval discovers that.
	Changes int
}

// Refresh decides when nodes are queued again.
type Refresh interface {
	// Name identifies the implementation, for logs and configuration.
	Name() string

	// Due returns when each node should next be fetched, keyed by URL.
	//
	// A node left out is not due and is not scheduled. A time in the past
	// means due now. The whole set is passed rather than one node at a time
	// because a rate is usually relative: keeping a crawl inside a budget
	// means deciding across nodes, not for each alone.
	Due(ctx context.Context, nodes []Node) (map[string]time.Time, error)
}

// RefreshConfig is what a refresh policy is built from.
type RefreshConfig struct {
	// Spec is the schedule as configuration wrote it, for policies that take
	// one: a cron expression, an interval, whatever the implementation reads.
	Spec string
	// Budget caps how many nodes one pass may schedule. Zero is the
	// implementation's default; negative means no limit.
	Budget int
	// Now is the clock, so a schedule can be tested without waiting. Zero
	// means time.Now.
	Now func() time.Time
}

// refreshReg holds the implementations. See internal/registry for the shape
// every extension point in scour shares, and for how to add one.
var refreshReg = registry.New[RefreshConfig, Refresh]("refresh policy")

// RegisterRefresh adds an implementation, from init.
func RegisterRefresh(name string, f registry.Factory[RefreshConfig, Refresh]) {
	refreshReg.Register(name, f)
}

// NewRefresh builds a registered implementation.
func NewRefresh(name string, cfg RefreshConfig) (Refresh, error) { return refreshReg.New(name, cfg) }

// RefreshNames lists what is registered.
func RefreshNames() []string { return refreshReg.Names() }

// HasRefresh reports whether a name is registered.
func HasRefresh(name string) bool { return refreshReg.Has(name) }

// cron schedules nodes by a calendar expression.
//
// Registered and not written. The name and the config shape are fixed now so
// that the store, the dispatcher and the command line can be built against
// them, and so that a spec in a config file resolves to "this is planned"
// rather than to "unknown refresh policy", which reads as a typo.
//
// What it will need, recorded while it is still cheap to say: an expression per
// role rather than one for the crawl, because a hub and an archived detail page
// are the two ends of the problem; the change history above, so a page that
// never changes earns a slower rate than its role suggests; and a cap, because
// a cron that fires across a frontier of 150,000 URLs schedules a crawl nobody
// asked for.
type cron struct{ spec string }

func (c cron) Name() string { return "cron" }

func (c cron) Due(context.Context, []Node) (map[string]time.Time, error) {
	return nil, ErrNoSchedule
}

func init() {
	RegisterRefresh("cron", func(cfg RefreshConfig) (Refresh, error) {
		return cron{spec: cfg.Spec}, nil
	})
}
