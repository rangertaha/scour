# e2e

A fixture site built out of the cases that have actually broken extraction.

```
go test ./e2e/          # the fixture still holds what it claims
go run ./e2e/cmd/site   # browse it at http://localhost:8099/
```

Point the real thing at it:

```
scour item add gazette --template article
scour job add gazette -i gazette -u http://localhost:8099/
scour run gazette --depth 4
scour model train gazette
scour record ls gazette
```

## Why

Extraction is measured against live corpora, which is the right way to find a
fault and the wrong way to keep it fixed. A live site needs the network, is
slow, and changes underneath the measurement, so a regression shows up as a
number moving rather than as a test failing.

Every page here exists because something real went wrong on it, and opens with a
comment saying what. `TestEveryPageSaysWhyItExists` enforces the comment and
`TestTheKnownFaultsAreStillHere` enforces the faults, so a fixture cannot
quietly become decoration.

## Layout

```
site/           the corpus, embedded, served by a file server
  news/         article shapes, most of them traps
  feeds/        rss, atom, rdf, json feed
  products/     microdata and json-ld
  people/       profiles that share a shape with articles
  places/       an address split five ways, coordinates, a map frame
  listings/     paginated through rel=next
  files/        a real PDF with link annotations, a real PNG, text, json
  app/          a React single-page app
  api/          the JSON the app fetches
  vendor/       React and ReactDOM, so it renders with no network

e2e.go          the handler, and the embed
rewrite.go      fills {{BASE}} in with this server's address
dynamic.go      routes that turn on headers or status
api.go          the product REST API
search.go       the query-string search page
longform.go     one article across six URLs
live.go         the section that keeps publishing
stream.go       server-sent events and a websocket
auth.go         the press area, and the credentials the media pack carries
```

## Adding a case

Put a file under `site/` and open it with a comment saying what went wrong. If
it is a fault worth keeping, add a line to `TestTheKnownFaultsAreStillHere` so
deleting it fails rather than quietly reducing the corpus.

If it needs a header, a status, a query or the clock, it cannot be a file. Add a
route instead, and register it on the `pages` mux in `Handler`, unless it
streams: streaming routes go on the outer mux, because the rewriter buffers and
a buffered stream is not a stream.

## Things worth knowing

**`{{BASE}}`** is replaced with the scheme and host the request arrived on, so a
file can carry an absolute URL without knowing the port. Binaries are never
rewritten, because editing a PDF moves every byte after the token and its xref
table stops matching.

**The PDF is real.** It parses with the same reader scour uses, and it links out
through a URI action rather than an href.

**The live section changes while you crawl it.** `PublishEvery` is a variable so
a test can make time pass in milliseconds, and `PublishNow` advances it without
waiting at all. `ResetLive` puts it back.

**React is vendored** under `site/vendor`, so the app renders offline. There is
no build step: `React.createElement` rather than JSX.

**`/private/` needs a credential**, and the credential is in
`/files/press-credentials.pdf` rather than in any page. A test reads it out of
the PDF's extracted text and uses it, so the document and the door cannot drift
apart. If you change `PressPass`, regenerate the PDF or that test fails, which
is the point.
