// SPDX-License-Identifier: GPL-3.0-or-later

package engine

import "sort"

// Where middleware conventionally sits in its chain.
//
// This is a table of positions, not a list of working parts. A name here is a
// claim about where something would go if it existed, and nothing more: what
// exists is what a registry says exists, and these are catalogued from Scrapy's
// built-in middleware because copying a known-good ordering is cheaper than
// rediscovering it.
//
// Keeping the two apart matters. If this table decided what a job may name, a
// catalogue of intentions would validate as a set of working parts, and the
// failure would arrive at run time on somebody else's machine.
//
// # Chains run both ways
//
// A chain wraps its stage, so every link sees the request on the way out and
// the response on the way back, in opposite orders. Low order is nearest the
// spider; high order is nearest the network.
//
//	order:      500        550        900
//	         ┌────────┐ ┌────────┐ ┌────────┐
//	request  │offsite │→│ retry  │→│ cache  │→ ─┐
//	         │        │ │        │ │        │   │ network
//	response │        │←│        │←│        │← ─┘
//	         └────────┘ └────────┘ └────────┘
//
// That is why the numbers are what they are. `offsite` at 500 drops a URL out
// of scope before anything else pays for it. `cache` at 900 is the last thing
// before the network, so a hit short-circuits the fetch only after every other
// request middleware has had its say, which is where Scrapy puts
// HttpCacheMiddleware and for the same reason.
//
// robots is not here because it is not a plugin: `robots = true` is an
// attribute of the downloader block, since there is nowhere else it could be
// written and nothing to reorder it against.
type Placement struct {
	// Name is the second label of a plugin block.
	Name string
	// Order is where it sits when the job does not say.
	Order int
	// Doc is one line about what it would do. Present tense because it reads
	// better, not because it is implemented.
	Doc string
}

// Placements is the conventional order of every middleware scour intends to
// have, by stage. Almost none of them are written yet.
var Placements = map[Stage][]Placement{
	StageDownloader: {
		{"offsite", 500, "Drops URLs outside domains, included and excluded"},
		{"contenttype", 520, "Refuses by extension and MIME before the body is read"},
		{"cookies", 543, "Session cookies, per host"},
		{"auth", 544, "HTTP authentication"},
		{"retry", 550, "Retries the temporarily failed"},
		{"headers", 560, "Default request headers"},
		{"metarefresh", 580, "Follows meta-refresh redirects"},
		{"proxy", 610, "Routes through a proxy"},
		{"stats", 850, "Counts requests, responses and failures"},
		{"cache", 900, "Reads and writes the page cache"},
	},
	StageSpider: {
		{"httperror", 50, "Drops non-2xx before anything parses them"},
		{"topic", 300, "Scores a page against a topic, and drops what is off it"},
		{"offsite", 500, "Drops discovered links outside scope"},
		{"referer", 700, "Sets Referer from the page a link was found on"},
		{"urllength", 800, "Drops absurdly long URLs"},
		{"depth", 900, "Tracks depth and enforces max_depth"},
	},
	// The scheduler chains the same way the downloader does. A request passes
	// through on its way into the frontier and back out on its way to the
	// downloader, so low order is nearest the spider that discovered it and
	// high order is nearest the queue itself.
	//
	// That ordering is what makes the numbers mean something: dupefilter at
	// 100 drops a URL already seen before anything else pays to think about
	// it, and the ordering policy at 500 sits against the queue, because
	// deciding what comes out next is the last thing that happens on the way
	// in and the first on the way out.
	StageScheduler: {
		{"dupefilter", 100, "Decides what counts as already seen"},
		{"offsite", 200, "Drops URLs outside domains, included and excluded"},
		{"cron", 300, "Defers a URL until it is due again"},
		{"budget", 400, "Refuses a URL the job can no longer pay for"},
		{"topic", 450, "Scores a URL against a topic, before the policy orders it"},
		{"priority", 500, "Best first, by score. The default"},
		{"breadth", 500, "Level by level, for an archival crawl"},
		{"depth", 500, "Follows a spur down before returning"},
		{"random", 500, "Samples without the sample being shaped by the scorer"},
	},
}

// PipelineKinds is what a `pipelines` block's first label may be.
//
// These are not plugins and have no order. A pipeline step is a node in a
// graph: it runs when its dependencies have run, and `requires` is how it says
// which those are. Ordering it by number as well would be two ways of saying
// the same thing, and they would disagree.
var PipelineKinds = []Placement{
	{"clean", 0, "Rule-driven tidying"},
	{"validate", 0, "Enforces required and types"},
	{"dedupe", 0, "Drops items already seen"},
	{"rank", 0, "Scores and orders"},
	{"python", 0, "Runs a Python script, inline or from a file"},
	{"rhai", 0, "Runs a Rhai script, inline or from a file"},
	{"nodejs", 0, "Runs a Node script, inline or from a file"},
	{"bash", 0, "Runs a shell script, inline or from a file"},
}

// PipelineKindNames lists what ships, for an error message.
func PipelineKindNames() []string {
	out := make([]string, 0, len(PipelineKinds))
	for _, k := range PipelineKinds {
		out = append(out, k.Name)
	}
	sort.Strings(out)
	return out
}

// Decoding is not here, and that is the one place this catalogue departs from
// Scrapy's on purpose.
//
// Turning bytes into text is what reading a body means, not a step in a chain.
// Two things read a body: the downloader on its way to the cache, and the
// spider, which reads the cache directly by key and never passes through this
// chain at all. A decode that lived here would apply to one of them and not the
// other. It is [internal/decode], a function both call.
//
// The cache therefore holds what the server sent, which is the more useful
// archive: detection improves, and original bytes can be decoded again to get a
// better answer, while a corpus decoded on the way in has its mistakes baked in
// until somebody re-crawls.

// DefaultOrder is where a catalogued middleware conventionally sits.
//
// The second return is false for a name with no conventional position, which is
// not an error: a plugin somebody else wrote is the point. It does mean the job
// has to say where it goes, because we cannot guess.
func DefaultOrder(stage Stage, name string) (int, bool) {
	for _, b := range Placements[stage] {
		if b.Name == name {
			return b.Order, true
		}
	}
	return 0, false
}

// PlacementNames lists the catalogued names for a stage.
func PlacementNames(stage Stage) []string {
	list := Placements[stage]
	out := make([]string, 0, len(list))
	for _, b := range list {
		out = append(out, b.Name)
	}
	return out
}
