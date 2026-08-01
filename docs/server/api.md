---
title: The HTTP API
description: A design for the API surface around the same five nouns as the command line, with full parity across HTTP, the CLI and MCP.
---

# The HTTP API

<p class="lede">This is a design for the API surface, not a description of the
one that ships today.</p>

It is the companion to
[the command surface]({{ '/cli/design.html' | relative_url }}) and uses the same
five nouns, on the principle that `scour job ls` and `GET /v1/jobs` should be the
same question asked twice. The last section maps every endpoint that exists now
onto the one proposed here. For what ships, see
[server &amp; MCP]({{ '/server/' | relative_url }}).

<figure>
<img src="{{ '/img/api.svg' | relative_url }}" alt="The resource tree. Aliases, properties, records and the model hang off an item; targets, types, the frontier and runs hang off a job; runs and nodes are top level collections. Every path is nouns.">
</figure>

## The fault

The API and the CLI disagree about what a job is, and one of them has the word
the other needs.

In the CLI a job is a saved crawl: an item, a set of targets, a policy, a
frontier, a state. In the API a job is a background operation with an id like
`crawl-7`, a `kind` of `crawl` or `train`, and a state of running, done or
failed. Those are different things wearing one name, and the API's version is
already defined elsewhere: a thing with a start, an end reason, counters and a
log is what CLI.md calls a **run**. Today's `/v1/jobs` is the run collection,
sitting in the job collection's URL.

Everything else follows from the same place the CLI's faults did. Targets are
posted to an item, so `POST /v1/items` accepts `domains`, `urls`, `depth` and
`subdomains`, and one item cannot have two target sets. `GET
/v1/items/{name}/frontier` hangs a frontier off an item when a frontier belongs
to a crawl.

Three paths end in a verb, `/crawl`, `/train` and `/label`, while every other
path ends in a noun. And the two surfaces have drifted apart in vocabulary: the
API says `label` where the CLI says verdict, and `unlabelled` where the CLI says
`none`.

The smaller inconsistencies are of a kind. Every collection invents its own
envelope, `{"items":[]}` and `{"urls":[]}` and `{"rules":[]}`, but only
`records` carries a `total`. Creating an item answers `200` while deleting one
answers `204`. `confidence` is a floor with no way to ask for a ceiling, so the
records most worth reviewing cannot be selected. An error is a bare English
string, so a client that wants to branch has to match on prose. And nothing can
be edited: properties and aliases only ever accumulate, so half the CLI has no
API behind it at all.

## The resources

The same five nouns as the CLI, plus the engine.

**item** is what you are hunting. **job** is a crawl of it. **run** is one
execution. **record** is what came out. **model** is what was learned. **node**
is an engine process.

Ownership decides nesting, and nothing is nested for any other reason:

```
/v1/items
/v1/items/{item}
/v1/items/{item}/aliases
/v1/items/{item}/aliases/{word}
/v1/items/{item}/properties
/v1/items/{item}/properties/{prop}
/v1/items/{item}/properties/{prop}/labels
/v1/items/{item}/properties/{prop}/labels/{word}
/v1/items/{item}/records
/v1/items/{item}/records/{id}
/v1/items/{item}/model
/v1/items/{item}/model/rules
/v1/items/{item}/model/runs

/v1/jobs
/v1/jobs/{job}
/v1/jobs/{job}/targets
/v1/jobs/{job}/targets/{id}
/v1/jobs/{job}/types
/v1/jobs/{job}/types/{type}
/v1/jobs/{job}/frontier
/v1/jobs/{job}/runs

/v1/runs
/v1/runs/{id}
/v1/runs/{id}/log

/v1/nodes
/v1/nodes/{node}

/v1/templates
/v1/schema/{noun}
/v1/version
```

Records and the model hang off the item because that is what owns them: two
jobs hunting one item fill one record table and train one model. Targets and
runs hang off the job because that is what owns those. The URL is the
relationship diagram, so a path that reads oddly is usually a modelling
mistake rather than a naming one.

An item's aliases and a property's labels are collections rather than fields on
their parent, because CLI.md edits them one word at a time with `--add` and
`--rm`. A word is a member, so it gets a URL.

`/v1/schema/{noun}` is where a blank annotated example lives, which is what
`scour job config` prints. It is not `/v1/jobs/config`, because that path is
indistinguishable from a job named `config`, and a reserved name inside a
collection of user-chosen names is a bug waiting for the user who picks it.

