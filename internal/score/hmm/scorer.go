// SPDX-License-Identifier: GPL-3.0-or-later

package hmm

import (
	"sync"

	"github.com/rangertaha/scour/internal/score"
)

// hops is how far ahead the chain looks when crediting a link.
//
// Two is the useful range: a hub's children, and its grandchildren through a
// pagination step. Looking further makes every link on a site score alike,
// because from far enough away everything reaches a detail page eventually.
const hops = 2

// Scorer combines a per-URL scorer with the crawl chain.
//
// The base scorer answers "does this URL look like a record page", which is
// the wrong question for a hub. The chain answers "does this URL lead to one",
// which is the right one. Neither is sufficient alone: the chain has nothing
// to say about a link it cannot place, and the base scorer has nothing to say
// about a page whose value is entirely in its children.
type Scorer struct {
	base  score.Scorer
	chain *Chain

	mu    sync.RWMutex
	roles map[string]Role // parent URL to its decoded role
	mean  float64         // cached reach of an average role
}

// NewScorer wraps a base scorer with a chain. Parent roles come from the last
// crawl's decoding; a link whose parent has no role recorded falls back to the
// base scorer alone.
func NewScorer(base score.Scorer, chain *Chain, roles map[string]Role) *Scorer {
	if chain == nil {
		chain = Default()
	}
	if roles == nil {
		roles = map[string]Role{}
	}
	return &Scorer{base: base, chain: chain, roles: roles}
}

// Name implements [score.Scorer].
func (s *Scorer) Name() string { return "hmm" }

// SetRole records the decoded role of a page, so links found on it can be
// credited for where they lead.
func (s *Scorer) SetRole(url string, r Role) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.roles[url] = r
}

// Role returns a page's decoded role.
func (s *Scorer) Role(url string) (Role, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.roles[url]
	return r, ok
}

// Score implements [score.Scorer].
//
// The two probabilities are combined as independent evidence, in odds form:
// a link that looks promising on a page that leads somewhere scores higher
// than either signal alone, and a link that looks promising on a dead end is
// discounted rather than trusted.
func (s *Scorer) Score(f score.Features) float64 {
	base := s.base.Score(f)

	// What this URL turned out to be last time is observation, not inference,
	// so it wins outright. A page decoded as a hub holds no records and every
	// per-URL signal says to skip it; its value is what it leads to, and that
	// is the whole reason this package exists.
	if role, ok := s.Role(f.URL); ok {
		if value := s.value(role); value > base {
			return value
		}
		return base
	}

	// An unseen URL is judged by the company it keeps: found on a hub it is
	// promising, found on a dead end it is not.
	if role, ok := s.Role(f.Parent); ok {
		return combine(base, s.calibrate(s.chain.Reach(role, hops)))
	}
	return base
}

// value is the expected worth of fetching a page in this role: a detail page
// holds records itself, anything else is worth what it leads to.
func (s *Scorer) value(r Role) float64 {
	if r == Detail {
		return 1
	}
	return s.chain.Reach(r, hops)
}

// calibrate recentres a reach so that an average page is neutral.
//
// Raw reach cannot be combined with the base score directly. Half of every
// role reaches a record eventually, so the raw number sits near 0.5 for
// everything, and 0.5 is exactly the value that changes nothing. Dividing the
// odds by the odds of an average role makes a hub evidence for following a
// link and a dead end evidence against it, which is the distinction the chain
// exists to draw.
func (s *Scorer) calibrate(reach float64) float64 {
	baseline := s.baseline()
	if baseline <= 0 || baseline >= 1 {
		return reach
	}

	reach = clamp(reach)
	ratio := (reach / (1 - reach)) / (baseline / (1 - baseline))
	return ratio / (1 + ratio)
}

// baseline is the reach of an average role, computed from the chain itself so
// there is no constant to keep in step with the transitions.
func (s *Scorer) baseline() float64 {
	s.mu.RLock()
	cached := s.mean
	s.mu.RUnlock()
	if cached > 0 {
		return cached
	}

	var total float64
	for r := range NumRoles {
		total += s.chain.Reach(Role(r), hops)
	}
	mean := total / NumRoles

	s.mu.Lock()
	s.mean = mean
	s.mu.Unlock()
	return mean
}

// combine merges two independent probabilities. Both being 0.5 leaves the
// answer at 0.5, and either being certain carries the result with it.
func combine(a, b float64) float64 {
	a = clamp(a)
	b = clamp(b)

	num := a * b
	den := num + (1-a)*(1-b)
	if den == 0 {
		return 0
	}
	return num / den
}

func clamp(p float64) float64 {
	const eps = 1e-6
	switch {
	case p < eps:
		return eps
	case p > 1-eps:
		return 1 - eps
	default:
		return p
	}
}
