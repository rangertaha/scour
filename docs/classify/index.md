---
title: classify
description: What a fetched page is about, what a node of the crawl graph is, and the circularity both exist to break.
---

# classify

<p class="lede">Package <code>classify</code> decides what a fetched page is
about. It exists to break a circularity.</p>

<figure>
<img src="{{ '/img/classify.svg' | relative_url }}" alt="A circular dependency: the scorer learns from labels, labels come from extraction, and extraction needs rules induced from pages the crawl already chose. Classification reads the page directly and supplies a label that does not come from extraction, breaking the loop. Separately, node classifiers answer per kind: role, topic and recency.">
</figure>

## The circularity

A page counts as worth crawling if extraction found records in it. Extraction
only works once induction has learned where the fields are, which it does from
pages the crawl already decided were worth fetching.

On the first crawl of a new site none of that has happened, so every page looks
equally unpromising and the scorer learns from labels that are mostly noise.

Reading the page and saying what it is breaks the loop: a listing page is a
listing page whether or not the rules can yet pull a price out of it.

```toml
[model]
classifier = "llm"     # "" is off, which is the default
ai         = "local"
```

Classification is off by default because it costs model calls on a path that
runs per page. A budget caps how many a run may spend and a cache keyed by the
question means the same one is never paid for twice, both of which are
[the ai package's]({{ '/ai/' | relative_url }}) job rather than this one's.

## nodeclass
{: #nodeclass }

`nodeclass` asks the same sort of question about a URL rather than about markup.
A node is a URL, and the graph is how the crawl reached it: the path back to the
seed, what came back, and what the page turned out to link to.

This is deliberately not classification of HTML. Whether a page holds records is
a fact about the URL and its place in the site, not about any element on it.
Answering it from the markup means asking the same question of every node in a
document instead of once per page, and "this page links to records but holds
none" cannot be seen from the markup at all.

### Several classifiers, several questions

A node carries more than one answer. What role it plays in the crawl, what topic
it is about, how fresh it is: different questions, different vocabularies,
different evidence.

So a classifier declares its `Kind`, and verdicts are held per kind. **Two
classifiers of one kind are alternatives; two of different kinds both run.**
That is what lets a second classifier arrive without displacing the first.

A verdict here is a classifier's answer about a URL, which is not the verdict a
person puts on a record. That one is a *mark*, and lives on the
[record]({{ '/train/' | relative_url }}#what-a-correction-changes).

| Kind | Vocabulary | State |
| --- | --- | --- |
| role | seed, hub, page 2, detail, boilerplate, dead | Decoded and stored by `score/hmm` |
| topic | what the page is about | Registered, not written |
| recency | how fresh it is | Registered, not written |

## The open fault, worked through

This is the newest extension point and the one with the most room, so it is
worth showing what it is actually for rather than only what it is.

The role question has an answer that nothing reads, and reading it would not
help. `internal/score/hmm` decodes six roles over the parent path of every URL,
stores them, and `scour item ls` prints them. Outside that package `hmm.Detail`
has no callers, so extraction runs over every fetched page whatever the crawl
graph concluded.

**Gating extraction on that role was the obvious fix and it is the wrong one,
because the role is derived from extraction.** Within one training run,
`Trainer.extract` writes a match count per URL and `Trainer.trainChain` then
fits the chain from those counts; `observationsOf` tests `Matches > 0` before
`Links > 0`, so any page a record came out of is observed as `Records` and
decoded as `Detail`. Gating extraction on the role would gate it on its own
output.

The corpus says so plainly: 866 of one corpus's 867 records come from pages
already labelled `detail`, and every one of the 118 short titles is among them.
The gate would drop one record and fix nothing. That figure is not the
classifier agreeing with extraction, it is the classifier restating it.

What the fault actually is, in two parts. Section index URLs like
`/news/community/` are decoded `detail` when they are hubs, which is the
circularity above. And `/eedition/special_section/page_...`, `/ads/sale/...` and
`/helpcenter/article_...` really are detail pages, just not articles: a
different subject, which is a topic question and not a role one.

### Finding evidence extraction did not produce

Outlink count is the obvious candidate and does not work: pages with a
section-like title average 18.6 queued children against 8.7 for the rest, with
ranges that overlap almost entirely, so no threshold separates them.

URL shape does separate them, measured over 867 records:

| Last path segment | Section-like title | Normal title |
| --- | --- | --- |
| a directory, trailing slash | 77 | 9 |
| carries an id | 29 | 735 |
| carries digits | 12 | 4 |

98% of the real articles carry an id in the last segment, and 65% of the section
pages are bare directories. That is known the moment a URL is discovered, before
anything is fetched, let alone extracted.

**It must not become a rule that articles have ids in their URLs.** That is true
of this publisher and false of plenty of others, and hardcoding it is exactly the
kind of belief about content a generic crawler does not get to hold. It belongs
as an observation the chain reads, so the association between shape and role is
fitted per item from that item's own crawl, and a site that numbers its sections
and slugs its articles learns the opposite association without anyone editing
code.

Implementing it means a new observation vocabulary, which changes the shape of
the emission matrix a fitted chain serialises. Stored chains are six roles by
four symbols today, so they need a version and a refit rather than being read
under the new meaning, which would be silently wrong rather than loudly stale.

<div class="pager" markdown="1">
<span markdown="1">&larr; [train]({{ '/train/' | relative_url }})</span>
<span markdown="1">[score]({{ '/score/' | relative_url }}) &rarr;</span>
</div>
