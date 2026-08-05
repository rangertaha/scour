// SPDX-License-Identifier: GPL-3.0-or-later

package engine

import (
	"fmt"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
)

// Submitting a job whose name is already running mutates it and applies the
// changes. The name is the identity: a client resubmitting the same crawl means
// the same crawl, and that is how they say so.
//
// What "applies" means is not the same for every change, which is what this
// file is about. Raising a page budget takes effect on the next request and
// nobody needs to know. Moving the cache to a different bucket makes every body
// already fetched unreachable, and applying that silently would look like a
// crawl that suddenly forgot everything it had done.
//
// So a change reports its effect, and whoever accepts the submission decides
// what to do about the ones that are not free.

// Effect is what applying a change costs.
type Effect int

// The effects, from free to expensive.
const (
	// EffectImmediate takes effect on the next request and disturbs nothing.
	EffectImmediate Effect = iota
	// EffectReseed adds start URLs to a frontier that is already running.
	EffectReseed
	// EffectRescope changes what is in bounds, so entries already queued may
	// no longer be, and pages already fetched may no longer be wanted.
	EffectRescope
	// EffectReextract changes the shape being extracted, so records written
	// before the change and after it do not agree.
	EffectReextract
	// EffectCacheMoved points the cache somewhere else, which leaves every
	// body already fetched where the job can no longer see it.
	EffectCacheMoved
)

// Free reports whether a change can be applied without anyone being told.
func (e Effect) Free() bool { return e == EffectImmediate }

func (e Effect) String() string {
	switch e {
	case EffectReseed:
		return "reseed"
	case EffectRescope:
		return "rescope"
	case EffectReextract:
		return "reextract"
	case EffectCacheMoved:
		return "cache moved"
	default:
		return "immediate"
	}
}

// Change is one difference between the job that is running and the job that
// was just submitted.
type Change struct {
	// Path names what changed, as it is written in the document.
	Path string
	// From and To are what it was and what it becomes. Empty From means added;
	// empty To means removed.
	From string
	To   string
	// Effect is what applying it costs.
	Effect Effect
}

func (c Change) String() string {
	switch {
	case c.From == "":
		return fmt.Sprintf("%s: added %s", c.Path, c.To)
	case c.To == "":
		return fmt.Sprintf("%s: removed %s", c.Path, c.From)
	default:
		return fmt.Sprintf("%s: %s -> %s", c.Path, c.From, c.To)
	}
}

// Changes is what a resubmission would do.
type Changes []Change

// Any reports whether the submission changes anything at all. A resubmission
// of an identical document is a no-op, and saying so is more useful than
// applying nothing and reporting success.
func (c Changes) Any() bool { return len(c) > 0 }

// Costly returns the changes that are not free, which are the ones a client
// should be made to look at before they happen.
func (c Changes) Costly() Changes {
	var out Changes
	for _, change := range c {
		if !change.Effect.Free() {
			out = append(out, change)
		}
	}
	return out
}

// Diff reports what applying the submitted job to the running one would change.
//
// Both jobs are assumed to have the same name; comparing two differently named
// jobs is a question nobody asked.
func Diff(running, submitted *Job) Changes {
	var out Changes

	out = append(out, diffList("start", running.Start, submitted.Start, EffectReseed)...)
	out = append(out, diffList("domains", running.Domains, submitted.Domains, EffectRescope)...)
	out = append(out, diffList("included", running.Included, submitted.Included, EffectRescope)...)
	out = append(out, diffList("excluded", running.Excluded, submitted.Excluded, EffectRescope)...)

	out = append(out, diffItems(running.Items, submitted.Items)...)
	out = append(out, diffStages(running, submitted)...)
	out = append(out, diffPlugins(running.Plugins(), submitted.Plugins())...)
	out = append(out, diffBlocks("step", stepFingerprints(running.Steps()), stepFingerprints(submitted.Steps()), EffectImmediate)...)
	out = append(out, diffBlocks("exporter", exporterFingerprints(running.Exporters), exporterFingerprints(submitted.Exporters), EffectImmediate)...)

	sort.SliceStable(out, func(a, b int) bool { return out[a].Path < out[b].Path })
	return out
}

// diffList compares two sets of strings, which is what every scope field is.
// Order is not meaningful in any of them, so a reordering is not a change.
func diffList(path string, from, to []string, effect Effect) Changes {
	var out Changes

	was := set(from)
	now := set(to)

	for _, v := range sorted(now) {
		if !was[v] {
			out = append(out, Change{Path: path, To: v, Effect: effect})
		}
	}
	for _, v := range sorted(was) {
		if !now[v] {
			// Removing a start URL changes nothing already fetched, so it is
			// only a rescope when the field decides what is in bounds.
			e := effect
			if path == "start" {
				e = EffectImmediate
			}
			out = append(out, Change{Path: path, From: v, Effect: e})
		}
	}
	return out
}

