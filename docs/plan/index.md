---
title: plan
description: What scour is made of, why each part is where it is, and what is built against what is still designed.
---

# plan

## 1. What scour is, in terms of its parts

scour is a focused crawler: it decides *what to fetch next*, fetches it, and
pulls records out of what comes back. Two of those three problems already have
good answers, and the plan is built on not rewriting them.

| Concern | Owner | Why |
| --- | --- | --- |
| Crawling: scheduling, queueing, robots, cookies, retries, redirects, link discovery, per-host politeness | **colly** | The crawl engine. Mature, hookable at every stage, and pluggable exactly where scour needs to substitute its own behaviour |
| Parsing documents and locating fields inside them | **wom**, scour's document engine at `internal/wom` | Induces XPath, CSS, JSONPath and regex locators with probabilities, across HTML, XML, RSS, JSON, JS, CSS and PDF |
| Deciding which URL is worth fetching next | **scour** | This is the actual product |

wom maps onto the README's vocabulary almost one to one, which is the strongest
argument for using it:

| README | wom |
| --- | --- |
| item, aliases, properties, examples | `wom.Prop{Name, Aliases, Examples, Props}` |
| `scour train` | `w.Model(schema...)`, plus `model.Train` from corrected items |
| `scour rules` output (ID, PID, HIT, XPATH, SELECTOR, REGEX, URL) | `schema.Item` and `schema.Locator`, nested, each with `p=` |
| `models/<name>.json` | `model.Save(path)` |
| `valid` / `invalid` then retrain | `model.Train(w, correctedItems...)` |
| content types | wom's format table |

So scour owns: the scoring model that orders colly's queue, storage, the service
topology, labelling, and the interfaces to the outside world.

colly is the crawl engine, not a fetching library called from a hand-rolled
loop. scour does not reimplement scheduling, robots, cookies, retries,
redirects, depth tracking or link discovery; it supplies colly with the pieces
colly leaves open and lets colly drive. The three seams that make this work:

| colly seam | What scour puts there |
| --- | --- |
| `queue.Storage` | A priority queue ordered by predicted probability, backed by NATS JetStream and gorm, so "what next" becomes scour's decision without touching colly's loop |
| `storage.Storage` | Visited-set and cookie jar backed by the same database, so a restart resumes and several crawl processes share one view |
| `http.RoundTripper` | Transport selection per request, which is how webdriver, caching and offline replay all plug in without a second code path |

## 2. Architecture

One binary, many roles. Components never call each other directly; they publish
and subscribe on NATS. In the default single-process mode scour runs an
**embedded NATS server** in-process, so a laptop user gets a normal CLI tool
with no broker to install. Point it at an external NATS cluster and the same
components spread across machines with no code change.

```
                      ┌──────────── embedded NATS + JetStream ────────────┐
                      │                                                   │
   scour CLI ────────>│  crawl.url      fetch.req      doc.parsed         │<──── scour server
   scour mcp ────────>│  fetch.res      url.scored     record.new         │      (HTTP + MCP)
                      │  train.req      train.done     sys.status         │
                      └───┬──────┬──────────┬──────────┬─────────┬────────┘
                          │      │          │          │         │
                     ┌────▼─┐ ┌──▼───┐ ┌────▼───┐ ┌────▼────┐ ┌──▼─────┐
                     │queue │ │crawl │ │ parse  │ │ score   │ │ store  │
                     │(prio)│ │colly │ │ (wom)  │ │(matcher)│ │(gorm)  │
                     └──────┘ └──┬───┘ └────────┘ └─────────┘ └────────┘
                                 │
                     colly Collector, one per host bucket
                                 │
                        ┌────────┴────────┐
                   http.Transport     webdriver
                                    (RoundTripper)
```

### 2.1 Components

Each is a `Service` with `Start(ctx, *nats.Conn) error`. Each can be disabled by
config, and `scour join --role crawl` starts a subset.

- **queue** implements colly's `queue.Storage` over JetStream and gorm. Dedupes
  by normalised URL hash, enforces budget, and pops in score order rather than
  colly's default FIFO. Depth, robots and per-host pacing stay colly's job.
- **crawl** owns the colly `Collector`s and is the only component that talks to
  the network. It registers the callbacks in 2.3, and hands each response to the
  bus. Everything colly already does well is left alone.
- **parser** consumes `fetch.res`, loads the body, builds the wom graph
  (`w.AddBody(url, contentType, body)`), publishes `doc.parsed` carrying
  discovered links, extracted text and the graph handle.
- **scorer** consumes `doc.parsed`, scores every discovered link against the
  item's model, publishes `url.scored` which the queue consumes. Also
  scores the page itself, which is what fills the `MATCHES` and `PROBABILITY`
  columns.
- **extractor** consumes `doc.parsed`, applies the item's saved wom model,
  publishes `record.new`.
- **trainer** consumes `train.req`, replays cached pages into a wom graph, runs
  induction plus `model.Train` over labelled items, writes the model, publishes
  `train.done`.
- **store** consumes everything durable and writes it through gorm. The only
  component that touches the database.
- **api** serves HTTP and MCP. Publishes commands, reads state through store.

### 2.2 Subjects and streams

Subjects are `scour.<item>.<stage>` so a subscriber can wildcard one item or
all of them. JetStream carries the work queues so a restart resumes rather than
losing the frontier.

| Stream | Subjects | Retention | Notes |
| --- | --- | --- | --- |
| `FETCH` | `scour.*.fetch.req` | work queue | One ack per URL, `Nats-Msg-Id` = URL hash for dedupe |
| `DOCS` | `scour.*.doc.parsed` | work queue | Fan-out to scorer and extractor via separate durable consumers |
| `RECORDS` | `scour.*.record.new` | limits, 7d | Store consumes; exporters can replay |
| `CONTROL` | `scour.*.train.*`, `scour.sys.*` | limits, 1h | Commands and status |

At-least-once delivery plus idempotent writes (upsert on URL hash, record
fingerprint) is the correctness model. Nothing assumes exactly-once.

### 2.3 How colly is wired

One `Collector` per host bucket, built from the `[[host]]` config, sharing the
gorm-backed storage and the scored queue. The callbacks are the whole
integration:

| Callback | What scour does there |
| --- | --- |
| `OnRequest` | Attach item, depth and trace id to `r.Ctx`; abort if the extension clearly disagrees with the allowed content types, which is the pre-request half of the type filter |
| `OnResponseHeaders` | Check the real `Content-Type` and `Content-Length`; `r.Request.Abort()` on mismatch or oversize, so an unwanted body is never downloaded |
| `OnResponse` | Write the body to the page cache, publish `fetch.res` |
| `OnHTML("a[href]")` | Collect links, score them, and `e.Request.Visit` only those above the cutoff. Scoring happens here, so colly's own depth and domain rules still apply on top |
| `OnError` | Classify: retry with backoff, escalate to webdriver, or record the failure. Feeds the status-class columns |
| `OnScraped` | Emit per-page metrics and close the trace |

