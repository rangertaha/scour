# scour

[![License: GPL v3](https://img.shields.io/badge/license-GPL--3.0-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/go-1.22%2B-00ADD8.svg)](https://go.dev)
[![Status](https://img.shields.io/badge/status-early%20development-orange.svg)](#status)

A focused web crawler that scores links by how likely they are to describe the
thing you are looking for.

You tell scour what you care about: an entity, its aliases, and the properties
it should have. scour then crawls outward from your seed targets, assigning
every discovered URL a probability that it holds a match. Instead of scraping
whole sites and filtering afterwards, you get a ranked frontier and spend your
crawl budget on the pages most likely to pay off.

## Status

Early development. The interface below is the intended design; it is not all
implemented yet. Expect commands and flags to change. The module is not
published, so `go install` will not work until the first release; clone and
`go build ./cmd/scour` in the meantime.

## Measured

Extraction is judged on two live corpora rather than on fixtures. The HTML one
is 808 pages crawled from 19 news sites in English, Greek, Russian and French;
the feed one is ten live RSS and Atom feeds. Both are re-measured after every
change to inference.

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

Describe the entity you are hunting for, with the other words a page might use
for it. The name is the handle for everything that follows:

```
scour add vehicle --alias 'car' --alias 'automobile' --alias 'pickup truck'
```

Add seed targets to crawl. A domain makes the whole site a target; a URL starts
from one page. Domains are normalised, so `example.com`, `www.example.com` and
`https://example.com/` are one target; pass `--subdomains` to follow
`shop.example.com` as well. An entity can have as many targets as you like,
across as many sites as you like:

```
scour add vehicle -d 'example.com' --subdomains
scour add vehicle -u 'http://www.example.com/cars/'
scour add vehicle -u 'http://www.example.co.uk/others/'

scour import vehicle --urls urls.txt
scour import vehicle --domains domains.txt
scour import vehicle --props props.csv
```

Describe the properties that entity should have, with an example value for each:

```
scour add vehicle -p make  -e Ford
scour add vehicle -p model -e 'F-Series'
scour add vehicle -p year  -e 2026
scour add vehicle -p type  -e 'Full-Size Pickup'
```

Or start from a schema scour ships with, which fills in the properties, the
other words a page might label them with, and an example of each:

```
scour templates

TEMPLATE  PROPS  FIELDS
--------  -----  ------------------------------------------------------------
article       5  headline, author, published, section, summary
job           6  title, company, location, salary, employment, posted
product       7  title, brand, price, currency, sku, availability, rating
vehicle       9  make, model, year, price, mileage, vin, body, fuel, trans...

scour add vehicle --template vehicle
```

A template is a starting point, not an answer. Its example values are generic,
and the examples are what bootstrap the first round of labels, so add one real
value from the site you are actually crawling:

```
scour add vehicle -p price -e '$42,000'
```

Declaring properties a site does not publish is fine. They simply go unfilled,
and the fields that are present are still found.

Then crawl, following links up to a given depth. Discovered URLs come back
ranked by probability. On the first run there is no trained model yet, so scour
scores links from the aliases and property examples alone; every later crawl
uses the model you trained from the run before:

```
scour crawl vehicle --depth 10

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
scour crawl vehicle --depth 10 --type html --type pdf
scour crawl vehicle --depth 10 --type 'text/*' --exclude-type 'text/css'
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

To make the choice permanent for an entity rather than passing it every crawl,
set it on the entity itself:

```
scour add vehicle --type html --type pdf
```

Three places can set this, and the narrowest wins: a `--type` on `crawl` beats
the entity's own setting, which beats `content_types` in `config.toml`.

### Pages that need a browser

Some sites send an empty shell and build the page in JavaScript. Plain HTTP sees
no content and no links, so a crawl stops at the front door. scour handles this
by fetching the page again in a real browser and carrying on with the rendered
DOM:

```
scour crawl vehicle --browser auto
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
scour search vehicle --confidence 0.5 --limit 20

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
scour search vehicle --type pdf
scour search vehicle --type html --confidence 0.9
scour search vehicle --exclude-type pdf --limit 50
```

`--format` is accepted as an alias for `--type` here, since a `TYPE` column may
already belong to one of your own properties, as it does above.

Record 1088 is a false positive: scour read prose off the page as if it were a
spec table. Label what is right and what is wrong, then retrain. Every label
sharpens both the scoring model and the extraction rules, so the next crawl
makes fewer of the same mistakes:

```
scour valid vehicle 1042 1043
scour invalid vehicle 1088
scour train vehicle
```

Check on a crawl in progress, or on where one left off. Crawls resume from the
stored frontier:

```
scour status vehicle

targets     3
frontier    1204 queued / 8871 visited
formats     html 8402, pdf 401, json 68  (image 1240 skipped)
matches     100  (97 valid, 1 invalid, 2 unlabelled)
model       trained 2026-07-30, accuracy 0.91
```

List the entities you have defined, and how many matches each has found:

```
scour list

NAME     MATCHES
-------  -------
vehicle      100
```

Remove what you no longer want. Every `add` has a matching `remove`:

```
scour remove vehicle -d 'example.com'
scour remove vehicle -p year
scour remove vehicle --rule 5
scour remove vehicle
```

## Commands

Every command that prints a table also accepts `--json` for machine-readable
output, and `--limit <n>` to cap the rows returned.

| Command | Description |
| --- | --- |
| `scour add <name> --alias <word>` | Define an entity, or add another alias to it |
| `scour add <name> -d <domain>` | Add a whole domain as a crawl target |
| `scour add <name> -u <url>` | Add a single URL as a crawl target |
| `scour add <name> -p <prop> -e <example>` | Add a property with an example value |
| `scour add <name> --type <type>` | Restrict this entity's crawls to a content type |
| `scour import <name> --urls <file>` | Load URLs from a file, one per line |
| `scour import <name> --domains <file>` | Load domains from a file, one per line |
| `scour import <name> --props <file>` | Load properties and examples from a CSV |
| `scour crawl <name> --depth <n>` | Crawl, and rank discovered URLs by probability |
| `scour crawl <name> --type <type>` | Limit this crawl to a content type |
| `scour crawl <name> --exclude-type <type>` | Skip a content type in this crawl |
| `scour crawl <name> --max-pages <n>` | Stop after this many pages, keeping the frontier |
| `scour crawl <name> --max-time <d>` | Stop after this long, keeping the frontier |
| `scour train <name>` | Train the model and extraction rules on the cached pages |
| `scour rules <name>` | List the extraction rules learned for an entity |
| `scour search <name> --confidence <p>` | Search extracted records at or above a confidence |
| `scour search <name> --type <type>` | Search only records extracted from a content type |
| `scour search <name> --exclude-type <type>` | Search everything except a content type |
| `scour valid <name> <id>...` | Label records as correct |
| `scour invalid <name> <id>...` | Label records as wrong |
| `scour status <name>` | Show target, frontier, match and model state |
| `scour status` | A line per entity, for when several are being crawled |
| `scour export <name>` | Write records out as CSV, JSON, or to a webhook |
| `scour list` | List defined entities and their match counts |
| `scour remove <name> [-d/-u/-p/--rule]` | Remove an entity, or one of its targets, properties or rules |
| `scour templates` | List the built-in schemas `--template` accepts |
| `scour mcp` | Run as an MCP server over stdio |
| `scour server --listen <addr>` | Run as a service, serving the HTTP API and MCP |

`--depth` has no short form, because `-d` already means domain.

### Exporting

The records are the product, so they belong wherever the rest of your pipeline
reads. Records are grouped by the domain they came from, one file per site, so
an export is diffable and a site that changed is a changed file:

```
scour export vehicle
scour export vehicle --format json
scour export vehicle --label valid --confidence 0.8
scour export vehicle --format webhook --to https://example.com/ingest
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

### Budgets

Both budgets end a crawl the way an exhausted frontier does: everything fetched
is kept, everything still queued stays queued, and the next run resumes.

```
scour crawl vehicle --max-pages 500
scour crawl vehicle --max-time 30m
```

A crawl that stopped on a budget says so, because that means there is more to
fetch rather than nothing left.

## User Configuration

Created in your OS user config directory the first time you run `scour`:

* `~/.config/scour/config.toml`: crawl defaults, including concurrency, rate limits, user agent, allowed content types, scoring algorithm, and directory paths
* `~/.config/scour/scour.db`: the single store for entities, properties, targets, the frontier, rules, matches and labels
* `~/.config/scour/models/<name>.json`: one scoring model per entity, holding the feature weights used to rank URLs

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
curl -X POST localhost:8080/v1/entities/vehicle/crawl -d '{"max_pages":200}'
{"id":"crawl-1","kind":"crawl","entity":"vehicle","state":"running", ...}

curl localhost:8080/v1/jobs/crawl-1
{"id":"crawl-1","state":"done","result":{"Fetched":200, ...}}
```

| Method | Path | Does |
| --- | --- | --- |
| `GET` | `/healthz` | Liveness. The one route that needs no token |
| `GET` | `/v1/entities` | List entities and their record counts |
| `POST` | `/v1/entities` | Create an entity, or add to one |
| `GET` `DELETE` | `/v1/entities/{name}` | Fetch or remove one |
| `GET` | `/v1/entities/{name}/frontier` | The ranked URLs |
| `GET` | `/v1/entities/{name}/rules` | The learned extraction rules |
| `GET` | `/v1/entities/{name}/records` | Search extracted records |
| `POST` | `/v1/entities/{name}/records/{id}/label` | Mark a record valid or invalid |
| `POST` | `/v1/entities/{name}/crawl` | Start a crawl, returns a job |
| `POST` | `/v1/entities/{name}/train` | Start training, returns a job |
| `GET` | `/v1/jobs` `/v1/jobs/{id}` | Watch jobs |
| `POST` | `/mcp` | MCP over HTTP |
| `GET` | `/metrics` | Prometheus metrics |

One entity cannot be crawled twice at once, since two crawls would race on the
frontier and double the load on the site. A second request returns `409` with
the id of the run already in progress.

Set `token_file` to require a bearer token on everything except `/healthz`:

```
head -c 32 /dev/urandom | base64 > /etc/scour/token
curl -H "Authorization: Bearer $(cat /etc/scour/token)" localhost:8080/v1/entities
```

Installed as a service, scour runs as the unprivileged `scour` user and follows
the filesystem hierarchy standard rather than the per-user paths above:

| Path | Contents |
| --- | --- |
| `/etc/scour/config.toml` | crawl defaults, listen address, and per-host overrides |
| `/var/lib/scour/scour.db` | the single store for entities, properties, targets, the frontier, rules, matches and labels |
| `/var/lib/scour/models/<name>.json` | one scoring model per entity, holding the feature weights used to rank URLs |
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
is hit. The `RATE` column in `scour crawl` output shows the value actually
applied to each URL, which is where a `[[host]]` override becomes visible.

### Page storage

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
SDKs takes the binary from 64MB to 105MB and most crawls keep their pages in a
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
server, so an agent can drive the crawl directly: defining entities, adding
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

Both views share one database, so an entity defined over MCP is the same entity
the CLI sees.

## License

GPL-3.0. See [LICENSE](LICENSE).
