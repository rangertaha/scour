// SPDX-License-Identifier: GPL-3.0-or-later

package engine

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
)

// Every default, in one place.
//
// # Why they are all here
//
// A default scattered next to the field it fills is easy to write and
// impossible to review: nobody can answer "what does an empty job do?" without
// reading every file. Gathered, the question is answerable, [Defaults] can
// print them, and a test can assert that nothing was left without one.
//
// # Why they are cautious
//
// A client who configures nothing is telling us they have not thought about it.
// The answer to that is a crawl that is slow, shallow and polite rather than one
// that gets somebody's address blocked, and a mutation policy that refuses what
// is expensive rather than doing it quietly.
//
// # When they are applied
//
// At submission, not at run. A stored job records what it will actually do, so
// resubmitting the same document next month does the same thing even if these
// numbers have moved since. See [Job.Resolved].
const (
	// Schema.
	DefaultItemType     = TypeObject
	DefaultPropertyType = TypeStr

	// Limits. MaxPages and MaxTime default to no limit, because a budget is a
	// thing a person chooses; depth and body size do not, because an unbounded
	// depth is not a crawl but the whole web, and the size of the largest page
	// on the web is not a number anyone should discover by filling a disk.
	DefaultMaxPages     = 0
	DefaultMaxTime      = time.Duration(0)
	DefaultMaxDepth     = 5
	DefaultMaxBodyBytes = 32 << 20 // 32 MiB

	// Politeness.
	DefaultRate        = time.Second
	DefaultConcurrency = 2
	DefaultRobots      = true
	DefaultUserAgent   = "scour (+https://github.com/rangertaha/scour)"

	// Components.
	DefaultExternalTimeout = 5 * time.Minute

	// Monitoring. Logging on and metrics off: a run that says nothing is hard
	// to debug, and a run that publishes numbers nobody asked for needs
	// somewhere to publish them.
	DefaultMetrics  = false
	DefaultLogging  = true
	DefaultLogLevel = "info"

	// Plugins. A listed plugin runs; a job gets exactly the chain it lists.
	DefaultPluginEnabled = true

	// Mutation, cautious throughout.
	DefaultCostly        = CostlyRefuse
	DefaultOutOfScope    = ScopeDrop
	DefaultStaleRecords  = RecordsKeep
	DefaultOrphanedCache = CacheRefuse
)

// MaxConcurrency is the ceiling on per-host parallelism. Unbounded concurrency
// against one host is a denial of service with our name in the logs.
const MaxConcurrency = 64

// LogLevels are what monitoring.level may be.
var LogLevels = []string{"debug", "info", "warn", "error"}

// Defaults lists every default as it would be written in a document, so a
// command can print them and a test can check none is missing.
func Defaults() map[string]string {
	return map[string]string{
		"item.type":                     string(DefaultItemType),
		"item.property.type":            string(DefaultPropertyType),
		"engine.limits.max_pages":       fmt.Sprint(DefaultMaxPages),
		"engine.limits.max_depth":       fmt.Sprint(DefaultMaxDepth),
		"engine.limits.max_time":        DefaultMaxTime.String(),
		"engine.limits.max_body_bytes":  fmt.Sprint(DefaultMaxBodyBytes),
		"engine.politeness.rate":        DefaultRate.String(),
		"engine.politeness.concurrency": fmt.Sprint(DefaultConcurrency),
		"engine.politeness.robots":      fmt.Sprint(DefaultRobots),
		"engine.politeness.user_agent":  DefaultUserAgent,
		"engine.components.timeout":     DefaultExternalTimeout.String(),
		"monitoring.metrics":            fmt.Sprint(DefaultMetrics),
		"monitoring.logging":            fmt.Sprint(DefaultLogging),
		"monitoring.level":              DefaultLogLevel,
		"plugin.enabled":                fmt.Sprint(DefaultPluginEnabled),
		"mutation.costly":               DefaultCostly,
		"mutation.out_of_scope":         DefaultOutOfScope,
		"mutation.stale_records":        DefaultStaleRecords,
		"mutation.orphaned_cache":       DefaultOrphanedCache,
	}
}