`/v1/jobs/{job}/frontier` is the one resource with no command behind it. CLI.md
has no frontier verb, because the ranked queue is something you watch during a
crawl rather than manage. Over HTTP it is worth reading, so it is here, and the
asymmetry is deliberate rather than an oversight.

`/v1/runs` is the flat view of every run in the install, which is what a
monitor wants. `/v1/jobs/{job}/runs` is the same collection filtered to one
job, and it is where a run is created. A run is never created at `/v1/runs`,
because a run with no parent is not a thing.

## One rule

    /v1/<collection>/<name>[/<sub-collection>/<name>]

Every path is nouns. No path ends in a verb. The method says what is being
done and the body says with what:

| Method | Means |
| --- | --- |
| `GET` | Read it |
| `POST` | Add one member to this collection, or several |
| `PATCH` | Change some fields of this resource |
| `PUT` | Declare a whole set, replacing what was there |
| `DELETE` | Remove it |

`POST` and `PATCH` are the two that need care, because they are the two the CLI
distinguishes. `POST /v1/jobs/{job}/targets` adds a target and leaves the rest,
which is `scour job add`. `PATCH /v1/jobs/{job}` overwrites the fields it
carries and leaves the fields it does not, which is `scour job set`. `PUT` is
only ever used on a collection, to say that these are its members and nothing
else is, which is `scour item tag --set`. The methods mean here exactly what
the verbs mean there.

Nothing takes a `PUT` on a single resource, because CLI.md is explicit that
teaching writes only what you give it: adding a label must not cost a property
the example it was taught with. That is `PATCH` semantics, and a `PUT` that
merged instead of replacing would be lying about which method it was.

Three paths sit outside `/v1` because they are not resources: `/healthz`,
the configured metrics path, and `/mcp`.

## Do you need to define a job?

The same answer as the CLI, expressed in HTTP.

A run is created against a job, so a job must exist before anything crawls. But
a client should not have to make two round trips to start its first crawl, so
`POST /v1/jobs` accepts a body with no `name` and derives one from the item:

```http
POST /v1/jobs
{"item": "vehicle", "targets": [{"domain": "example.com"}], "depth": 10}

201 Created
Location: /v1/jobs/vehicle
{"name": "vehicle", "item": "vehicle", "state": "ready", ...}
```

The name in the response is the handle for everything after. A second job for
the same item names itself:

```http
POST /v1/jobs
{"name": "uk", "item": "vehicle", "targets": [{"domain": "example.co.uk"}]}
```

An unnamed `POST` for an item that already has a derived job is a `409`, not a
second nameless job, because the second one is where a client has to decide
what it is for.

## The workflow

Define the item:

```http
POST /v1/items
{"name": "vehicle", "template": "vehicle"}
```

Give one property a real example, since the template's are generic:

```http
PATCH /v1/items/vehicle/properties/price
{"example": "$42,000"}
```

Create the job and start a run. Creating the run is what starts the crawl,
which is why there is no `/crawl`:

```http
POST /v1/jobs
{"item": "vehicle", "targets": [{"domain": "example.com", "subdomains": true}],
 "depth": 10, "max_pages": 5000}

POST /v1/jobs/vehicle/runs

202 Accepted
Location: /v1/runs/r-41
{"id": "r-41", "job": "vehicle", "kind": "crawl", "state": "running", ...}
```

Watch it, or wait for it:

```http
GET /v1/runs/r-41
GET /v1/runs/r-41/log?follow=true
```

Train, which is a run of the model rather than of the job:

```http
POST /v1/items/vehicle/model/runs
GET  /v1/items/vehicle/model/rules
```

Review the doubtful records and mark them:

```http
GET   /v1/items/vehicle/records?confidence=..0.5&verdict=none
PATCH /v1/items/vehicle/records/41
{"verdict": "invalid"}
```

Then search for what you came for:

```http
GET /v1/items/vehicle/records?q=make:Ford+%22crew+cab%22
```

## Runs and long work

A crawl takes minutes and a training takes longer. Holding a request open for
that long hands the decision about when to give up to the caller's timeout or
to a proxy in between, and leaves nothing to ask afterwards. So work that takes
minutes is a resource, not a response.

`POST` to a run collection answers `202 Accepted` with a `Location` header and
the run as the body. The `Location` is the only thing a client needs to keep:

