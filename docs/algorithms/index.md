---
title: The algorithms
description: Every algorithm scour runs, what each one consumes and produces, and the rules about evidence they all obey.
---

# The algorithms

<p class="lede">scour runs eight algorithms over two graphs. This page says what
each one is and where it lives; the component pages say how it fits into the
engine around it.</p>

<figure>
<img src="{{ '/img/algorithms.svg' | relative_url }}" alt="Two graphs, two families of algorithm. Over the crawl graph, naive Bayes on the URL tokens and a Markov chain over the parent path combine in odds form to order the frontier. Over the document graph, grouping chooses a container, a matcher scores each candidate, a chain orders the fields, and pattern synthesis emits a locator. Both chains share three constraints.">
</figure>

| Algorithm | Consumes | Produces | Lives in |
| --- | --- | --- | --- |
| Naive Bayes over URL tokens | a discovered link | how likely it is to be a record page | `score/bayes` |
| Hidden Markov chain over page roles | the path back to the seed | how likely it is to *lead* to one | `score/hmm` |
| Odds combination | both of the above | one probability per URL | `score/hmm` |
| Graph traversal | the frontier and those probabilities | the order URLs are actually visited | `schedule` |
| Grouping and container choice | the parsed graph | which node repeats, and what a record is | `wom/infer` |
| Semantic node scoring | a property and a candidate node | how strongly one is the other | `wom/match` |
| Hidden Markov chain over field order | records seen so far | which field follows which | `wom/seq` |
| Pattern synthesis | the located nodes | XPath, CSS, native path, value regex | `wom/pattern` |

