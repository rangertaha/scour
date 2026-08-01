---
title: The markup
description: Real sequences from the crawled corpus, the four shapes a field takes in them, and the questions inference has not answered.
---

# The markup we are dealing with

<p class="lede">Real sequences from the crawled corpus: 808 pages, 19 news
sites, 13 with cached pages, in English, Greek, Russian and French. Nothing here
is invented.</p>

<figure>
<img src="{{ '/img/shapes.svg' | relative_url }}" alt="Four shapes a field takes in real markup: the name in one attribute and the value in another, the tag itself as the name, the class or id as the name, and the name carried inside the value. A fifth is the same shape as the fourth and wrong.">
<figcaption>The shapes, and the one pair that has the same structure and opposite verdicts.</figcaption>
</figure>

This is the evidence page for
[the algorithms]({{ '/algorithms/' | relative_url }}): what the corpus actually
contains, rather than what the inference does with it.

> **Two things below have since been fixed, and the page is kept as it was
> written.** It records what inference did at the time, which is what makes the
> measurements after it meaningful.
>
> The record container landing on `/html/head`, called "the largest open
> failure" in section 6, is closed: the container is now chosen before the
> fields, and the body markup it put out of reach is reachable. `<link
> rel="alternate">` and `rel="preconnect"` no longer win `title` and `link`.
>
> [What that changed, in numbers]({{ '/results/' | relative_url }}).

Annotations say what inference does with each today, and `<-` marks the ones
that mislead it.

## 1. The head block, which is what wins today

WordPress (smokymountainnews.com):

```html
<meta property="og:title"   content="Sylva denies pride parade, festival still a go -"/>
<meta property="og:url"     content="https://smokymountainnews.com/archived/archived-news/sylva-denies-pride-parade-festival-still-a-go/"/>
<meta property="article:published_time" content="2024-03-27T18:18:24+00:00"/>
<meta name="author"         content="Hannah McLeod"/>
<link rel="canonical"  href="https://smokymountainnews.com/archived/.../"/>
<link rel="alternate"  href="https://smokymountainnews.com/feed/"
      title=" &raquo; Feed" type="application/rss+xml"/>          <!-- <- was `title` -->
<link rel="preconnect" href="https://i0.wp.com"/>                 <!-- <- was `link`  -->
<title>Sylva denies pride parade, festival still a go -</title>
```


```html
<meta property="og:title"   content="Sylva denies pride parade, festival still a go -"/>

meta -> property -> contains("title") -> "Sylva denies pride parade, festival still a go -"
```

```html
<meta property="og:url"     content="https://smokymountainnews.com/archived/archived-news/sylva-denies-pride-parade-festival-still-a-go/"/>

meta -> property -> contains("url) -> "https://smokymountainnews.com/archived/archived-news/sylva-denies-pride-parade-festival-still-a-go/"
```


```html
<link rel="alternate" title="West Florida News">

link -> title -> "West Florida News"
```



```html
<img class="user-placeholder" alt="Author:THE NEWSROOM"/>

img -> alt -> alt -> Author -> "THE NEWSROOM"
```



`rel="alternate"` carries an attribute literally named `title`, and `preconnect`
carries one named `href`. Both used to win their property outright: 503 records
shared ten titles, and `link` came back as four CDN hostnames. The record
container is `/html/head`, so everything in section 2 is currently out of reach.

`rel="canonical"` is on 10 of 13 domains at perfect precision and is still unused.

## 2. The same fields in the body, which is what we cannot reach

Five sites, five ways of saying the same three things.

WordPress, semantic classes:

```html
<h1 class="entry-title">Sylva denies pride parade, festival still a go</h1>
<time class="entry-date published updated" datetime="2024-03-27T14:18:24-04:00">March 27, 2024</time>
<span class="byline"><span>by</span>
  <span class="author vcard"><a class="url fn n" href="/author/hannah-mcleod/">Hannah McLeod</a></span>
</span>
```

Tailwind, no semantic class at all:

