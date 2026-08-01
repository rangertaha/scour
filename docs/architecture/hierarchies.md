---
title: The hierarchies
description: Three trees run through scour, and they are not the same tree.
---

# The hierarchies

<p class="lede">Three trees run through scour, and they are not the same tree.
One is what you teach and what gets stored. One is what a schema declares and
what comes back. One is the crawl graph, which is the only one whose shape scour
discovers rather than being told.</p>

## What an item owns

<figure>
<img src="{{ '/img/model.svg' | relative_url }}" alt="An item owns its aliases, its properties which own their own aliases, and its jobs, which own the targets and content types. A crawl fills urls, responses, queue items and page roles. Training fills rules, which nest, and records which own values.">
</figure>

An item is the thing you are hunting for, and everything hangs off it in three
columns: what you define, what the crawl fills in, and what training produces.
Deleting an item cascades through all three.

**Targets belong to a job rather than to the item.** An item says what you are
hunting and knows nothing about where it might be found, so two jobs can send
one item at two different sets of sites under two different policies. The item
used to carry a definition, a target list, a budget, a frontier and a run state
at once, which left nowhere to put a second crawl and nowhere to say that one of
them was paused while the other was not.

The frontier and the records stay keyed to the item even so, because two jobs
hunting one item are filling one queue and one table, and one model is trained
from every page all of its jobs cached.

Two tables sit outside that. `hosts` and `cookies` are keyed by site rather than
by item, because politeness and session state are owed to a server and not to
one hunt: two items crawling the same site should share the rate limit and not
the queue. `judgements` is a cache of what a model was asked, keyed by the
question, so the same question asked across pages, items and retrains is paid
for once.

The [store]({{ '/store/' | relative_url }}) page is where those tables live.

## What a schema becomes

<figure>
<img src="{{ '/img/schema.svg' | relative_url }}" alt="A schema is a tree of props. The outer prop names the record; its children are the fields. Induction turns each into a locator, and extraction returns the same shape filled with values.">
</figure>

The three shapes are one tree. A schema nests, so its locators nest, so its
records nest. The outer prop names the record and its children are the fields,
and a prop may itself hold props, so a record can contain records. Depth is not
limited by the model, only by what a page actually repeats.

The order matters and was got wrong once. The container is chosen first, and
only then are the fields fixed inside it. Choosing where a field lives before
knowing where the record lives let a feed's logo beat the article, and forty
five articles became one. [Training]({{ '/train/' | relative_url }}) is where
that order is enforced.

## What the crawl discovers

<figure>
<img src="{{ '/img/score.svg' | relative_url }}" alt="Two layers rank a URL: a base scorer on the tokens of the link, and a chain over the whole path back to the seed.">
</figure>

The crawl graph is nodes and the paths taken to reach them. Every URL has a
parent, back to a seed, and that path is what a sequence model reads to decide
what each page is: a seed, a hub that lists records, another page of the same
listing, a detail page that holds them, boilerplate, or a dead end.

That is the one hierarchy scour is not told. It comes out of crawling, which is
why `scour item ls` reports it as something learned:

```
roles       detail 793, dead 41, seed 17
```

Six roles over the parent path of every URL, stored per node. See
[score]({{ '/score/' | relative_url }}) for what reads them, and
[classify]({{ '/classify/' | relative_url }}#nodeclass) for what does not yet.

<div class="pager" markdown="1">
<span markdown="1">&larr; [Extending it]({{ '/architecture/extending.html' | relative_url }})</span>
<span markdown="1">[crawl]({{ '/crawl/' | relative_url }}) &rarr;</span>
</div>
