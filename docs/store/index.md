---
title: store
description: The one place that talks to the database, the frontier it hands out, and the keys that make retries safe.
---

# store

<p class="lede">Package <code>store</code> is scour's persistence layer. It owns
the gorm models, the migrations, and every query; nothing else in the program
talks to the database.</p>

<figure>
<img src="{{ '/img/model.svg' | relative_url }}" alt="An item owns its definition: aliases, content types, targets, and properties which own their own aliases. A crawl fills urls, responses, queue items and page roles. Training fills rules, which nest, and records which own values.">
<figcaption>What you teach, what the crawl fills in, and what training produces. Deleting an item cascades through all three.</figcaption>
</figure>

## Why one package owns it

Every component in scour is replaceable except this one. A crawler holds no
state about an item: what is in scope, what has been visited and what is worth
fetching next are all decided here, which is what makes crawlers interchangeable
and losing one cost only the lease on whatever it was holding.

That property only survives if there is exactly one place the schema is known.
A second package writing its own query is a second place to update when the
frontier grows a column, and a second opinion about what a lease means.

## The tables that sit outside an item

Two do not hang off an item, and the reason in each case is a modelling one.

`hosts` and `cookies` are keyed by site, because politeness and session state
are owed to a server and not to one hunt. Two items crawling the same site
should share the rate limit and not the queue.

`judgements` is keyed by the question a model was asked, so the same question
across pages, items and retrains is paid for once. Keying it by item would have
made a second item on the same sites pay again for answers it already had.

## The frontier

<figure>
<img src="{{ '/img/frontier.svg' | relative_url }}" alt="A frontier entry is queued, then leased to one crawler, then either fetched or failed. An expired lease returns it to the queue, and a failure retries it up to three attempts.">
</figure>

The frontier is a table, ordered by whatever the current
[policy]({{ '/schedule/' | relative_url }}) asked for, and handed out as leases.

It stays in the store rather than becoming a stream, and that is worth saying
plainly because it looks like an obvious candidate for the message bus. The
order is the product. A frontier pops highest score first and a broker delivers
in publish order, so the component that can sort the queue hands out the next
few and keeps the rest.

| Constant | Value | Means |
| --- | --- | --- |
| `DefaultLease` | 10 minutes | How long one crawler holds a URL before it returns to the queue |
| `MaxAttempts` | 3 | How many times a failing URL is retried before it is done |
| `TargetBatch` | 500 | How many targets are written per batch on import |

## Keys, because delivery is at-least-once

The [bus]({{ '/bus/' | relative_url }}) delivers at least once, so every consumer
must be idempotent and every write keyed on something stable rather than on an
autoincrement:

```go
func URLHash(itemID uint, rawURL string) string
func Fingerprint(itemID uint, values map[string]string) string
```

The frontier keys on a URL hash, so a URL discovered twice collapses to one row.
Records key on a fingerprint of their values, so the same record extracted twice
is one record. Both are scoped by item, because two items crawling one site are
filling different tables and must not collide.

This is also what makes a record id stable across retraining: an id read off one
listing still names the same record on the next, and a mark put on it survives.

## Where it lives

| Path | Contents |
| --- | --- |
| `~/.config/scour/scour.db` | Items, properties, targets, the frontier, rules, records and marks |
| `~/.config/scour/models/<name>.json` | One scoring model per item |
| `/var/lib/scour/scour.db` | The same, on a packaged install |

Only the [cache]({{ '/cache/' | relative_url }}) is safe to delete. Everything
here is state you would have to re-crawl to rebuild, and the marks could not be
rebuilt at all.

## What survives what

| | Definition | Cached pages | Frontier | Records | Model |
| --- | --- | --- | --- | --- | --- |
| `pause` | kept | kept | kept | kept | kept |
| `stop` | kept | kept | dropped | kept | kept |
| `start --reset` | kept | kept | dropped | kept | kept |
| `item rm` | dropped | dropped | dropped | dropped | dropped |

`stop` asks for `--force` when there is a frontier to lose, because on a large
site that frontier is hours of deciding what to fetch next, and it is the one
thing here that cannot be recomputed cheaply.

<div class="pager" markdown="1">
<span markdown="1">&larr; [The markup]({{ '/algorithms/markup.html' | relative_url }})</span>
<span markdown="1">[export]({{ '/export/' | relative_url }}) &rarr;</span>
</div>