func diffItems(running, submitted []*Item) Changes {
	var out Changes

	was := map[string]string{}
	for _, i := range running {
		was[i.Name] = i.fingerprint()
	}
	now := map[string]string{}
	for _, i := range submitted {
		now[i.Name] = i.fingerprint()
	}

	for _, name := range sortedKeys(now) {
		path := "item." + name
		switch old, existed := was[name]; {
		case !existed:
			// A new item extracts nothing retroactively, but everything
			// fetched from here on is read for a shape that was not being
			// looked for before.
			out = append(out, Change{Path: path, To: "added", Effect: EffectReextract})
		case old != now[name]:
			out = append(out, Change{Path: path, From: "schema", To: "changed", Effect: EffectReextract})
		}
	}
	for _, name := range sortedKeys(was) {
		if _, still := now[name]; !still {
			out = append(out, Change{Path: "item." + name, From: "removed", Effect: EffectReextract})
		}
	}
	return out
}

func diffStages(running, submitted *Job) Changes {
	var out Changes

	for path, pair := range stageFields(running, submitted) {
		if pair[0] != pair[1] {
			out = append(out, Change{Path: path, From: pair[0], To: pair[1], Effect: EffectImmediate})
		}
	}
	return out
}

// stageFields flattens both jobs' stage settings to comparable strings.
//
// Everything here is free to change: a budget, a delay and a user agent are
// read per request, so the next request simply reads the new one.
func stageFields(a, b *Job) map[string][2]string {
	return map[string][2]string{
		"scheduler.policy":      {a.Scheduler.OrderPolicy(), b.Scheduler.OrderPolicy()},
		"scheduler.rate":        {rateOf(a.Scheduler), rateOf(b.Scheduler)},
		"scheduler.concurrency": {itoa(a.Scheduler.Parallelism()), itoa(b.Scheduler.Parallelism())},
		"scheduler.max_depth":   {itoa(a.Scheduler.Depth()), itoa(b.Scheduler.Depth())},
		"scheduler.max_pages":   {itoa(a.Scheduler.Pages()), itoa(b.Scheduler.Pages())},
		"scheduler.max_time":    {budgetOf(a.Scheduler), budgetOf(b.Scheduler)},
		"downloader.robots":     {btoa(a.Downloader.ObeysRobots()), btoa(b.Downloader.ObeysRobots())},
		"downloader.user_agent": {a.Downloader.Agent(), b.Downloader.Agent()},
		"downloader.timeout":    {timeoutOf(a.Downloader), timeoutOf(b.Downloader)},
		"downloader.max_body":   {i64toa(a.Downloader.BodyBytes()), i64toa(b.Downloader.BodyBytes())},
		"downloader.external":   {btoa(a.Downloader.IsExternal()), btoa(b.Downloader.IsExternal())},
		"spider.external":       {btoa(a.Spider.IsExternal()), btoa(b.Spider.IsExternal())},
		"pipeline.external":     {btoa(a.Pipeline.IsExternal()), btoa(b.Pipeline.IsExternal())},
	}
}

func diffPlugins(running, submitted []*Plugin) Changes {
	var out Changes

	was := map[string]*Plugin{}
	for _, p := range running {
		was[string(p.Stage())+"."+p.Name] = p
	}
	now := map[string]*Plugin{}
	for _, p := range submitted {
		now[string(p.Stage())+"."+p.Name] = p
	}

	for _, key := range sortedPluginKeys(now) {
		path := "plugin." + key
		p := now[key]
		switch old, existed := was[key]; {
		case !existed:
			out = append(out, Change{Path: path, To: "added", Effect: effectOfPlugin(p)})
		case old.fingerprint() != p.fingerprint():
			out = append(out, Change{Path: path, From: "config", To: "changed", Effect: effectOfPlugin(p)})
		}
	}
	for _, key := range sortedPluginKeys(was) {
		if _, still := now[key]; !still {
			out = append(out, Change{Path: "plugin." + key, From: "removed", Effect: effectOfPlugin(was[key])})
		}
	}
	return out
}

// effectOfPlugin is immediate for everything except the cache, which is the one
// plugin whose configuration says where work already done is kept.
func effectOfPlugin(p *Plugin) Effect {
	if p.Stage() == StageDownloader && p.Name == "cache" {
		return EffectCacheMoved
	}
	return EffectImmediate
}