colly features scour uses rather than reimplements: `MaxDepth`, `AllowedDomains`
and `URLFilters` from the target list, `Limit` with `LimitRule{DomainGlob, Delay,
Parallelism, RandomDelay}` mapped directly from `[[host]]`, `Async` with colly's
worker pool as the bounded concurrency, robots.txt handling, cookie jar,
redirect policy via `SetRedirectHandler`, and `extensions.Referer` plus a
configured user agent.

The escalation path is deliberately not a second crawler: webdriver is an
`http.RoundTripper` installed with `Collector.WithTransport`, so a JS-rendered
page travels the same callbacks, the same queue and the same metrics as any
other response.

## 3. Package layout

All code lives under `cmd/` and `internal/`. There is no root package and no
exported library API: scour is a program, not a library, and keeping the whole
implementation in `internal/` means nothing outside the module can depend on a
shape before it has settled. The one exception is the module's own tooling
(`Makefile`, `.golangci.yml`, `.goreleaser.yaml`), which lives at the root.

`cmd/scour` stays thin: flag wiring, config loading, and a call into
`internal/...`. No business logic in `cmd`, so every behaviour is reachable from
a test that does not shell out.

```
cmd/scour/              main and the root command's wiring
internal/cli/           what every command needs: App, args, tables, help
internal/cli/urls/      import, export
internal/cli/items/     item add|rm|ls|tag|templates, stream
internal/cli/learn/     rules, train, mark
internal/cli/search/    status, top, start, stop, pause
internal/cli/serve/     mcp, server, join
internal/wom/           the document engine: graph, parse, match, seq, infer, model
internal/config/        config.toml, env, flag precedence
internal/bus/           embedded NATS, JetStream setup, subjects, codecs
internal/service/       Service interface, supervisor, role selection
internal/store/         gorm models, migrations, repositories
internal/crawl/         colly collectors, callbacks, host buckets
  crawl/queue/          colly queue.Storage: scored priority queue
  crawl/storage/        colly storage.Storage: visited set and cookie jar
internal/transport/     RoundTripper interface + registry
  transport/http/       net/http with the configured dialer and proxy
  transport/webdriver/  chromedp behind a RoundTripper
  transport/replay/     serves the page cache, for offline tests
internal/parse/         wom graph construction, link extraction
internal/score/         Scorer interface + registry
  score/bayes/          naive bayes over URL and anchor features
  score/embed/          ONNX embedding similarity
  score/hmm/            page-role chain: Viterbi decode, forward scoring, MAP fit
internal/matcher/       wom Matcher implementations + registry
  matcher/heuristic/    wom's built-in
  matcher/llm/          Claude and other providers
internal/extract/       wom model application
internal/train/         induction, labelling, model lifecycle
internal/export/        Exporter interface + registry (csv, json, webhook)
internal/api/           HTTP handlers
internal/mcp/           MCP server, stdio and HTTP
```

Everything is `internal/` until v1, following wom's reasoning: promoting a
package later is non-breaking, demoting one is not.

The five groups under `internal/cli` are grouped by what a command does, which
is what the help prints. M11 regroups them by the noun they act on, so they
become `item`, `job`, `record`, `model` and `node`. That is a rename of the
directories and the constructors, not of the machinery: the shared `App`, the
argument checks and the registry-shaped help stay where they are.

## 4. Extension model

Five registries, all the same shape, all populated from `init()` so a plugin is
a blank import:

```go
// internal/transport/registry.go
//
// The extension point is http.RoundTripper, not a bespoke Fetcher interface,
// because that is what colly already accepts. A plugin that satisfies the
// standard library satisfies scour.
func Register(name string, f func(Config) (http.RoundTripper, error))
```

| Registry | Interface | Ships with | Selected by |
| --- | --- | --- | --- |
| transport | `http.RoundTripper` | http, webdriver, replay | `[[host]] transport = "webdriver"` |
| score | `Scorer` | bayes, embed, hmm | `[model] scorer = "hmm"` |
| matcher | `wom.Matcher` | heuristic, llm | `[model] matcher = "heuristic"` |
| store | gorm dialector | sqlite, postgres, mysql | `[store] driver` |
| export | `Exporter` | csv, json, webhook | `scour stream --write` |
| schedule | `Policy` | best, breadth, depth, random, warmup | `[crawl] scheduler` |
| refresh | `Refresh` | cron (planned) | `[crawl] refresh` |
| classify | `Classifier` | llm | `[model] classifier` |
| cache | `Store` | local, s3, gcs | `[cache] driver` |
| nodeclass | `Classifier` | recency, topic (both planned) | not yet wired |

A third-party build is then:

```go
import (
    _ "github.com/rangertaha/scour/internal/transport/webdriver"
    _ "github.com/example/scour-transport-splash"
)
```

All of them are one type. `internal/registry` holds the generic registry the
six older points used to each carry a copy of, and [Extending it]({{ '/architecture/extending.html' | relative_url }}) documents how to
add an implementation and how to add a kind.

`nodeclass` is the seat for classifying nodes of the crawl graph: what role a
URL plays, what it is about, how fresh it is. The role question already has a
decoder in `internal/score/hmm` whose answer nothing reads; moving it behind
this point and reading it is what closes the section-pages-as-records fault.


## 5. AI models

Two separate jobs, deliberately not merged:

**Scoring a URL** happens millions of times and must be microseconds. Default is
naive bayes over URL path tokens, anchor text, and page context, persisted as
`models/<name>.json`. An embedding scorer using a small ONNX model is the
upgrade path for semantic similarity without a network call.

**Understanding a page** happens rarely, during induction, and can be expensive.
This is wom's `Matcher`, and it is the single seam where a language model
changes the intelligence of the whole engine:

```go
w := wom.New(wom.WithMatcher(llm.New(cfg)))
```

Provider config, one section per model, referenced by name:

```toml
[model]
scorer  = "bayes"
matcher = "heuristic"          # or "llm"

[[ai]]
name     = "claude"
provider = "anthropic"
model    = "claude-opus-5"     # sonnet-5 for volume, haiku-4-5 for cheap passes
effort   = "low"
api_key_env = "ANTHROPIC_API_KEY"

[[ai]]
name     = "local"
provider = "onnx"
path     = "/var/lib/scour/models/minilm.onnx"
```

The LLM matcher uses structured outputs so the response validates against the
prop schema rather than being parsed out of prose, and every call is cached by
content hash so induction over 400 pages does not mean 400 calls.

### 5.1 Two hidden Markov chains

Both the extraction side and the crawl side have sequences in them, and both are
badly served by scoring each element in isolation. They get the same treatment,
on two different axes.

