# Plan

Build order for the rewrite. [NOTES.md](NOTES.md) is the design and argues why;
this is what gets built, in what order, and what proves each piece works.

Two things are built: the page cache with its backends, and the job document
with everything that reads it. Nothing crawls yet.

## The order, and why it is this order

Each phase is chosen so the next one has something real to stand on, and so
that the piece most likely to be wrong is exercised earliest.

**Phase 1. The chain.** A plugin registry per stage, and the runner that takes
a chain and walks it out and back. No middleware yet beyond a pair of test
doubles.

This is first because it is the mechanism every later phase plugs into, and
because two decisions in it cannot be changed afterwards without touching every
plugin ever written: whether a link may short-circuit, and whether it may drop.
Both are in the contract from the start or not at all.

*Proved by:* a chain of doubles that records the order it was walked, forwards
and back. A short-circuit at the middle link means the outer links still see a
response and the inner ones never ran.

**Phase 2. The downloader, and the four middleware that decide whether a corpus
is trustworthy.** `charset`, `cache`, `robots`, `redirect`.

`charset` first, not last. Bodies are cached transcoded, so a downloader that
writes raw bytes does not merely score badly on non-UTF-8 sites, it fills the
cache with evidence that is wrong, and every measurement taken afterwards is
taken against it. This is the single highest-risk item in the plan.

*Proved by:* fetching a windows-1251 page from a local server and reading UTF-8
back out of the cache. robots.txt honoured under a server that disallows.
Redirects landing at their target.

**Phase 3. The frontier and the scheduler.** Dedup, depth, politeness, the
ordering policies, leases.

*Blocked on a decision.* See below.

*Proved by:* a recorded frontier handed out in the order each policy claims,
and a lease that expires and is handed out again.

**Phase 3.5. `scour try`, the development loop.** One page, fetched once,
cached, and re-run against the cache from then on.

Worth its own phase because it is the first thing that is *useful* rather than
merely correct, and because it forces the cache and the downloader to work
together before anything depends on them at scale. It also needs no scheduler:
one URL, no frontier.

*Proved by:* a second run against a local server that the server never sees.

**Phase 4. The spider.** Parsing a response into items against the `item` and
`property` blocks, and discovering links.

This is where the job document stops being a document. Everything before it
could be tested against fixtures; this is the first phase whose quality is a
number rather than a pass or a fail.

*Proved by:* a corpus, and per-field fill rates that get written down. The old
implementation's numbers are on the `main` branch and are the bar.

**Phase 4.5. `scour train`, locators written back into the document.**
Induction over the cached pages, proposing an xpath or a css selector per
property, edited into the job with `hclwrite` so comments survive.

This is where the old implementation's induction returns, in a form that can be
argued with: a locator in the document is something a person reads, corrects and
commits, and a correction is never overwritten by a later guess. That last rule
is the one that makes the loop converge instead of going in circles.

*Proved by:* training over a corpus, hand-correcting one locator, retraining,
and finding the correction still there.

### How training should work

Written out because this is the part most likely to be got wrong, and because
the old implementation on `main` already made two of these mistakes and has the
measurements that show it.

**Do not induce from scratch. Start with what the site declares.** JSON-LD,
microdata, RDFa, OpenGraph, `<time datetime>`. A page carrying
`@type: NewsArticle` is telling you which node is the headline, the byline and
the date. That is not a guess, it is present on most sites anybody wants to
crawl, and it is stable because the site maintains it deliberately. On a
well-marked-up site this is the only pass needed.

Then aliases against `class`, `id` and `itemprop`, which are a real hint that
sites reuse carelessly. Then structural heuristics, which are broad, weak, and
where blind induction goes wrong.

**Score a candidate on two numbers, not one: coverage and distinctness.** This
is the lesson that cost the most. On `main`, `title` scored 243 pages filled
from 10 distinct values, because it had found the site's own name, which appears
on every page. Coverage alone calls that a success. It went to 644 from 627
distinct once distinctness was scored.

A field whose value is the same across a site is describing the site, not the
thing. That is the single most useful signal in induction and it costs one
count.

**Generalise the locator, or it pins the page it came from.** Also from `main`:
an induced CSS selector led with `#asset-59da10e1-...`, a per-page id, so it
matched exactly the page it was induced from while the XPath for the same field
stayed generic and worked across 660 records. Where a group's instances share no
leading segment, the selector has to generalise to the tail they do share. This
guard goes in from the first commit, or the bug comes back.

**An example outranks everything.** Given `article.title.text="Hello World"`,
look for nodes producing that value and induce from there. One node is an
answer, none is an error worth reporting, and several is a question to ask
rather than a coin to toss.

