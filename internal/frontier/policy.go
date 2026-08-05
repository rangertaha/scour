// SPDX-License-Identifier: GPL-3.0-or-later

package frontier

import (
	"fmt"
	"math/rand/v2"
	"sort"
	"strings"
)

// Policy decides which of the waiting requests comes out next.
//
// A closed set of orderings rather than a comparison function, because the
// durable implementation has to express this as an ORDER BY: a policy handing
// SQL to a database would be handing it an injection point and a dependency on
// the schema at once.
type Policy interface {
	// Name identifies it, as configuration spells it.
	Name() string
	// Less reports whether a should be handed out before b.
	Less(a, b Request) bool
}

// DefaultPolicy is what an unconfigured scheduler uses. Best first, which is
// what makes a focused crawl focused.
const DefaultPolicy = "priority"

// Policies returns a policy by name.
//
// A closed set rather than a registry, because these four are not extension
// points: they are the orderings a durable frontier can express as an index,
// and a fifth would need the store to change with it.
func Policies(name string) (Policy, error) {
	switch strings.TrimSpace(name) {
	case "", DefaultPolicy:
		return priority{}, nil
	case "breadth":
		return breadth{}, nil
	case "depth":
		return depth{}, nil
	case "random":
		return random{}, nil
	default:
		return nil, fmt.Errorf("frontier: no policy %q, have %s",
			name, strings.Join(PolicyNames(), ", "))
	}
}

// PolicyNames lists the orderings, sorted.
func PolicyNames() []string {
	out := []string{DefaultPolicy, "breadth", "depth", "random"}
	sort.Strings(out)
	return out
}

// priority is best first: the highest scoring URL, which is what a focused
// crawl is for. Ties break on discovery order so the crawl is reproducible.
type priority struct{}

func (priority) Name() string { return "priority" }
func (priority) Less(a, b Request) bool {
	if a.Score != b.Score {
		return a.Score > b.Score
	}
	return a.Discovered.Before(b.Discovered)
}

// breadth is level by level: every URL at one depth before any at the next,
// which is what an archival crawl wants.
type breadth struct{}

func (breadth) Name() string { return "breadth" }
func (breadth) Less(a, b Request) bool {
	if a.Depth != b.Depth {
		return a.Depth < b.Depth
	}
	return a.Discovered.Before(b.Discovered)
}

// depth follows a spur down before returning, which is cheap to run and useful
// for reaching a deep example quickly.
type depth struct{}

func (depth) Name() string { return "depth" }
func (depth) Less(a, b Request) bool {
	if a.Depth != b.Depth {
		return a.Depth > b.Depth
	}
	return a.Discovered.After(b.Discovered)
}

// random draws without regard to score or age.
//
// The only way to sample a site without the sample being shaped by the scorer,
// which is what makes it worth having: every other policy answers a question
// the scorer has already had an opinion about.
type random struct{}

func (random) Name() string               { return "random" }
func (random) Less(Request, Request) bool { return rand.IntN(2) == 0 }
