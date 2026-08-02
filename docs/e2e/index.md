---
title: e2e
description: A fixture site built out of the cases that have actually broken extraction, and what running against it proves.
---

# e2e

<p class="lede">A site built out of the cases that have actually broken
extraction, served locally, so a fault that was found once stays found.</p>

## Why it exists

Extraction is measured against [live corpora]({{ '/results/' | relative_url }}):
808 pages from 19 news sites, ten live feeds, and 1,267 pages from 30 more. That
is the right way to *find* a fault and the wrong way to *keep* it fixed. A live
site needs the network, is slow, and changes underneath the measurement, so a
regression shows up as a number moving rather than as a test failing, and only
when somebody reruns the corpus.

Every page in `e2e/site` exists because something real went wrong on it. The
comment at the top of each says what, in the words of the results page, so a
fixture cannot quietly become decoration. A test asserts those comments are
there, and another asserts the faults themselves are still present.

```
go test ./e2e/          # the fixture is correct
go run ./e2e/cmd/site   # browse it at http://localhost:8099/
```

## What it serves

41 files and 25 routes, in every content type scour claims to read.

| Surface | Where | What it is for |
| --- | --- | --- |
| News articles | `/news/` | Twelve article shapes, most of them traps |
| Long-form | `/longform/dredging-contract/1`…`/5`, `/print` | One story across six URLs |
| Live wire | `/live/`, `/live/feed.xml` | Publishes another article on a schedule |
| Products | `/products/` | schema.org as microdata and as JSON-LD |
| People | `/people/` | Profiles that share a shape with articles |
| Places | `/places/` | An address split five ways, coordinates, a map frame |
| Listings | `/listings/` | Paginated through `rel="next"` |
| Feeds | `/feeds/rss.xml` `atom.xml` `rdf.xml` `feed.json` | Four feed dialects |
| Files | `/files/` | A real PDF, a real PNG, plain text, JSON |
| React app | `/app/` | Content is a fetch away, not in the HTML |
| Product API | `/api/products`, `/api/products/{sku}` | Search, filter, sort, page |
| Site search | `/search?q=` | One path, unbounded documents |
| Streams | `/stream/events`, `/stream/ws` | SSE and a WebSocket |
| Press area | `/private/` | Basic auth, with the way in in a PDF |
| Awkward | `/odd/`, `/dynamic/` | Mislabelled types, failures, slowness, change |

## The faults it holds

Each of these is a row from the [results]({{ '/results/' | relative_url }})
table, turned into a page that reproduces it.

| Fault | Page |
| --- | --- |
| A per-page id a selector overfits to | `/news/per-page-id.html` |
| Utility classes as the only labels | `/news/utility-classes.html` |
| An attribute's name outscoring the headline | `/news/attribute-outscores.html` |
| `rel="canonical"` as the only declaring attribute | `/news/canonical-only.html` |
| Published and modified genuinely differing | `/news/published-vs-modified.html` |
| A value that never changes across pages | `/news/constant-section-1..3.html` |
| A section page with an article's shape | `/news/short-title.html` |
| A feed's channel image competing with its items | `/feeds/rss.xml` |
| Non-English pages, where English-only vectors fall back | `greek`, `russian`, `arabic`, `turkish` |
| A feed skipped by its filename | `/odd/feed-as-plain-xml` |

## The one that needs a credential

`/private/` answers 401 to everything, with a `WWW-Authenticate` challenge and a
page saying the credentials are in the media pack. The media pack is
`/files/press-credentials.pdf`, and the username and password are in its text.

That makes getting in a puzzle with an answer rather than a wall, and the answer
is only available to something that can pull text out of a PDF. Basic auth
rather than a login form because it is the one scheme a client can satisfy
without running a browser or keeping a session, so it is the case worth being
able to reproduce.

The PDF and the door cannot drift apart: `TheCredentialsInTheMediaPackOpenThePressArea`
fetches the PDF, parses it with the same reader scour uses, reads the username
and password out of the extracted text rather than out of the constants, and
uses them. The trail starts on ordinary pages, since a document nothing links to
is a document nothing finds.

## Links, in all three forms

Every content type points somewhere, because a format that cannot be followed is
a dead end a crawl stops at. Each carries an absolute URL, a root-relative one
and a document-relative one, since resolving all three is the crawler's job.

A file cannot know the port it is served on, so it writes `{{ "{{BASE}}" }}`
where it wants this server's address and the handler fills it in. Binaries are
never rewritten: editing a PDF would move every byte after the token and its
xref table would stop matching. The PDF links out through a real URI action
rather than an href.

## The test cases, and results

23 tests, one second, no network.

| Test | What it asserts | Result |
| --- | --- | --- |
| `EveryFileIsServed` | All 41 embedded files answer at their own path | pass |
| `EveryPageSaysWhyItExists` | Every HTML page carries the comment saying what it is for | pass |
| `TheKnownFaultsAreStillHere` | Each fault above is still in the page that holds it | pass |
| `MislabelledResponses` | The header is the authority, not the filename or the body | pass |
| `FailingAndSlowResponses` | 500, 404, redirect and a slow page behave | pass |
| `AChangingPageChanges` | A second fetch differs from the first | pass |
| `TheReactAppIsEmptyWithoutABrowser` | The shell holds none of the content; React is vendored | pass |
| `TheScriptedPageNeedsAScript` | Content in a script string is a different case from a fetch away | pass |
| `AbsoluteLinksAreRewritten` | The token becomes this server; the PDF is left alone | pass |
| `EveryContentTypeLinksOnward` | JSON, text, HTML and PDF each link out, in all three forms | pass |
| `ProductSearchAPI` | Query, brand, stock, sort and paging, with a next link | pass |
| `ProductDetailAPI` | One product, case-insensitive sku, a miss that says where the index is | pass |
| `SearchPageReadsItsQuery` | One path serving many documents, and `noindex` where it should | pass |
| `LongformSpansSeveralURLs` | Five parts share a headline; the print view holds all of it | pass |
| `ServerSentEvents` | Five events then done, and a link out of the stream | pass |
| `WebSocket` | Frames over an upgrade, and a plain GET gets 426 rather than a hang | pass |
| `TheLiveSectionPublishesOverTime` | An unpublished article is genuinely absent | pass |
| `TheLiveFeedGrows` | The feed gains an item per publication, addressable | pass |
| `TheScheduleActuallySchedules` | The clock publishes, not only the test | pass |
| `ThePressAreaRefusesAnonymousRequests` | 401 with a challenge, and a refusal that says where the key is | pass |
| `TheCredentialsInTheMediaPackOpenThePressArea` | The PDF's own text opens the door | pass |
| `TheMediaPackIsLinkedFromHTML` | Something links the media pack, so the trail starts somewhere | pass |
| `TheMediaPackSurvivesBeingServed` | The served PDF is byte-identical to the embedded one | pass |

These test the fixture rather than scour: they say the site still holds what it
claims to. What tests scour against it lives with the crawler, and the first of
those is in `cmd/scour`, where a feed is crawled through to its articles.

## Adding a case

Put a file under `e2e/site` and open it with a comment saying what went wrong.
If it is a fault worth keeping, add a line to `TheKnownFaultsAreStillHere` so
deleting it fails rather than quietly reducing the corpus. If it needs a header,
a status, a query or the clock, it cannot be a file: add a route instead.

<div class="pager" markdown="1">
<span markdown="1">&larr; [Measured results]({{ '/results/' | relative_url }})</span>
<span markdown="1"></span>
</div>