*Also proved by:* a corpus spanning several sites. A locator induced from one
site is pinned to that site's markup, and the failure is invisible until the
second site quietly extracts nothing.

**Phase 5. Pipelines and exporters.** The DAG runner over `Waves()`, and the
formats.

*Proved by:* independent steps demonstrably running at once, and export output
that round-trips.

**Phase 6. The bus.** Stages talking over NATS instead of calling each other,
in one process against an embedded server.

*Proved by:* the same job producing the same records whether the stages are
wired directly or through the broker. That equivalence is the whole claim, and
it is a test, not an aspiration.

**Phase 7. The cluster.** `--join`, jobs in JetStream, work distributed by queue
group, and bring-your-own-stage.

*Proved by:* two nodes, one job, work done on both. An external spider written
as a subscriber, in a language that is not Go.

## What blocks Phase 3

**The frontier needs a store, and the store has not been chosen.**

The workload is a priority queue with dedup and leases: pop-highest-score,
upsert-by-hash, lease with a timeout, survive restart. Nothing else in scour
needs a database. Bodies are in the cache, models will be files, config is the
job document, and records are append-only.

What is already ruled out: NATS alone. JetStream is a work queue and work queues
are FIFO, so it cannot rank, and a focused crawl is ranking.

The real question is whether more than one process writes one frontier.
Politeness argues no: two schedulers handing out the same host cannot honour a
crawl delay between them without a distributed lock, so the frontier wants to be
single-writer per host, and the scaling story is to shard by host rather than to
share a writer. If that holds, SQLite is correct and stays correct.

This needs an answer before Phase 3 and not before Phase 2.

## How it is written

Standard Go layout and standard Go idioms, so that somebody who has read any
other Go project can read this one.

- `cmd/scour` is the binary and holds no logic. `internal/` holds the packages.
  A package is exported out of `internal/` only when something outside needs it,
  which so far nothing does.
- **The command line is `urfave/cli/v3`**, one package per command group, each
  returning its own `*cli.Command`. The tree is built in `cmd/scour`; no command
  reaches into another.
- **Every extension point is the same registry**: a name, a factory, and a
  config. `internal/cache` already is one, and the stages, middleware, pipeline
  kinds and exporters each get theirs. One shape to learn, and a generic
  registry rather than six copies of the same forty lines.
- **Interfaces are declared by the consumer**, not the implementation, and kept
  narrow enough to be satisfied by a test double without a mocking library.
- **`context.Context` first, errors wrapped with `%w`,** `errors.Is` and
  `errors.As` at the boundaries, `log/slog` for logging, no global state, no
  `init()` beyond registration.
- **Tests are table-driven and behavioural.** A contract that several
  implementations satisfy gets one suite they all run, as
  `internal/cache/cachetest` already does. That is what makes a backend
  swappable in fact rather than in principle.
- `make all`: format, vet, lint, test, build. Nothing merges that does not pass
  it.

## Risks

**Extraction quality is a number, and the number is on another branch.** Phase 4
is not done when it runs, it is done when it matches what the old implementation
measured. Anything less is a rewrite that lost something and did not notice.

**The chain contract is a one-way door.** Short-circuit and drop have to be in
it from Phase 1.

**Two stacks, one repository.** The old implementation is on `main` and is the
only thing that can say whether this one is worse. It should be kept until
Phase 4 has its numbers, and deleted the day after.

**Scope creep through plugins.** Seventeen downloader middleware are
catalogued. Four are needed to crawl anything. The rest are a list to work
through when there is something to work through them with, not Phase 2.

## Where induction went

The old implementation learned locators from a corpus into a model file. The job
document now holds `xpath` and `css` directly, so induction has somewhere better
to put its answer: back into the document, as text, where it can be read and
corrected.

That reframes what was an open question into a design. `scour train` proposes,
the person disposes, and the model is a file in version control rather than an
artefact nobody can inspect. What is still open is whether the learned locators
are the product or a starting point, and that is answerable only once Phase 4
has numbers to compare against the ones on `main`.

## Status

| Phase | State |
| --- | --- |
| Page cache, three backends, one contract suite | Done |
| Job document: parse, validate, chains, DAG, diff | Done |
| 1. The chain | Not started |
| 2. Downloader and the four middleware | Not started |
| 3. Frontier and scheduler | Blocked on the store decision |
| 3.5. `scour try`, the development loop | Not started |
| 4. Spider | Not started |
| 4.5. `scour train`, locators into the document | Not started |
| 5. Pipelines and exporters | Not started |
| 6. The bus | Not started |
| 7. The cluster | Not started |
