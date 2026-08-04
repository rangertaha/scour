// SPDX-License-Identifier: GPL-3.0-or-later

package engine

// The built-in middleware, and where each sits in its chain.
//
// Taken from Scrapy's built-in middleware and its ordering logic, adapted where
// scour needs something Scrapy gets elsewhere.
//
// # Chains run both ways
//
// A chain wraps its stage, so every link sees the request on the way out and
// the response on the way back, in opposite orders. Low order is nearest the
// spider; high order is nearest the network.
//
//	order:      100        550        900
//	         ┌────────┐ ┌────────┐ ┌────────┐
//	request  │ robots │→│ retry  │→│ cache  │→ ─┐
//	         │        │ │        │ │        │   │ network
//	response │        │←│        │←│        │← ─┘
//	         └────────┘ └────────┘ └────────┘
//
// That is why the numbers are what they are. `robots` at 100 refuses a request
// before anything else pays for it. `cache` at 900 is the last thing before the
// network, so a hit short-circuits the fetch only after every other request
// middleware has had its say, which is where Scrapy puts HttpCacheMiddleware
// and for the same reason.
type Builtin struct {
	// Name is the second label of a plugin block.
	Name string
	// Order is where it sits when the job does not say.
	Order int
	// Doc is one line about what it does.
	Doc string
}

// Builtins is every plugin that ships, by stage.
var Builtins = map[Stage][]Builtin{
	StageDownloader: {
		{"robots", 100, "Refuses what robots.txt forbids"},
		{"useragent", 400, "Sets the User-Agent"},
		{"offsite", 500, "Drops URLs outside domains, included and excluded"},
		{"contenttype", 520, "Refuses by extension and MIME before the body is read"},
		{"timeout", 540, "Per-request deadline"},
		{"cookies", 543, "Session cookies, per host"},
		{"auth", 544, "HTTP authentication"},
		{"retry", 550, "Retries the temporarily failed"},
		{"headers", 560, "Default request headers"},
		{"metarefresh", 580, "Follows meta-refresh redirects"},
		{"compression", 590, "gzip, deflate, br, zstd"},
		{"charset", 600, "Transcodes the body to UTF-8"},
		{"proxy", 610, "Routes through a proxy"},
		{"redirect", 630, "Follows HTTP redirects"},
		{"maxsize", 700, "Refuses bodies over the limit"},
		{"stats", 850, "Counts requests, responses and failures"},
		{"cache", 900, "Reads and writes the page cache"},
	},
	StageSpider: {
		{"httperror", 50, "Drops non-2xx before anything parses them"},
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
		{"scope", 200, "Drops URLs outside domains, included and excluded"},
		{"cron", 300, "Defers a URL until it is due again"},
		{"budget", 400, "Refuses a URL the job can no longer pay for"},
		{"priority", 500, "Best first, by score. The default"},
		{"breadth", 500, "Level by level, for an archival crawl"},
		{"depth", 500, "Follows a spur down before returning"},
		{"random", 500, "Samples without the sample being shaped by the scorer"},
	},
	StagePipeline: {
		{"clean", 100, "Rule-driven tidying"},
		{"validate", 200, "Enforces required and types"},
		{"dedupe", 300, "Drops items already seen"},
		{"rank", 400, "Scores and orders"},
	},
}

// charset has no Scrapy equivalent, because Scrapy decodes in its response
// object rather than in a middleware. It is not optional here, and it must run
// after compression and before cache: bodies are cached transcoded, so the
// corpus is UTF-8 whatever the site served. Getting this wrong does not merely
// score badly, it poisons the evidence every later measurement is taken
// against.

// DefaultOrder is where a built-in sits when the job does not say.
//
// The second return is false for a plugin scour does not ship, which is not an
// error: a plugin somebody else wrote is the point. It does mean the job has to
// say where it goes, because we cannot guess.
func DefaultOrder(stage Stage, name string) (int, bool) {
	for _, b := range Builtins[stage] {
		if b.Name == name {
			return b.Order, true
		}
	}
	return 0, false
}

// BuiltinNames lists what ships for a stage.
func BuiltinNames(stage Stage) []string {
	list := Builtins[stage]
	out := make([]string, 0, len(list))
	for _, b := range list {
		out = append(out, b.Name)
	}
	return out
}
