---
title: scour
description: A focused web crawler that ranks links by how likely they are to hold what you want.
---

# scour

<p class="lede">A focused web crawler that ranks links by how likely they are to hold what you want.</p>

You tell scour what you care about: an item, the other words a page might use for
it, and the properties it should have. scour crawls outward from your seed
targets, giving every discovered URL a probability that it holds a match.
Instead of scraping whole sites and filtering afterwards, you get a ranked
frontier and spend your crawl budget on the pages most likely to pay off.

<figure>
<img src="{{ '/img/system.svg' | relative_url }}" alt="Three interfaces, a command line, an HTTP API and MCP, all sit on one broker. The components crawl, parse, train, score, classify and export publish and subscribe on it. Two durable stores sit underneath: the database and the page cache.">
<figcaption>Every grey box is an extension point: one interface, several implementations, chosen by name in a config file.</figcaption>
</figure>

## The rule the architecture is built on

<div class="rule" markdown="1">
**If a behaviour cannot be expressed by teaching, and it still changes the
answer, it is in the wrong place.**

Teaching means the schema, the examples, the aliases, the value and name
patterns, the per-domain overrides, and the marks a person puts on records.
The engine does not know what you are looking for, and is not allowed to.
</div>

That is why nothing in scour hardcodes what an article or a vehicle looks like,
and why every part that could hold such a belief is instead a registry with a
default in it. [Extending it]({{ '/architecture/extending.html' | relative_url }})
is the whole of that mechanism.

## The parts

<div class="cards" markdown="0">
<a href="{{ '/architecture/' | relative_url }}"><strong>architecture</strong><span>How the parts fit, and what each one owns</span></a>
<a href="{{ '/crawl/' | relative_url }}"><strong>crawl</strong><span>The fetch loop, scope, budgets, politeness</span></a>
<a href="{{ '/transport/' | relative_url }}"><strong>transport</strong><span>How a request reaches the network, browser included</span></a>
<a href="{{ '/schedule/' | relative_url }}"><strong>schedule</strong><span>What the frontier hands out next, and what is due again</span></a>
<a href="{{ '/cache/' | relative_url }}"><strong>cache</strong><span>Page bodies, on a disk or in a bucket</span></a>
<a href="{{ '/parse/' | relative_url }}"><strong>parse &amp; wom</strong><span>Every format as one addressable graph</span></a>
<a href="{{ '/matcher/' | relative_url }}"><strong>matcher</strong><span>How strongly one node satisfies one property</span></a>
<a href="{{ '/train/' | relative_url }}"><strong>train</strong><span>Inducing the rules, and applying them</span></a>
<a href="{{ '/classify/' | relative_url }}"><strong>classify</strong><span>What a page is, and what a URL is</span></a>
<a href="{{ '/score/' | relative_url }}"><strong>score</strong><span>How likely a URL is to lead to a match</span></a>
<a href="{{ '/store/' | relative_url }}"><strong>store</strong><span>The database, the frontier, the leases</span></a>
<a href="{{ '/export/' | relative_url }}"><strong>export</strong><span>Getting the records out</span></a>
<a href="{{ '/bus/' | relative_url }}"><strong>bus &amp; service</strong><span>One process, or many machines</span></a>
<a href="{{ '/server/' | relative_url }}"><strong>server &amp; MCP</strong><span>The same scour over a socket</span></a>
<a href="{{ '/cli/' | relative_url }}"><strong>command line</strong><span>The commands, and the loop they form</span></a>
<a href="{{ '/results/' | relative_url }}"><strong>measured results</strong><span>What it extracts, on live corpora</span></a>
<a href="{{ '/plan/' | relative_url }}"><strong>plan</strong><span>Why each part is where it is, and what is still designed</span></a>
</div>

Two surfaces are designed ahead of what ships, and both are on the site: the
[command surface]({{ '/cli/design.html' | relative_url }}) around five nouns and
one rule, and [the HTTP API]({{ '/server/api.html' | relative_url }}) putting
those same nouns over HTTP with full parity across the CLI, HTTP and MCP.

## The loop

<figure>
<img src="{{ '/img/pipeline.svg' | relative_url }}" alt="A URL is taken from the frontier, fetched through a transport, and cached. Training parses the cache into one graph and induces locators. Scoring feeds the frontier.">
</figure>

A crawl takes a URL from the frontier, fetches it through a transport, and
stores the body in the cache. Training reads the cache back, parses every body
into one graph, and induces the locators that find each property. Scoring
decides what the frontier hands out next, and is the only part of the loop that
learns from the last run.

## Getting started

```
scour item add vehicle --alias car -d example.com
scour item add vehicle -p make -e Ford -p model -e 'F-Series'
scour start vehicle --depth 3
scour train vehicle
scour stream vehicle
```

`scour item templates` lists the schemas that ship with it, so a common shape
does not have to be typed out. The [command line]({{ '/cli/' | relative_url }})
page has the rest.

## How it is judged

Extraction is measured on live corpora rather than on fixtures, and re-measured
after every change to inference. On 1,267 pages from 30 sites the algorithm had
never seen, every field is filled more often than on the 19 sites it was built
against, which is the opposite of what overfitting looks like.

[The numbers, and what the corpora exposed]({{ '/results/' | relative_url }}).
