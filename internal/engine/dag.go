// SPDX-License-Identifier: GPL-3.0-or-later

package engine

import (
	"fmt"
	"sort"
	"strings"
)

// validatePipelines checks the item graph: that every dependency exists, that
// nothing depends on itself, and that there are no cycles.
//
// All of it at submission. A cycle found when the graph runs is a job that
// hangs, and the person who wrote it has long since stopped watching.
func (j *Job) validatePipelines() []error {
	var problems []error

	byAddress := make(map[string]*Pipeline, len(j.Pipelines))
	for _, p := range j.Pipelines {
		address := p.Address()
		if _, dup := byAddress[address]; dup {
			problems = append(problems, fmt.Errorf("pipelines %q: declared twice", address))
			continue
		}
		byAddress[address] = p
	}

	for _, p := range j.Pipelines {
		for _, req := range p.requires {
			switch {
			case req == p.Address():
				problems = append(problems, fmt.Errorf("pipelines %q: requires itself", p.Address()))
			case byAddress[req] == nil:
				problems = append(problems, fmt.Errorf(
					"pipelines %q: requires %q, which is not declared. Declared: %s",
					p.Address(), req, strings.Join(addresses(j.Pipelines), ", ")))
			}
		}
	}

	if cycle := findCycle(j.Pipelines, byAddress); len(cycle) > 0 {
		problems = append(problems, fmt.Errorf("pipelines: dependency cycle %s", strings.Join(cycle, " -> ")))
	}

	return problems
}

// Order returns the pipelines in an order that satisfies their dependencies.
//
// Steps that do not depend on each other come out sorted by address, so the
// same document always produces the same order. A graph that ran in map order
// would be reproducible only by accident.
//
// It returns an error if the graph does not resolve, which [Job.validate]
// should already have reported: this is the second line of defence, not the
// first.
func (j *Job) Order() ([]*Pipeline, error) {
	byAddress := make(map[string]*Pipeline, len(j.Pipelines))
	for _, p := range j.Pipelines {
		byAddress[p.Address()] = p
	}

	// Kahn's algorithm, taking ready nodes in sorted order.
	remaining := make(map[string]int, len(j.Pipelines))
	dependents := make(map[string][]string, len(j.Pipelines))

	for _, p := range j.Pipelines {
		count := 0
		for _, req := range p.requires {
			if byAddress[req] == nil {
				return nil, fmt.Errorf("pipelines %q: requires %q, which is not declared", p.Address(), req)
			}
			count++
			dependents[req] = append(dependents[req], p.Address())
		}
		remaining[p.Address()] = count
	}

	var ready []string
	for address, count := range remaining {
		if count == 0 {
			ready = append(ready, address)
		}
	}
	sort.Strings(ready)

	out := make([]*Pipeline, 0, len(j.Pipelines))
	for len(ready) > 0 {
		address := ready[0]
		ready = ready[1:]
		out = append(out, byAddress[address])

		var freed []string
		for _, dependent := range dependents[address] {
			remaining[dependent]--
			if remaining[dependent] == 0 {
				freed = append(freed, dependent)
			}
		}
		if len(freed) > 0 {
			ready = append(ready, freed...)
			sort.Strings(ready)
		}
	}

	if len(out) != len(j.Pipelines) {
		return nil, fmt.Errorf("pipelines: dependency cycle among %s", strings.Join(unresolved(remaining), ", "))
	}
	return out, nil
}

// findCycle returns one cycle, as a readable path, or nil.
//
// One rather than all: a document usually has a single mistake in it, and a
// list of every cycle a single mistake creates is harder to read than the path
// that shows it.
func findCycle(pipelines []*Pipeline, byAddress map[string]*Pipeline) []string {
	const (
		unvisited = 0
		active    = 1
		done      = 2
	)
	state := make(map[string]int, len(pipelines))

	var path []string
	var walk func(address string) []string

	walk = func(address string) []string {
		switch state[address] {
		case active:
			// Found it: cut the path back to where this address first appears.
			for i, seen := range path {
				if seen == address {
					return append(append([]string(nil), path[i:]...), address)
				}
			}
			return []string{address}
		case done:
			return nil
		}

		state[address] = active
		path = append(path, address)

		p := byAddress[address]
		if p != nil {
			// Sorted, so the cycle reported for a given document is stable.
			reqs := append([]string(nil), p.requires...)
			sort.Strings(reqs)
			for _, req := range reqs {
				if byAddress[req] == nil {
					continue // already reported as undeclared
				}
				if cycle := walk(req); cycle != nil {
					return cycle
				}
			}
		}

		path = path[:len(path)-1]
		state[address] = done
		return nil
	}

	for _, address := range addresses(pipelines) {
		if cycle := walk(address); cycle != nil {
			return cycle
		}
	}
	return nil
}

func addresses(pipelines []*Pipeline) []string {
	out := make([]string, 0, len(pipelines))
	for _, p := range pipelines {
		out = append(out, p.Address())
	}
	sort.Strings(out)
	return out
}

func unresolved(remaining map[string]int) []string {
	var out []string
	for address, count := range remaining {
		if count > 0 {
			out = append(out, address)
		}
	}
	sort.Strings(out)
	return out
}
