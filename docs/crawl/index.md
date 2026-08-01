---
title: crawl
description: The fetch loop, what scope means, what a budget does, and who is responsible for politeness.
---

# crawl

<p class="lede">Package <code>crawl</code> drives colly. colly is the crawl
engine, not a fetching library called from a hand-rolled loop, and this package
supplies the parts colly leaves open.</p>

<figure>
<img src="{{ '/img/crawl.svg' | relative_url }}" alt="The life of one fetch. A URL is leased from the frontier, checked against scope on request, checked against content type when the headers arrive so the body can be abandoned, cached and recorded on response, and mined for links which are scored and returned to the frontier.">
<figcaption>The four callbacks are the whole of the integration with colly.</figcaption>
</figure>

## What is whose

| Owned by colly | Owned by scour |
| --- | --- |
| Scheduling, retries, redirects | Which URL is worth fetching next |
| robots.txt, cookies | The [frontier]({{ '/store/' | relative_url }}) it pops from |
| Depth tracking, link discovery | What counts as in scope |
| The request loop itself | What a response is worth keeping |

Not reimplementing the first column is a deliberate constraint rather than
laziness. Every one of those is a place where a subtle bug produces a crawler
that works on your test site and misbehaves on somebody else's, and none of them
is what makes scour interesting.

## Scope

A target is a domain or a URL. A domain makes the whole site a target; a URL
starts from one page. Domains are normalised, so `example.com`,
`www.example.com` and `https://example.com/` are one target, and `--subdomains`
widens it to `shop.example.com` as well.

```
scour item add vehicle -d 'example.com' --subdomains
scour item add vehicle -u 'http://www.example.com/cars/'
```

An item can have as many targets as you like, across as many sites as you like.
Scope is checked on `OnRequest`, before anything leaves the machine, so a link
out of scope costs nothing at all.

## Content types
{: #content-types }

scour follows HTML only by default. Widen or narrow that with `--type`, which
takes a MIME type, a wildcard, or one of the shorthands:

```
scour run vehicle --depth 10 --type html --type pdf
scour run vehicle --depth 10 --type 'text/*' --exclude-type 'text/css'
```

| Shorthand | Expands to |
| --- | --- |
| `html` | `text/html`, `application/xhtml+xml` |
| `pdf` | `application/pdf` |
| `json` | `application/json`, `application/ld+json` |
| `xml` | `application/xml`, `text/xml` |
| `feed` | `application/rss+xml`, `application/atom+xml`, `application/rdf+xml`, `application/feed+json`, `text/rss+xml`, `text/atom+xml` |
| `text` | `text/plain`, `text/markdown`, `text/csv` |
| `image` | `image/*` |

`feed` is separate from `xml` because almost nothing serves a feed as plain
xml. Sampled across a real list of news feeds, eight in twelve arrived as
`application/rss+xml` and only one as `application/xml`, so a crawl restricted
to `xml` would have skipped most of the feeds it was pointed at. Feeds are also
where extraction does best, since
[every field is named by its own element]({{ '/parse/' | relative_url }}#why-feeds-work-and-html-is-hard).

Filtering happens twice, so unwanted content costs as little as possible. Before
a request, scour skips links whose extension clearly disagrees with the allowed
types. After the response headers arrive, it checks the real `Content-Type` and
abandons the body if it does not match, without downloading it. That is the
right-hand branch in the diagram above, and it is why allowing a type you cannot
read costs bandwidth rather than nothing.

Types scour can extract text from are scored and mined for properties like any
other page. Types it cannot read, such as images, are recorded in the frontier
with their status and size but never parsed.

Three places can set this, and the narrowest wins: a `--type` on the crawl beats
the item's own setting, which beats `content_types` in `config.toml`.

## Budgets

```
scour run vehicle --max-pages 500
scour run vehicle --max-time 30m
```

Both budgets end a crawl the way an exhausted frontier does: everything fetched
is kept, everything still queued stays queued, and the next run resumes. A crawl
that stopped on a budget says so, because that means there is more to fetch
rather than nothing left, and a script that cannot tell those apart cannot
decide whether to run again.

`pause` freezes a crawl and keeps the frontier. `stop` throws the frontier away,
which is why it asks for `--force` when there is anything to lose: the
definition and the cached bodies survive either way, and what `stop` discards is
the work of deciding what to fetch next, which on a large site is hours of it.

To throw the frontier away and begin from the seeds in one step, without
stopping first:

```
scour run vehicle --reset
```

The cached bodies survive that too, so a reset crawl re-decides what to fetch
without re-downloading what it already has. `scour run --debug` additionally
logs colly's own request trace, which is what to reach for when a site behaves
differently from the way the frontier says it should.

## Politeness

`rate` and `concurrency` are per host, not global, so raising
`crawl.concurrency` widens how many hosts are in flight at once rather than how
hard any one of them is hit.

```toml
[crawl]
concurrency = 8       # in-flight requests across all hosts
rate        = "1s"    # delay between requests to one host

[[host]]
host        = "example.com"
rate        = "5s"
concurrency = 2
```

The first `[[host]]` block whose host matches wins, so the specific ones go
first.

Politeness is enforced where work is handed out rather than inside a crawler,
and that distinction is load-bearing once there is more than one crawler. A rate
limit inside a crawler bounds only what that crawler does; a site sees the sum
of all of them. Measured against a live site with one crawler, dispatching
without pacing asked for 5.6 pages a second where the configuration said one.
The dispatcher paces per host, using the host's own recorded rate when it has
one, so the site sees the configured rate however many crawlers there are.

This is also why a [scheduling policy]({{ '/schedule/' | relative_url }}) is not
allowed to decide politeness: a policy that could override it could hammer a
server by choosing badly.

## Watching one happen

```
scour run vehicle --depth 10

PROBABILITY  MATCHES  SPEED   LATENCY  RATE  200  300  400  500  URL
-----------  -------  ------  -------  ----  ---  ---  ---  ---  --------------------------------
       0.98       90  0.85/s    180ms    1s  98%   1%   1%   0%  http://www.example.com/cars/one/
       0.71       30  0.81/s    240ms    1s  95%   3%   2%   0%  http://www.example.com/cars/one/two/
       0.44       12  0.76/s    310ms    1s  91%   2%   6%   1%  http://www.example.com/cars/one/two/three/
       0.19        2  0.55/s    820ms    1s  74%   1%  22%   3%  http://www.example.com/cars/one/two/three/four/
```

The `200` to `500` columns are the share of responses in that subtree by status
class, so a URL sinking into `400` is mostly dead links and worth pruning. The
`RATE` column shows the value actually applied to each URL, which is where a
`[[host]]` override becomes visible.

<div class="pager" markdown="1">
<span markdown="1">&larr; [The hierarchies]({{ '/architecture/hierarchies.html' | relative_url }})</span>
<span markdown="1">[transport]({{ '/transport/' | relative_url }}) &rarr;</span>
</div>
