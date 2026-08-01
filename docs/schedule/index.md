---
title: schedule
description: What the frontier hands out next, what is due again, and why those are two registries.
---

# schedule

<p class="lede">A crawl has more URLs waiting than it will ever fetch, so the
order they come out in is most of what a crawl is.</p>

<figure>
<img src="{{ '/img/schedule.svg' | relative_url }}" alt="Two separate decisions over one frontier. A policy orders what is already waiting and hands out a lease. A refresh policy decides which fetched URLs are due again and returns them to the frontier. Politeness is neither of them.">
</figure>

## Two questions, not one

The first is which of the waiting URLs to hand out. The second is when a URL
goes back in: a front page carries something different every hour, an archived
article never changes again, and crawling them at one rate is either wasteful at
one end or stale at the other.

They are separate registries because they are separate decisions. `Policy`
orders what is already waiting; `Refresh` decides what is due. Splitting them is
what lets a site be crawled once and then kept current cheaply, which is a
different job from crawling it the first time.

## The policies

```toml
[crawl]
scheduler = "best"
```

| Name | Order | For |
| --- | --- | --- |
| `best` | Highest scoring first | A focused crawl. The default, and what makes it focused |
| `breadth` | Oldest first, a level at a time | An archival crawl, where a complete level beats a deep spur |
| `depth` | Newest first, down a spur | Following one thread as far as it goes |
| `random` | An unbiased sample | Sampling, where anything else is a biased sample |
| `warmup` | Breadth until a model exists, then best | The first crawl of a new item |

These are the textbook graph searches, and
[the algorithms page]({{ '/algorithms/' | relative_url }}#walking-it) sets them
side by side on one crawl graph with the visit order in each node.

`warmup` is the one that shows why the policy is asked once per lease rather
than once per crawl. Before there is a model every score is equal, so ordering
by score is ordering by noise. Being able to change its mind partway through a
crawl is what lets one policy cover both halves of a first run.

## What a policy may decide

The order, from a closed set. Not a SQL fragment.

The frontier is a table with 150,000 rows in it, so the choice has to be made by
the database, and taking SQL from a policy would be taking an injection point
and a dependency on the schema at once. A policy that returns
`ByScore` will keep working when the frontier grows a column; a policy that
returned `ORDER BY score DESC` would keep working right up until it did not.

A policy does not decide politeness. Which hosts are cooling is worked out from
when each was last fetched, and a policy that could override that could hammer
one server by choosing badly. See
[politeness]({{ '/crawl/' | relative_url }}#politeness).

## Refresh
{: #refresh }

`Refresh` answers which already-fetched URLs are due again.

It is the least built of the extension points, and worth being exact about how
little: the interface and its registry exist, `cron` is registered against it
and answers `ErrNoSchedule`, and that is all. **There is no config key that
selects one**, and nothing outside the registry calls `NewRefresh`. Naming a
refresh policy is not yet something a configuration can do.

Registering a name that is not written is deliberate even so. It makes the name
resolve to "this is planned" rather than to "unknown", which reads as a typo,
and it is the same courtesy the
[node classifiers]({{ '/classify/' | relative_url }}#nodeclass) get.

The distinction it will carry is the one the [crawl chain]({{ '/score/' | relative_url }})
already draws. A hub is worth revisiting because its contents change; a detail
page usually is not. Deciding that per role, per item, from the item's own
crawl, is what keeps it from becoming a rule that news sites change hourly.

## Leases

<figure>
<img src="{{ '/img/frontier.svg' | relative_url }}" alt="A frontier entry is queued, then leased to one crawler, then either fetched or failed. An expired lease returns it to the queue, and a failure retries it up to three attempts.">
</figure>

What a policy hands out is a lease, not a removal. A crawler dying mid-crawl
costs a retry rather than a page: the lease expires, the entry returns to the
queue, and another crawler takes it. That was tested by killing a crawler
mid-crawl, and fetching continued on the survivor with nothing fetched twice.

The [store]({{ '/store/' | relative_url }}) page has the rest of the frontier.

<div class="pager" markdown="1">
<span markdown="1">&larr; [transport]({{ '/transport/' | relative_url }})</span>
<span markdown="1">[cache]({{ '/cache/' | relative_url }}) &rarr;</span>
</div>
