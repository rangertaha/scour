// SPDX-License-Identifier: GPL-3.0-or-later

// Package schedule decides what the frontier hands out next.
//
// A crawl has more URLs waiting than it will ever fetch, so the order they come
// out in is most of what a crawl is. scour's default is best first: the highest
// scoring URL, which is what makes a focused crawl focused. That is a policy,
// not a law, and other work wants other policies. An archival crawl wants
// breadth first so the result is a complete level rather than a deep spur. A
// crawl checking whether a site changed wants the freshest. A sampling run
// wants random, because anything else is a biased sample.
//
// # What a policy may decide
//
// The order, from a closed set. Not a SQL fragment: the frontier is a table
// with 150,000 rows in it, so the choice has to be made by the database, and a
// policy handing SQL to the store would be handing it an injection point and a
// dependency on the schema at once.
//
// A policy does not decide politeness. Which hosts are cooling is worked out
// from when each was last fetched, and a policy that could override that could
// hammer one server by choosing badly.
package schedule

import "github.com/rangertaha/scour/internal/registry"

// Order is how the waiting URLs are sorted. The frontier takes the first.
type Order int

const (
	// ByScore is highest score first, ties broken by insertion order. This is
	// what makes a focused crawl focused, and is the default.
	ByScore Order = iota
	// Breadth is insertion order, oldest first. Every URL at one depth is
	// fetched before any at the next, which is what an archival crawl wants.
	Breadth
	// Depth is insertion order reversed, newest first, following a spur down
	// before returning. Cheap to run and useful for reaching a deep example
	// quickly.
	Depth
	// Random draws without regard to score or age, which is the only way to
	// sample a site without the sample being shaped by the scorer.
	Random
)

// String names the order as configuration spells it.
func (o Order) String() string {
	switch o {
	case Breadth:
		return "breadth"
	case Depth:
		return "depth"
	case Random:
		return "random"
	default:
		return "score"
	}
}

// Policy decides the order a crawl's frontier is drained in.
//
// It is asked once per lease rather than once per crawl, so a policy may change
// its mind: a crawl that starts breadth first and switches to best first once
// the scorer has something to say is a policy, not a special case.
type Policy interface {
	// Name identifies the implementation, for logs and configuration.
	Name() string
	// Order is how to sort what is waiting.
	Order(State) Order
}

// State is what a policy knows when it is asked.
type State struct {
	// Item is which item's frontier is being drained.
	Item uint
	// Fetched is how many pages this crawl has taken so far.
	Fetched int
	// Queued is how many URLs are waiting, at the last count.
	Queued int
	// Trained reports whether a model exists. Before one does, every score is
	// equal, so best first is breadth first with extra steps.
	Trained bool
}

// Config is what a policy is built from.
type Config struct {
	// Switch is how many pages a policy that changes its mind should wait
	// before doing so. Zero is the implementation's default.
	Switch int
}

// reg holds the implementations. See internal/registry for the shape every
// extension point in scour shares, and for how to add one.
var reg = registry.New[Config, Policy]("scheduler").Default("best")

// Register adds an implementation, from init.
func Register(name string, f registry.Factory[Config, Policy]) { reg.Register(name, f) }

// New builds a registered implementation.
func New(name string, cfg Config) (Policy, error) { return reg.New(name, cfg) }

// Names lists what is registered.
func Names() []string { return reg.Names() }

// Has reports whether a name is registered.
func Has(name string) bool { return reg.Has(name) }