The split down the middle is the one the whole engine is organised around:
**one family ranks URLs of the crawl graph, the other ranks nodes of a parsed
page.** They are separate registries because a scorer of one kind takes an input
the other cannot produce. See
[the two graphs]({{ '/architecture/' | relative_url }}#two-graphs-both-ranked).

## Over the crawl graph

The problem is the oldest one in focused crawling. A per-URL scorer judges a
link on its own tokens, and **the hub page leading to a hundred records usually
contains none itself**: it scores near zero, never gets followed, and the
hundred records behind it are never seen.

So there are two answers and they are combined rather than ranked against each
other. Naive Bayes over the tokens answers whether a URL *looks* like a record
page. A six-state chain over the parent path answers whether it *leads* to one.
Neither is sufficient alone, so both enter as independent evidence in odds form.

Scoring runs once per discovered link, millions of times over a large crawl, so
both halves have to be cheap. Anything expensive belongs in the matcher, which
runs during training rather than during a crawl.

[In depth: score]({{ '/score/' | relative_url }}).

### Walking it

A probability per URL is not yet an order to visit them in. The traversal is a
separate algorithm, and it is the classic set: a crawl is a search over a graph
whose edges are discovered as you go, so the strategies are the textbook ones.

<figure>
<img src="{{ '/img/traversal.svg' | relative_url }}" alt="One crawl graph walked four ways. Best first follows the highest predicted probability, breadth first takes a level at a time, depth first goes down one spur, and random is an unbiased sample. The numbers in each node are the order it is visited.">
</figure>

| Strategy | Classic name | Ordered by |
| --- | --- | --- |
| `best` | best-first (greedy) search | the predicted probability, highest first |
| `breadth` | breadth-first search | oldest first, so a level completes before the next begins |
| `depth` | depth-first search | newest first, down one spur |
| `random` | uniform sampling | nothing, deliberately |
| `warmup` | breadth-first, then best-first | the level, until a model exists; the score after |

**Best-first is what makes a focused crawler focused**, and it is also the one
that needs the other seven algorithms to be any good: greedy search with a bad
heuristic is a worse crawl than breadth-first, because it commits the budget to
the wrong subtree and never comes back. That is the whole reason the heuristic
is measured rather than assumed.

`warmup` exists because of the same fact seen from the other end. Before there
is a model every score is equal, so ordering by score is ordering by noise, and
a greedy search over noise is an arbitrary walk that *looks* principled. Walking
breadth-first until there is something to be greedy about is the honest version.
It is expressible because the policy is asked once per lease rather than once
per crawl, so a single run can change strategy partway through.

Two things the traversal is deliberately not allowed to decide:

**The query.** A policy returns an order from a closed set, never a SQL
fragment. The frontier is a table with 150,000 rows, so the ordering has to be
done by the database, and taking SQL from a policy would take an injection point
and a dependency on the schema at once.

**Politeness.** Which hosts are cooling is worked out from when each was last
fetched. A traversal that could override that could hammer one server by
choosing badly, which is a failure the visiting order should not be able to
cause.

[In depth: schedule]({{ '/schedule/' | relative_url }}).

## Over the document graph

Four steps, and the order of the first two is the part that was got wrong once.

**Grouping and containers.** `wom/infer` owns the structural half: it groups
equivalent nodes and finds the record container, and delegates semantics to a
matcher and field ordering to a sequence model. The container is chosen
*first*, and only then are the fields fixed inside it. Choosing where a field
lives before knowing where the record lives let a feed's logo beat the article,
and forty five articles became one.

**Semantic scoring.** `wom/match` is the only place semantic judgement lives,
which is what makes replacing the matcher replace the intelligence of the whole
engine without touching graph construction or locator synthesis.

**Field order.** A second chain, over which field follows which inside one
record. It is fitted from records a person has marked valid, which is why
marking one right is what tells training to fit the chain at all.

**Pattern synthesis.** The located nodes become a `Locator`: paths generalized
across instances, a value regex synthesized from the observed values, a URI
pattern from the URIs they came from. This is what lets a model outlive the
graph it was induced from, and it is the line between expensive induction and
cheap per-page extraction.

[In depth: parse &amp; wom]({{ '/parse/' | relative_url }}),
[matcher]({{ '/matcher/' | relative_url }}),
[train]({{ '/train/' | relative_url }}).

## The rules both families obey

### Three constraints on every chain

Both hidden Markov chains, the one over page roles and the one over field
order, are fitted the same way, because both learn from very little data.

1. **Only transitions are trained.** Emissions come from what a fetched page
   turned out to hold. An unsupervised chain's states carry no inherent meaning
   and would drift off the roles they are supposed to name.
2. **Estimation is MAP, not maximum likelihood.** The prior enters as
   pseudo-counts, so twenty pages leave it mostly intact while twenty thousand
   let the data speak.
3. **Fitting runs over paths, never the whole visited set.** Fitted to
   everything, the likelihood is dominated by the boilerplate the chain exists
   to discount.

### Three rules about evidence

Each of these was once missing, and each absence cost something measurable.

**Support counts independent observations, not matches.** Counting matches
rewards a locator for being ambiguous: one that fires twenty times on one page
outscores one that fires once on twenty pages.

**Reach counts.** How many sites a locator works on has to enter the score, or a
body `div` that is perfect on one site beats a meta tag that is good on
thirteen.

**A value that never changes is not a field.** A field describes its record and
so changes from one to the next. `section` once resolved to a real section-line
class carrying one heading repeated across 211 records: the label was correct
and the field was wrong, and only distinctness across the corpus could see it.

That last rule is why [one graph]({{ '/parse/' | relative_url }}) holds every
page at once. No amount of reading a single document finds it.

## Measured, not tuned

The six roles, the inference constants and the thresholds in `wom` are the
engine's own and are deliberately not configuration. A knob is a way of asking
someone to tune what should have been measured. They change by measuring against
the corpora and re-measuring, and
[the results]({{ '/results/' | relative_url }}) are that record.

The sensitivity is real rather than theoretical: moving one weight from 0.95 to
1.0 flipped the `title` field between 631 and 340 filled records. A number that
powerful does not belong in a config file.

## What is still open

[The markup]({{ '/algorithms/markup.html' | relative_url }}) is the evidence
page: real sequences from the corpus, the shapes they take, and six questions
with the measurements attached. In short:

| Question | Why it is hard |
| --- | --- |
| Where do rules come from? | Grounding body markup from `og:title` recovers `h1` on only 2 of 13 domains, because of the site-name suffix |
| Two declarations that disagree | `og:title` and `twitter:title` are both correct and say different things |
| Substring or token? | `subtitle` contains `title`; splitting `news__title` is required and leaves `subtitle` unmatched |
| Language | Shape A is language independent by specification. Shape C is not: what about `class="titulo"`? |
| Cleaning | A located value can carry a trailing separator and still be the right node |

One of the six has since been answered. The record boundary landing on
`/html/head` was the largest open failure when that page was written, and
choosing the container before the fields is what closed it. The results page
records what that change was worth.

<div class="pager" markdown="1">
<span markdown="1">&larr; [ai]({{ '/ai/' | relative_url }})</span>
<span markdown="1">[The markup]({{ '/algorithms/markup.html' | relative_url }}) &rarr;</span>
</div>