```html
<h1 class="font-serif text-3xl sm:text-[42px] font-bold leading-tight text-text-dark"
    data-uuid="4f15d68a-4e66-4565-b5bb-f17c003033f3">
  Where could drivers find the cheapest gas in cities within Charlotte County ...
</h1>
<time datetime="2026-07-20T16:50:06-05:00">Jul 20, 2026</time>
```

Every class on that `<h1>` is layout vocabulary. The tag is the only signal.

Plain ids, no classes (merrillfotonews.com):

```html
<h1 id="headline">Natural vs. organic vs. clean: The truth behind beauty marketing</h1>
<div class="byline" id="byline">Taylor Audette for Ogee</div>
<time datetime="Fri, 24 Jul 2026 08:05:05 -0500">
  <span class="weekday">Friday, </span>
  <span class="monthday">July 24, 2026 </span>
</time>
```

That date is split across child spans, so the element's own text is empty and
only the `datetime` attribute holds a usable value.

Russian (utro.ru):

```html
<h1 class="news__title">Welt: Киев теряет возможность повлиять на исход конфликта</h1>
<time class="news__date" datetime="2026-07-31" pubdate="">12:14, 31.07.2026</time>
<div class="news-center__top-author io-author"><i></i><a href="/author/%D0%9E...">...</a></div>
```

Greek, Next.js (luben.tv):

```html
<h1 class="single-news-title">“Θα με έψηνε το FBI όταν το κόψω” παραδέχτηκε ο Φραντζί Πιερό</h1>
<div class="author-link"><div class="taxonomy-item">
  <a href="/user/thenewsroom"><img class="user-placeholder" alt="Author:THE NEWSROOM"/></a>
</div></div>
```

French (furansujapon.com):

```html
<time class="post-card__date" datetime="2026-07-31T10:19:24+02:00">31/07/2026</time>
<div class="post-card__meta">
  <a class="post-card__category" href="/category/manga-anime/">Manga et Anime</a>
</div>
```

What survives translation is the tag and the standardised attribute.
`news__title`, `single-news-title` and `post-card__date` share nothing with each
other, but every one of these pages uses `<h1>` and `<time datetime>`.

## 3. Patterns worth designing against

**og:title is not the headline.** It usually carries a site-name suffix:

```
og:title  "Where could drivers find the cheapest gas ... July 11? - West Florida News"
h1        "Where could drivers find the cheapest gas ... July 11?"

og:title  "Sylva denies pride parade, festival still a go -"     <- trailing separator
h1        "Sylva denies pride parade, festival still a go"
```

This is why grounding body markup by exact string match failed: `tag:h1` was
recovered on only 2 of 13 domains despite `<h1>` being on all 13.

**Framework noise on every attribute.** Next.js stamps `data-next-head=""` on
every meta; Tailwind stamps a dozen layout classes on every element:

```html
<meta property="og:title" data-next-head="" content="..."/>
<h1 class="font-serif text-3xl sm:text-[42px] font-bold leading-tight text-text-dark">
```

**Canonical can point off-site.** A syndicated story keeps the originator's URL,
so `og:url` and `canonical` both name a different domain than the page was
fetched from:

```html
<!-- fetched from merrillfotonews.com -->
<meta property="og:url" content="https://ogee.com/blogs/all/natural-vs-organic-vs-clean..."/>
<link rel="canonical"   href="https://ogee.com/blogs/all/natural-vs-organic-vs-clean..."/>
```

**Not every crawled page is an article.** A homepage has an `<h1>` too, and it is
not a headline:

```html
<h1 class="sakura-welcome__title">Bienvenue sur <span>FuransuJapon</span></h1>
```

**A value can be an attribute while its name is a sibling attribute.** This is
the shape both `<meta>` and `<time>` use, and it is why a value node's own name
is often useless:

```html
<meta property="article:published_time" content="2026-07-20T16:50:06-05:00"/>
       \_______ the name _______/        \____ the value ____/

<time datetime="2026-07-20T16:50:06-05:00">Jul 20, 2026</time>
      \_ name _/ \______ value ______/     \_ human rendering _/
```

## 4. For contrast, a feed

Where every field is named by its own element, which is why feeds work and HTML
does not:

