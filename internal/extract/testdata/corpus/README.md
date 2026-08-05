# The extraction corpus

Fifteen hand-written pages, and the job they are measured with. `Rates` in
`internal/extract/fillrate.go` runs the job over them and
`TestFillRatesOverTheCorpusClearTheFloor` holds the result to a floor.

Every page is here to be different from the others in a way that breaks
extraction on the real web. A corpus of fifteen copies of one template would
report a hundred per cent and prove nothing, so if a page looks odd, look here
before assuming it is a mistake: most of them are odd on purpose.

## What each page is for

Each is loaded as if it had been fetched from
`https://corpus.example/pages/<filename without .html>`, which is what the
canonical links in them say, so the `absurl` transform is measured against the
address the page claims.

| Page | What it is for |
| --- | --- |
| `01-og-jsonld.html` | The easy case, and the one most large publishers are: full Open Graph, JSON-LD `NewsArticle`, a byline with a link, a `<time datetime>`. If anything ever fails on this one, something is badly wrong. |
| `02-microdata.html` | schema.org microdata and nothing else. No Open Graph, no JSON-LD, and deliberately no canonical link, because plenty of sites have none. |
| `03-class-layout.html` | A blog with no machine-readable metadata at all. Everything has to come from class names and the shape of the document. |
| `04-masthead-headline.html` | An `<h1>` in the masthead as well as in the article. Taking the first `<h1>` in document order gets the name of the newspaper, which has bitten this project before: induction learned it and froze it into a selector. |
| `05-visible-date.html` | The date exists only as "4 August 2026" in a dateline. Nothing in the markup says it is a date, so only the regex can find it. |
| `06-windows-1251.html` | Stored as windows-1251 bytes, not UTF-8, with the encoding declared in an `http-equiv` element the way an older Russian site would. The harness decodes it through `internal/decode`, which is what the downloader and the spider both do. Read as UTF-8 it would be mojibake and would score as if extraction had failed. |
| `07-error-page.html` | A 404 with almost nothing on it. It still yields a title, because every page has a `<title>`, which is exactly why a fill rate needs the required-properties column beside it. |
| `08-malformed.html` | Unclosed `<p>`, `<div>`, `<time>` and `<html>`, unquoted attributes, a stray `</span>`. The parser is meant to recover, and this is the page that says whether it does. |
| `09-no-headline.html` | A content fragment from a CMS: no `<title>`, no `<h1>`, no metadata, just the body. A required property is genuinely absent, and the report should say so rather than invent one. |
| `10-split-body.html` | The body is three sibling `<div>`s with an advertisement between them, and there is no single element that contains the article and nothing else. |
| `11-jsonld-graph.html` | JSON-LD written as an `@graph` array, plus Twitter cards and no Open Graph, which is what one widely used SEO plugin emits. |
| `12-spa-shell.html` | A client-rendered shell: real metadata in the head, an empty `<div id="root">` where the story would be. The body is unfindable, which is the honest result. |
| `13-press-release.html` | Corporate rather than editorial: no author, a media contact linked by `mailto:`, and a date only a person would read. The `mailto:` is what the `empty` column counts, because `absurl` correctly refuses it and leaves a value that is present and blank. |
| `14-forum-thread.html` | Not an article at all. Three posts by two people, and a job aimed at articles will take something off it whatever anybody intended. A corpus of only article pages would flatter every rate. |
| `15-amp-mobile.html` | An AMP page: minimal markup, `<style amp-custom>`, an `<article>` with no useful class names. |

## What the numbers do not say

A fill rate counts a value, not a correct value. Several pages here are found
imprecisely on purpose: the body of `10-split-body.html` comes back as the whole
of `<main>`, headline included, and the author on `13-press-release.html` comes
back as the whole byline. Both count as filled. Measuring correctness needs
expected values written down per page, which is a different and larger job, and
pretending the fill rate already does it would be worse than not having it.

Fifteen pages are a floor-check. They are not a sample of the web, and no number
taken from them is a claim about it.

## Adding a page

Add one when a real site breaks extraction in a way none of these does, and say
in the table above what it is for. Do not add a page that is easy to extract
from in order to lift the numbers: the floors in the test exist to catch
extraction getting worse, and a corpus that gets easier defeats them silently.
