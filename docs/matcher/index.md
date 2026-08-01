---
title: matcher
description: How strongly one node satisfies one property, and the seam where scour's intelligence is chosen.
---

# matcher

<p class="lede">Package <code>matcher</code> decides how strongly a node
satisfies a property. It is the seam where scour's intelligence is chosen.</p>

<figure>
<img src="{{ '/img/matcher.svg' | relative_url }}" alt="A property and a candidate node go into the matcher, which answers how strongly that node is that field. Three implementations are registered: a heuristic one needing no network, one comparing word vectors, and one that asks a language model.">
</figure>

## What it is asked

One question, per candidate: given this property, and this node of a parsed
page, how strongly is the second an instance of the first?

```go
Score(ctx context.Context, prop wom.Prop, node *wom.Node) float64
```

[wom]({{ '/parse/' | relative_url }}) locates fields by scoring candidate nodes,
so swapping the matcher swaps what the engine understands, without touching
graph construction, locator synthesis, or a line of crawl code.

## The three implementations

| Name | Needs | Good at |
| --- | --- | --- |
| `heuristic` | Nothing. No network, no key, no training data | Labels that are words: `entry-title`, `byline`, `dateModified` |
| `embed` | A word vector file, `[model] vectors` | Labels that mean the right thing in the wrong words |
| `llm` | An [`[[ai]]` block]({{ '/ai/' | relative_url }}) | Judgements a word list cannot make |

```toml
[model]
matcher = "heuristic"
```

The heuristic implementation is the default and the baseline, and everything
richer is measured against it. That ordering is deliberate: a matcher that
cannot be beaten by a model is a matcher worth keeping, and a model that cannot
beat one is worth knowing about before it is in the default path.

## What the heuristic cannot see

It compares spellings. A page labelling its field `manufacturer` scores zero
against a schema calling it `make`, and a Greek page labelling it anything at
all scores zero against every English word in the schema. Those are not weak
matches, they are invisible ones, and no weight on a token overlap finds them.

`embed` compares meanings instead, against the same word vector file the
[embed scorer]({{ '/score/' | relative_url }}) ranks links with. Naming the file
once is what keeps a link and a node judged against the same vocabulary.

```toml
[model]
matcher = "embed"
vectors = "glove.6B.300d.txt"
```

It adjusts the heuristic rather than replacing it. A similarity moves the score
by how far it sits from neutral, which is where unrelated text lands, so vectors
that recognise nothing change nothing. That property is what makes it safe to
leave on: the failure mode of a vector file is an absent answer rather than a
wrong one, and an absent answer costs nothing.

It speaks only inside the same undecided band the LLM matcher is consulted in,
and for a different reason. Asking a model is expensive; asking a dictionary is
free. The band is there because outside it the heuristic already has real
evidence, and a dictionary that has never seen the page does not get to argue
with it.

Two limits are worth knowing before you turn it on. Published vectors are
usually English-only, so a page in another language falls back to the heuristic
rather than being helped. And orthogonal is not opposite: an unrelated label
cannot argue against a match, only fail to argue for one.

## Why the expensive one is bounded

An LLM matcher is asked about every candidate node on every page, which is a
number in the millions on a real crawl. Two things bound it.

**A floor.** Candidates below `DefaultFloor` are not worth asking about, so the
model sees only the ones the cheap pass could not settle.

**A budget.** `DefaultBudget` caps model calls per training run. Spending it
returns an error rather than silently continuing without judgement, so a run
that ran out says so.

Behind both sits the `judgements` table, which caches what a model was asked,
keyed by the question. The same question asked across pages, items and retrains
is paid for once, which is what makes retraining after a correction affordable
when the matcher is a hosted model.

## The two-graph distinction

This registry ranks nodes of a parsed page. Ranking URLs of the crawl graph is a
different registry, and lives in [score]({{ '/score/' | relative_url }}).

```go
const Ranks = score.KindDocument   // matcher
const Ranks = KindURL              // score
```

They are the same kind of extension over different graphs, and they looked
unrelated only because one of them was called a matcher. Each declares what it
ranks, and `score.Kinds` is the one place that says what can be scored and where
the registry for it lives. They stay separate because a scorer of one kind takes
an input the other cannot produce.

## Writing one

```go
func init() {
    matcher.Register("mine", func(cfg matcher.Config) (wom.Matcher, error) {
        return &mine{}, nil
    })
}
```

Ask `wom.Labels(node)` for the names that describe a value rather than reading
attributes yourself. Which strings name a value is not a matter of taste: it is
which attributes carry meaning, which tags name their content, which classes are
layout vocabulary rather than description, and which neighbour is a label rather
than a sibling value. All of that was settled by measurement, and each answer
comes back with how far its source is trusted.

Selected with `[model] matcher`. See
[extending it]({{ '/architecture/extending.html' | relative_url }}).

<div class="pager" markdown="1">
<span markdown="1">&larr; [parse &amp; wom]({{ '/parse/' | relative_url }})</span>
<span markdown="1">[train]({{ '/train/' | relative_url }}) &rarr;</span>
</div>
