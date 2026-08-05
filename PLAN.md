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

**Phase 2. The downloader, and the middleware that decide whether a corpus is
trustworthy.** `cache`, `robots`, `redirect`.

Encoding is the highest-risk item, and it is not one of them. The cache holds
what the server sent, so what makes the corpus trustworthy is that a hit still
carries the headers the body arrived with. A page in windows-1251 that declared
its encoding in the `Content-Type` header and nowhere else decodes correctly on
the way in and into mojibake on the way back out if they are lost, and nothing
about the resulting text says it went wrong. That is why a cache entry is two
keys rather than one.

*Proved by:* fetching a windows-1251 page from a local server, checking that
what is on disk is the bytes it sent, and decoding the same text out of a hit as
out of the fetch. robots.txt honoured under a server that disallows. Redirects
landing at their target.

*Where it is:* the downloader, `cache` and robots are built and tested.
`redirect` is not.

robots.txt turned out not to be middleware. There is one correct position for
it, outside everything, so it is a `downloader` attribute that wraps the chain
rather than a plugin with a number somebody could change. RFC 9309 is
implemented here rather than imported: being wrong is too permissive on somebody
else's site, under our name, and nothing in our own output looks wrong when it
happens.

**Phase 3. The frontier and the scheduler.** Dedup, depth, politeness, the
ordering policies, leases. SQLite, one database, hand-written SQL: see
[NOTES.md](NOTES.md#where-the-frontier-lives) for why.

Two tables carry it. `urls` is the frontier, keyed by a hash of the normalised
URL so re-discovering one is an upsert rather than a duplicate. `hosts` is
politeness, and it is shared across jobs because a rate limit is per host: two
jobs on one site get one allowance between them, not one each.

The lease is the query worth getting right, because everything else follows its
indexes:

    BEGIN
    SELECT u.* FROM urls u JOIN hosts h ON h.host = u.host
     WHERE u.job = ? AND u.status = 'queued'
       AND (u.leased_until IS NULL OR u.leased_until < now)
       AND h.next_at <= now
     ORDER BY u.score DESC
     LIMIT 1 FOR UPDATE
    UPDATE urls SET leased_until = ?, attempts = attempts + 1 WHERE id = ?
    COMMIT

`FOR UPDATE` is a no-op on SQLite, which serialises writers anyway, and is what
Postgres needs the day sharding is not enough. Writing it in from the start
makes that a dialect change rather than a rewrite.

*Proved by:* `internal/frontier/frontiertest`, which every implementation runs.
The order each policy claims; a lease that expires and is handed out again; a
host skipped until it is due; a slow host not stalling a fast one; and a failure
retried three times and then abandoned.

**And measured by the same package.** A crawl leases once per page, so lease
cost is the ceiling on how fast anything downstream can go, and two
implementations are compared on identical workloads rather than on whatever
each author found convenient.

The in-memory reference is deliberately naive: it scans, so a lease is O(n) and
costs 45µs over a thousand URLs and 4.3ms over a hundred thousand. That is the
bar rather than the target. A durable frontier ordering by an index should be
roughly flat across both, and if it is not, the index is wrong — which is a
thing to find out in a benchmark rather than at two hundred thousand pages.

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

**Phase 4.75. Topic classification, for the jobs that want it.** The `topic`
plugin in both chains, a named classifier trained over the corpus, and the
storage and versioning that go with it.

After the spider because classifying a page needs its text, and text needs
extraction that strips the navigation: a classifier trained on raw HTML learns
the menu. Optional throughout, so a node running no topiced jobs loads nothing.

*Proved by:* a crawl that reaches on-topic pages sooner than an unfocused one
over the same budget, measured rather than asserted; and a job with no topic
plugin never opening the classifier at all.

**Phase 5. Pipelines and exporters.** The DAG runner over `Waves()`, and the
formats.

*Proved by:* independent steps demonstrably running at once, and export output
that round-trips.

**Phase 5.5. Secrets.** `SCOUR_SECRETS` sealed with a cluster key, `secret()`
resolved when a plugin is built, and the S3 and GCS backends taught to accept
explicit credentials.

Those two currently use gocloud's URL openers, which take a region, an endpoint
and a profile but deliberately not raw keys. Accepting a secret means
constructing the clients directly, `s3blob.OpenBucketV2` with a built client and
`gcsblob.OpenBucket` with an HTTP client, roughly forty lines each. The ambient
credential chain stays as the fallback, so a laptop is unaffected.

*Proved by:* a job whose cache credentials come from a secret, running on a node
with no cloud credentials in its environment; and the value appearing in neither
the stored job, a plan, nor `scour show`.

**Phase 5.75. The entity store.** Typed entities and typed relations, both
carrying properties, every fact an assertion with provenance, behind a service
because two stages touch it.

Staged, because built as one thing this is a year that never ships. The
property-to-entity reference and a store of typed entities is contained and
useful alone: it answers which authors a publisher has published for nothing.
Identity resolution is the middle piece. Recognition and linking is the large
one, and the feedback into extraction needs all three.

*Proved by:* a byline belonging to nobody in the store extracting as easily as a
familiar one, because known entities must raise confidence and never gate
extraction; and one job's assertions being removable with a single delete.

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
job document, and records are in a database of their own per job.

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
| 3. Frontier and scheduler | Contract, policies and benchmarks built. Durable store not started |
| 3.5. `scour try`, the development loop | Not started |
| 4. Spider | Not started |
| 4.5. `scour train`, locators into the document | Not started |
| 4.75. Topic classification, optional | Designed, not started |
| 5. Pipelines and exporters | Not started |
| 5.5. Secrets | Designed, not started |
| 5.75. Entity store, assertions with provenance | Designed. Reference built |
| 6. The bus | Not started |
| 7. The cluster | Not started |