func diffBlocks(kind string, was, now map[string]string, effect Effect) Changes {
	var out Changes

	for _, key := range sortedKeys(now) {
		path := kind + "." + key
		switch old, existed := was[key]; {
		case !existed:
			out = append(out, Change{Path: path, To: "added", Effect: effect})
		case old != now[key]:
			out = append(out, Change{Path: path, From: "config", To: "changed", Effect: effect})
		}
	}
	for _, key := range sortedKeys(was) {
		if _, still := now[key]; !still {
			out = append(out, Change{Path: kind + "." + key, From: "removed", Effect: effect})
		}
	}
	return out
}

// snapshot records the source text of every block carrying an undecoded body.
//
// A plugin's own fields are opaque to this package by design, so the only
// honest way to tell whether one changed is to compare what was written. The
// alternative would be for this package to know every plugin's schema, which is
// the coupling the opaque body exists to avoid.
func (j *Job) snapshot(src []byte) {
	for _, p := range j.Plugins() {
		p.raw = bodyText(src, p.Config)
	}
	for _, p := range j.Steps() {
		p.raw = bodyText(src, p.Config)
	}
	for _, e := range j.Exporters {
		e.raw = bodyText(src, e.Config)
	}
}

// bodyText is the source of a block body, byte for byte.
func bodyText(src []byte, body hcl.Body) string {
	b, ok := body.(*hclsyntax.Body)
	if !ok {
		return ""
	}
	r := b.SrcRange
	if r.Start.Byte < 0 || r.End.Byte > len(src) || r.Start.Byte > r.End.Byte {
		return ""
	}
	return string(src[r.Start.Byte:r.End.Byte])
}

func (p *Plugin) fingerprint() string {
	return fmt.Sprintf("%d|%v|%s", p.Order, p.Enabled != nil && !*p.Enabled, p.raw)
}

func stepFingerprints(steps []*Step) map[string]string {
	out := make(map[string]string, len(steps))
	for _, p := range steps {
		reqs := append([]string(nil), p.requires...)
		sort.Strings(reqs)
		out[p.Address()] = fmt.Sprintf("%s|%s|%s|%s", strings.Join(reqs, ","), p.Inline, p.Script, p.raw)
	}
	return out
}

func exporterFingerprints(exporters []*Exporter) map[string]string {
	out := make(map[string]string, len(exporters))
	for _, e := range exporters {
		out[e.Address()] = e.raw
	}
	return out
}

// fingerprint reduces an item to a string that changes when its shape does.
func (i *Item) fingerprint() string {
	var b strings.Builder
	b.WriteString(i.Type)
	b.WriteByte('|')
	writeProperties(&b, i.Properties)

	// Relations are shape rather than evidence: changing one changes what is
	// asserted about the world, so it is a re-extraction and not a free edit.
	relations := append([]*Relation(nil), i.Relations...)
	sort.Slice(relations, func(a, c int) bool { return relations[a].Name < relations[c].Name })
	for _, r := range relations {
		fmt.Fprintf(&b, "[%s:%s:%s:%s]", r.Name, r.Entity, r.Property, strings.Join(r.Topic, ","))
	}
	return b.String()
}

func writeProperties(b *strings.Builder, props []*Property) {
	// Sorted, so reordering properties in a document is not a schema change.
	sorted := append([]*Property(nil), props...)
	sort.Slice(sorted, func(a, c int) bool { return sorted[a].Name < sorted[c].Name })

	for _, p := range sorted {
		// Examples are deliberately absent from the fingerprint. They are
		// evidence about a shape rather than part of it, so adding one must
		// not read as a schema change and force a re-extraction of records
		// that are still correct.
		fmt.Fprintf(b, "(%s:%s:%s:%t:%s:%s:%s:%s:%s",
			p.Name, p.Type, p.Entity, p.Required,
			strings.Join(p.Aliases, ","),
			strings.Join(p.Regexes, ","),
			strings.Join(p.Transforms, ","),
			strings.Join(p.XPath, ","),
			strings.Join(p.CSS, ","))
		writeProperties(b, p.Properties)
		b.WriteByte(')')
	}
}

func set(values []string) map[string]bool {
	out := make(map[string]bool, len(values))
	for _, v := range values {
		out[v] = true
	}
	return out
}

func sorted(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for v := range m {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedPluginKeys(m map[string]*Plugin) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func itoa(v int) string     { return fmt.Sprintf("%d", v) }
func i64toa(v int64) string { return fmt.Sprintf("%d", v) }
func btoa(v bool) string    { return fmt.Sprintf("%t", v) }

func budgetOf(s *Scheduler) string {
	d, err := s.MaxTimeDuration()
	if err != nil {
		return "invalid"
	}
	return d.String()
}

func rateOf(s *Scheduler) string {
	d, err := s.RateDuration()
	if err != nil {
		return "invalid"
	}
	return d.String()
}

func timeoutOf(d *Downloader) string {
	v, err := d.RequestTimeout()
	if err != nil {
		return "invalid"
	}
	return v.String()
}
