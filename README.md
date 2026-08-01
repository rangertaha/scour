# scour

[![License: GPL v3](https://img.shields.io/badge/license-GPL--3.0-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/go-1.22%2B-00ADD8.svg)](https://go.dev)
[![Status](https://img.shields.io/badge/status-early%20development-orange.svg)](#status)

A focused web crawler that scores links by how likely they are to describe the
thing you are looking for.

You tell scour what you care about: an item, its aliases, and the properties
it should have. scour then crawls outward from your seed targets, assigning
every discovered URL a probability that it holds a match. Instead of scraping
whole sites and filtering afterwards, you get a ranked frontier and spend your
crawl budget on the pages most likely to pay off.

## Status

Early development. Expect commands and flags to change. The module is not
published, so `go install` will not work until the first release; clone and
`go build ./cmd/scour` in the meantime.

Crawling, extraction, training, export, the HTTP API and MCP all work, and are
measured below against live sites rather than fixtures. Running the components
across several machines works and has been tested end to end against a real
NATS server and a real S3 endpoint. What is least settled is the extraction
model itself: it is being changed by measurement, and the open questions are
kept in `ALGO.md` and `PLAN.md`.

## Measured

Extraction is judged on live corpora rather than on fixtures, and re-measured
after every change to inference. There are three: 808 pages from 19 news sites
in English, Greek, Russian and French; ten live RSS and Atom feeds; and a
second, larger HTML corpus of 1,267 pages from 30 different sites, kept
deliberately separate so it can answer a question the first cannot.

Per field, how many of the extracted records carry a value, and how many
distinct values those are. Distinctness is the number that matters: a field
whose value is the same on every page of a site is describing the site, not the
article.

### 808 HTML pages, 19 sites

| Field | Before | After | What it had found |
| --- | --- | --- | --- |
| records | 503 | **713** | |
| title | 243 / 10 | **644 / 627** | the site's name, one per site |
| link | 77 / 4 | **713 / 700** | preconnect hints naming CDNs |
| author | 0 | **301 / 52** | nothing at all |
| published | 224 / 74 | **249 / 204** | |
| summary | 470 / 470 | 473 / 470 | og:description, already correct |
| section | 1 / 1 | **166 / 19** | a related-articles heading |

### Ten live feeds

| | Records |
| --- | --- |
| Before | 9 |
| After | **266** |

### 1,267 pages, 30 sites the model has never seen

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
  really are that short. *Open*, and the reason `internal/classify` exists.

### What the corpora exposed

Every one of these was found by running against live data and measuring, not by
reading the code:

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

The last of those is worth stating on its own, because no amount of reading the
markup finds it. `section` resolved to
`<p class="kicker">Other items that may interest you</p>`, and `kicker` is a
real name for a section line, so the label was correct. What marked it was that
211 records shared one value. A field describes its record and so changes from
one to the next; a value that never changes is describing the site.

## Installation

```
go install github.com/Rangertaha/scour@latest
```

## Quick start

Describe the item you are hunting for, with the other words a page might use
for it. The name is the handle for everything that follows:

```
scour item add vehicle --alias 'car' --alias 'automobile' --alias 'pickup truck'
```

Add seed targets to crawl. A domain makes the whole site a target; a URL starts
from one page. Domains are normalised, so `example.com`, `www.example.com` and
`https://example.com/` are one target; pass `--subdomains` to follow
`shop.example.com` as well. An item can have as many targets as you like,
across as many sites as you like:

```
scour item add vehicle -d 'example.com' --subdomains
scour item add vehicle -u 'http://www.example.com/cars/'
scour item add vehicle -u 'http://www.example.co.uk/others/'

scour import vehicle --urls urls.txt
scour import vehicle --domains domains.txt
scour import vehicle --props props.csv
```

Describe the properties that item should have, with an example value for each:

```
scour item add vehicle -p make  -e Ford
scour item add vehicle -p model -e 'F-Series'
scour item add vehicle -p year  -e 2026
scour item add vehicle -p type  -e 'Full-Size Pickup'
```

Or start from a schema scour ships with, which fills in the properties, the
other words a page might label them with, and an example of each:

```
scour item templates

TEMPLATE  PROPS  FIELDS
--------  -----  ------------------------------------------------------------
article       5  headline, author, published, section, summary
job           6  title, company, location, salary, employment, posted
product       7  title, brand, price, currency, sku, availability, rating
vehicle       9  make, model, year, price, mileage, vin, body, fuel, trans...

scour item add vehicle --template vehicle
```

A template is a starting point, not an answer. Its example values are generic,
and the examples are what bootstrap the first round of labels, so add one real
value from the site you are actually crawling:

```
scour item add vehicle -p price -e '$42,000'
```

Declaring properties a site does not publish is fine. They simply go unfilled,
and the fields that are present are still found.

Teach a property the other words a page might label it with. `scour item add -a` only
ever adds one; `scour item tag` shows the set and edits it:

```
scour item tag vehicle -p make
scour item tag vehicle -p make --append manufacturer --append 'built by'
scour item tag vehicle -p make --delete brand
scour item tag vehicle -p make --update make --update marque
```

Each flag carries one word and repeats, because a word is often a phrase:
`'pickup truck'`, `'model year'`, `'asking price'`. Splitting one argument on
spaces would eventually cut one of those in half.

Scope it to a site with `--on`, so what one publisher calls a byline does not
overwrite what the next one calls it:

```
scour item tag news -p author --on example.com --append 'staff writer'
```

Then crawl, following links up to a given depth. Discovered URLs come back
ranked by probability. On the first run there is no trained model yet, so scour
scores links from the aliases and property examples alone; every later crawl
uses the model you trained from the run before:

```
scour start vehicle --depth 10

PROBABILITY  MATCHES  SPEED   LATENCY  RATE  200  300  400  500  URL
-----------  -------  ------  -------  ----  ---  ---  ---  ---  ---------------------------------------
       0.98       90  0.85/s    180ms    1s  98%   1%   1%   0%  http://www.example.com/cars/one/
       0.71       30  0.81/s    240ms    1s  95%   3%   2%   0%  http://www.example.com/cars/one/two/
       0.44       12  0.76/s    310ms    1s  91%   2%   6%   1%  http://www.example.com/cars/one/two/three/
       0.19        2  0.55/s    820ms    1s  74%   1%  22%   3%  http://www.example.com/cars/one/two/three/four/
```

The `200` to `500` columns are the share of responses in that subtree by status
class, so a URL sinking into `400` is mostly dead links and worth pruning.

### Limiting content types

scour follows HTML only by default. Widen or narrow that with `--type`, which
takes a MIME type, a wildcard, or one of the shorthands below:

```
scour start vehicle --depth 10 --type html --type pdf
scour start vehicle --depth 10 --type 'text/*' --exclude-type 'text/css'
```

| Shorthand | Expands to |
| --- | --- |
| `html` | `text/html`, `application/xhtml+xml` |
| `pdf` | `application/pdf` |
| `json` | `application/json`, `application/ld+json` |
| `xml` | `application/xml`, `text/xml` |
| `text` | `text/plain`, `text/markdown`, `text/csv` |
| `image` | `image/*` |

Filtering happens twice, so unwanted content costs as little as possible. Before
a request, scour skips links whose extension clearly disagrees with the allowed
types. After the response headers arrive, it checks the real `Content-Type` and
abandons the body if it does not match, without downloading it.

Types that scour can extract text from (HTML, PDF, plain text, JSON, XML) are
scored and mined for properties like any other page. Types it cannot read, such
as images, are recorded in the frontier with their status and size but never
parsed, so allowing them costs bandwidth without adding matches.

To make the choice permanent for an item rather than passing it every crawl,
set it on the item itself:

```
scour item add vehicle --type html --type pdf
```

Three places can set this, and the narrowest wins: a `--type` on `crawl` beats
the item's own setting, which beats `content_types` in `config.toml`.

### Pages that need a browser

Some sites send an empty shell and build the page in JavaScript. Plain HTTP sees
no content and no links, so a crawl stops at the front door. scour handles this
by fetching the page again in a real browser and carrying on with the rendered
DOM:

```
scour start vehicle --browser auto
```

| `--browser` | What happens |
| --- | --- |
| `never` | Plain HTTP only |
| `auto` | HTTP first, browser only when the response looks unrendered (default) |
| `always` | Skip the HTTP attempt, render everything |

`auto` is the default because most of the web needs no browser and rendering
every page would be slow and expensive. A page is only re-fetched when it is
HTML, carries scripts, and yet has no links and almost no text. Any one of
those alone is normal, so all three must hold together.

Once a host proves it needs a browser, scour remembers and stops paying for the
HTTP attempt it is going to discard. If the browser cannot start, the crawl
keeps the plain response and continues rather than failing.

Rendering happens at the transport layer, so nothing downstream can tell the
difference: rendered pages are cached, scored, trained on, and searched exactly
like any other. The tab pool is deliberately small, since a browser tab costs
far more than a socket:

```toml
[browser]
enabled = true
pool    = 2
timeout = "45s"
```

Requires Chrome or Chromium on the machine. Without one, `auto` degrades to
plain HTTP.

Train on the pages that crawl downloaded to the cache. Until you have labelled
anything, scour bootstraps its labels from your property examples: a page whose
text contains them is a positive, one that was crawled and matched nothing is a
negative. Accuracy is measured on a held-out fifth of those pages:

```
scour train vehicle

pages       412 cached
examples    138 positive / 274 negative  (bootstrapped from property examples)
accuracy    0.91  (held out)

model written to ~/.config/scour/models/vehicle.json
```

Training also produces the extraction rules. List them to see what scour
learned. Rules nest: the parent locates each record on the page, and its
children pull one property out of that record. `HIT` is the share of matching
pages where the rule fires:

```
scour rules vehicle

ID  PID   HIT  PROP   XPATH                       SELECTOR          REGEX        URL
--  ---  ----  -----  --------------------------  ----------------  -----------  --------------------------
 1        .98         //div[@class='vehicle']     .vehicle                       http://www.example.com/...
 2    1   .98  make   .//dd[@class='make']        .vehicle .make    ^[A-Z][a-z]  http://www.example.com/...
 3    1   .95  model  .//dd[@class='model']       .vehicle .model                http://www.example.com/...
 4    1   .91  year   .//dd[@class='year']        .vehicle .year    \d{4}        http://www.example.com/...
 5    1   .72  type   .//dd[@class='body-type']   .vehicle .type                 http://www.example.com/...
```

Search what has been extracted, one row per match and one column per property
you defined. `FORMAT` is the content type the record came from. Confidence is on
the same 0 to 1 scale as everything else, and IDs are stable record IDs, so they
stay valid across queries and reruns:

```
scour stream vehicle --confidence 0.5 --limit 20

  ID  CONF  FORMAT  MAKE       MODEL      YEAR  TYPE
----  ----  ------  ---------  ---------  ----  ----------------
1042   .99  html    Ford       F-Series   2026  Full-Size Pickup
1043   .97  html    Chevrolet  Silverado  2025  Full-Size Pickup
1088   .54  pdf     no way                2000  On my way home..

showing 3 of 100 matches
```

Narrow a search to the formats a record was extracted from with the same
`--type` shorthands the crawler uses. This is how you check whether one source
is dragging your results down, without re-crawling:

```
scour stream vehicle --type pdf
scour stream vehicle --type html --confidence 0.9
scour stream vehicle --exclude-type pdf --limit 50
```

`--format` is accepted as an alias for `--type` here, since a `TYPE` column may
already belong to one of your own properties, as it does above.

Record 1088 is a false positive: scour read prose off the page as if it were a
spec table. A record marked wrong is held out of the next training run, so both
the scoring model and the extraction rules stop making that mistake, and one
marked right is what tells `scour train` to fit the field-order chain at all.

Labelling has no command of its own. It is done over the HTTP API or MCP, which
accept `valid`, `invalid` and `unlabelled`:

```
curl -X POST http://localhost:8080/v1/items/vehicle/records/1088/label \
  -H 'Content-Type: application/json' -d '{"label":"invalid"}'

scour train vehicle
```

A record keeps its id and its label across retraining, so an id read off one
listing still names the same record on the next.

Check on a crawl in progress, or on where one left off. Crawls resume from the
stored frontier:

```
scour item ls vehicle

targets     3
frontier    1204 queued / 8871 visited
formats     html 8402, pdf 401, json 68  (image 1240 skipped)
matches     100  (97 valid, 1 invalid, 2 unlabelled)
model       trained 2026-07-30, accuracy 0.91
```

List the items you have defined, and how many matches each has found:

```
scour item ls

NAME     MATCHES
-------  -------
vehicle      100
```

Remove what you no longer want. Every `add` has a matching `remove`:

```
scour item rm vehicle -d 'example.com'
scour item rm vehicle -p year
scour item rm vehicle --rule 5
scour item rm vehicle
```

## Commands

Every command that prints a table also accepts `--json` for machine-readable
output, and `--limit <n>` to cap the rows returned.

| Command | Description |
| --- | --- |
| `scour item add <name> --alias <word>` | Define an item, or add another alias to it |
| `scour item add <name> -d <domain>` | Add a whole domain as a crawl target |
| `scour item add <name> -u <url>` | Add a single URL as a crawl target |
| `scour item add <name> -p <prop> -e <example>` | Add a property with an example value |
| `scour item add <name> --type <type>` | Restrict this item's crawls to a content type |
| `scour item tag <name> -p <prop>` | List the words a property might be labelled with |
| `scour item tag <name> -p <prop> -a <word>` | Add a word (repeatable) |
| `scour item tag <name> -p <prop> -d <word>` | Remove a word (repeatable) |
| `scour item tag <name> -p <prop> -u <word>` | Replace the whole set (repeatable) |
| `scour import <name> --urls <file>` | Load URLs from a file, one per line |
| `scour import <name> --domains <file>` | Load domains from a file, one per line |
| `scour import <name> --props <file>` | Load properties and examples from a CSV |
| `scour start <name> --depth <n>` | Crawl, and rank discovered URLs by probability |
| `scour start <name> --type <type>` | Limit this crawl to a content type |
| `scour start <name> --exclude-type <type>` | Skip a content type in this crawl |
| `scour start <name> --max-pages <n>` | Stop after this many pages, keeping the frontier |
| `scour start <name> --max-time <d>` | Stop after this long, keeping the frontier |
| `scour train <name>` | Train the model and extraction rules on the cached pages |
| `scour rules <name>` | List the extraction rules learned for an item |
| `scour stream <name> --confidence <p>` | Search extracted records at or above a confidence |
| `scour stream <name> --type <type>` | Search only records extracted from a content type |
| `scour stream <name> --exclude-type <type>` | Search everything except a content type |
| `scour stream <name> --follow` | Keep printing records as they are extracted |
| `scour stream <name> --write csv --to <dir>` | Write records out as CSV, JSON, or to a webhook |
| `scour status` | A line per item: what it has, how far it got, whether it is trained |
| `scour status <name>` | Everything known about one item |
| `scour top` | Monitor engine activity, live |
| `scour pause <name>` | Pause a search, keeping its frontier |
| `scour stop <name> --force` | Stop a search, discarding its frontier |
| `scour item ls` | A line per item: what it has, how far it got, whether it is trained |
| `scour item ls <name>` | Everything known about one item |
| `scour export <name>` | Write an item's domains and urls back out to files |
| `scour item rm <name> [-d/-u/-p/--rule]` | Remove an item, or one of its targets, properties or rules |
| `scour item templates` | List the built-in schemas `--template` accepts |
| `scour mcp` | Run as an MCP server over stdio |
| `scour server --listen <addr>` | Run as a service, serving the HTTP API and MCP |
| `scour join --role <role>` | Join a cluster for distributed workload |

`--depth` has no short form, because `-d` already means domain. On `scour item
tag`, `-d` means `--delete` and a domain is given with `--on`.

### start, pause and stop

`start` runs a search, resuming a paused one. `pause` freezes it and keeps the
frontier, so starting again carries on from where it got to. `stop` throws the
frontier away, so starting again begins from the seeds, which is why it asks for
`--force` when there is anything to lose.

The definition and the cached page bodies survive all three. What `stop`
discards is the work of deciding what to fetch next, which on a large site is
hours of it.

### Getting the records out

The records are the product, so they belong wherever the rest of your pipeline
reads. `scour stream` prints them, follows them as they are extracted, or writes
them out:

```
scour stream vehicle
scour stream vehicle --follow
scour --json stream vehicle --follow | jq .
```

Written out, records are grouped by the domain they came from, one file per
site, so an export is diffable and a site that changed is a changed file:

```
scour stream vehicle --write csv
scour stream vehicle --write json --to ./out
scour stream vehicle --write csv --confidence 0.8
scour stream vehicle --write webhook --to https://example.com/ingest
```

```
exports/vehicle/www.example.com/2026-03-14.csv

id,url,confidence,format,label,make,model,price,year
1,http://www.example.com/cars/1/,0.9100,html,valid,Ford,F-150,"$42,000",2026
2,http://www.example.com/cars/2/,0.7200,html,unlabelled,Ram,1500,"$39,500",2026
```

The columns are the union of every record's fields, not the first record's, so
a field that only some pages carry still gets a column. `label` travels with the
record, because an export is also how records get corrected outside scour.

Re-running on the same day overwrites rather than accumulating. The webhook
posts in batches and reports what it delivered before any failure, so a retry
does not double-deliver.

`scour export` is a different job, and the other half of `import`: it writes the
domains and urls an item was built from, so a list assembled over a long crawl
can be moved between databases or kept under version control.

```
scour export vehicle --domains domains.txt --urls urls.txt
scour import other --domains domains.txt --urls urls.txt
```

A domain that covers its subdomains is written `*.example.com`, which is how
import reads it back, so a round trip does not quietly narrow the target.

### Budgets

Both budgets end a crawl the way an exhausted frontier does: everything fetched
is kept, everything still queued stays queued, and the next run resumes.

```
scour start vehicle --max-pages 500
scour start vehicle --max-time 30m
```

A crawl that stopped on a budget says so, because that means there is more to
fetch rather than nothing left.

## User Configuration

Created in your OS user config directory the first time you run `scour`:

* `~/.config/scour/config.toml`: crawl defaults, including concurrency, rate limits, user agent, allowed content types, scoring algorithm, and directory paths
* `~/.config/scour/scour.db`: the single store for items, properties, targets, the frontier, rules, matches and labels
* `~/.config/scour/models/<name>.json`: one scoring model per item, holding the feature weights used to rank URLs

Working data lives outside the config directory, so it can be cleared without
losing your setup:

* `~/.cache/scour/pages/<domain>/`: fetched page bodies, so a re-crawl doesn't re-download
* `~/.local/share/scour/exports/<name>/<domain>/<date>.csv`: extracted records, with a `label` column holding `valid`, `invalid` or `unlabelled`


## Server Configuration

Run scour as a service when you want crawls to continue without a terminal
attached, a database shared by several users, or an HTTP and MCP endpoint other
machines can reach:

```
scour server --listen 127.0.0.1:8080
```

Reads answer immediately. Crawling and training return a job id instead of
blocking, because they run for minutes:

```
curl -X POST localhost:8080/v1/items/vehicle/crawl -d '{"max_pages":200}'
{"id":"crawl-1","kind":"crawl","item":"vehicle","state":"running", ...}

curl localhost:8080/v1/jobs/crawl-1
{"id":"crawl-1","state":"done","result":{"Fetched":200, ...}}
```

| Method | Path | Does |
| --- | --- | --- |
| `GET` | `/healthz` | Liveness. The one route that needs no token |
| `GET` | `/v1/items` | List items and their record counts |
| `POST` | `/v1/items` | Create an item, or add to one |
| `GET` `DELETE` | `/v1/items/{name}` | Fetch or remove one |
| `GET` | `/v1/items/{name}/frontier` | The ranked URLs |
| `GET` | `/v1/items/{name}/rules` | The learned extraction rules |
| `GET` | `/v1/items/{name}/records` | Search extracted records |
| `POST` | `/v1/items/{name}/records/{id}/label` | Mark a record valid or invalid |
| `POST` | `/v1/items/{name}/crawl` | Start a crawl, returns a job |
| `POST` | `/v1/items/{name}/train` | Start training, returns a job |
| `GET` | `/v1/jobs` `/v1/jobs/{id}` | Watch jobs |
| `POST` | `/mcp` | MCP over HTTP |
| `GET` | `/metrics` | Prometheus metrics |

One item cannot be crawled twice at once, since two crawls would race on the
frontier and double the load on the site. A second request returns `409` with
the id of the run already in progress.

Set `token_file` to require a bearer token on everything except `/healthz`:

```
head -c 32 /dev/urandom | base64 > /etc/scour/token
curl -H "Authorization: Bearer $(cat /etc/scour/token)" localhost:8080/v1/items
```

Installed as a service, scour runs as the unprivileged `scour` user and follows
the filesystem hierarchy standard rather than the per-user paths above:

| Path | Contents |
| --- | --- |
| `/etc/scour/config.toml` | crawl defaults, listen address, and per-host overrides |
| `/var/lib/scour/scour.db` | the single store for items, properties, targets, the frontier, rules, matches and labels |
| `/var/lib/scour/models/<name>.json` | one scoring model per item, holding the feature weights used to rank URLs |
| `/var/cache/scour/pages/<domain>/` | fetched page bodies, so a re-crawl doesn't re-download |
| `/var/lib/scour/exports/<name>/<domain>/<date>.csv` | extracted records, with a `label` column holding `valid`, `invalid` or `unlabelled` |

Only `/var/cache/scour` is safe to delete; everything under `/var/lib/scour` is
state you would have to re-crawl to rebuild.

Settings resolve in this order, so a flag always wins and a packaged default
never overwrites a local one:

1. command line flags
2. environment variables (`SCOUR_CONFIG`, `SCOUR_LISTEN`, `SCOUR_DATA_DIR`, `SCOUR_CACHE_DIR`)
3. `/etc/scour/config.toml` if it exists, otherwise `~/.config/scour/config.toml`
4. built-in defaults

The two config files are the same format; only their paths and defaults for the
directory settings differ.

### config.toml

The same format in both locations. Everything shown here is the default, so an
empty file behaves identically:

```toml
[server]
listen     = "127.0.0.1:8080"        # HTTP API and MCP endpoint
mcp        = true                    # serve MCP at /mcp as well as over stdio
metrics    = "/metrics"              # Prometheus endpoint, empty to disable
token_file = ""                      # path to a bearer token; empty means no auth

[crawl]
concurrency    = 8                   # in-flight requests across all hosts
rate           = "1s"                # delay between requests to one host
timeout        = "30s"               # per-request timeout
max_size       = "10MB"              # abandon bodies larger than this
user_agent     = "scour/0.1 (+https://github.com/Rangertaha/scour)"
robots         = true                # honour robots.txt
content_types  = ["html"]            # see "Limiting content types" above
depth          = 10

[browser]
enabled   = true                     # allow rendering at all
policy    = "auto"                   # never, auto or always
pool      = 2                        # tabs rendering at once
timeout   = "45s"                    # per render, not per request
exec_path = ""                       # browser binary; empty means find one

[bus]
url       = ""                       # NATS server; empty runs an embedded one in-process
store_dir = ""                       # where the embedded broker keeps JetStream data;
                                     # empty keeps streams in memory

[cache]
driver = "local"                     # where fetched bodies go: local, s3, gcs
url    = ""                          # empty means the default pages directory
# [cache.options]                    # whatever the driver needs beyond the location
# region = "us-east-1"

[model]
scorer    = "bayes"                  # URL scoring: bayes or embed
vectors   = ""                       # word vectors, for the embed scorer
matcher   = "heuristic"              # how candidate values are matched: heuristic or llm
classifier = ""                      # read pages to label them: "" (off) or llm
ai        = ""                       # which [[ai]] block the llm matcher uses
budget    = 0                        # model calls per training run, 0 for the default
holdout   = 0.2                      # share of pages reserved for accuracy
min_score = 0.1                      # do not follow links below this

# One block per model. Referenced by name from [model].
[[ai]]
name     = "local"
provider = "ollama"
model    = "gemma3:270m"
endpoint = "http://localhost:11434"

[[ai]]
name        = "claude"
provider    = "anthropic"
model       = "claude-opus-5"
effort      = "low"
api_key_env = "ANTHROPIC_API_KEY"    # the key itself is never written to config

# Per-host overrides. The first block whose host matches wins, so put the
# specific ones first. Use these to be politer to fragile servers than the
# global default, or faster against ones you own.
[[host]]
host        = "example.com"
rate        = "5s"
concurrency = 2

[[host]]
host        = "*.internal.example.com"
rate        = "100ms"
robots      = false

# A host known to need a browser can be pinned here, skipping the wasted HTTP
# attempt. scour also learns this on its own and remembers it in the database,
# so setting it by hand is only worth it to skip the first discovery.
[[host]]
host      = "app.example.com"
transport = "webdriver"
```

`rate` and `concurrency` are per host, not global, so raising `crawl.concurrency`
widens how many hosts are in flight at once rather than how hard any one of them
is hit. The `RATE` column in `scour start` output shows the value actually
applied to each URL, which is where a `[[host]]` override becomes visible.

## Running across machines

A single `scour start` needs nothing installed: the components talk over a
broker, and with no broker configured one runs embedded in the process. The same
code spread over several machines is the same components pointed at a real one.

```
scour join --role store --bus-url nats://broker:4222
scour join --role crawl --bus-url nats://broker:4222     # as many as you like
```

The store owns the database and the frontier; crawlers own the network and
nothing else. A crawler holds no state about an item: what is in scope, what
has been visited and what is worth fetching next are all decided by the store,
so crawlers are interchangeable and losing one costs only the lease on whatever
it was holding.

The frontier stays in the store rather than becoming a stream, because the order
is the product. It pops highest score first and a broker delivers in publish
order, so the component that can sort the queue hands out the next few and keeps
the rest.

Three things follow from that, and each was a bug before it was a feature:

**Politeness is enforced where work is handed out.** A rate limit inside a
crawler bounds only what that crawler does; a site sees the sum of all of them.
Measured against a live site with one crawler, dispatching without pacing asked
for 5.6 pages a second where the configuration said one. The dispatcher paces
per host, using the host's own recorded rate when it has one, so the site sees
the configured rate however many crawlers there are.

**A crawler dying costs a retry, not a page.** Work is leased rather than
removed from the frontier. Killing a crawler mid-crawl was tested: fetching
continued on the survivor and nothing was fetched twice.

**Bodies have to be somewhere shared**, which is what the page store below is
for. Without it the trainer reads an empty cache with a database full of keys.

Tested end to end against a real NATS server and a real S3 endpoint: two
crawlers and a store, 48 pages fetched, 48 objects in the bucket, nothing on
local disk, then training in a fourth process reading every one of them.

## Instrumentation

Everything the pipeline measures is published on `scour.<item>.metric`, so a
crawl can be watched while it happens rather than summarised when it ends.

| Metric | Unit | Labels |
| --- | --- | --- |
| `fetch.latency` | ms | host, status |
| `fetch.bytes` | bytes | host, status |
| `fetch.status` | count | host, status |
| `queue.depth` | count | |
| `queue.in_flight` | count | |
| `extract.records` | count | item |
| `extract.rules` | count | item |

Its own stream, and not a work queue like the others: a work queue delivers a
message once and removes it, so one dashboard consuming a metric would take it
from every other. Metrics are kept for anyone who asks, the oldest are dropped
when full, and they are forgotten after fifteen minutes, so nothing watching the
pipeline can slow it down or fill a disk.

Publishing is fire and forget. It does not deduplicate, returns no error and
retries nothing, because observability must not be able to break the thing it
observes.

The pairs are what answer a question. Latency and status per host say whether a
site is straining or has started blocking. Queue depth beside in-flight says
whether crawlers are keeping up, since a queue growing while in-flight sits at
its ceiling means discovery is outrunning fetching. Rules beside records say
whether a model still understands a site: a rule count holding while records
fall is a site changing under a model that has not noticed.

## Page storage

Fetched bodies are the only part of scour's state that does not have to be
local. The database records that a page was fetched and what its key is; the
body itself is kept separately.

On one machine a directory is right. With crawlers on several it is wrong: each
writes to its own disk, and the trainer reads an empty cache with a database
full of keys pointing at nothing. Point them all at the same bucket instead:

```toml
[cache]
driver = "s3"
url    = "s3://my-bucket/pages"

[cache.options]
region = "us-east-1"
```

```toml
[cache]
driver = "gcs"
url    = "gs://my-bucket/pages"
```

Credentials are the provider's own: `AWS_*` and the shared config for S3,
application default credentials for Google. scour takes no keys in its
configuration, so a crawler needs no secrets in the file that also says what to
crawl.

The object stores are behind a build tag, because linking the AWS and Google
SDKs adds 29MB to the stripped binary and most crawls keep their pages in a
directory:

```
make build-cloud        # or: go build -tags cloud ./cmd/scour
```

A build without them still knows the names, and says the driver needs a
different build rather than pretending it does not exist.

### systemd

A hardened unit and the config it points at ship in `packaging/`:

```
install -m 0755 scour                          /usr/bin/scour
install -m 0644 packaging/systemd/scour.service /etc/systemd/system/
install -d -m 0755                              /etc/scour
install -m 0644 packaging/etc/config.toml       /etc/scour/config.toml
useradd --system --no-create-home --shell /usr/sbin/nologin scour

systemctl daemon-reload
systemctl enable --now scour
```

`StateDirectory` and `CacheDirectory` create and own `/var/lib/scour` and
`/var/cache/scour`, so there is no preinstall script and nothing to chown. The
unit stops with `SIGTERM` and waits, because scour drains running crawls on the
way out and a crawl killed outright loses whatever it had not yet written.

Crawls resume from the stored frontier, so a restart picks up where the previous
run stopped rather than starting the queue again.



## MCP server

scour runs as a [Model Context Protocol](https://modelcontextprotocol.io)
server, so an agent can drive the crawl directly: defining items, adding
targets, training the model, inspecting the cache, reading back the ranked
frontier and extracted results, and suggesting fixes.

```
scour mcp
```

That form speaks MCP over stdio, which is what a local agent launches directly.
A running service also serves MCP over HTTP at `/mcp`, so an agent can attach
without spawning a process. The default listen address is loopback and auth is
off; to reach it from another machine, bind an external address and set
`token_file`, or leave it on loopback behind a reverse proxy:

```json
{
  "mcpServers": {
    "scour": { "command": "scour", "args": ["mcp"] },
    "scour-remote": { "url": "http://127.0.0.1:8080/mcp" }
  }
}
```

Both views share one database, so an item defined over MCP is the same item
the CLI sees.

## License

GPL-3.0. See [LICENSE](LICENSE).
