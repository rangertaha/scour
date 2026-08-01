---
title: score
description: How likely a URL is to lead to a match, and why one scorer is not enough.
---

# score

<p class="lede">Package <code>score</code> predicts how likely a URL is to lead
to a match. This is what makes scour a focused crawler rather than a crawler:
the frontier pops in score order, so the budget is spent on the promising part of
a site.</p>

<figure>
<img src="{{ '/img/score.svg' | relative_url }}" alt="Two layers rank a URL. A base scorer reads the tokens of the link itself and answers whether it looks like a record page. A hidden Markov chain reads the whole path back to the seed and answers whether it leads to one. The two are combined as independent evidence in odds form.">
</figure>

## The classic failure this exists to avoid

A per-URL scorer judges a link on its own tokens. That fails the oldest problem
in focused crawling: **the hub page leading to a hundred records usually
contains none itself.** It scores near zero, never gets followed, and the
hundred records behind it are never seen.

Modelling the path fixes it, because the value of a link becomes the expected
value of where it leads.

## Two layers

| Layer | Sees | Answers |
| --- | --- | --- |
| base scorer | the tokens of this URL | does this *look* like a record page? |
| crawl chain | the whole path back to the seed | does this *lead* to one? |

`internal/score/hmm` wraps a base scorer with the chain. Neither is sufficient
alone, so the two are combined as independent evidence in odds form rather than
one overruling the other.

## The base scorers

```toml
[model]
scorer    = "bayes"    # or embed
min_score = 0.1        # do not follow links below this
```

| Name | How | Needs |
| --- | --- | --- |
| `bayes` | Naive Bayes over URL tokens. The default | Nothing |
| `embed` | Vectors over the same tokens | `[model] vectors` |

Scoring happens once per discovered link, millions of times over a large crawl,
so it has to be cheap. That constraint is why the interface takes a `Features`
struct and returns a `float64`, and why anything expensive belongs in the
[matcher]({{ '/matcher/' | relative_url }}), which runs per candidate node during
training rather than per link during a crawl.

## The chain

Six roles are decoded over the parent path of every URL:

```
roles       detail 793, dead 41, seed 17
```

| Role | Means |
| --- | --- |
| `seed` | A target you gave it |
| `hub` | Lists records |
| `page 2` | Another page of the same listing |
| `detail` | Holds records |
| `boilerplate` | Navigation, terms, about |
| `dead` | Leads nowhere |

Three constraints govern how it is fitted, and they are the same three wom
applies to its field-order chain, for the same reason: learning safely from very
little data.

1. **Only transitions are trained.** Emissions come from what a fetched page
   turned out to hold. An unsupervised chain's states carry no inherent meaning
   and would drift off the roles they are supposed to name.
2. **Estimation is MAP, not maximum likelihood.** The prior enters as
   pseudo-counts, so twenty pages leave it mostly intact while twenty thousand
   let the data speak.
3. **Fitting runs over crawl paths, never the whole visited set.** Fitted to
   everything, the likelihood is dominated by the boilerplate the chain exists to
   discount.

The roles are the engine's own and are not configurable. See
[what is not extensible]({{ '/architecture/' | relative_url }}#what-is-not-extensible-and-why),
and [classify]({{ '/classify/' | relative_url }}#the-open-fault-worked-through)
for what the roles cannot yet be used for, and why.

## Scoring is per graph

`score.Kinds` is the one place that says what can be scored and where the
registry for it lives:

| Kind | Node | Registry |
| --- | --- | --- |
| `KindURL` | a URL in the crawl graph | `internal/score` |
| `KindDocument` | a node of a parsed page | [`internal/matcher`]({{ '/matcher/' | relative_url }}) |

Two registries rather than one, because a scorer of one kind takes an input the
other cannot produce. Folding them together would mean a cast at every call, and
a feed's items or a PDF's regions would want a third input again.

## Writing one

```go
func init() {
    score.Register("mine", func(cfg score.Config) (score.Scorer, error) {
        return score.FuncScorer(func(f score.Features) float64 { return 0.5 }), nil
    })
}
```

A scorer that also implements `Trained` is fitted by
[training]({{ '/train/' | relative_url }}); one that does not is used as it is.
Selected with `[model] scorer`.

<div class="pager" markdown="1">
<span markdown="1">&larr; [classify]({{ '/classify/' | relative_url }})</span>
<span markdown="1">[ai]({{ '/ai/' | relative_url }}) &rarr;</span>
</div>
