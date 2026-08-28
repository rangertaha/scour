---
title: Where everything lives
description: Eleven stores, each with one owner and one reason to exist.
---

# Where everything lives

*Chapter ten of [the scour book](../index.md).*

Eleven kinds of thing get kept and they want different stores. The decisions
were argued out one at a time; the map is the thing worth being able to read
at once.

<figure>
<img src="../img/storage.svg" alt="The four stages above the stores they touch. The scheduler holds the frontier in SQLite and the pipeline holds records in SQLite, and they share no file. The downloader and the spider touch no database at all, only the shared cache. The entity graph and the event log sit behind one process, `scour server`, because two stages need them and a file cannot have two owners. Jobs, run state, secrets and nodes live in NATS key-value buckets, which is the one store every node already has.">
<figcaption>The two stages that touch a database touch different ones, and they share no file. The two in the middle touch none: a fetching node needs the network and the cache, and a parsing node needs the cache and the spec.</figcaption>
</figure>

| What | Where | Why |
| --- | --- | --- |
| Page bodies | The cache: a directory, S3 or GCS | Large, immutable, shared between machines |
| Jobs, as desired state | NATS KV, `SCOUR_JOBS` | The only store every node already has |
| Run state | NATS KV, `SCOUR_RUNS`, shallow history | A phase, kept apart so a resubmission's diff stays readable. Progress is published rather than stored, so a crawl of a thousand pages is not a thousand writes |
| Secrets | NATS KV, `SCOUR_SECRETS`, sealed | Per job, so the environment cannot carry them |
| Nodes | NATS KV, `SCOUR_NODES`, with a TTL | Not durable state. A row outliving its process is a lie |
| Frontier and hosts | SQLite, one shared database | Politeness is shared, so this cannot be partitioned. The hosts half also keeps what each site asked for |
| Records and marks | SQLite, one database per job | Unbounded, unshared, deleted by unlinking |
| Entities | SQLite, shared, behind a service | Its whole value is that two jobs agree who Acme is |
| Events | SQLite, shared, behind a service | The same, and the one most likely to want another backend |
| Trained topics | Files, one per version | Counts that never change once written, and an operator can look |
| Exports | Whatever the exporters write | Copies. Not the record of truth |

**A laptop needs nothing installed.** An embedded broker, a directory of
bodies, and a SQLite file per store a crawl actually asks for. A job with no
entities and no events opens neither.

**The two behind a service are the two that broke the ownership rule**, and
the service is what restores it: one process owns the file and everything else
asks. Both are an interface with a registry and a conformance suite that every
registered backend runs, so a backend that is not SQLite is a registration
rather than a rewrite. `internal/storage` is the seam their SQL is written
against, and it is four functions wide.

## Secrets

A job document holds `secret("acme-s3-key")`, never a value. The call is
unevaluated everywhere the document travels, and is resolved only on the node
building the plugin that needs it.

The bucket is sealed with a key that lives outside it, and keeps no history: a
secrets store that remembers every previous value is a store that leaks the
one you rotated away from. `scour secret set` reads from stdin rather than a
flag, because a flag is in the shell history and in the process table. There
is no `scour secret get`.

## What is built

| Piece | State |
| --- | --- |
| The job document: parse, validate, defaults, diff, mutation policy | Built, tested |
| The extraction spec: separable, renders to HCL, fingerprinted | Built, tested |
| Chains, the registry, and the plugin seam | Built, tested |
| The cache: local, S3 and GCS on one contract suite | Built, tested |
| The downloader: the fetch, the chain, robots.txt, redirects | Built, tested |
| The frontier in SQLite, and a lease that is flat while nothing is due | Built, tested |
| The scheduler: scope, budget, politeness, `dupefilter` | Built, tested |
| A site's own `Crawl-delay`, carried back from the downloader and paced against | Built, tested |
| The spider: four ways to find a value, link discovery | Built, tested |
| The pipeline: waves, concurrency, five step kinds | Built, tested |
| Exporters: json, jsonlines, csv, parquet, nats, sqlite | Built, tested |
| A whole crawl, and `scour scrape`, `crawl`, `job train` | Built, tested |
| The bus: same job, same records, either wiring | Built, tested |
| The cluster: two nodes, one job, work on both | Built, tested |
| Secrets, sealed, resolved where a plugin is built | Built, tested |
| Topic classification, in the spider and in the scheduler | Built, tested |
| `scour topic`: propose, correct, train, versioned | Built, tested |
| The entity store: typed entities, relations, provenance | Built, tested |
| Entity identity resolution: recorded merges, one rule, nothing fuzzy | Built, tested |
| The event log, and `scour server` serving both stores | Built, tested |
| Entity recognition and linking | Designed, not started |
| `plan`: printing the diff before anything happens | The diff is built and applied, and nothing prints it |
| A crawl driven over the bus | Built, tested. The job service holds the frontier and the nodes fetch and read |
| Applying a change to a running job | Built, tested. `job update` reads the diff through the job's `mutation` policy and refuses what it says to refuse |
| Fill rates measured against a real corpus | 54.7% over fifteen hand-written pages, held to a floor. The real number is not yet taken |

Two questions are still open, and both are worth stating rather than quietly
deciding later. **Fairness across jobs:** one shared scheduler holding one
frontier has to decide what a job's share of a host is, and nothing about the
current design answers that yet. **Quiescence across machines:** in one
process a run knows it is finished from a stall bound it can compute, but work
leased on one node while an item is mid-graph on another is the genuinely new
distributed-systems problem here.

> **On this book**
>
> These chapters are checked by tests rather than trusted. Every job document
> printed in them is parsed and validated, every bare word in one is checked
> against the vocabulary the parser resolves against, and every plugin and
> exporter block is decoded against the schema the implementation itself reads
> it with, those bodies being opaque to the engine and therefore the one part
> of an example nothing else looks inside. Every number in the position tables
> is compared with the catalogue the code uses, in both directions, so a plugin
> the code ships and no chapter lists is a failure too. The command chapter's
> table of what exists is held to the commands the binary has, and its flags to
> the flags they take, again both ways. The lease the frontier chapter prints is
> compared with the query the frontier runs, in both directions, after it had
> quietly lost a column.
>
> Then the checks about being a book: a link that goes nowhere, a chapter the
> contents leave out, a Back or Next that points past its neighbour, a diagram
> with nothing in it, and a diagram nobody described for a reader who cannot see
> it. A chapter that drifts from the code fails the build rather than misleading
> somebody quietly.
>
> There were two documents at the repository root, working notes and a command
> reference, and both are gone: what they carried is in these chapters, and the
> checks that held them were repointed here rather than deleted with them.

---

[Back: Local until it has to be shared](../cli/index.md)