| | wom's chain | scour's chain |
| --- | --- | --- |
| Runs over | fields inside one record | pages along a crawl path |
| Hidden states | one per prop, plus background | page roles: seed, hub, pagination, detail, boilerplate, dead |
| Observation | a candidate node's `Matcher` score | a URL's `Scorer` score, plus what the fetch returned |
| Transitions | how records are written: make, then model, then year | how sites are laid out: hub leads to detail, hub leads to next page |
| Buys | records with missing fields and varying field order | tunnelling: crediting a hub that holds no records but leads to many |
| Ships as | `wom.WithHMM`, `wom.WithChainPrior` | `score/hmm`, a `Scorer` |

**The extraction chain is wom's and already exists.** scour's job is to use it
properly rather than reimplement it: enable it during induction, persist the
fitted chain, and reuse it. Because a chain describes how people write records
rather than how one site marks them up, it transfers across sites and across
items, so it is stored once at `models/chain.json` and seeded into every
induction:

```go
w := wom.New(
    wom.WithMatcher(matcher),
    wom.WithChainPrior(shared),  // learned across every crawl so far
    wom.WithHMM(20),             // fitting passes; -1 disables
)
```

This matters most for PDF. There is no tree in a PDF, only a flat run of lines,
so the chain is the only structural signal available, and scour crawls PDFs.

**The crawl chain is scour's and is new.** A naive bayes scorer judges a URL on
its own tokens, which fails on the classic focused-crawling problem: the page
that leads to a hundred records often contains none itself and scores near zero,
so the crawler never follows it. Modelling the crawl path as a Markov chain over
page roles fixes that, because the value of a link is the expected value of
where it leads.

- **Decode** the role of each fetched page with Viterbi over the path that
  reached it. A page is not classified in isolation; a page reached from a hub,
  matching pagination shape, and yielding no records is pagination.
- **Score** a discovered link as the forward probability of reaching a `detail`
  state within *k* hops, given the decoded role of the page it was found on.
  That is what the `PROBABILITY` column becomes once the chain is in play.
- **Learn** transitions from crawl outcomes: which role actually followed which,
  and where records were actually found.

Three constraints, taken directly from wom's `seq` package because they are what
make learning from little data safe:

1. **Only transitions are trained.** Emissions come from the `Scorer`, which
   already knows what a promising URL looks like. An unsupervised HMM's states
   carry no inherent meaning, and the emission model is the only thing that pins
   state 3 to "detail page" rather than letting it drift.
2. **Estimation is MAP, not maximum likelihood.** A seeded prior enters the
   M-step as pseudo-counts, so twenty pages leave the prior mostly intact while
   twenty thousand let the data speak.
3. **Fitting runs over crawl paths, never the whole visited set.** Fitted to
   everything, the likelihood is dominated by navigation and boilerplate, which
   is exactly the class the chain is supposed to discount.

The prior ships, the transitions do not: a hub-to-detail structure is a property
of how sites are built, while the fitted transitions for one site are worth
nothing on another. Same reasoning as wom shipping `DefaultChainPrior` and no
locators.

`scour item ls` gains a role breakdown, which is also the debugging view for the
chain:

```
roles       hub 412, detail 6903, pagination 1088, boilerplate 388, dead 80
```

## 6. Webdriver

chromedp behind an `http.RoundTripper`: `RoundTrip` drives a browser tab, waits
for the network to settle, and returns the rendered DOM as a synthetic
`*http.Response`. colly cannot tell the difference, so cookies, robots, depth,
retries, callbacks and metrics all keep working with no second code path.

Policy per host, defaulting to `auto`:

- `never`: plain HTTP only.
- `auto`: fetch over HTTP first; escalate to the browser when the response is
  HTML but yields no links and no text of substance, which is the signature of a
  client-rendered page. The escalation is recorded on the `Host` row, so the
  next crawl of that host starts in the browser.
- `always`: skip the HTTP attempt.

Browsers are pooled with a hard cap and a per-page timeout, and they run in the
crawl role only, so a deployment can put browser-capable crawlers on their own
machines and leave the rest of the pipeline light.

## 7. Data model (gorm)

```
Item      id, name, created_at
Alias       item_id, word
Property    item_id, name, type, example
Job         item_id, name unique, state, depth, max_pages, max_time,     -- M11
            last_run_at
Target        job_id, kind(domain|url), value, subdomains, depth        -- item_id today
ContentType   job_id, type                                              -- item_id today
Run           job_id, started_at, ended_at, reason, pages, records      -- M11
Host        host, rate, concurrency, robots, transport   -- learned or configured
URL         item_id, hash unique, url, parent_id, depth, score, role,
            status, content_type, size, latency, fetched_at, next_at
Response    url_id, status, headers, cache_key, fetched_at
Rule        item_id, parent_id, prop, xpath, selector, path, regex,
            uri_pattern, probability, support
Record      item_id, url_id, fingerprint unique, confidence, format, label
Value       record_id, prop, text
ModelMeta   item_id, path, algorithm, accuracy, trained_at, observations
Chain       item_id null, kind(extract|crawl), states, transitions json,
            observations, fitted_at
```

`Job` and `Run` are M11 and are not built. Targets and content types hang off
the item today, and the two lines marked above are what changes. Everything else
in this block is what ships.

`Job` is what the item carries alongside its definition today: the targets, the
policy, the frontier and the run state. Splitting it is what lets one item be
hunted over two different site sets, with one of them paused and the other not,
and it is why `Target` and `ContentType` hang off a job rather than an item. An
item knows what you are looking for; a job knows where to look.

Records still belong to the item, not the job, because two jobs hunting one item
fill one table, and a model belongs to the item for the same reason: it is
trained from every page every job of that item cached.

`Run` is written and never edited. A job accumulates runs, and `state` on the
job is the last one's outcome, distinguishing `budget` from `done` because both
end with the frontier intact and only one means there is more to fetch.

`URL.parent_id` and `URL.role` are what the crawl chain needs: the parent edge
reconstructs the path for Viterbi, and the role is the decoded state, which is
also what `scour item ls` counts. `Chain` holds both chains, with a null
`item_id` for the shared extraction prior that transfers between items.

`Rule` is a flattened `schema.Item` tree, which is what makes `scour rules`
a plain select. `Record.label` is the enum the README's CSV export column
mirrors. Migrations run through gorm automigrate for sqlite and through
versioned migrations for postgres.

## 8. Milestones

Each is independently useful and independently demoable.

M1 and M2 are done. Everything from M2.5 on is still ahead.

**M1. Skeleton.** *(done)* go.mod, CLI, config precedence, gorm store on sqlite,
`scour item add`, `scour item ls`, `scour remove`. No crawling. Proves the config and
storage layers.

