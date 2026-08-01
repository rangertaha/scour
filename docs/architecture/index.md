---
title: Architecture
description: What the parts are, what each one owns, and why they are separate.
---

# Architecture

<p class="lede">scour is a generic crawler. The engine does not know what you are
looking for, and is not allowed to: what specializes it is trained, not compiled
in.</p>

<figure>
<img src="{{ '/img/system.svg' | relative_url }}" alt="Three interfaces, a command line, an HTTP API and MCP, all sit on one broker. The components crawl, parse, train, score, classify and export publish and subscribe on it. Two durable stores sit underneath: the database and the page cache.">
<figcaption>Three ways in, one engine behind them, two durable stores underneath.</figcaption>
</figure>

The one rule that governs the rest:

<div class="rule" markdown="1">
**If a behaviour cannot be expressed by teaching, and it still changes the
answer, it is in the wrong place.** Teaching means the schema, the examples, the
aliases, the value and name patterns, the per-domain overrides, and the marks a
person puts on records.
</div>

## The path a page takes

<figure>
<img src="{{ '/img/pipeline.svg' | relative_url }}" alt="A URL is taken from the frontier, fetched through a transport, and cached. Training parses the cache into one graph and induces locators. Scoring feeds the frontier.">
</figure>

A crawl takes a URL from the [frontier]({{ '/store/' | relative_url }}), fetches
it through a [transport]({{ '/transport/' | relative_url }}), and stores the body
in the [cache]({{ '/cache/' | relative_url }}). [Training]({{ '/train/' | relative_url }})
reads the cache back, [parses]({{ '/parse/' | relative_url }}) every body into one
graph, and induces the locators that find each property.
[Scoring]({{ '/score/' | relative_url }}) decides what the frontier hands out
next, and is the only part of the loop that learns from the last run.

Two properties of that picture are worth naming, because most of the design
follows from them.

**The cache sits between fetching and understanding.** Training does not crawl.
It reads bytes that a previous crawl already paid for, which is what makes it
safe to retrain after every correction, and what makes a change to inference
measurable against a fixed corpus rather than against a moving web.

**Only one arrow points backwards.** Everything from the frontier to the records
is a forward pipeline. The single loop is what scoring learns from the last run,
and keeping it single is what stops a change in extraction from silently
changing what gets fetched.

## The parts

Each of these owns one decision, and the pages behind them say what that
decision is and how to replace it.

| Package | What it owns |
| --- | --- |
| [`store`]({{ '/store/' | relative_url }}) | Persistence. Owns the gorm models, the frontier, and every query. |
| [`crawl`]({{ '/crawl/' | relative_url }}) | Drives colly: the fetch loop, budgets, politeness, escalation. |
| [`transport`]({{ '/transport/' | relative_url }}) | How a request reaches the network. |
| [`schedule`]({{ '/schedule/' | relative_url }}) | What the frontier hands out next, and what is due again. |
| [`cache`]({{ '/cache/' | relative_url }}) | Fetched page bodies, so a re-crawl and a retrain cost nothing twice. |
| [`content`]({{ '/crawl/' | relative_url }}#content-types) | Which content types a crawl may traverse. |
| [`parse`]({{ '/parse/' | relative_url }}) | Turns cached pages into one wom graph. |
| [`wom`]({{ '/parse/' | relative_url }}) | The Web Object Model: HTML, JSON, XML, PDF as one addressable graph, and induction over it. |
| [`matcher`]({{ '/matcher/' | relative_url }}) | How strongly one node satisfies one property. |
| [`score`]({{ '/score/' | relative_url }}) | How likely a URL is to lead to a match. |
| [`nodeclass`]({{ '/classify/' | relative_url }}#nodeclass) | What a node of the crawl graph is: its role, its topic, its recency. |
| [`classify`]({{ '/classify/' | relative_url }}) | What a fetched page is about, per page. |
| [`train`]({{ '/train/' | relative_url }}) | Induces an item's rules from its cached pages, and fits the chains. |
| [`export`]({{ '/export/' | relative_url }}) | Writes extracted records somewhere useful. |
| [`bus`]({{ '/bus/' | relative_url }}) | How components talk when they are not in one process. |
| [`service`]({{ '/bus/' | relative_url }}#roles) | Runs those components, in any combination. |
| [`server`]({{ '/server/' | relative_url }}) | The HTTP API and MCP. |
| [`cli`]({{ '/cli/' | relative_url }}) | The command line, grouped by what it does. |
| [`registry`]({{ '/architecture/extending.html' | relative_url }}) | The extension point the pluggable parts share. |
| [`ai`]({{ '/ai/' | relative_url }}) | Access to language models, for the parts that consult one. |
| [`config`]({{ '/config/' | relative_url }}) | config.toml, environment, flag precedence. |
| [`defaults`]({{ '/cli/' | relative_url }}#templates) | The schemas scour ships with. |
| [`tui`]({{ '/cli/' | relative_url }}#top) | What the live view shows, apart from how it is drawn. |

## One binary, many roles

<figure>
<img src="{{ '/img/bus.svg' | relative_url }}" alt="The same components run inside one process against an embedded broker, or across machines against an external one.">
</figure>

Components never call each other directly: they publish and subscribe. In the
default single-process mode the broker is an embedded NATS server running
in-process, so a laptop user gets a normal CLI tool with nothing to install.
Point the same code at an external cluster and the components spread across
machines without changing.

That is not a deployment convenience so much as an architectural constraint that
keeps itself honest. A component that reached into another directly would work
on a laptop and fail on a cluster, and the topology-equivalence tests exist to
catch exactly that. [The bus and the roles]({{ '/bus/' | relative_url }}).

## Two graphs, both ranked

scour has more than one graph, and the distinction runs through the whole design.

| Node | Question | Interface | Registry |
| --- | --- | --- | --- |
| a URL in the crawl graph | how likely is this to lead to a match? | `Score(Features) float64` | [`internal/score`]({{ '/score/' | relative_url }}) |
| a node of a parsed page | how strongly is this that field? | `Score(ctx, Prop, *Node) float64` | [`internal/matcher`]({{ '/matcher/' | relative_url }}) |

They are the same kind of extension over different graphs, and they looked
unrelated only because one of them was called a matcher. Each registry declares
what it ranks, as `score.Ranks` and `matcher.Ranks`, and `score.Kinds` is the one
place that says what can be scored and where the registry for it lives.

They stay two registries rather than one. A scorer of one kind takes an input
the other cannot produce, so folding them together would mean a cast at every
call, and a feed's items or a PDF's regions would want a third input again. The
kind is what makes room for that without pretending the inputs are the same.

## What is not extensible, and why

The six page roles, the inference constants, and the tuned thresholds in `wom`
are the engine's own. They are not configuration, because a knob is a way of
asking someone to tune what should have been measured. They are changed by
measuring against the corpora and re-measuring, which is what
[the results]({{ '/results/' | relative_url }}) record.

Entries like `semanticTags` are allowed to name HTML elements because their
meaning comes from the specification rather than from a corpus: `<time>` is a
date on a Greek page as much as an English one. A table of words that only holds
for news would not be allowed there, and belongs in a shipped schema under
`defaults`, where it is a starting point somebody can replace.

<div class="pager" markdown="1">
<span markdown="1">&larr; [Overview]({{ '/' | relative_url }})</span>
<span markdown="1">[plan]({{ '/plan/' | relative_url }}) &rarr;</span>
</div>
