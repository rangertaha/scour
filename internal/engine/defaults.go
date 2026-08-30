// SPDX-License-Identifier: GPL-3.0-or-later

package engine

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
)

// Every default, in one place, and every accessor that applies one.
//
// # Why they are all here
//
// A default written next to the field it fills is easy to write and impossible
// to review: nobody can answer "what does an empty job do?" without reading
// every file. Gathered, the question is answerable, [Defaults] can print them,
// and a test can assert nothing was left without one.
//
// # Why every accessor is nil-safe
//
// Each stage block is optional, so a job that configures nothing has nil where
// its stages would be. Reading through a method rather than a field means an
// absent block is never a special case at the call site, and there is exactly
// one place a default can be applied from.
//
// # Why they are cautious
//
// A client who configures nothing is telling us they have not thought about it.
// The answer is a crawl that is slow, shallow and polite rather than one that
// gets somebody's address blocked, and a mutation policy that refuses what is
// expensive rather than doing it quietly.
const (
	// Schema.
	DefaultItemType     = TypeObject
	DefaultPropertyType = TypeStr

	// Scheduler. Pages and time default to no limit because a budget is a
	// thing a person chooses; depth does not, because an unbounded depth is
	// not a crawl but the whole web.
	DefaultPolicy      = "priority"
	DefaultMaxPages    = 0
	DefaultMaxTime     = time.Duration(0)
	DefaultMaxDepth    = 5
	DefaultRate        = time.Second
	DefaultConcurrency = 2

	// Downloader.
	DefaultRobots         = true
	DefaultUserAgent      = "scour (+https://github.com/rangertaha/scour)"
	DefaultRequestTimeout = 30 * time.Second
	DefaultMaxBody        = 32 << 20 // 32 MiB
	DefaultMaxRedirects   = 10

	// External stages.
	DefaultExternalTimeout = 5 * time.Minute

	// Monitoring. Logging on and metrics off: a run that says nothing is hard
	// to debug, and a run that publishes numbers needs somewhere to publish
	// them.
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

// Policies are what scheduler.policy may be. They are alternatives rather than
// a chain: one order comes out of the frontier.
var Policies = []string{"priority", "breadth", "depth", "random"}

// Defaults lists every default as it would be written in a document, so a
// command can print them and a test can check none is missing.
func Defaults() map[string]string {
	return map[string]string{
		"item.type":                string(DefaultItemType),
		"item.property.type":       string(DefaultPropertyType),
		"scheduler.policy":         DefaultPolicy,
		"scheduler.rate":           DefaultRate.String(),
		"scheduler.concurrency":    fmt.Sprint(DefaultConcurrency),
		"scheduler.max_depth":      fmt.Sprint(DefaultMaxDepth),
		"scheduler.max_pages":      fmt.Sprint(DefaultMaxPages),
		"scheduler.max_time":       DefaultMaxTime.String(),
		"downloader.robots":        fmt.Sprint(DefaultRobots),
		"downloader.user_agent":    DefaultUserAgent,
		"downloader.timeout":       DefaultRequestTimeout.String(),
		"downloader.max_body":      fmt.Sprint(DefaultMaxBody),
		"downloader.max_redirects": fmt.Sprint(DefaultMaxRedirects),
		"external_timeout":         DefaultExternalTimeout.String(),
		"monitoring.metrics":       fmt.Sprint(DefaultMetrics),
		"monitoring.logging":       fmt.Sprint(DefaultLogging),
		"monitoring.level":         DefaultLogLevel,
		"plugin.enabled":           fmt.Sprint(DefaultPluginEnabled),
		"mutation.costly":          DefaultCostly,
		"mutation.out_of_scope":    DefaultOutOfScope,
		"mutation.stale_records":   DefaultStaleRecords,
		"mutation.orphaned_cache":  DefaultOrphanedCache,
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

// Scheduler.

// OrderPolicy is the order the frontier is drained in.
func (s *Scheduler) OrderPolicy() string {
	if s == nil || s.Policy == "" {
		return DefaultPolicy
	}
	return s.Policy
}

// RateDuration is the least time between two requests to one host.
func (s *Scheduler) RateDuration() (time.Duration, error) {
	if s == nil || s.Rate == "" {
		return DefaultRate, nil
	}
	d, err := time.ParseDuration(s.Rate)
	if err != nil {
		return 0, fmt.Errorf("scheduler.rate: %w", err)
	}
	return d, nil
}

// Parallelism is how many requests may be in flight to one host.
func (s *Scheduler) Parallelism() int {
	if s == nil || s.Concurrency == 0 {
		return DefaultConcurrency
	}
	return s.Concurrency
}

// Depth is the depth ceiling.
func (s *Scheduler) Depth() int {
	if s == nil || s.MaxDepth == 0 {
		return DefaultMaxDepth
	}
	return s.MaxDepth
}

// Pages is the fetch ceiling. Zero means no limit, which is one of the two
// places an unset value is not replaced by a number.
func (s *Scheduler) Pages() int {
	if s == nil {
		return DefaultMaxPages
	}
	return s.MaxPages
}

// MaxTimeDuration is the crawl budget. Zero means no limit.
func (s *Scheduler) MaxTimeDuration() (time.Duration, error) {
	if s == nil || s.MaxTime == "" {
		return DefaultMaxTime, nil
	}
	d, err := time.ParseDuration(s.MaxTime)
	if err != nil {
		return 0, fmt.Errorf("scheduler.max_time: %w", err)
	}
	return d, nil
}

func (s *Scheduler) validate() []error {
	if s == nil {
		return nil
	}
	var problems []error

	if s.Policy != "" && !slices.Contains(Policies, s.Policy) {
		problems = append(problems, fmt.Errorf("scheduler.policy: %q is not one of %s",
			s.Policy, strings.Join(Policies, ", ")))
	}
	if s.Concurrency < 0 {
		problems = append(problems, fmt.Errorf("scheduler.concurrency: %d is negative", s.Concurrency))
	}
	if s.Concurrency > MaxConcurrency {
		problems = append(problems, fmt.Errorf(
			"scheduler.concurrency: %d is more than %d against a single host", s.Concurrency, MaxConcurrency))
	}
	if s.MaxDepth < 0 {
		problems = append(problems, fmt.Errorf("scheduler.max_depth: %d is negative", s.MaxDepth))
	}
	if s.MaxPages < 0 {
		problems = append(problems, fmt.Errorf("scheduler.max_pages: %d is negative", s.MaxPages))
	}
	if d, err := s.RateDuration(); err != nil {
		problems = append(problems, err)
	} else if d < 0 {
		problems = append(problems, fmt.Errorf("scheduler.rate: %s is negative", d))
	}
	if d, err := s.MaxTimeDuration(); err != nil {
		problems = append(problems, err)
	} else if d < 0 {
		problems = append(problems, fmt.Errorf("scheduler.max_time: %s is negative", d))
	}

	return problems
}

// Downloader.

// ObeysRobots reports whether this job honours robots.txt.
func (d *Downloader) ObeysRobots() bool {
	if d == nil || d.Robots == nil {
		return DefaultRobots
	}
	return *d.Robots
}

// Agent is the User-Agent this job identifies itself with.
func (d *Downloader) Agent() string {
	if d == nil || d.UserAgent == "" {
		return DefaultUserAgent
	}
	return d.UserAgent
}

// RequestTimeout is how long one request may take.
func (d *Downloader) RequestTimeout() (time.Duration, error) {
	if d == nil || d.Timeout == "" {
		return DefaultRequestTimeout, nil
	}
	v, err := time.ParseDuration(d.Timeout)
	if err != nil {
		return 0, fmt.Errorf("downloader.timeout: %w", err)
	}
	return v, nil
}

// BodyBytes is the largest body this job will accept.
func (d *Downloader) BodyBytes() int64 {
	if d == nil || d.MaxBody == 0 {
		return DefaultMaxBody
	}
	return d.MaxBody
}

// Redirects is how many hops a request may be forwarded through.
//
// Zero is a real answer, meaning follow none and hand the 3xx back, so an unset
// value cannot be told from it by looking. That is what the pointer is for.
func (d *Downloader) Redirects() int {
	if d == nil || d.MaxRedirects == nil {
		return DefaultMaxRedirects
	}
	return *d.MaxRedirects
}

// IsExternal reports whether somebody else runs this stage.
func (d *Downloader) IsExternal() bool { return d != nil && d.External }

// Timeout is how long an external downloader has to answer.
func (d *Downloader) ExternalWait() (time.Duration, error) {
	if d == nil {
		return DefaultExternalTimeout, nil
	}
	return externalWait("downloader", d.ExternalTimeout)
}

func (d *Downloader) validate() []error {
	if d == nil {
		return nil
	}
	var problems []error

	if d.MaxBody < 0 {
		problems = append(problems, fmt.Errorf("downloader.max_body: %d is negative", d.MaxBody))
	}
	if d.MaxRedirects != nil && *d.MaxRedirects < 0 {
		problems = append(problems, fmt.Errorf("downloader.max_redirects: %d is negative", *d.MaxRedirects))
	}
	if v, err := d.RequestTimeout(); err != nil {
		problems = append(problems, err)
	} else if v < 0 {
		problems = append(problems, fmt.Errorf("downloader.timeout: %s is negative", v))
	} else if v == 0 {
		// Zero is refused too, and it is the one somebody writes on purpose. It
		// does not mean "the default": it means no deadline at all, on the
		// fetch and on the client, so one page that dribbles its body holds a
		// worker forever. The lease is sized from the timeout, so it collapses
		// to its floor while the fetch it covers becomes unbounded - the URL
		// comes due again, a second worker takes it, and both hit the same host
		// at once while the crawl reports itself stalled.
		//
		// Leaving the field out is how a document asks for the default.
		problems = append(problems, fmt.Errorf(
			"downloader.timeout: zero is not a timeout, it is no timeout. "+
				"Leave it out for the default of %s", DefaultRequestTimeout))
	}
	if _, err := d.ExternalWait(); err != nil {
		problems = append(problems, err)
	}

	return problems
}

// Spider.

// IsExternal reports whether somebody else runs this stage.
func (s *Spider) IsExternal() bool { return s != nil && s.External }

// ExternalWait is how long an external spider has to answer.
func (s *Spider) ExternalWait() (time.Duration, error) {
	if s == nil {
		return DefaultExternalTimeout, nil
	}
	return externalWait("spider", s.ExternalTimeout)
}

func (s *Spider) validate() []error {
	if s == nil {
		return nil
	}
	if _, err := s.ExternalWait(); err != nil {
		return []error{err}
	}
	return nil
}

// Pipeline.

// IsExternal reports whether somebody else runs this stage.
func (p *Pipeline) IsExternal() bool { return p != nil && p.External }

// validate checks the pipeline block's own attributes.
//
// Only the external timeout, because a step's configuration is the step's to
// decode. It exists because the block was being skipped entirely:
// `external_timeout = "eventually"` in a pipeline validated clean, where the
// same mistake in a downloader or a spider was refused with a named field.
// Resolved() then swallowed the parse error, so `scour job show` printed a pipeline
// with no timeout and the stored job quietly lost the setting.
func (p *Pipeline) validate() []error {
	if p == nil {
		return nil
	}

	var problems []error
	if v, err := p.ExternalWait(); err != nil {
		problems = append(problems, err)
	} else if v < 0 {
		problems = append(problems, fmt.Errorf("pipeline.external_timeout: %s is negative", v))
	}
	return problems
}

// ExternalWait is how long an external pipeline has to answer.
func (p *Pipeline) ExternalWait() (time.Duration, error) {
	if p == nil {
		return DefaultExternalTimeout, nil
	}
	return externalWait("pipeline", p.ExternalTimeout)
}

// externalWait parses one stage's external timeout.
//
// Generous by default, because the reason to bring your own stage is usually
// that it does something slow: a model, a browser, a service somewhere else. A
// stage that is merely slow must not look like one that has died.
func externalWait(stage, value string) (time.Duration, error) {
	if value == "" {
		return DefaultExternalTimeout, nil
	}
	d, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s.external_timeout: %w", stage, err)
	}
	if d < 0 {
		return 0, fmt.Errorf("%s.external_timeout: %s is negative", stage, d)
	}
	return d, nil
}

// IsExternal reports whether a job expects somebody else to run a stage.
func (j *Job) IsExternal(s Stage) bool {
	switch s {
	case StageDownloader:
		return j.Downloader.IsExternal()
	case StageSpider:
		return j.Spider.IsExternal()
	case StagePipeline:
		return j.Pipeline.IsExternal()
	default:
		// The scheduler has no external attribute at all, so this cannot be
		// reached from a document. See [Scheduler].
		return false
	}
}

// Monitoring.

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
// says something about itself when it goes wrong. A bool could not express
// that, because false and unset would be the same value, so the field is a
// pointer and only an explicit `logging = false` turns it off.
func (m *Monitoring) LoggingOn() bool {
	if m == nil || m.Logging == nil {
		return DefaultLogging
	}
	return *m.Logging
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

// Plugins and schema.

// IsEnabled reports whether a plugin is in its chain.
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
// Two defaults are inferred from shape rather than fixed, and the order matters.
// Validation has already refused the cases where the document says otherwise,
// so neither can quietly disagree with what was written.
//
// A property naming an entity kind is an entity reference. It said so: `entity`
// is not a modifier on some other type, it is what the property is. Without
// this, `property "author" { entity = "person" }` resolved as a string,
// validated cleanly, and then behaved as one everywhere - the entities step
// skipped it and the record filed it as an ordinary field. The operator wrote
// down which kind of thing it referred to and nothing ever resolved it, silently.
// Validation's message for the neighbouring case says exactly what goes wrong,
// "so nothing would resolve it", and this variant did it while saying nothing.
//
// Entity is checked before children, because an entity reference may have them
// and the book documents that case: a reference is a name that refers to
// something, and its children describe the thing referred to rather than the
// item, which is how `author.role` is the person's role. Deciding "object" from
// the children would take that away from the one shape it was added for.
//
// A property with children is otherwise an object whether it said so or not.
func (p *Property) PropertyType() Type {
	if p.Type != "" {
		return Type(p.Type)
	}
	if p.Entity != "" {
		return TypeEntity
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
// stored, so a job resubmitted next month behaves the way it did today even if
// these defaults have moved since.
func (j *Job) Resolved() *Job {
	out := *j

	out.Items = make([]*Item, 0, len(j.Items))
	for _, item := range j.Items {
		out.Items = append(out.Items, item.resolved())
	}

	sched := &Scheduler{
		Policy:      j.Scheduler.OrderPolicy(),
		Concurrency: j.Scheduler.Parallelism(),
		MaxDepth:    j.Scheduler.Depth(),
		MaxPages:    j.Scheduler.Pages(),
		Plugins:     resolvedPlugins(j.Scheduler.plugins()),
	}
	if d, err := j.Scheduler.RateDuration(); err == nil {
		sched.Rate = d.String()
	}
	if d, err := j.Scheduler.MaxTimeDuration(); err == nil && d > 0 {
		sched.MaxTime = d.String()
	}
	out.Scheduler = sched

	robots := j.Downloader.ObeysRobots()
	hops := j.Downloader.Redirects()
	down := &Downloader{
		External:     j.Downloader.IsExternal(),
		Robots:       &robots,
		UserAgent:    j.Downloader.Agent(),
		MaxBody:      j.Downloader.BodyBytes(),
		MaxRedirects: &hops,
		Plugins:      resolvedPlugins(j.Downloader.plugins()),
	}
	if v, err := j.Downloader.RequestTimeout(); err == nil {
		down.Timeout = v.String()
	}
	if v, err := j.Downloader.ExternalWait(); err == nil {
		down.ExternalTimeout = v.String()
	}
	out.Downloader = down

	spider := &Spider{
		External: j.Spider.IsExternal(),
		Plugins:  resolvedPlugins(j.Spider.plugins()),
	}
	if v, err := j.Spider.ExternalWait(); err == nil {
		spider.ExternalTimeout = v.String()
	}
	out.Spider = spider

	pipe := &Pipeline{External: j.Pipeline.IsExternal(), Steps: j.Steps()}
	if v, err := j.Pipeline.ExternalWait(); err == nil {
		pipe.ExternalTimeout = v.String()
	}
	out.Pipeline = pipe

	logging := j.Monitoring.LoggingOn()
	out.Monitoring = &Monitoring{
		Metrics: j.Monitoring.MetricsOn(),
		Logging: &logging,
		Level:   j.Monitoring.LogLevel(),
	}

	out.Mutation = &Mutation{
		Costly:        j.Mutation.CostlyPolicy(),
		OutOfScope:    j.Mutation.OutOfScopePolicy(),
		StaleRecords:  j.Mutation.StaleRecordsPolicy(),
		OrphanedCache: j.Mutation.OrphanedCachePolicy(),
	}

	return &out
}

func resolvedPlugins(plugins []*Plugin) []*Plugin {
	if len(plugins) == 0 {
		return nil
	}
	out := make([]*Plugin, 0, len(plugins))
	for _, p := range plugins {
		clone := *p
		enabled := p.IsEnabled()
		clone.Enabled = &enabled
		clone.Order = p.order()
		out = append(out, &clone)
	}
	return out
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