**M2. Crawl, single process, no bus.** *(done)* colly `Collector` with the full
callback set, colly's own in-memory queue and storage, page cache,
`scour start --depth`, `scour item ls`. Scoring is `FixedScorer(1)`, named so an
untrained crawl cannot be mistaken for a trained one. Delivers the frontier
table from the README, and proved the callback wiring before anything is
swapped out: two bugs surfaced there that a mocked fetcher would have hidden,
both recorded in the tests.

**M2.5. colly backends.** *(done)* colly's in-memory `queue.Storage` and
`storage.Storage` replaced by gorm-backed implementations, so crawls resume
across restarts and several processes share one visited set. The queue already
pops in score order; with no model every score is equal, so the behaviour is
M2's until M4 fills the scorer in.

Two things worth remembering from it. colly's request IDs are FNV-64 hashes and
`database/sql` cannot bind a `uint64` with the high bit set, so they are stored
as `int64` holding the same bits. And a crawl stops by freezing the queue rather
than aborting requests: colly marks a request visited *before* the callback that
would abort it, so aborting burns the URL and makes the stop unresumable.

**M3. wom integration.** *(done)* Parse with wom, `scour train` via
`w.Model(schema...)`, `scour rules`, `scour stream`, and `model.Train` on
labelled items. Labelling itself now lives on the API rather than the CLI. The README's core loop now works end
to end.

Two things learned. wom's `SynthesizeURI` dropped the trailing slash from
directory-style URLs, so induced locators matched none of the pages they came
from and extraction returned nothing; fixed in `internal/pattern/regex.go` and
covered by a test. And records are reconciled by
fingerprint rather than replaced, because labelling is given ids a user
read off a search: renumbering them on every retrain would label the wrong rows.

**M4. Real scoring.** *(done)* Naive bayes over URL, anchor and depth tokens,
trained from crawl outcomes and labels, persisted per item at
`models/<name>.score.json`. The queue pops in score order, which is the moment
scour becomes a focused crawler rather than a crawler.

Measured on a site with 20 pages holding records among 65 that do not: a cold
crawl fetched all 85, of which 23% paid off; after one training round the same
crawl fetched 22 pages, 21 of them in the section holding the records.

The cold start is a seeded model rather than a separate code path: the item's
name, aliases and property examples enter as pseudo-counts worth three
observations each, so a first crawl already has a direction and real evidence
outweighs the hint as it accumulates.

**M4.5. Sequence models.** *(done)* wom's field-order chain is fitted during
induction and stored once with no item attached, since it transfers, then
seeded into every later induction. The crawl chain has page-role states, Viterbi
decoding over the parent path, MAP fitting from crawl outcomes, and a role
breakdown in `scour item ls` and `scour train`.

The gate passed: on a site whose records sit behind a hub sharing no words with
the item, the chain fetched 12 pages of which 10 held records; without it the
crawl fetched a single page and found none, because the scorer had learned that
the hub itself was worthless. `scour train --no-chain` keeps that comparison
runnable.

The design changed once during the work. Crediting a link by the role of the
page it was found on is too weak to rescue a hub, because every link on the seed
gets the same credit. What works is using the role of the *link's own URL* when
the last crawl decoded one: that is observation rather than inference, so it
takes precedence over the base score instead of being averaged with it.

**M5. Bus decomposition.** *(done)* Embedded NATS with JetStream, the store
component moved behind subjects, `scour join --role`, and `scour start --bus` to
route a crawl through it. Nothing needs installing: with no `bus.url` configured
the broker runs in-process.

The seam is a `crawl.Sink` interface, so the crawler cannot tell whether its
results are written directly or published for another process to write. That is
what makes the two topologies one implementation rather than two to keep in
step, and `TestTopologyEquivalence` asserts the same crawl leaves the same
database state either way.

Two things the async path forced into the open. A command that prints what
another component wrote has to wait for it, so the crawl summary is gated on a
drain barrier; without it the first bus crawl reported an empty frontier it was
about to be given. And `--json` was printing a prose header before the JSON,
which was invisible until something tried to parse it.

Still direct rather than on the bus: parse, extract, train and score. They run
inside `scour train` against the cache, which is already a clean boundary, and
moving them buys nothing until there is a scheduler to drive them.

**M6. Webdriver.** *(done)* chromedp `RoundTripper`, escalation policy, browser
pool. Because it installs with `Collector.WithTransport`, no callback or queue
code changed: `internal/crawl` gained a transport constructor and nothing else.

`internal/transport` is a registry keyed on `http.RoundTripper`, so a plugin
that satisfies the standard library satisfies scour. `internal/transport/
webdriver` is a blank import away, drives a tab per request through a counting
semaphore, and returns the rendered DOM as a synthetic 200.

`Escalating` is the policy in front of it. Under `auto` it fetches over HTTP
first and re-fetches in the browser only when the response parses as HTML,
ships scripts, and yet has no links and under 200 characters of text. All three
signals together, because each alone is too eager: a page with no links may be
a leaf article, a page with little text may be an index. A browser that will
not start keeps the plain response, because an empty page beats no page.

Escalation is sticky per host, written back to the shared `Host` row, and read
back at the start of the next crawl. Writing it without reading it was the bug
worth catching here: the decision survived in memory for one process and was
rediscovered, one wasted request at a time, by every crawl after it. Now
`Escalating.Prime` seeds the sticky set from the database and from any
`[[host]]` block naming the webdriver transport, and the second crawl of a
JavaScript site makes no plain HTTP request at all. Config patterns such as
`*.example.com` are deliberately not primed, since the sticky set matches a
request's host exactly and a pattern would silently never match.

Measured on a site whose only content is injected by script: `--browser never`
fetched the seed and stopped, one page, no links. `--browser auto` escalated
once, discovered three links that exist only in the rendered DOM, and fetched
all four pages. `scour train` then induced rules anchored on `#root`, a node
plain HTTP never sees, and `scour stream` returned the record. The rendered
page is indistinguishable downstream from any other, which was the point of
putting the browser at the transport layer rather than beside the crawler.

**M7. AI matchers.** *(done)* `internal/ai` is a provider registry over
one narrow question: given a prompt and a JSON Schema, return JSON. Two
implementations, `ollama` for the offline path and `anthropic` for the accurate
one, both constraining the answer with a schema rather than parsing prose.
`internal/matcher` sits on top as a registry of `wom.Matcher`s, with the
heuristic as the default and everything else measured against it.

Three things make a model affordable at induction scale, and all three are
necessary. The **cascade** consults a model only where the heuristic is
undecided. The **cache** keys on a hash of the question rather than the node,
so a site that repeats its template asks once and a retrain pays nothing; it is
a table in the store, so it survives the process. The **budget** caps calls per
run, and exhausting it degrades to the heuristic rather than failing.

The measured results are worth stating plainly, because two of them contradict
what the code originally claimed:

