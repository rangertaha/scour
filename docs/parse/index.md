---
title: parse & wom
description: The Web Object Model. Every format as one addressable graph, and inference over it.
---

# parse &amp; wom

<p class="lede">Package <code>parse</code> turns an item's cached pages into a
<code>wom</code> graph. Package <code>wom</code> is the Web Object Model: a
single graph that unifies HTTP exchanges and the documents they carry into
nodes, paths and attributes.</p>

<figure>
<img src="{{ '/img/parse.svg' | relative_url }}" alt="HTML, XML, RSS, JSON, JavaScript, CSS and PDF bodies all become subtrees of one graph, shaped root to domain to uri to document to content.">
</figure>

## One graph, not a pile of documents

A graph is built by adding responses to it:

```go
w := wom.New()
w.Add(resp)   // as many responses as you like, in any format
```

HTML, XML, SVG, RSS and Atom, JSON, JavaScript, CSS and PDF bodies all become
subtrees of the same structure, shaped root → domain → uri → document → content.

The reason to build one graph rather than parse pages one at a time is that
almost every question worth asking spans pages. Which candidate locator fires on
the most sites, whether a value is the same on every page of a site, whether a
field ever changes from record to record: none of those can be answered inside
one document, and all of them turned out to be the difference between a rule
that works and a rule that looks right.

Every node can report its own address, as `Node.XPath`, `Node.Selector` and
`Node.Path`. That is what makes a located field expressible as a rule that runs
against pages it was never induced from.

## Inference

The main operation is schema inference: given a description of the data you
want, wom reports where that data lives.

```go
items, err := w.Schema(wom.Prop{
    Name:    "vehicles",
    Aliases: []string{"car"},
    Props: []wom.Prop{
        {Name: "make", Examples: []string{"Toyota"}},
        {Name: "model"},
        {Name: "year", Type: wom.TypeNumber},
        {Name: "fuel"},
    },
})
```

Each returned `Item` carries a probability and a `Locator` holding the URI
pattern, XPath, CSS selector, native path and extraction regex for the value it
found. `w.Model` gives that result as a `Model`, which can be saved, reloaded
and applied to pages it was never induced from. That is the line between the two
halves of the work: [induction is expensive and happens once, extraction is
cheap and happens per page]({{ '/train/' | relative_url }}).

## What the markup actually looks like

The design is answerable to real corpora rather than to a mental model of clean
HTML. Five sites say the same three things five ways:

```html
<h1 class="entry-title">Sylva denies pride parade, festival still a go</h1>

<h1 class="font-serif text-3xl sm:text-[42px] font-bold leading-tight">…</h1>

<h1 id="headline">Natural vs. organic vs. clean</h1>

<h1 class="news__title">Welt: Киев теряет возможность повлиять на исход конфликта</h1>

<h1 class="single-news-title">“Θα με έψηνε το FBI όταν το κόψω”</h1>
```

`news__title`, `single-news-title` and `entry-title` share nothing with each
other. Every one of these pages uses `<h1>`, and every one that carries a date
uses `<time datetime>`. **What survives translation is the tag and the
standardised attribute**, which is why discarding tag names as labels cost 13
sites out of 13, and why a Tailwind page where every class is layout vocabulary
is still readable.

Four shapes cover the corpus:

| Shape | Form | Example |
| --- | --- | --- |
| Name in one attribute, value in another | `meta → property → contains("title") → @content` | `og:title` |
| The tag is the name | `time → <tag> → means("date") → @datetime` | `<time>` |
| The class or id is the name | `h1 → class → contains("title") → text()` | `entry-title` |
| The name is inside the value | `img → alt → splitLabel()` | `alt="Author:THE NEWSROOM"` |

The fifth shape is the same as the fourth and wrong: `<link rel="alternate"
title="West Florida News">` has a real name source in `rel`, and reading past it
to the attribute literally named `title` won the title field on 19 sites out of
19. Telling those two apart is what the [matcher]({{ '/matcher/' | relative_url }})
is for.

## Inside the package

`wom` is a facade. The implementation lives in internal packages, each owning
one layer, which is what lets inference change without the graph changing
underneath it:

| Layer | Owns |
| --- | --- |
| `graph` | Nodes and addressing |
| `parse` | The format parsers |
| `schema` | The vocabulary of props and items |
| `match` | Semantic scoring of a candidate |
| `seq` | The sequence model over field order |
| `pattern` | Pattern synthesis and matching |
| `infer` | The inference engine |
| `model` | The saved artifact |

## Why feeds work and HTML is hard

```xml
<item>
  <title>Council approves transit line</title>
  <link>https://example.com/transit/</link>
  <pubDate>Tue, 14 Mar 2026 09:00:00 GMT</pubDate>
  <dc:creator>Jane Doe</dc:creator>
</item>
```

Every field is named by its own element. Ten live feeds yield 267 records with
six or seven fields each. The same schema over 808 HTML pages once yielded 631
records, all of them read out of `<head>`, which is the fault the container work
fixed. [The measured results]({{ '/results/' | relative_url }}) are where that
story is told with numbers.

<div class="pager" markdown="1">
<span markdown="1">&larr; [cache]({{ '/cache/' | relative_url }})</span>
<span markdown="1">[matcher]({{ '/matcher/' | relative_url }}) &rarr;</span>
</div>
