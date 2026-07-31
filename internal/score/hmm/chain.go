// SPDX-License-Identifier: GPL-3.0-or-later

// Package hmm models a crawl path as a hidden Markov chain over page roles.
//
// A per-URL scorer judges a link on its own tokens, which fails the classic
// focused-crawling problem: the hub page leading to a hundred records usually
// contains none itself, scores near zero, and never gets followed. Modelling
// the path fixes that, because the value of a link becomes the expected value
// of where it leads.
//
// The same three constraints wom applies to its field-order chain apply here,
// and for the same reason, which is learning safely from very little data:
//
//  1. Only transitions are trained. Emissions come from what a fetched page
//     turned out to hold. An unsupervised chain's states carry no inherent
//     meaning and would drift off the roles they are supposed to name.
//  2. Estimation is MAP, not maximum likelihood. The prior enters as
//     pseudo-counts, so twenty pages leave it mostly intact while twenty
//     thousand let the data speak.
//  3. Fitting runs over crawl paths, never the whole visited set. Fitted to
//     everything, the likelihood is dominated by the boilerplate the chain
//     exists to discount.
package hmm

import (
	"encoding/json"
	"fmt"
	"math"
)

// Role is a hidden state: what a page turned out to be for this crawl.
type Role int

// The roles a page can play. Order is part of the serialised format.
const (
	// Seed is a page the user named directly.
	Seed Role = iota
	// Hub lists links to records without holding any itself. Crediting these
	// is the whole point of the chain.
	Hub
	// Pagination is another page of the same listing.
	Pagination
	// Detail holds the records.
	Detail
	// Boilerplate is navigation, legal text and other filler.
	Boilerplate
	// Dead led nowhere: an error, or a page with neither links nor records.
	Dead
)

// NumRoles is how many roles the chain has.
const NumRoles = 6

// RoleNames are the roles as they appear in the database and in `scour status`.
var RoleNames = [NumRoles]string{"seed", "hub", "pagination", "detail", "boilerplate", "dead"}

// String implements fmt.Stringer.
func (r Role) String() string {
	if r < 0 || int(r) >= NumRoles {
		return "unknown"
	}
	return RoleNames[r]
}

// ParseRole turns a stored name back into a role.
func ParseRole(s string) (Role, bool) {
	for i, name := range RoleNames {
		if name == s {
			return Role(i), true
		}
	}
	return 0, false
}

// Observation is what a fetched page turned out to be, which is the evidence
// the chain reads. It is deliberately coarse: finer symbols would need more
// data to estimate than a single crawl provides.
type Observation int

// The observation symbols.
const (
	// Records means the page yielded at least one record.
	Records Observation = iota
	// Links means the page yielded links but no records.
	Links
	// Barren means the page was fetched and held neither.
	Barren
	// Failed means the fetch did not succeed.
	Failed
)

// NumObservations is how many symbols the chain distinguishes.
const NumObservations = 4

// Chain is a hidden Markov chain over page roles.
//
// Start, Trans and Emit hold ordinary probabilities rather than logs, so a
// saved chain stays readable. Decoding converts to logs internally.
type Chain struct {
	Start        []float64   `json:"start"`
	Trans        [][]float64 `json:"trans"`
	Emit         [][]float64 `json:"emit"`
	Observations int         `json:"observations,omitempty"`
}

// Default returns the prior: what page roles do on a typical site, before any
// evidence from this one.
//
// This is the part that ships. A hub leading to detail pages is a property of
// how sites are built; the transitions fitted for one site are worth nothing
// on another, which is the same distinction wom draws between its chain prior
// and its locators.
func Default() *Chain {
	c := &Chain{
		Start: make([]float64, NumRoles),
		Trans: make([][]float64, NumRoles),
		Emit:  make([][]float64, NumRoles),
	}

	// A crawl starts at a page the user named.
	c.Start[Seed] = 0.94
	c.Start[Hub] = 0.02
	c.Start[Detail] = 0.02
	c.Start[Boilerplate] = 0.01
	c.Start[Pagination] = 0.005
	c.Start[Dead] = 0.005

	//                      seed  hub  page detail boiler dead
	c.Trans[Seed] = row(0.01, 0.45, 0.04, 0.25, 0.20, 0.05)
	c.Trans[Hub] = row(0.00, 0.15, 0.20, 0.50, 0.10, 0.05)
	c.Trans[Pagination] = row(0.00, 0.10, 0.30, 0.50, 0.05, 0.05)
	c.Trans[Detail] = row(0.00, 0.10, 0.05, 0.35, 0.40, 0.10)
	c.Trans[Boilerplate] = row(0.00, 0.10, 0.02, 0.08, 0.65, 0.15)
	c.Trans[Dead] = row(0.00, 0.05, 0.02, 0.08, 0.35, 0.50)

	//                     records links barren failed
	c.Emit[Seed] = row(0.10, 0.75, 0.10, 0.05)
	c.Emit[Hub] = row(0.05, 0.85, 0.05, 0.05)
	c.Emit[Pagination] = row(0.05, 0.85, 0.05, 0.05)
	c.Emit[Detail] = row(0.85, 0.08, 0.05, 0.02)
	c.Emit[Boilerplate] = row(0.02, 0.55, 0.40, 0.03)
	c.Emit[Dead] = row(0.01, 0.04, 0.35, 0.60)

	return c
}

func row(values ...float64) []float64 { return values }

// Valid reports whether the chain is well formed.
func (c *Chain) Valid() bool {
	if c == nil || len(c.Start) != NumRoles || len(c.Trans) != NumRoles || len(c.Emit) != NumRoles {
		return false
	}
	for _, r := range c.Trans {
		if len(r) != NumRoles {
			return false
		}
	}
	for _, r := range c.Emit {
		if len(r) != NumObservations {
			return false
		}
	}
	return true
}