| Matcher | Accuracy | Precision | Recall | Consulted |
| --- | --- | --- | --- | --- |
| heuristic | 72% | 0.83 | 0.56 | n/a |
| `gemma3:270m`, numeric score prompt | 61% | 0.58 | 0.78 | 22% of candidates |
| `gemma3:270m`, verdict enum prompt | 72% | 0.75 | 0.67 | 22% of candidates |

First, a 270M model does not beat a good heuristic here. That is a real result
rather than a disappointing one: it is the argument for the heuristic being the
default and the model being opt-in, and it is why the benchmark exists at all.

But most of the gap turned out to be the question, not the model. The first
prompt asked for a score from 0 to 1, and a small model asked to rate agreement
simply agrees. Measured separately on page-topic classification, the same model
answered yes to all ten pages, recipe included, at a confidence of exactly 0.90
every time; asked instead to pick from eight categories it got all ten right.
Replacing the score with a four-way verdict, `exact` / `likely` / `unrelated` /
`not_a_value`, moved the matcher from 61% to 72% on the identical corpus with
the identical model. A question with only one plausible-sounding answer is not
a question, and the confidence such a model reports is a token it likes rather
than a probability.

Second, the cascade band was wrong. The heuristic's scores do not spread over
[0,1]; they cluster low, with a best decision cut near 0.31. A band of 0.15 to
0.65 therefore consulted the model on 61% of candidates, which is not a
cascade. Narrowing it to 0.22 to 0.45 brought that to 22% with no change in
accuracy. Both matchers are also now compared at the threshold that suits each
best, since judging the heuristic at 0.5 measured its calibration rather than
its judgement.

The embedding scorer landed as `internal/score/embed`, but not as ONNX. The
cgo-free constraint that chose `glebarez/sqlite` rules out the usual ONNX
runtimes, and an HTTP embedding service is worse than useless here: scoring
happens once per discovered link, so a network round trip per link would cost
more than the crawl it exists to direct. Static word vectors keep the property
that actually mattered, semantic similarity without a network call, at a map
lookup and an average. The file is the format word2vec and GloVe both write, so
an operator points scour at a file they already have.

It answers the counting model's blind spot: bayes can only reward words a crawl
has already proved, so a link reading "saloon" is worthless until something
proves saloons are cars, which on a site avoiding the word "car" never happens.
Comparing meaning rather than spelling closes that, and it is strongest exactly
where the counting model is weakest, on the first crawl.

Its limit is worth stating because it bounds where it should be used. Cosine
puts unrelated text near zero rather than at -1, so an unrelated link and an
unrecognised one both land near 0.5 and cannot be told apart: orthogonal is not
opposite. The useful range is the upper half. That is survivable because a
frontier needs an ordering rather than a calibrated probability, and it is why
this is an alternative to bayes rather than a replacement, since bayes learns
what is genuinely bad and this cannot.

Adding it exposed that the scorer registry was dead code. `train.Scorer`
constructed bayes directly, so `scorer = "..."` in config was recorded as
metadata and otherwise ignored, and `score.New` was called only by a test. The
same class of bug as the host transport that was written and never read. The
factory now takes a `score.Config`, `train.Scorer` goes through the registry,
and an unknown name is an error naming what is available. Whether a scorer was
fitted or is still working from seed words moved to an optional `score.Trained`
interface, so a crawl never claims a ranking it cannot back up.

**M7.5. Shipped defaults.** `internal/defaults` embeds starter schemas in the
binary with `go:embed`, reached by `scour item templates` and `scour item add --template`.
Only what transfers between sites ships: a schema says what a vehicle is, which
is true everywhere, while an XPath says where one site put it, which is true
nowhere else. The loader rejects a shipped model carrying located items, so
that distinction cannot erode by someone checking in a trained model.

Two gaps surfaced from using them. Per-property aliases and descriptions had
nowhere to live, so `schemaOf` built `wom.Prop{Name, Type, Examples}` and
discarded two of the three label signals wom scores against; properties now
carry both, and `PropertyAlias` is a table rather than a delimited column
because an alias is usually a phrase.

The second was worse. Applying a nine-field template to a site publishing three
of them located *nothing*, where a hand-typed three-field schema located
everything on the same pages. `inferRecord` scaled a record's probability by
the share of **declared** fields it recovered, so a thorough schema was
punished for its thoroughness and fell under `MinProbability`. Coverage is now
the share of *findable* fields, and the penalty is softened to its root: a
partially recovered record is still discounted, so more coverage still means
more confidence, but it is no longer annihilated. On the demo site the same
nine-field template went from 0 records to 3, one per vehicle.

**M8. Server.** *(done)* `internal/server` serves the same scour the command
line drives: one database, one set of models, one cache. An item created over
the API is the item the CLI sees, because both go through the store rather
than through each other.

Reads answer immediately; crawling and training return a job id. An HTTP request
that blocks for the minutes a crawl takes is one that times out somewhere in
the middle, leaving the caller unable to find out what happened. Jobs are also
where the concurrency guard lives: the same item cannot be crawled twice at
once, since two crawls would race on the frontier and double the load on
somebody else's server. A second request gets 409 with the id of the run that
is already going, which is the useful answer rather than an error. The work is
given a context that outlives the request, because a caller who starts a crawl
and hangs up has still started a crawl.

MCP comes from the official Go SDK rather than hand-rolled JSON-RPC, over both
stdio and HTTP at `/mcp`. The ten tools mirror the CLI verbs rather than the
database: an agent driving scour does the same job a person does, and exposing
tables would make it reimplement the workflow.

Auth is a bearer token from a file, compared in constant time, with `/healthz`
exempt so an orchestrator needs no credential to ask whether the process is up.
The token lives in a file rather than in `config.toml` because a config file is
usually readable and checked into configuration management. An empty token file
is refused outright: it almost always means a failed provisioning step, and
silently serving without auth would be the worst possible response.

Metrics are the Prometheus text format, hand-written. The exposition format is a
stable, documented handful of lines and this exports a handful of series, so a
client library would have been larger than the thing it replaced.

Three bugs worth recording. The race detector caught the job manager copying a
job for the response *after* releasing the lock, while the worker goroutine was
free to write the outcome into it; the snapshot is now taken under the lock. A
client closing stdin made `scour mcp` exit non-zero, which would make every
clean session look like a crash to whatever supervises it. Diagnosing that
turned up the third: the SDK signals a closed connection through its internal
jsonrpc2 package and formats the cause with `%v` rather than `%w`, so neither
the sentinel nor the `io.EOF` beneath it is reachable with `errors.Is`, and
matching the message is the only way to tell a hangup from a real failure.

Packaging is under `packaging/`: a hardened systemd unit using
`StateDirectory` and `CacheDirectory` so it needs no preinstall script, and the
`/etc/scour/config.toml` the unit points at. Both are covered by tests, because
a typo in the unit is an outage and a config that does not parse is the first
thing a new operator would hit.