| Path | Creates |
| --- | --- |
| `POST /v1/jobs/{job}/runs` | A crawl run |
| `POST /v1/items/{item}/model/runs` | A training run |

Both are runs and both appear in `/v1/runs`, so one poller watches everything.
CLI.md defines a run as one execution of a job, which is the crawl case; the
API widens it to one execution of long work, because training needs the same
machinery and inventing a second name for a thing with a start, an end reason,
counters and a log would be inventing a synonym.

### Run states

| State | Means |
| --- | --- |
| `running` | In flight |
| `paused` | Frozen, frontier kept |
| `done` | Finished, frontier exhausted |
| `budget` | Finished on `max_pages` or `max_time`, frontier kept |
| `stopped` | Ended by request, frontier discarded |
| `failed` | Ended by an error, which is in `error` |

Pausing and stopping act on the run, because the run is the thing that is
happening:

```http
PATCH /v1/runs/r-41
{"state": "paused"}

PATCH /v1/runs/r-41
{"state": "stopped", "force": true}
```

A job's own `state` is read-only and derived from its runs: it is the state of
the last one, or `ready` when there have been none, which is the one job state
in CLI.md's table that is not also a run state. That is why `PATCH
/v1/jobs/{job}` will not accept a state. Two ways to pause the same crawl
would be two names for one act, and the states in CLI.md's table are a
description of where a job got to rather than a setting anybody assigns.

`force` is required to stop a run with a frontier worth losing, matching the
CLI. Without it the answer is `409` and the frontier survives.

Starting a run for a job that already has one running is `409` with the id of
the run that is already going:

```json
{"error": {"code": "conflict", "message": "vehicle is already crawling",
           "run": "r-41"}}
```

## Representations

### Collections

Every collection has one envelope. There are no per-collection keys, so a
client writes one function:

```json
{
  "data": [ ... ],
  "total": 1284,
  "next": "eyJpZCI6NDF9"
}
```

`total` is the count before `limit`, so a caller knows what it is sampling.
`next` is an opaque cursor, absent on the last page. Paging is `?limit=` and
`?cursor=`, and a cursor outlives inserts, which an offset does not: a crawl
writing records while a client pages through them would make offsets skip and
repeat rows.

### Items

```json
{
  "name": "vehicle",
  "aliases": ["car", "automobile"],
  "properties": [
    {"name": "price", "example": "$42,000", "labels": ["asking price"]}
  ],
  "jobs": 2,
  "records": 1284,
  "trained": true
}
```

### Jobs

```json
{
  "name": "uk",
  "item": "vehicle",
  "targets": [{"domain": "example.co.uk", "subdomains": true}],
  "depth": 4,
  "max_pages": 5000,
  "types": [{"type": "text/html"}, {"type": "text/css", "exclude": true}],
  "state": "paused",
  "last_run": "r-40"
}
```

`state` and `last_run` are read-only. `depth`, `max_pages` and `max_time` are
writable by `PATCH`, because `scour job set` overwrites them.

`targets` and `types` are shown here for reading but are edited through their
sub-collections, because `scour job add -d` and `scour job add -t` add one
member at a time and `scour job rm -t` removes one. A field that `PATCH`
replaced wholesale would make removing one content type mean sending the other
nine back, and would lose the difference between `add` and `set` that the CLI
is careful about. An exclusion is a type with `"exclude": true`, so one
collection carries both rather than two collections that can disagree.

### Runs

```json
{
  "id": "r-41",
  "kind": "crawl",
  "job": "vehicle",
  "item": "vehicle",
  "state": "budget",
  "started": "2026-03-14T09:12:04Z",
  "finished": "2026-03-14T09:41:55Z",
  "pages": 5000,
  "records": 318,
  "error": null
}
```

`kind` is `crawl` or `train`. A crawl run carries `job` and the `item` that job
names; a training run carries `item` with `job` null, because a model is
trained from every page every job of that item cached and belongs to no single
one of them. `error` is set only when `state` is `failed`, and is the message
CLI.md's `failed` state refers to.

`pages` and `records` are counters rather than a result, so a client polling a
running crawl sees them climb. There is no `result` blob: everything a run
produced is readable through the item it produced it for, and a copy hanging
off the run would be a second place for the same rows to disagree.

### Records

```json
{
  "id": 41,
  "url": "http://www.example.com/cars/1/",
  "confidence": 0.91,
  "verdict": "valid",
  "type": "text/html",
  "job": "vehicle",
  "fields": {"make": "Ford", "model": "F-150", "price": "$42,000"}
}
```

Extracted values live under `fields` rather than at the top level, so a
property called `id`, `url` or `verdict` cannot collide with the record's own
metadata. The CSV export flattens them, because a file has one row of headers
and no room for the distinction; JSON has room, so it keeps it.

### Models

```json
{
  "item": "vehicle",
  "trained": "2026-03-14T10:02:11Z",
  "pages": 6267,
  "marked": 412,
  "properties": {"make": 0.94, "model": 0.91, "price": 0.88},
  "stale": true
}
```

`pages` is how much the model was trained from and `marked` how many of its
records carry a verdict, which is the supervision it had. `properties` is the
model's confidence per property, which is what says whether another round of
marking is worth anybody's time. `stale` is true when pages
have been cached or records marked since `trained`, so a client can tell that
retraining would use evidence the current model has never seen. The rules
themselves are a collection of their own, because there are many per property
and per site.

### Nodes

```json
{
  "name": "worker-3",
  "role": "worker",
  "state": "up",
  "queue": 1842,
  "rate": 4.7,
  "seen": "2026-03-14T10:31:02Z"
}
```

A node is `up`, `draining` or `down`. `draining` is what `scour node leave`
puts it in, finishing the pages it already holds and accepting no more, which
is why CLI.md says leaving drains first. `down` is a node whose heartbeat has
aged out rather than one that said goodbye.

`rate` is pages a second and `queue` is what this node has left to fetch, which
together are what `scour top` draws. `seen` is the last heartbeat, so a node
that has gone away is visible as a stale timestamp rather than by vanishing
from the list, which would make a partition look like a clean shutdown.

### Errors

```json
{"error": {"code": "not_found", "message": "no item named vehicle",
           "field": null}}
