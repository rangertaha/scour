---
title: Measured results
description: What extraction achieves on live corpora, and what running against real sites exposed.
---

# Measured results

<p class="lede">Extraction is judged on live corpora rather than on fixtures, and
re-measured after every change to inference.</p>

There are three corpora: 808 pages from 19 news sites in English, Greek, Russian
and French; ten live RSS and Atom feeds; and a second, larger HTML corpus of
1,267 pages from 30 different sites, kept deliberately separate so it can answer
a question the first cannot.

Per field, how many of the extracted records carry a value, and how many
distinct values those are. Distinctness is the number that matters: a field
whose value is the same on every page of a site is describing the site, not the
article.

<figure>
<img src="{{ '/img/fields.svg' | relative_url }}" alt="Per-field fill rates on two corpora: the nineteen sites the algorithm was built against, and thirty it had never seen.">
</figure>

## 808 HTML pages, 19 sites

| Field | Before | After | What it had found |
| --- | --- | --- | --- |
| records | 503 | **713** | |
| title | 243 / 10 | **644 / 627** | the site's name, one per site |
| link | 77 / 4 | **713 / 700** | preconnect hints naming CDNs |
| author | 0 | **301 / 52** | nothing at all |
| published | 224 / 74 | **249 / 204** | |
| summary | 470 / 470 | 473 / 470 | og:description, already correct |
| section | 1 / 1 | **166 / 19** | a related-articles heading |

## Ten live feeds

| | Records |
| --- | --- |
| Before | 9 |
| After | **266** |

## 1,267 pages, 30 sites the model has never seen

The first corpus is the one the work was done against, so its numbers say only
that the faults found in it were fixed. This one is thirty different sites,
sharing no host with the first, in Arabic, Turkish, Spanish, Malayalam and
English. Nothing was tuned against it.

| Field | 19 sites, developed against | 30 sites, unseen |
| --- | --- | --- |
| title | 90% | **100%** |
| link | 100% | 100% |
| summary | 66% | **90%** |
| published | 35% | **76%** |
| modified | 34% | **76%** |
| author | 42% | **69%** |
| section | 23% | **98%** |

867 records from 1,267 pages. Every field is filled more often on the sites the
algorithm had never seen than on the ones it was built against, which is the
opposite of what overfitting looks like.

What it exposed, and what came of it:

- **The CSS dialect pinned a per-page id.** `#asset-59da10e1-...` led the
  selector for `published` and `modified`, so the rule matched exactly the page
  it was induced from, while the XPath for the same field stayed generic and
  worked across 660 records. *Fixed.* Where a group's instances share no leading
  segment, the selector generalizes to the tail they do share, which CSS already
  reads as descendant-anchored. Re-inducing both corpora changes no extraction
  number and removes the id.

- **`published` and `modified` were thought to be one node.** They are not. The
  locators are `dateCreated` and `dateModified`, distinct and correct, and 273
  of the 660 records carrying both hold *different* values, which could not
  happen if one node fed them. The 387 that agree are articles that were never
  edited. *Not a fault.*

- **118 of 867 titles are shorter than twenty characters**: `"Page A1"`,
  `"Ads"`, `"Community"`. Most are distinct rather than one section name
  repeated, so this is a mix of section pages read as articles and titles that
  really are that short. *Open*, and the reason
  [`internal/classify`]({{ '/classify/' | relative_url }}) exists.

## What training costs
{: #what-training-costs }

Measured on one corpus at four sizes, so the numbers say something about scaling
rather than about two different collections of sites.

| Pages | Seconds | Per page |
| --- | --- | --- |
| 100 | 49 | 0.49 |
| 200 | 84 | 0.42 |
| 400 | 241 | 0.60 |
| 800 | 394 | 0.49 |
| 1,267 | 794 | 0.63 |

Linear: the exponent over the whole range is 1.09, and 1.03 over the top end.

It is linear in bytes rather than in pages, which is what makes one corpus look
slower than another. news-html averages 95KB a page and costs 0.18s each;
news2 averages 297KB and costs 0.63s. Three times the markup, three times the
work. Comparing the two totals, 78MB against 422MB, gives 5.4 times the bytes
for 5.6 times the time.

So a corpus is budgeted by its size on disk, not by its page count, and the
[cache]({{ '/cache/' | relative_url }}) reports that directly:

```
scour status news2
cache       2,829 pages, 580.2MB
```

## What the corpora exposed

Every one of these was found by running against live data and measuring, not by
reading the code. The last of them was found against the
[e2e fixture]({{ '/e2e/' | relative_url }}) rather than a live corpus, which is
what the fixture is for:

| Fault | Effect |
| --- | --- |
| A field's location was fixed before the record's container was known | A feed's logo beat the article; 45 articles became 1 |
| The container was always the deepest ancestor | It was `/html/head` on every site, so the article's own markup was never a candidate |
| Support counted matches, not independent observations | A locator was rewarded for being ambiguous |
| Reach did not count at all | A body div on one site beat a meta tag on thirteen |
| HTML tag names were discarded as labels | `<h1>` is on 13/13 sites and `<time>` on 10/13, both ignored |
| An attribute's own name was a full-weight label | `<link rel="alternate" title="...">` outscored the headline |
| `rel` was never read as a label | `rel="canonical"` unused despite 10/13 sites at perfect precision |
| Layout classes were read as labels | `class="text-3xl"` and `class="brand"` named fields |
| The sequence model averaged a distribution into a confidence | Every score fell by about a third, and further as the schema grew |
| A value that never changed was still read as a field | `section` was one heading repeated on 211 records |
| A label matched by substring rather than by word | `subtitle` contains `title`, so every page's `<title>` answered for `summary` |

The last one is worth stating on its own, because it was invisible in every
number that was being watched. `summary` was filled on 100% of records, which
reads as the field working. What it held was the `<title>` element, so `summary`
and `title` said the same thing and one of the two was worthless on every page.
A field that always agrees with another field is not a field, and no coverage
figure can say so.

It reached further than `summary`. Measured on the fixture over the same
corpus, before and after: records where `title` and `summary` were identical
went from 9 of 14 to 0 of 12, `author` from 3 to 10, `published` from 5 to 10,
and `link` from 0 to 4. The `section` kicker above, which the table records as
its own fault, is no longer extracted either, because it was reached through the
same containment. That is a side effect rather than a proof: whether the
constant-value test would also have caught it is still unknown, and the fixture
no longer exercises it.

The one before it is worth stating too, because no amount of reading the
markup finds it. `section` resolved to
`<p class="kicker">Other items that may interest you</p>`, and `kicker` is a
real name for a section line, so the label was correct. What marked it was that
211 records shared one value. A field describes its record and so changes from
one to the next; a value that never changes is describing the site.

These numbers are re-measured after every change to inference, against the same
two corpora, and [the architecture]({{ '/architecture/' | relative_url }}#what-is-not-extensible-and-why)
explains why the constants behind them are not configuration.

<div class="pager" markdown="1">
<span markdown="1">&larr; [The HTTP API]({{ '/server/api.html' | relative_url }})</span>
<span markdown="1">[e2e]({{ '/e2e/' | relative_url }}) &rarr;</span>
</div>
