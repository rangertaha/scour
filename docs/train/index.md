---
title: train
description: Inducing an item's rules from its cached pages, applying them, and what a correction changes.
---

# train

<p class="lede">Package <code>train</code> induces an item's extraction rules
from its cached pages, and applies them. Induction is expensive and happens once;
extraction is cheap and happens per page.</p>

<figure>
<img src="{{ '/img/train.svg' | relative_url }}" alt="Cached pages become one graph. Induction chooses the record container first and only then fixes the fields inside it, producing rules. Extraction applies those rules per page to make records, and the marks a person puts on records feed the next induction.">
</figure>

## Running it

```
scour train vehicle

pages       412 cached
examples    138 positive / 274 negative  (bootstrapped from property examples)
accuracy    0.91  (held out)

model written to ~/.config/scour/models/vehicle.json
```

Until you have marked anything, scour bootstraps its labels from your property
examples: a page whose text contains them is a positive, one that was crawled
and matched nothing is a negative. Accuracy is measured on a held-out fifth of
those pages, set by `[model] holdout`.

Training reads the [cache]({{ '/cache/' | relative_url }}), never the network,
so it can be repeated after every correction and re-measured against a corpus
that did not move.

## The order that matters

Two steps, and doing them in this order is most of what makes extraction work:

1. **Where does a record live?** Choose the container.
2. **Where does each field live inside it?** Only then fix the fields.

Getting that backwards was a real fault with a measurable cost. Choosing where a
field lives before knowing where the record lives let a feed's logo beat the
article, and forty five articles became one. A second version made the container
always the deepest common ancestor, which put it at `/html/head` on every site,
so the article's own markup was never a candidate at all.

## What makes a rule good

Three properties, each of which was once missing and each of which cost
something:

**Support counts independent observations, not matches.** Counting matches
rewarded a locator for being ambiguous: one that fired twenty times on one page
scored higher than one that fired once on twenty pages.

**Reach counts.** How many sites a locator works on has to enter the score, or a
body `div` that is perfect on one site beats a meta tag that is good on
thirteen.

**A value that never changes is not a field.** `section` once resolved to
`<p class="kicker">Other items that may interest you</p>`, and `kicker` is a
real name for a section line, so the label was correct. What marked it as wrong
was that 211 records shared one value. A field describes its record and so
changes from one to the next; a value that never changes is describing the site.

No amount of reading the markup finds that last one. It is only visible across a
corpus, which is why [one graph]({{ '/parse/' | relative_url }}) holds every page
at once.

## Reading the rules back

```
scour rules vehicle

ID  PID   HIT  PROP   XPATH                       SELECTOR          REGEX        URL
--  ---  ----  -----  --------------------------  ----------------  -----------  --------------------------
 1        .98         //div[@class='vehicle']     .vehicle                       http://www.example.com/...
 2    1   .98  make   .//dd[@class='make']        .vehicle .make    ^[A-Z][a-z]  http://www.example.com/...
 3    1   .95  model  .//dd[@class='model']       .vehicle .model                http://www.example.com/...
 4    1   .91  year   .//dd[@class='year']        .vehicle .year    \d{4}        http://www.example.com/...
```

Rules nest. The parent locates each record on the page, and its children pull
one property out of that record. `HIT` is the share of matching pages where the
rule fires, and the first row having no `PROP` is the container itself.

## What a correction changes

```
scour mark vehicle 1088 --invalid
scour mark vehicle 1042 1043 --valid
scour train vehicle
```

A record marked wrong is held out of the next training run, so both the scoring
model and the extraction rules stop making that mistake. One marked right is
what tells `scour train` to fit the field-order chain at all.

A verdict is a *mark*, not a label, because a label here is a tag: the words a
page might name a property with, which is what `scour item tag` edits. The two
were one word for a while and it was never clear which was meant.

Records keep their id and their mark across retraining, so an id read off one
listing still names the same record on the next.

## The chains

Training fits two sequence models, and they answer different questions.

The **field-order chain** lives in wom and reads how fields are ordered inside a
record. The **crawl chain** lives in [score]({{ '/score/' | relative_url }}) and
reads the path a URL was reached by. Both are fitted here, both are trained on
transitions only, and both take their emissions from what a fetched page turned
out to hold rather than from an unsupervised fit that would drift off the roles
it is supposed to name.

One earlier version averaged a distribution into a confidence, and every score
fell by about a third, further as the schema grew.

## What it costs

Linear in bytes, not in pages. 1,267 pages at 297KB average take 794 seconds;
808 pages at 95KB average take a fraction of that. The exponent over the whole
measured range is 1.09. [The numbers]({{ '/results/' | relative_url }}#what-training-costs).

<div class="pager" markdown="1">
<span markdown="1">&larr; [matcher]({{ '/matcher/' | relative_url }})</span>
<span markdown="1">[classify]({{ '/classify/' | relative_url }}) &rarr;</span>
</div>