```xml
<item>
  <title>Council approves transit line</title>
  <link>https://example.com/transit/</link>
  <pubDate>Tue, 14 Mar 2026 09:00:00 GMT</pubDate>
  <dc:creator>Jane Doe</dc:creator>
  <description>The vote clears the way for construction.</description>
</item>
```

Ten live feeds yield 267 records with six or seven fields each. The same schema
over 808 HTML pages yields 631 records, all of them read out of `<head>`.

---

# 5. Every case in the notation

Leave notes on the `>` lines.

## Shape A: name in one attribute, value in another

```
meta -> property -> contains("title")     -> @content
meta -> property -> contains("published") -> @content
meta -> name     -> equals("author")      -> @content
link -> rel      -> equals("canonical")   -> @href
```

8 to 10 of 13 domains each. Language independent: utro.ru and luben.tv publish
`og:` in English on Russian and Greek pages. `canonical` is the one we ignore,
and `canonical` is not a word in any shipped schema.

> feedback:

## Shape B: the tag is the name

```
h1   -> <tag> -> means("heading") -> text()
time -> <tag> -> means("date")    -> @datetime
time -> <tag> -> means("date")    -> text()
```

`<h1>` is on 13/13 domains, `<time datetime>` on 10/13. The only shape that
works when every class is Tailwind. Note the last two: same element, same name,
two different values, one machine-readable and one for people.

> feedback:

## Shape C: the class or id is the name

```
h1   -> class -> contains("title")    -> text()      entry-title, news__title
h1   -> id    -> contains("headline") -> text()      merrillfotonews
div  -> class -> contains("byline")   -> text()
span -> class -> contains("byline")   -> text()      value is two levels down
time -> class -> contains("date")     -> @datetime   entry-date, post-card__date
```

> feedback:

## Shape D: the name is inside the value

```
img -> alt -> splitLabel() -> "Author" : "THE NEWSROOM"
```

Same form as `"Make: Toyota"`. The string carries its own label, so name-source
and value-source are the same slot and it is still correct.

> feedback:

## Shape E: collapsed, and wrong

```
link -> title -> "West Florida News"        won title on 19/19 sites
link -> href  -> "https://i0.wp.com"        won link on 19/19 sites
```

The element had a real name-source and we read past it:

```
link -> rel -> equals("alternate")  -> @href
link -> rel -> equals("preconnect") -> @href
```

D and E have the same structure and opposite verdicts. That is the thing to
resolve.

> feedback:

---

# 6. Open questions

## Q1. Where do rules come from?

The notation is a representation, not a procedure. Hand-writing them per site
per field is wrapper maintenance. Learning them needs labels, and grounding body
markup from `og:title` recovered `h1` on only 2 of 13 domains, because of the
site-name suffix shown in section 3.

> answer:

## Q2. Two declarations that disagree

```
meta -> property -> contains("title") -> @content     og:title
meta -> name     -> contains("title") -> @content     twitter:title
```

Both are Shape A, both say title. Moving one weight from 0.95 to 1.0 flipped
`title` between 631 and 340 filled records.

> answer:

## Q3. Substring or token?

`subtitle` and `titlebar` both contain `title`. Splitting `news__title` on the
underscore is required, but splitting leaves `subtitle` as one token that no
longer matches. Opposite answers on real corpus classes.

> answer:

## Q4. Language

Shape A is language independent by specification. Shape C is not.
`contains("title")` survives on utro.ru only because the Russian site wrote its
class in English. What happens on `class="titulo"`?

> answer:

## Q5. The record boundary

Every shape here is per element. On a listing page Shape C fires twenty times.
Nothing says which fields belong to the same record, and the container landing
on `/html/head` is the largest open failure.

> answer:

## Q6. Cleaning

Shape A locates `"Sylva denies pride parade, festival still a go -"`, separator
included. Located and still wrong. Fifth slot, or outside the notation?

> answer:




<div class="pager" markdown="1">
<span markdown="1">&larr; [The algorithms]({{ '/algorithms/' | relative_url }})</span>
<span markdown="1">[store]({{ '/store/' | relative_url }}) &rarr;</span>
</div>