**M9. Exporters and hardening.** *(done)* `internal/export` is a registry over
three formats. Records are grouped by the domain they came from, one file per
site, which is what makes an export diffable: a site that changed shows up as a
changed file rather than as a diff across everything ever crawled. CSV takes the
union of every record's fields rather than the first record's, because
extraction is per page and a column appearing only in row 500 would otherwise be
dropped silently. The webhook posts in batches, and reports what it delivered
before a failure so a retry does not double-deliver. Item names and domains
both reach the path from user input, so they are reduced to a single safe
segment; the test asserts the result cannot escape the export directory rather
than asserting a string, which is the property that actually matters.

The time budget was written twice. The first version wrapped the crawl context
in a deadline, which stopped the crawl and also cancelled the database writes
underneath it, so a spent budget arrived as a tail of failed writes and a fatal
error. That is the wrong shape: a budget is not a timeout. `--max-pages` already
had the right mechanism, freezing the queue so everything still queued stays
queued, and the time budget now uses it. A budget-stopped crawl exits zero,
keeps what it fetched, says it stopped on the budget rather than on an exhausted
frontier, and resumes on the next run. Verified: a two second budget fetched one
page, and the next run picked up the remaining three.

`scour item ls` with no name gives a line per item, which is what a service
crawling several at once needs. The most useful column is when each was last
trained, because an item that has never been trained is crawling blind.

**M10. Page classification.** *(done, off by default)* `internal/classify`
reads a fetched page and says what it is about, which breaks a circularity in
label bootstrapping: a page counted as relevant only if extraction already found
a record in it, and extraction only works once induction has learned where the
fields are, from the very pages being labelled. On a first crawl none of that
has happened. Reading the page settles the question independently.

It works, and the measurements are more interesting than the feature.

The first design asked for the categories a crawler naturally wants: detail,
listing, pagination, related, navigation, unrelated. On a mixed corpus a 270M
model answered `detail` for every page, including the recipe and the privacy
notice, and "rescued" four pages that should have been negatives. It made the
labels worse. Reversing the enum order changed nothing; simplifying the subject
description changed nothing. The question was wrong, not the model: deciding how
a page *relates* to a described subject is abstract reasoning, while saying what
a page *is about* is recognition, and a model this size can do the second and
not the first. Rewritten as subject matter, the same model on the same pages
went from 1 of 5 to 5 of 5.

Then the item's name turned out to carry the whole question:

| Item name | name alone | name plus aliases |
| --- | --- | --- |
| `vehicle`, a real word | **9/10**, no false positives | 7/10, three false positives |
| `api-cars`, coined | 2/10 | 7/10, three false positives |
| `proj7`, coined | 6/10, no false positives | 7/10, three false positives |

Two things follow. Offering the aliases as extra ways to say "yes" looks
obviously helpful and is not: it lifts a coined name from 2 to 7 and drops a
real one from 9 to 7, always by inventing the same three false positives. That
is the assent bias again in a different hat, and it is why the classifier asks
about the item's name alone. And a false positive is the expensive error here:
without a classifier an off-topic page is correctly negative because it yielded
no records, so a classifier that calls it relevant is strictly worse than none.

Hence the default is off, and the benchmark is the tool for deciding whether to
turn it on for a given model and a given item. Named after what it actually
collects, the classifier removes false negatives at no cost; named `proj7`, it
should stay off.

**M11. The command surface.** *(designed, in [the command surface]({{ '/cli/design.html' | relative_url }}))* The item carries a
definition, a target list, a budget, a frontier and a run state at once, which
leaves nowhere to put a second crawl of one item over a different site set, and
nowhere to say that one of them is paused and the other is not. `CLI.md` is the
design; this is what it costs to build.

Five nouns, one rule, `scour <noun> <verb> [target] [flags]`, with four
shortcuts for what is typed all day: `run`, `search`, `status`, `top`. The verbs
mean one thing everywhere, which is what the current surface breaks when `add`
creates an item and `--append` adds a word to one.

Three stages, in dependency order.

*Data model.* `Job` arrives, `Target` and `ContentType` move onto it, and the
item's paused flag becomes the job's state. Every item that has targets migrates
to an item plus one job named after it, so `scour start vehicle` and `scour run
vehicle` reach the same frontier and nothing is re-seeded. `Run` is a new table,
written and never edited. Measured on the database here: the migration touches
981,461 target rows, so it is SQL rather than rows read into Go, in the shape
the entity-to-item rename already used.

The blast radius is 69 call sites across 23 files, because `item.Targets` is
reached from the crawler, the dispatcher, the API, the TUI and training. That is
the whole cost of the change and it is worth knowing before starting: the model
change is a day, the call sites are the rest.

*Command tree.* Five groups under `internal/cli/{item,job,record,model,node}`,
the four shortcuts, and the flag renames: `--append`/`--delete`/`--update`
become `--add`/`--rm`/`--set`, and clearing becomes `--clear <detail>` rather
than a boolean per field. The 60-odd CLI tests drive `newRootCmd` and read what
comes out, so they are the safety net for this stage, as they were for the move
off cobra.

*New capability.* The query language for `scour search`, confidence bands,
`--strict` and the exit codes, run history and `job log --follow`. None of it is
reachable today under any spelling, which is why it goes last: everything above
is a renaming of things that work.

## 8.5 What crawling real sites exposed

Eight bugs found by running against live feeds and news sites, each one
measured. They are recorded together because they are not eight accidents: most
of them are the same missing idea wearing different clothes.

| Bug | Effect | Measured |
| --- | --- | --- |
| Absent fields voted on where the record is | Container chosen at the document root | 1 to 36 records per feed |
| A field's location fixed before the container was known | Feed logo beat the article, container rose to the channel | 9 to 260 records over ten feeds |
| Date shape missed RFC-822 and ISO with a time | Locator found the node, pattern rejected every value | 0 to 3,245 values |
| A whole inline script accepted as a field value | Publisher of 28,610 characters | 28,610 to 34 characters |
| One observation quoted as a literal pattern | Rule matched one page and no other | Rules generalise |
| A namespace declaration competing as a value | Guardian's author came back as a schema URI | Bylines extract |
| The sequence model diluted every score it touched | A correct byline discarded at 0.240 against 0.25 | 5-6 fields per record to 6-7 |
| Boilerplate outscores content on support | `data-rh="true"` ties with the headline and wins | Still open |

### The systemic part

**Structure is checked and plausibility is not.** wom decides where a value is
with real care and then accepts whatever is there. Three of the six bugs are
that gap: a date that is not a date, a value that is an entire program, a
pattern transcribed from a single sample. Each was invisible, because a field
that fails silently is simply absent from the record and nothing reports it.
The bounds added so far are specific and defensive; the general form is a
plausibility check between locating a value and accepting it, which does not
exist as a stage today.