```

`code` is a fixed string a client can branch on. `message` is English and may
be reworded at any time. `field` names the offending input when there is one,
so a form can point at it.

| Code | Status | Means |
| --- | --- | --- |
| `invalid` | 400 | The request was malformed or a value was out of range |
| `unauthorized` | 401 | Missing or wrong token |
| `not_found` | 404 | No such item, job, run, record or node |
| `conflict` | 409 | Already running, already exists, or needs `force` |
| `unsupported` | 415 | A body this build cannot decode |
| `internal` | 500 | A fault on this side |

Unknown fields in a request body are a `400` rather than being ignored, so a
typo in a client fails loudly instead of silently doing nothing.

### Success codes

| Code | When |
| --- | --- |
| 200 | A read, or an update that returns the new state |
| 201 | A resource was created, with `Location` |
| 202 | Long work was accepted, with `Location` of the run |
| 204 | A delete, with nothing to say |

`POST /v1/items` answers `201` rather than today's `200`, so creation is
distinguishable from a read without inspecting the body.

## Filtering and streaming

Query parameters are the CLI's flags, spelled the same way, so anyone who
knows one surface can guess the other:

| CLI | HTTP |
| --- | --- |
| `--limit <n>` | `?limit=n` |
| `--confidence 0.8` | `?confidence=0.8` |
| `--confidence ..0.5` | `?confidence=..0.5` |
| `--verdict none` | `?verdict=none` |
| `-j <job>` | `?job=<job>` |
| `-t <type>` | `?type=<type>`, repeatable |
| `--exclude-type <type>` on `record ls` | `?exclude_type=<type>`, repeatable |
| `--on <domain>` | `?on=<domain>` |
| `<query>` on `record search` | `?q=<query>` |
| `--follow` | `?follow=true` |
| `-i <item>` on `job ls` | `?item=<item>` |
| `--pages` on `job rm` | `?pages=true` |
| no flag, paging | `?cursor=<cursor>` |
| no flag, ordering | `?sort=<field>`, `-` for descending |

`--exclude-type` appears twice in CLI.md and lands in two places here, which
is right rather than sloppy. On `job add` it is part of what the job is, so it
is a member of the job's type collection with `exclude` set. On `record ls` it
is a question about rows already extracted, so it is a filter. The flag reads
the same because the intent is the same; the resource differs because one is
stored and the other is asked.

`confidence` takes a floor or a band, `0.8` or `..0.5` or `0.4..0.6`, which is
the CLI's rule and exists for the same reason: export wants the good rows and
review wants the doubtful ones.

`?q=` is the search query in the CLI's syntax, bare words and `field:value`
terms. There is no separate search path, because searching records is reading
the record collection with a query, and a `/search` sub-collection would be a
second URL for one set of rows.

`?sort=` names a field, prefixed with `-` for descending, and defaults to
`-id`, which is the newest-first order `scour record ls` prints. Passing `?q=`
changes the default to relevance, because that is what asking a question means,
and passing both sorts by the field and keeps the query as a filter.

This is the one place the three surfaces differ in shape rather than spelling.
The CLI has `record ls` and `record search` as two commands and MCP has two
tools, because a command that means "show me everything" and one that means
"find me this" are worth telling apart at a prompt. HTTP has one path, because
they return the same rows from the same collection and the only difference is
whether a query and an ordering were supplied.

### Formats

The representation is chosen by `Accept`, with `?format=` as an override for
callers that cannot set headers, which is most of the ones typed by hand:

| Accept | `?format=` | Gives |
| --- | --- | --- |
| `application/json` | `json` | The default, an envelope and rows |
| `text/csv` | `csv` | The flattened export `scour record write` produces |
| `application/toml` | `toml` | A job as a config file, `scour job show --toml` |
| `application/x-ndjson` | `ndjson` | One object per line, no envelope |

`?format=` and `Accept` say the same thing, so a request that sets both and
disagrees is a `400` rather than a silent preference for one of them.

### Streaming

`?follow=true` switches the response to `application/x-ndjson` whatever was
asked for: one JSON object per line, flushed as it happens, with no envelope.
The envelope describes a page and a stream has no pages, so a stream that
carried one would be lying about `total`.

```
GET /v1/items/vehicle/records?follow=true&q=make:Ford
GET /v1/runs/r-41/log?follow=true
```

A client that wants both a snapshot and a tail asks twice, which is honest
about the race between them. Server-sent events were the alternative and buy
nothing here: there is one event type, no client-side reconnection semantics
worth having, and NDJSON is what `scour --json` already prints.

## Auth

A bearer token, checked in constant time:

```
Authorization: Bearer <token>
```

The threat model is a service on a private network or behind a reverse proxy,
so this is deliberately not a user system. Accounts and sessions would be more
surface than the problem has.

`/healthz` is exempt, so an orchestrator can check liveness without holding a
credential. It carries no data, which is what makes the exemption free. A
rejected request answers `401` with `WWW-Authenticate: Bearer realm="scour"`.

The token is read from a file rather than from the config file, because a
config is often world-readable and checked into configuration management while
a secret should be neither. An empty token file is a startup error rather than
a service with auth quietly disabled, since an empty file almost always means a
provisioning step failed.

## Versioning

`/v1` changes additively. New fields, new optional parameters and new endpoints
can appear; existing fields do not change meaning or vanish. Anything that
cannot be done additively is `/v2`, served alongside `/v1` rather than in place
of it.

Clients must ignore fields they do not recognise. The server does the opposite
and rejects request fields it does not recognise, which is the asymmetry that
lets the API grow without breaking anyone and still catch a client's typo.

## Full parity

CLI.md says there is no separate client, only an address. That holds only if
every command has an endpoint behind it and a tool behind that, so this table
is the check rather than a summary. Every row of every command table in CLI.md
appears here.

| CLI | HTTP | MCP tool |
| --- | --- | --- |
| `item add <n>` | `POST /v1/items` | `item_add` |
| `item add <n> --template <t>` | `POST /v1/items` | `item_add` |
| `item add <n> -p <p> -e <v>`, new | `POST /v1/items/{n}/properties` | `item_add` |
| `item add <n> -p <p> -e <v>`, existing | `PATCH /v1/items/{n}/properties/{p}` | `item_add` |
| `item tag <n>` | `GET /v1/items/{n}/aliases` | `item_tag` |
| `item tag <n> --add <w>` | `POST /v1/items/{n}/aliases` | `item_tag` |
| `item tag <n> --rm <w>` | `DELETE /v1/items/{n}/aliases/{w}` | `item_tag` |
| `item tag <n> --set <w>...` | `PUT /v1/items/{n}/aliases` | `item_tag` |
| `item tag <n> -p <p>` | `GET /v1/items/{n}/properties/{p}/labels` | `item_tag` |
| `item tag <n> -p <p> --add <w>` | `POST /v1/items/{n}/properties/{p}/labels` | `item_tag` |
| `item tag <n> -p <p> --rm <w>` | `DELETE /v1/items/{n}/properties/{p}/labels/{w}` | `item_tag` |
| `item tag <n> -p <p> --set <w>...` | `PUT /v1/items/{n}/properties/{p}/labels` | `item_tag` |
| `item tag <n> -p <p> --on <d>` | any of the above with `?on=<d>` | `item_tag` |
| `item ls` | `GET /v1/items` | `item_ls` |
| `item show <n>` | `GET /v1/items/{n}` | `item_show` |
| `item rm <n>` | `DELETE /v1/items/{n}` | `item_rm` |
| `item rm <n> -p <p>` | `DELETE /v1/items/{n}/properties/{p}` | `item_rm` |
| `item rm <n> -p <p> --clear <d>` | `PATCH /v1/items/{n}/properties/{p}` | `item_rm` |
| `item templates` | `GET /v1/templates` | `item_templates` |
| `job add <name> -i <item>` | `POST /v1/jobs` | `job_add` |
| `job add -f <file>` | `POST /v1/jobs`, a whole body | `job_add` |
| `job add <name> -d <domain>` | `POST /v1/jobs/{name}/targets` | `job_add` |
| `job add <name> -u <url>` | `POST /v1/jobs/{name}/targets` | `job_add` |
| `job add <name> -t <type>` | `POST /v1/jobs/{name}/types` | `job_add` |
| `job add <name> --exclude-type <t>` | `POST /v1/jobs/{name}/types` | `job_add` |
| `job set <name> --depth <n>` | `PATCH /v1/jobs/{name}` | `job_set` |
| `job set <name> --max-pages <n>` | `PATCH /v1/jobs/{name}` | `job_set` |
| `job set <name> --max-time <d>` | `PATCH /v1/jobs/{name}` | `job_set` |
| `job rm <name> -d <domain>` | `DELETE /v1/jobs/{name}/targets/{id}` | `job_rm` |
| `job rm <name> -t <type>` | `DELETE /v1/jobs/{name}/types/{type}` | `job_rm` |
| `job rm <name>` | `DELETE /v1/jobs/{name}` | `job_rm` |
| `job rm <name> --pages` | `DELETE /v1/jobs/{name}?pages=true` | `job_rm` |
| `job config` | `GET /v1/schema/job` | `job_config` |
| `job validate -f <file>` | `POST /v1/jobs?validate=true` | `job_validate` |
| `job import <name>` | `POST /v1/jobs/{name}/targets`, a list | `job_import` |
| `job export <name>` | `GET /v1/jobs/{name}/targets` | `job_export` |
| `job ls` | `GET /v1/jobs` | `job_ls` |
| `job ls -i <item>` | `GET /v1/jobs?item=<item>` | `job_ls` |
| `job show <name>` | `GET /v1/jobs/{name}` | `job_show` |
| `job show <name> --toml` | `GET /v1/jobs/{name}?format=toml` | `job_show` |
| `job start <name>`, `run` | `POST /v1/jobs/{name}/runs` | `job_start` |
| `job pause <name>` | `PATCH /v1/runs/{id}` | `job_pause` |
| `job stop <name> --force` | `PATCH /v1/runs/{id}` | `job_stop` |
| `job runs <name>` | `GET /v1/jobs/{name}/runs` | `run_ls` |
| `job log <name> --run <n>` | `GET /v1/runs/{id}/log` | `run_log` |
| `job log <name> --follow` | `GET /v1/runs/{id}/log?follow=true` | `run_log` |
| no command | `GET /v1/runs/{id}` | `run_show` |
| no command | `GET /v1/jobs/{name}/frontier` | `job_frontier` |
| `record ls <item>` | `GET /v1/items/{item}/records` | `record_ls` |
| `record ls` with any filter | the same, with query parameters | `record_ls` |
| `record search <item> <query>`, `search` | `GET /v1/items/{item}/records?q=` | `record_search` |
| `record search <item> <query> --follow` | the same, with `&follow=true` | `record_search` |
| `record show <item> <id>` | `GET /v1/items/{item}/records/{id}` | `record_show` |
| `record mark <item> <id>...` | `PATCH /v1/items/{item}/records` | `record_mark` |
| `record write <item>` | `GET /v1/items/{item}/records?format=csv` | `record_write` |
| `record rm <item> -j <job>` | `DELETE /v1/items/{item}/records?job=` | `record_rm` |
| `model train <item>` | `POST /v1/items/{item}/model/runs` | `model_train` |
| `model show <item>` | `GET /v1/items/{item}/model` | `model_show` |
| `model rules <item>` | `GET /v1/items/{item}/model/rules` | `model_rules` |
| `model rules <item> --on <d>` | `GET /v1/items/{item}/model/rules?on=<d>` | `model_rules` |
| `model rm <item>` | `DELETE /v1/items/{item}/model` | `model_rm` |
| `status` | `GET /v1/jobs` | `job_ls` |
| `top` | `GET /v1/nodes?follow=true` | `node_ls` |
| `node ls` | `GET /v1/nodes` | `node_ls` |
| `node show <node>` | `GET /v1/nodes/{node}` | `node_show` |
| `node join --role <r>` | `POST /v1/nodes` | `node_join` |
| `node leave` | `DELETE /v1/nodes/{node}` | `node_leave` |
| `version` | `GET /v1/version` | `version` |
| `server`, `mcp` | none | none |

`scour server` and `scour mcp` are the only commands with nothing behind them,
because they start the process that serves the other two surfaces. A tool for
starting the server would have to be called by the server.

Two rows run the other way, and are reads the CLI does not offer. A single run
and a job's frontier are both worth looking at over a wire and neither is worth
a command: the CLI shows a run through `job runs` and `job log`, and shows the
frontier as the ranked output of a crawl while it happens. A remote client has
no crawl in front of it, so it gets a URL instead.

Four rows collapse where the CLI splits and HTTP does not care. A domain and a
url are both targets, so both are one `POST` with a different body field. An
allowed and an excluded content type are both types, so both are one `POST`
with `exclude` set or not. Nothing is lost, because the CLI split is about
having two short flags rather than two concepts.

Two rows do work worth naming. `record mark` takes several ids at once, so it
is a `PATCH` on the collection with `{"ids": [41,42], "verdict": "invalid"}`
rather than one request per record: a reviewer marking a screenful should not
make forty round trips. And `job validate` is `POST /v1/jobs?validate=true`,
which parses and checks the body and creates nothing, so validation runs the
code path the real create runs rather than a second implementation that can
drift from it.

One row changes shape rather than collapsing. `item rm -p <prop> --clear
example` is a `PATCH` that sets the field to null, not a `DELETE`, because it
clears a detail while keeping the property. `DELETE` on the property would
remove the property, which is the row above it.

## MCP

The API and the CLI both assume someone who has read something. MCP assumes an
agent that has read the tool descriptions and nothing else, and everything
below follows from that one assumption.

`/mcp` serves these tools over HTTP so an agent can attach to a running
service, and `scour mcp` serves the same set over stdio for an agent that would
rather spawn one.

The tools are the CLI's commands, named `<noun>_<verb>`, one per command. An
agent that has seen `scour job start` can guess `job_start` and be right, and
an agent that has seen `job_start` can read CLI.md and know what it does. The
naming is mechanical on purpose: a tool set that renames things for elegance
makes every document about the other two surfaces useless to the agent.

Flags become parameters with the same names, minus the dashes. `--max-pages`
is `max_pages`, `-d` is `domain`, `--verdict` is `verdict`. A flag that repeats
becomes an array.

`job_frontier` is the one tool that is not `<noun>_<verb>`, because the CLI has
no frontier verb for it to borrow and the thing it reads is a sub-resource
rather than an action. Naming it `frontier_ls` would have made `frontier` look
like a sixth noun, which it is not.

| Group | Tools |
| --- | --- |
| item | `item_add`, `item_tag`, `item_ls`, `item_show`, `item_rm`, `item_templates` |
| job | `job_add`, `job_set`, `job_rm`, `job_ls`, `job_show`, `job_start`, `job_pause`, `job_stop`, `job_config`, `job_validate`, `job_import`, `job_export`, `job_frontier` |
| run | `run_ls`, `run_show`, `run_log` |
| record | `record_ls`, `record_search`, `record_show`, `record_mark`, `record_write`, `record_rm` |
| model | `model_train`, `model_show`, `model_rules`, `model_rm` |
| node | `node_ls`, `node_show`, `node_join`, `node_leave` |
| install | `version` |

Thirty-seven tools is a lot to put in a context window, and the honest tradeoff
is that full parity costs the agent tokens it might have spent on the task.
The alternative, a handful of coarse tools with a `verb` string parameter,
saves that space by moving the schema out of the type system and into prose,
where the agent cannot be told it got the arguments wrong until it has already
called. Parity is worth the tokens; a tool that lies about its own shape is
not.

### What differs from HTTP

Three things, each because an agent is not a program someone wrote:

**Long work does not return a handle and leave.** `job_start` and
`model_train` return the run, and take an optional `wait` with a timeout. An
agent that polls burns a turn per poll, and a turn is expensive in a way an
HTTP round trip is not. With `wait` the tool returns when the run finishes or
the timeout expires, and says which happened.

**Errors carry the fix.** The HTTP error is a code and a message for a program
that will branch on the code. The MCP error is a sentence saying what to do
instead, because the agent is the one that will act on it: not `conflict`, but
that `vehicle` is already crawling as run `r-41` and the run can be watched
with `run_log` or stopped with `job_stop`.

**Results are bounded by default.** `record_ls` without a limit returns a page
and says how many there are, rather than ten thousand rows that push the
agent's own instructions out of context. `record_write` writes a file and
returns the path, so bulk data leaves through the filesystem rather than
through the conversation.

Everything else is the same. The nouns are the same, the verbs are the same,
the vocabulary is the same, and all three surfaces share one database, so they
are the same scour whichever one is holding the handle.

## Migration

| Today | Proposed |
| --- | --- |
| `GET /healthz` | unchanged |
| `GET /v1/items` | unchanged, new envelope |
| `POST /v1/items` | `POST /v1/items`, 201, targets moved out |
| `GET /v1/items/{name}` | unchanged |
| `DELETE /v1/items/{name}` | unchanged |
| `GET /v1/items/{name}/frontier` | `GET /v1/jobs/{job}/frontier` |
| `GET /v1/items/{name}/rules` | `GET /v1/items/{name}/model/rules` |
| `GET /v1/items/{name}/records` | unchanged, new envelope |
| `POST /v1/items/{n}/records/{id}/label` | `PATCH /v1/items/{n}/records/{id}` |
| `POST /v1/items/{name}/crawl` | `POST /v1/jobs/{job}/runs` |
| `POST /v1/items/{name}/train` | `POST /v1/items/{name}/model/runs` |
| `GET /v1/jobs` | `GET /v1/runs` |
| `GET /v1/jobs/{id}` | `GET /v1/runs/{id}` |
| no equivalent | `GET /v1/jobs`, the saved crawls |
| no equivalent | editing properties, aliases, labels, targets and types |
| no equivalent | `GET /v1/runs/{id}/log` |
| no equivalent | `GET /v1/nodes` |
| no equivalent | `GET /v1/version` |
| `{"label": "unlabelled"}` | `{"verdict": "none"}` |
| `{"error": "message"}` | `{"error": {"code": ..., "message": ...}}` |
| `{"records": [], "total": n}` | `{"data": [], "total": n, "next": ...}` |

The `/v1/jobs` move is the one that cannot be done additively, because the path
keeps its name and changes its meaning. Either it goes in `/v2`, or `/v1/jobs`
keeps answering with runs until `/v2` exists and the new job collection arrives
as `/v1/crawls` in the meantime. The second is uglier and the first is honest,
so this is a `/v2` change and the rest of this document is `/v1` work that can
land before it.

### Names not taken

`/v1/items/{item}/search` is not a path. Searching records is reading the
record collection with a query on it, and a second URL returning the same rows
in a different order is the kind of duplicate the CLI design spent its effort
removing. `?q=` does it, and `?sort=` says how.

`/v1/crawls` is not the job collection. It reads like the act rather than the
thing, and the act is a run.

`POST /v1/jobs/{job}/start` is not how a crawl starts. It is a verb in a path,
and it hides the thing it makes: starting a crawl produces a run, the run is
what a client then watches, so the request that starts one should hand it back
as a created resource.

A GraphQL endpoint is not offered. The clients are a CLI, an agent over MCP and
whatever a user scripts, and all three want a handful of fixed shapes rather
than arbitrary graph traversal. The cost of a query language is a resolver for
every field combination anyone might ask for, and nothing here has asked.

<div class="pager" markdown="1">
<span markdown="1">&larr; [The command surface]({{ '/cli/design.html' | relative_url }})</span>
<span markdown="1">[Measured results]({{ '/results/' | relative_url }}) &rarr;</span>
</div>