// Decode returns the most likely sequence of roles for one crawl path, by the
// Viterbi algorithm.
//
// A page is never classified alone: one reached from a hub, matching no
// records but carrying links, is pagination, while the same page reached from
// a detail page is more likely boilerplate. That context is exactly what a
// per-page classifier cannot use.
func (c *Chain) Decode(obs []Observation) []Role {
	if len(obs) == 0 || !c.Valid() {
		return nil
	}

	negInf := math.Inf(-1)

	delta := make([][]float64, len(obs))
	back := make([][]int, len(obs))
	for t := range obs {
		delta[t] = make([]float64, NumRoles)
		back[t] = make([]int, NumRoles)
	}

	for r := range NumRoles {
		delta[0][r] = safeLog(c.Start[r]) + safeLog(c.emitOf(Role(r), obs[0]))
	}

	for t := 1; t < len(obs); t++ {
		for next := range NumRoles {
			best, bestFrom := negInf, 0
			for prev := range NumRoles {
				score := delta[t-1][prev] + safeLog(c.Trans[prev][next])
				if score > best {
					best, bestFrom = score, prev
				}
			}
			delta[t][next] = best + safeLog(c.emitOf(Role(next), obs[t]))
			back[t][next] = bestFrom
		}
	}

	last, bestEnd := negInf, 0
	for r := range NumRoles {
		if delta[len(obs)-1][r] > last {
			last, bestEnd = delta[len(obs)-1][r], r
		}
	}

	path := make([]Role, len(obs))
	path[len(obs)-1] = Role(bestEnd)
	for t := len(obs) - 1; t > 0; t-- {
		bestEnd = back[t][bestEnd]
		path[t-1] = Role(bestEnd)
	}
	return path
}

func (c *Chain) emitOf(r Role, o Observation) float64 {
	if o < 0 || int(o) >= NumObservations {
		return 1
	}
	return c.Emit[r][o]
}

// Reach is the probability of passing through a Detail page within k hops of a
// page in the given role.
//
// This is what credits a hub: a page holding no records at all still scores
// well when the chain says its children usually do.
func (c *Chain) Reach(from Role, hops int) float64 {
	if !c.Valid() || hops <= 0 {
		return 0
	}

	// Distribution over roles, with Detail made absorbing so probability mass
	// that has already arrived is not counted again on a later hop.
	dist := make([]float64, NumRoles)
	dist[from] = 1

	var reached float64
	for range hops {
		next := make([]float64, NumRoles)
		for r := range NumRoles {
			mass := dist[r]
			if mass == 0 {
				continue
			}
			for to := range NumRoles {
				p := mass * c.Trans[r][to]
				if Role(to) == Detail {
					reached += p
					continue
				}
				next[to] += p
			}
		}
		dist = next
	}
	if reached > 1 {
		reached = 1
	}
	return reached
}

// pseudoCount is how many observations the prior is worth when fitting.
//
// It has to be large enough that a handful of paths cannot rewrite the chain,
// and small enough that a real crawl eventually does. Ten is roughly "one
// short crawl's worth of belief".
const pseudoCount = 10.0

// Fit re-estimates the transitions from decoded paths, MAP rather than
// maximum likelihood: the prior enters as pseudo-counts.
//
// Emissions are left alone. They are what anchors a state to its name, and
// re-estimating them is how an unsupervised chain ends up with six states that
// mean nothing.
func (c *Chain) Fit(paths [][]Observation) error {
	if !c.Valid() {
		return fmt.Errorf("chain is not well formed")
	}
	if len(paths) == 0 {
		return nil
	}

	start := make([]float64, NumRoles)
	trans := make([][]float64, NumRoles)
	for r := range NumRoles {
		start[r] = c.Start[r] * pseudoCount
		trans[r] = make([]float64, NumRoles)
		for to := range NumRoles {
			trans[r][to] = c.Trans[r][to] * pseudoCount
		}
	}

	var counted int
	for _, path := range paths {
		roles := c.Decode(path)
		if len(roles) == 0 {
			continue
		}
		counted++
		start[roles[0]]++
		for i := 1; i < len(roles); i++ {
			trans[roles[i-1]][roles[i]]++
		}
	}
	if counted == 0 {
		return nil
	}

	c.Start = normalise(start)
	for r := range NumRoles {
		c.Trans[r] = normalise(trans[r])
	}
	c.Observations += counted
	return nil
}

func normalise(counts []float64) []float64 {
	var total float64
	for _, v := range counts {
		total += v
	}
	if total == 0 {
		out := make([]float64, len(counts))
		for i := range out {
			out[i] = 1 / float64(len(out))
		}
		return out
	}
	out := make([]float64, len(counts))
	for i, v := range counts {
		out[i] = v / total
	}
	return out
}

func safeLog(p float64) float64 {
	if p <= 0 {
		return math.Inf(-1)
	}
	return math.Log(p)
}

// MarshalJSON is the stored form.
func (c *Chain) MarshalJSON() ([]byte, error) {
	type alias Chain
	buf, err := json.Marshal((*alias)(c))
	if err != nil {
		return nil, fmt.Errorf("encode chain: %w", err)
	}
	return buf, nil
}

// Parse reads a stored chain, falling back to the prior when the stored one is
// unusable rather than failing a crawl over it.
func Parse(data []byte) (*Chain, error) {
	if len(data) == 0 {
		return Default(), nil
	}
	var c Chain
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("decode chain: %w", err)
	}
	if !c.Valid() {
		return Default(), nil
	}
	return &c, nil
}