**Repetition is treated as evidence without asking what repeats.** Support
raises confidence, which is right for a record that occurs many times and wrong
for chrome that occurs on every element. A framework attribute repeated across
a page outweighs the headline it sits beside. The same instinct, applied to
containers, is what made an earlier attempt at the container bug pick a JSON-LD
array over the right node. Support needs to be weighed against whether the
repeated thing is content, and nothing currently makes that distinction.

**A schema is a wish list, and was read as an assertion.** Naming a field the
document does not have used to move the record, weaken its probability, and
change what was extracted. Two of these are fixed; the general rule, that
absence of a declared field is absence of data rather than evidence against the
record, is worth applying wherever a schema is consulted.

**A decision made early is a decision made blind.** Each field was committed to
one location before the record's container existed, so a field that guessed
wrong took the container with it. Two real feeds show the two shapes of this.
The Guardian's channel carries an `<image>` for the site's own logo, whose
`<title>` and `<link>` outscored the ones in every `<item>`; the BBC publishes
`lastBuildDate` on the channel and nothing resembling it on an item at all. In
both the container climbed to the channel and forty-five articles became one.

The fix is to defer: a field keeps its rival locations until the container is
chosen, and a field with no reading inside the record is tested by its absence,
since a record that starts repeating once a field is set aside was never that
field's record. Ten live feeds went from 9 extracted records to 260. The wider
lesson is that container and field locations are one decision, not two, and any
stage that resolves half of it first will sometimes resolve it wrongly.

**Two stages looked at different documents.** The pass that finds where fields
live filtered out what could not be a value: inline scripts, whole programs,
namespace URIs. The pass that assigns fields inside the chosen container read
the graph directly and saw all of it again. A node ruled out as a candidate
could still be handed a field once the container was known, which is how a
schema URI became a byline. Any filter worth applying once is worth applying
wherever the same question is asked.

**A confidence and a distribution are not the same quantity.** The sequence
model averaged its posterior into the Matcher's score. A posterior spreads over
states, so with seven fields and a background state it sits near an eighth
wherever the chain is unsure, and forty per cent of every score became that
eighth. The threshold was calibrated against scores that had not been through
it. Nothing looked broken, because every field simply came back a third less
certain and the ones near the line quietly vanished: a byline located correctly
in forty-five items was dropped for want of a hundredth. Blending two numbers is
only meaningful when they measure the same thing.

### Still open

`headline` is not located on HTML pages while working on feeds. Confirmed not a
regression, not the alias list, and not `data-*` attribute values, all three
checked and ruled out. Fifty-eight candidates cleared the floor on one real
page and the true headline lost among them, which points at grouping and
support rather than at scoring.

HTML article pages were addressed by position, and a wider corpus fixed that
without a code change, which is what the diagnosis predicted. Nineteen news
sites and 808 pages replaced five pages of one site, the indices varied,
Generalize dropped them and the discriminator took over on its own:
`./meta[@property="og:description"]/@content` where it had been `./meta[28]`.
Summary is now filled on 93% of records. Nothing needed to be changed, and
changing it against the narrow sample would have been the wrong fix.

What the same corpus exposed instead is worse and more interesting.

**The record anchors on `<head>`.** The container is `/html/head` on every site,
so every field is fished out of page metadata and the article in `<body>` is
never a candidate. `<head>` wins because metadata is exactly the part of a page
that carries strong labels: og:, article:, itemprop, JSON-LD. The body has the
headline and the byline and almost no labels at all, so it loses on coverage to
the part of the document that is *about* the article rather than the article.

**A field can be filled, confident and constant.** Over 503 records:

| Field | Filled | Distinct values | What it actually located |
| --- | --- | --- | --- |
| summary | 93% | many | og:description, correct |
| title | 48% | 10 | the site's name, one per site |
| link | 15% | 4 | preconnect hints to CDNs |
| published | 45% | many | correct |
| modified | 45% | many | the same value as published |
| section | 0.2% | 1 | one site |
| author | 0% | none | never located |

`title` came back as `./link[@rel="alternate"]/@title`, the RSS autodiscovery
title, so every article on a site shares one headline. `link` came back as
`./link[@rel="preconnect"]/@href`, which is a performance hint naming a CDN.
Both are structurally perfect: right shape, right type, high confidence, and
wrong.

This is the sharpest statement yet of the theme already recorded above, that
structure is checked and plausibility is not, and the corpus makes it
measurable for the first time. Ten distinct titles across 503 records is not a
subtle signal. A field whose value is fixed for a host and varies only between
hosts is describing the site, not the record, and no amount of label agreement
should let it stand as a per-record field. Detecting that needs exactly what was
missing until now: several sites in one corpus. On a single site every field
looks constant and nothing can be concluded.

## 9. Engineering standards

Mirrors what `wom` already does, so the two repos feel like one codebase.

### Layout and naming

- Only `cmd/` and `internal/`. Package names are short, lower case, no
  underscores, and never `util`, `common`, `helpers`, `base` or `misc`. A
  package that cannot be named after what it does is a package that has not been
  designed.
- No package-level mutable state outside the registries, and those are written
  only from `init()` and read after.
- Interfaces are declared by the consumer, kept small, and returned as concrete
  types. `Scorer` and `Exporter` are consumer-side by construction, and the
  transport seam is `http.RoundTripper`, which the standard library already
  declares.

### Errors

- Wrap with `%w` and enough context to locate the call site:
  `fmt.Errorf("fetch %s: %w", u, err)`. No `errors.New` of a formatted string.
- Sentinels for conditions callers branch on (`ErrNotFound`, `ErrBudget`),
  typed errors when the caller needs fields. Compare with `errors.Is` and
  `errors.As`, never string matching. `errorlint` enforces this.
- Never panic outside `init()` and genuinely impossible states. A crawler meets
  hostile input by definition, so parsers recover and record, they do not crash
  the process.

### Context and lifecycle

- `ctx context.Context` first parameter on anything doing IO, honoured all the
  way down to the HTTP request and the database query.
- Every goroutine has a known owner and a way to stop. Services take a context
  and return from `Start` when it is cancelled. `errgroup` for fan-out.
- No `time.Sleep` in production paths; timers and context deadlines instead.

### Logging and observability

- `log/slog` with structured attributes, no `fmt.Println` and no third-party
  logger. Levels: `error` for lost work, `warn` for degraded, `info` for
  lifecycle, `debug` for per-URL detail.
- One request-scoped logger carrying `item`, `url` and `host`, passed on the
  context.
- Prometheus metrics from the start: fetch count by status class, fetch
  duration, queue depth, records extracted, model accuracy. These are the same
  numbers the CLI already prints, so they cost nothing extra to expose.

### Concurrency

