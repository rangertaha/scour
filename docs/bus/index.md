---
title: bus & service
description: How components talk, why the same code is one process or many machines, and what is published while a crawl runs.
---

# bus &amp; service

<p class="lede">Components never call each other directly: they publish and
subscribe. In the default single-process mode the broker is an embedded NATS
server running in-process, so a laptop user gets a normal CLI tool with nothing
to install.</p>

<figure>
<img src="{{ '/img/bus.svg' | relative_url }}" alt="The same components run inside one process against an embedded broker, or across machines against an external one. Three streams: crawl and records are work queues, metrics are kept for anyone watching.">
</figure>

## One binary, many roles
{: #roles }

```
scour run vehicle                                       # every role, one process
scour node join --role store --bus-url nats://broker:4222
scour node join --role crawl --bus-url nats://broker:4222      # as many as you like
```

| Role | Owns |
| --- | --- |
| `store` | The database and the frontier. Decides what is worth fetching next |
| `crawl` | The network, and nothing else |

A crawler holds no state about an item. What is in scope, what has been visited
and what is worth fetching next are all decided by the store, so crawlers are
interchangeable and losing one costs only the lease on whatever it was holding.

A single-process crawl writes its results directly, since publishing them to
itself would be a round trip for nothing. To exercise the bus path on one
machine, which is how the topology stays honest without a cluster to test
against:

```
scour run vehicle --bus
```

The components are identical in both topologies, which is a property the
topology-equivalence tests exist to protect rather than a claim. A component
that reached into another directly would work on a laptop and fail on a cluster,
and it would fail late.

## Subjects and streams

Subjects are `scour.<item>.<stage>`, so a subscriber can wildcard one item or all
of them. Characters NATS gives meaning to are stripped from the item name, so an
item named with a dot or a star cannot widen a subscription or split a subject.

| Stream | Carries | Retention |
| --- | --- | --- |
| `SCOUR_CRAWL` | `work`, `discovered`, `fetched` | Work queue |
| `SCOUR_RECORDS` | `record` | Work queue |
| `SCOUR_METRICS` | `metric` | Kept 15 minutes, oldest dropped when full |

The first two are work queues: a message is delivered to one consumer and
removed once acknowledged, which is what makes a restarted component pick up
where the last one stopped rather than replaying everything.

Metrics are deliberately not a work queue. One dashboard consuming a measurement
would take it from every other. They are kept for anyone who asks, the oldest
dropped when full, and forgotten quickly, so nothing watching the pipeline can
slow it down or fill a disk.

## At-least-once, and what that requires

Delivery is at-least-once. Every consumer must therefore be idempotent, and
every write keyed on a stable hash, which is why the
[frontier keys on a URL hash and records key on a fingerprint]({{ '/store/' | relative_url }}#keys-because-delivery-is-at-least-once).
Duplicate suppression on the stream keyed on the message id collapses both a
redelivery and a re-published URL into one write.

## Three things distribution made true

Each was a bug before it was a feature.

**Politeness is enforced where work is handed out.** A rate limit inside a
crawler bounds only what that crawler does; a site sees the sum of all of them.
Measured against a live site with one crawler, dispatching without pacing asked
for 5.6 pages a second where the configuration said one.

**A crawler dying costs a retry, not a page.** Work is leased rather than removed
from the frontier. Killing a crawler mid-crawl was tested: fetching continued on
the survivor and nothing was fetched twice.

**Bodies have to be somewhere shared.** Without a shared
[cache]({{ '/cache/' | relative_url }}) the trainer reads an empty directory with
a database full of keys.

Tested end to end against a real NATS server and a real S3 endpoint: two
crawlers and a store, 48 pages fetched, 48 objects in the bucket, nothing on
local disk, then training in a fourth process reading every one of them.

## Instrumentation

Everything the pipeline measures is published on `scour.<item>.metric`, so a
crawl can be watched while it happens rather than summarised when it ends.

| Metric | Unit | Labels |
| --- | --- | --- |
| `fetch.latency` | ms | host, status |
| `fetch.bytes` | bytes | host, status |
| `fetch.status` | count | host, status |
| `queue.depth` | count | |
| `queue.in_flight` | count | |
| `extract.records` | count | item |
| `extract.rules` | count | item |

Publishing is fire and forget. It does not deduplicate, returns no error and
retries nothing, because observability must not be able to break the thing it
observes.

The pairs are what answer a question. Latency and status per host say whether a
site is straining or has started blocking. Queue depth beside in-flight says
whether crawlers are keeping up, since a queue growing while in-flight sits at
its ceiling means discovery is outrunning fetching. Rules beside records say
whether a model still understands a site: a rule count holding while records
fall is a site changing under a model that has not noticed.

## Configuring it

```toml
[bus]
url       = ""    # NATS server; empty runs an embedded one in-process
store_dir = ""    # where the embedded broker keeps JetStream data;
                  # empty keeps streams in memory
```

<div class="pager" markdown="1">
<span markdown="1">&larr; [export]({{ '/export/' | relative_url }})</span>
<span markdown="1">[server &amp; MCP]({{ '/server/' | relative_url }}) &rarr;</span>
</div>