// DefaultNames lists the settings that have a default, sorted.
func DefaultNames() []string {
	d := Defaults()
	out := make([]string, 0, len(d))
	for name := range d {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Monitoring accessors, nil-safe like the rest.

// MetricsOn reports whether this job publishes measurements.
func (m *Monitoring) MetricsOn() bool {
	if m == nil {
		return DefaultMetrics
	}
	return m.Metrics
}

// LoggingOn reports whether this job logs.
//
// Logging defaults to on, so a job that says nothing about monitoring still
// says something about itself when it goes wrong. That means the zero value of
// the field cannot be read directly: false and unset are the same bool, and
// unset has to mean on. A job turning logging off says so with an explicit
// `logging = false`, which is why the field is read through here.
func (m *Monitoring) LoggingOn() bool {
	if m == nil {
		return DefaultLogging
	}
	// An absent block means the default; a present block means what it says,
	// which is the only reading under which turning logging off is possible.
	return m.Logging || !m.explicit()
}

// explicit reports whether the block set anything at all, which is how an
// absent value is told from a false one.
func (m *Monitoring) explicit() bool {
	return m != nil && (m.Metrics || m.Level != "" || m.Logging)
}

// LogLevel is the level this job logs at.
func (m *Monitoring) LogLevel() string {
	if m == nil || m.Level == "" {
		return DefaultLogLevel
	}
	return m.Level
}

func (m *Monitoring) validate() []error {
	if m == nil {
		return nil
	}
	if m.Level != "" && !slices.Contains(LogLevels, m.Level) {
		return []error{fmt.Errorf("monitoring.level: %q is not one of %s",
			m.Level, strings.Join(LogLevels, ", "))}
	}
	return nil
}

// Enabled reports whether a plugin is in its chain.
func (p *Plugin) IsEnabled() bool {
	if p.Enabled == nil {
		return DefaultPluginEnabled
	}
	return *p.Enabled
}

// ItemType is this item's type, defaulted.
func (i *Item) ItemType() Type {
	if i.Type == "" {
		return DefaultItemType
	}
	return Type(i.Type)
}

// PropertyType is this property's type, defaulted.
//
// A property with children is an object whether it said so or not, which is the
// one place a default is inferred from the shape rather than fixed. Validation
// has already refused the case where the document says otherwise, so this
// cannot quietly disagree with what was written.
func (p *Property) PropertyType() Type {
	if p.Type != "" {
		return Type(p.Type)
	}
	if len(p.Properties) > 0 {
		return TypeObject
	}
	return DefaultPropertyType
}

// Resolved returns the job with every default filled in.
//
// A copy: what the client submitted and what the job will do are two different
// things, and both are worth being able to show. The submitted form is what a
// person recognises; this is what actually runs, and it is what should be
// stored, so that a job resubmitted next month behaves the way it did today
// even if these defaults have moved since.
func (j *Job) Resolved() *Job {
	out := *j

	out.Items = make([]*Item, 0, len(j.Items))
	for _, item := range j.Items {
		out.Items = append(out.Items, item.resolved())
	}

	engine := Engine{}
	if j.Engine != nil {
		engine = *j.Engine
	}
	limits := Limits{
		MaxPages:     engine.Limits.Pages(),
		MaxDepth:     engine.Limits.Depth(),
		MaxBodyBytes: engine.Limits.BodyBytes(),
	}
	if d, err := engine.Limits.MaxTimeDuration(); err == nil && d > 0 {
		limits.MaxTime = d.String()
	}

	robots := engine.Politeness.ObeysRobots()
	politeness := Politeness{
		Concurrency: engine.Politeness.Parallelism(),
		Robots:      &robots,
		UserAgent:   engine.Politeness.Agent(),
	}
	if d, err := engine.Politeness.RateDuration(); err == nil {
		politeness.Rate = d.String()
	}

	components := Components{}
	if engine.Components != nil {
		components = *engine.Components
	}
	if d, err := engine.Components.ExternalTimeout(); err == nil {
		components.Timeout = d.String()
	}

	out.Engine = &Engine{Limits: &limits, Politeness: &politeness, Components: &components}

	out.Monitoring = &Monitoring{
		Metrics: j.Monitoring.MetricsOn(),
		Logging: j.Monitoring.LoggingOn(),
		Level:   j.Monitoring.LogLevel(),
	}

	out.Mutation = &Mutation{
		Costly:        j.Mutation.CostlyPolicy(),
		OutOfScope:    j.Mutation.OutOfScopePolicy(),
		StaleRecords:  j.Mutation.StaleRecordsPolicy(),
		OrphanedCache: j.Mutation.OrphanedCachePolicy(),
	}

	out.Plugins = make([]*Plugin, 0, len(j.Plugins))
	for _, p := range j.Plugins {
		clone := *p
		enabled := p.IsEnabled()
		clone.Enabled = &enabled
		clone.Order = p.order()
		out.Plugins = append(out.Plugins, &clone)
	}

	return &out
}

func (i *Item) resolved() *Item {
	out := *i
	out.Type = string(i.ItemType())
	out.Properties = resolvedProperties(i.Properties)
	return &out
}

func resolvedProperties(props []*Property) []*Property {
	if len(props) == 0 {
		return nil
	}
	out := make([]*Property, 0, len(props))
	for _, p := range props {
		clone := *p
		clone.Type = string(p.PropertyType())
		clone.Properties = resolvedProperties(p.Properties)
		out = append(out, &clone)
	}
	return out
}