- Channels for handoff, mutexes for state, and the race detector on in CI.
- Bounded worker pools everywhere. Unbounded goroutine spawning per URL is the
  standard way crawlers fall over.
- The bus gives at-least-once delivery, so every consumer is idempotent and
  every write is an upsert keyed on a stable hash.

### Dependencies

- Standard library first. A dependency needs a reason that survives the question
  "what happens when this is unmaintained in two years".
- Current set: colly, gorm, nats-server and nats.go, chromedp, urfave/cli, and the
  Anthropic SDK behind the LLM matcher. `internal/wom` is scour's own code, so
  what it needs is a direct dependency like any other: `ledongthuc/pdf`,
  `tdewolff/parse/v2`, `golang.org/x/net`. Everything else needs an argument.
- `go.mod` pinned to the current stable Go release, CI matrix covering it and
  the one before. Dependabot on, `go mod tidy` verified in CI.

### Tooling

Same files as wom, same targets, so muscle memory carries over:

- `.golangci.yml` with `errcheck`, `errorlint`, `gocritic`, `revive`,
  `bodyclose`, `noctx`, `nilerr`, `prealloc`, `unparam`, `misspell` and
  `usestdlibvars`, plus `goimports` with `local-prefixes` set to the module.
  `bodyclose` and `noctx` matter more here than in wom: this program is mostly
  HTTP.
- `Makefile` with `all fmt fmt-check vet lint test cover bench build install
  tidy clean snapshot docs version`.
- `.goreleaser.yaml` for the CLI binaries, `.editorconfig`, and CI on push, PR
  and a weekly cron to catch sites changing under the fixtures.
- CI runs `gofmt -l`, `go vet`, golangci-lint, and `go test -race -cover ./...`.

### Documentation and versioning

- Doc comment on every package and every exported identifier, starting with the
  identifier's name. `revive`'s `exported` rule enforces it.
- Semantic versioning, tags drive releases, `internal/version` stamped at build
  time via ldflags.
- A `CHANGELOG.md` kept by hand, because generated ones read like commit logs.

## 10. Testing

- **Golden corpus.** A `testdata/` set of saved responses across every format,
  replayed through parse, induce and extract. wom already works this way, so the
  same fixtures serve both projects.
- **httptest sites.** Small servers that emulate the awkward cases: JS-only
  rendering, pagination, infinite scroll, 429 with `Retry-After`, redirect
  loops, mislabelled `Content-Type`.
- **Topology equivalence.** The M5 requirement, run in CI: the same scenario
  through the in-process path and through NATS must produce identical database
  state.
- **No network in unit tests.** Live-site tests are a separate build tag, as in
  wom's `live_test.go`.

## 11. Open decisions

0. **Where a data assumption is allowed to live.** *(decided)* scour is a
   generic crawler, so the engine does not get to believe things about the
   content. What specializes it is trained: the schema, the examples, the
   aliases, the value and name patterns, the per-domain overrides, and the
   marks a person puts on records. Anything that cannot be expressed that way
   and still changes the answer is an assumption in the wrong place.

   Entries like `semanticTags` are allowed because their meaning is fixed by
   the HTML specification rather than by a corpus: `<time>` is a date on a Greek
   page as much as an English one.

   Deciding whether a page holds records is a classification, and it belongs at
   the URL level, on the crawl graph, not on the HTML. The classifier already
   exists: `internal/score/hmm` decodes six roles over the parent path of every
   URL, seed, hub, pagination, detail, boilerplate and dead, stores them in
   `page_roles`, and reports them in `scour item ls`. Detail is the role that
   means "this page holds the records".

   Nothing reads it, and reading it would not help, because the role is derived
   from extraction. In one training run `Trainer.extract` writes a match count
   per URL and `Trainer.trainChain` fits the chain from those counts;
   `observationsOf` tests `Matches > 0` before `Links > 0`, so a page a record
   came out of is observed as `Records` and decoded as `Detail`. Gating
   extraction on the role would gate it on its own output.

   Measured: 866 of news2's 867 records already come from pages labelled
   `detail`, and all 118 short titles are among them. The gate drops one record
   and fixes nothing.

   The fault is in two parts. `/news/community/` is decoded `detail` when it is
   a hub, which is the circularity. `/eedition/.../page_...`, `/ads/sale/...`
   and `/helpcenter/article_...` genuinely are detail pages and are not
   articles, which is a topic question, not a role one.

   So the chain needs evidence extraction did not produce. Outlink count does
   not work: section-like pages average 18.6 queued children against 8.7 for
   the rest, and the ranges overlap almost entirely.

   URL shape does, measured over news2's 867 records. Of the real articles, 735
   of 749 carry an id in the last path segment; of the section pages, 77 of 118
   are bare directories with a trailing slash. That is known when a URL is
   discovered, before it is fetched.

   It must not become a rule that articles carry ids. True of this publisher,
   false of others, and exactly the belief about content a generic crawler may
   not hold. It belongs as an observation the chain reads, so the association is
   fitted per item and a site with the opposite convention learns the opposite.

   The cost is a new observation vocabulary, which changes the emission matrix a
   fitted chain serialises. Stored chains are six roles by four symbols, so they
   need a version and a refit rather than being read under the new meaning.

   Inference does some of this work instead, in the wrong place. `variety`
   halves a location whose value never changes, which is what caught `section`
   resolving to a related-articles heading. It is a statement about content, and
   corpora break it: a shop selling one make publishes the same brand on every
   page, and brand is a real field of every product. It survives today only
   because a field the markup names outright outscores the discount, and there
   is a test holding that. The discount is classification reasoning deciding a
   field's fate inside the scorer, and it should move once the role gate exists.

1. **Offline replay through the transport.** `transport/replay` serving the page
   cache would make the whole pipeline testable without a network and would let
   `scour train` re-run induction without re-crawling. The question is whether
   it belongs in the transport registry or one layer up, at the parser.
2. **Page cache location.** *(settled)* The interface was kept narrow and the
   swap happened: `internal/cache` is a driver registry, local by default, with
   s3 and gcs behind the `cloud` build tag. A crawler and a trainer on different
   machines now share bodies through object storage rather than a disk, which
   was the point. What is still open is whether the same interface should carry
   NATS object store, which would avoid a second dependency for a fleet that
   already has a broker.
3. **How far wom's boundary should hold.** wom is scour's own code and is
   edited in place like any other package, but it deliberately knows nothing
   about crawling, scoring, queues or storage. That ignorance is what keeps
   induction testable against fixed documents. The open question is where the
   line moves as the matchers get richer: an LLM matcher wants a cache and a
   budget, and neither is a document concern.
4. **Per-item versus global frontier.** Two items crawling the same host
   should share politeness state but not queues. The `Host` table is shared, the
   `URL` table is per item, which resolves it, but the accounting needs care.
