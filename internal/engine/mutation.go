// SPDX-License-Identifier: GPL-3.0-or-later

package engine

import (
	"fmt"
	"slices"
	"strings"
)

// Mutation says what to do when a job is resubmitted under a name that is
// already running.
//
//	mutation {
//	  costly         = "refuse"
//	  out_of_scope   = "drop"
//	  stale_records  = "keep"
//	  orphaned_cache = "refuse"
//	}
//
// [Diff] reports what a resubmission would change and what each change costs.
// This is where the job says what should happen about it, so the answer travels
// with the job rather than living in a flag somebody has to remember or a
// server default that differs between machines.
//
// Every attribute names a situation and every value names what to do about it,
// so a document can be read without knowing which effect is which.
type Mutation struct {
	// Costly decides whether a change that is not free may be applied at all.
	// It gates the three below: nothing is disposed of if the submission is
	// refused.
	Costly string `hcl:"costly,optional"`

	// OutOfScope is what happens to a URL already in the frontier that the new
	// scope no longer admits.
	OutOfScope string `hcl:"out_of_scope,optional"`

	// StaleRecords is what happens to records written under a schema that has
	// since changed.
	StaleRecords string `hcl:"stale_records,optional"`

	// OrphanedCache is what happens when the cache moves and every body
	// already fetched is somewhere the job can no longer read.
	OrphanedCache string `hcl:"orphaned_cache,optional"`
}

// What Costly may be.
const (
	// CostlyRefuse rejects the whole submission and changes nothing. The
	// default, because the alternative is a job quietly doing something
	// expensive that nobody asked it to.
	CostlyRefuse = "refuse"
	// CostlyApply applies the change and disposes of the consequences as the
	// attributes below say.
	CostlyApply = "apply"
)

// What OutOfScope may be.
const (
	// ScopeDrop removes the entries from the frontier now.
	ScopeDrop = "drop"
	// ScopeKeep leaves them, so they are refused later by offsite, at the cost
	// of a scheduler that hands out work which is going to be thrown away.
	ScopeKeep = "keep"
)

// What StaleRecords may be.
const (
	// RecordsKeep leaves them, so the output holds two shapes and whoever
	// reads it has to cope. The default, because deleting somebody's data on a
	// configuration change is not a thing to do without being asked.
	RecordsKeep = "keep"
	// RecordsDiscard deletes the records that no longer match.
	RecordsDiscard = "discard"
	// RecordsReextract re-runs extraction over the cached bodies, which is
	// what the cache is for: the pages were already paid for, so the new shape
	// can be applied to them without asking any site for anything again.
	RecordsReextract = "reextract"
)

// What OrphanedCache may be.
const (
	// CacheRefuse rejects the change, because a cache that moves takes every
	// body already fetched with it and the job would look like it had suddenly
	// forgotten what it had done.
	CacheRefuse = "refuse"
	// CacheAccept moves anyway and treats the corpus as empty.
	CacheAccept = "accept"
)

var (
	costlyValues        = []string{CostlyRefuse, CostlyApply}
	outOfScopeValues    = []string{ScopeDrop, ScopeKeep}
	staleRecordsValues  = []string{RecordsKeep, RecordsDiscard, RecordsReextract}
	orphanedCacheValues = []string{CacheRefuse, CacheAccept}
)

// Accessors, nil-safe, so an absent block is not a special case at the call
// site.

// CostlyPolicy is whether a change that is not free may be applied.
func (m *Mutation) CostlyPolicy() string {
	return pick(m, func(m *Mutation) string { return m.Costly }, DefaultCostly)
}

// OutOfScopePolicy is what happens to newly out-of-bounds frontier entries.
func (m *Mutation) OutOfScopePolicy() string {
	return pick(m, func(m *Mutation) string { return m.OutOfScope }, DefaultOutOfScope)
}

// StaleRecordsPolicy is what happens to records under a changed schema.
func (m *Mutation) StaleRecordsPolicy() string {
	return pick(m, func(m *Mutation) string { return m.StaleRecords }, DefaultStaleRecords)
}

// OrphanedCachePolicy is what happens when the cache moves.
func (m *Mutation) OrphanedCachePolicy() string {
	return pick(m, func(m *Mutation) string { return m.OrphanedCache }, DefaultOrphanedCache)
}

func pick(m *Mutation, get func(*Mutation) string, def string) string {
	if m == nil {
		return def
	}
	if v := get(m); v != "" {
		return v
	}
	return def
}

func (m *Mutation) validate() []error {
	if m == nil {
		return nil
	}
	var problems []error

	for _, check := range []struct {
		field  string
		value  string
		values []string
	}{
		{"costly", m.Costly, costlyValues},
		{"out_of_scope", m.OutOfScope, outOfScopeValues},
		{"stale_records", m.StaleRecords, staleRecordsValues},
		{"orphaned_cache", m.OrphanedCache, orphanedCacheValues},
	} {
		if check.value != "" && !slices.Contains(check.values, check.value) {
			problems = append(problems, fmt.Errorf("mutation.%s: %q is not one of %s",
				check.field, check.value, strings.Join(check.values, ", ")))
		}
	}

	return problems
}

// Action is something that has to happen to work already done, because a change
// was applied.
type Action struct {
	// Effect is what the change costs.
	Effect Effect
	// Do is the disposition the job configured.
	Do string
	// Why is the change that caused it, for a log or a reply.
	Why Change
}

func (a Action) String() string {
	return fmt.Sprintf("%s: %s (%s)", a.Effect, a.Do, a.Why)
}

// Review is a resubmission read through the job's mutation policy.
type Review struct {
	// Changes is everything the resubmission would do.
	Changes Changes
	// Refused is why it will not be done, empty if it will.
	Refused Changes
	// Actions is what has to happen to work already done, if it is.
	Actions []Action
}

// OK reports whether the submission may be applied.
func (r Review) OK() bool { return len(r.Refused) == 0 }

// Review reads a resubmission through this job's mutation policy.
//
// The receiver is the job being submitted, because its policy is the one that
// governs: a client changing the rules and the configuration in one document
// means the new rules, or they would have to submit twice to change anything
// interesting.
func (j *Job) Review(running *Job) Review {
	changes := Diff(running, j)

	out := Review{Changes: changes}
	if !changes.Costly().Any() {
		return out
	}

	m := j.Mutation
	refuseAll := m.CostlyPolicy() == CostlyRefuse

	for _, change := range changes.Costly() {
		// The cache has its own refusal, because losing a corpus is worse than
		// the other costly changes and a job may reasonably refuse only that.
		if change.Effect == EffectCacheMoved && m.OrphanedCachePolicy() == CacheRefuse {
			out.Refused = append(out.Refused, change)
			continue
		}
		if refuseAll {
			out.Refused = append(out.Refused, change)
			continue
		}
		if do := m.dispositionFor(change.Effect); do != "" {
			out.Actions = append(out.Actions, Action{Effect: change.Effect, Do: do, Why: change})
		}
	}

	// Nothing is disposed of if the submission is not applied.
	if !out.OK() {
		out.Actions = nil
	}
	return out
}

// dispositionFor is what to do about work already done, for one effect. An
// empty string means there is nothing to dispose of.
func (m *Mutation) dispositionFor(e Effect) string {
	switch e {
	case EffectRescope:
		return m.OutOfScopePolicy()
	case EffectReextract:
		return m.StaleRecordsPolicy()
	case EffectCacheMoved:
		return m.OrphanedCachePolicy()
	default:
		// A reseed adds work rather than invalidating any, so there is nothing
		// to decide about what came before.
		return ""
	}
}
