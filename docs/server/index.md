---
title: server & MCP
description: The same scour over a socket, why long work is a resource rather than a response, and how an agent drives it.
---

# server &amp; MCP

<p class="lede">Package <code>server</code> exposes scour over HTTP. It is the
same scour the command line drives, over a socket: one database, one set of
models, one cache.</p>

<figure>
<img src="{{ '/img/server.svg' | relative_url }}" alt="Reads are plain GETs that answer immediately. Work that runs for minutes answers 202 with a job id, which the caller then polls. Both go through one store.">
</figure>

## Running it

```
scour server --listen 127.0.0.1:8080
```

An item defined through the API is the item the CLI sees, because both go
through the store rather than through each other. There is no separate client:
a client is an address, not a set of commands.

## Reads answer, work returns a handle

The API is deliberately small. Everything that reads is a plain `GET` that
answers immediately; everything that crawls or trains is a job.

```
curl -X POST localhost:8080/v1/items/vehicle/crawl -d '{"max_pages":200}'
{"id":"crawl-1","kind":"crawl","item":"vehicle","state":"running", ...}

curl localhost:8080/v1/jobs/crawl-1
{"id":"crawl-1","state":"done","result":{"Fetched":200, ...}}
```

An HTTP request that blocks for minutes is a request that times out somewhere in
the middle and leaves the caller unable to find out what happened. Handing back
a resource instead moves the decision about when to give up to the caller, and
leaves something to ask afterwards.

| Method | Path | Does |
| --- | --- | --- |
| `GET` | `/healthz` | Liveness. The one route that needs no token |
| `GET` | `/v1/items` | List items and their record counts |
| `POST` | `/v1/items` | Create an item, or add to one |
| `GET` `DELETE` | `/v1/items/{name}` | Fetch or remove one |
| `GET` | `/v1/items/{name}/frontier` | The ranked URLs |
| `POST` | `/v1/items/{name}/properties` | Add a property |
| `PATCH` `DELETE` | `/v1/items/{name}/properties/{prop}` | Clear a detail, or remove the property |
| `GET` `POST` `PUT` | `/v1/items/{name}/aliases` | The other words the item goes by |
| `DELETE` | `/v1/items/{name}/aliases/{word}` | Drop one of them |
| `GET` `POST` `PUT` | `/v1/items/{name}/properties/{prop}/labels` | What a page might call that field |
| `DELETE` | `/v1/items/{name}/properties/{prop}/labels/{word}` | Drop one of those |
| `GET` | `/v1/templates` | The shipped schemas |
| `GET` `DELETE` | `/v1/items/{name}/model` | What was learned, and discarding it |
| `GET` | `/v1/items/{name}/model/rules` | The learned extraction rules, per format |
| `GET` `PATCH` `DELETE` | `/v1/items/{name}/records` | Search, export, stream, mark or drop extracted records |
| `GET` | `/v1/items/{name}/records/{id}` | One record, with the page it came from |
| `POST` | `/v1/items/{name}/model/runs` | Start training, returns the run |
| `POST` | `/v1/jobs/{name}/runs` | Start a crawl of one job, returns the run |
| `GET` | `/v1/jobs/{name}/runs` | That job's history |
| `GET` | `/v1/runs` | Recent runs, of every kind |
| `GET` | `/v1/runs/{id}` | One run |
| `GET` | `/v1/runs/{id}/log` | The pages that run fetched |
| `POST` | `/mcp` | MCP over HTTP |
| `GET` | `/metrics` | Prometheus metrics |

Long work is a run, and a run is what you are handed back: the response carries
it and a `Location` header addressing it, so a caller polls `/v1/runs/{id}`
rather than holding a connection open for minutes. Crawls and trainings are both
runs, which is why there is one id space and one way to watch either.

The run outlives the process that started it. It is a row rather than a handle
in memory, so a crawl started last night can still be asked about this morning,
and a training that finished while nobody was polling is still there.

One item cannot be crawled twice at once, since two crawls would race on the
frontier and double the load on the site. A second request returns `409`, and
the run that was opened for it is removed rather than left in the history: a row
for work that never began is worse than no row.

> A redesigned surface, sharing the CLI's five nouns so that `scour job ls` and
> `GET /v1/jobs` are the same question asked twice, is worked out in
> [the HTTP API design]({{ '/server/api.html' | relative_url }}), with full
> parity across HTTP, the CLI and MCP. The table above is what ships today.

## Auth

```
head -c 32 /dev/urandom | base64 > /etc/scour/token
curl -H "Authorization: Bearer $(cat /etc/scour/token)" localhost:8080/v1/items
```

Set `token_file` to require a bearer token on everything except `/healthz`. The
threat model is a service on a private network or behind a reverse proxy, so
this is deliberately not a user system: accounts and sessions would be more
surface than the problem has.

The token is read from a file rather than from the config file, because a config
is often world-readable and checked into configuration management while a secret
should be neither.

## MCP

scour runs as a [Model Context Protocol](https://modelcontextprotocol.io)
server, so an agent can drive the crawl directly: defining items, adding
targets, training the model, inspecting the cache, reading back the ranked
frontier and extracted results.

```json
{
  "mcpServers": {
    "scour":        { "command": "scour", "args": ["mcp"] },
    "scour-remote": { "url": "http://127.0.0.1:8080/mcp" }
  }
}
```

`scour mcp` speaks MCP over stdio, which is what a local agent launches
directly. A running service also serves MCP over HTTP at `/mcp`, so an agent can
attach without spawning a process. Both views share one database, so an item
defined over MCP is the same item the CLI sees.

The default listen address is loopback and auth is off. To reach it from another
machine, bind an external address and set `token_file`, or leave it on loopback
behind a reverse proxy.

### What an agent needs that a program does not

Three differences, each because an agent is not a program somebody wrote.

**Long work should not return a handle and leave.** An agent that polls burns a
turn per poll, and a turn is expensive in a way an HTTP round trip is not.

**Errors should carry the fix.** An HTTP error is a code for a program to branch
on. An agent wants a sentence saying what to do instead.

**Results should be bounded by default.** Ten thousand rows push the agent's own
instructions out of context, so bulk data leaves through the filesystem rather
than through the conversation.

## Installed as a service

```
install -m 0755 scour                           /usr/bin/scour
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
run stopped.

<div class="pager" markdown="1">
<span markdown="1">&larr; [bus &amp; service]({{ '/bus/' | relative_url }})</span>
<span markdown="1">[command line]({{ '/cli/' | relative_url }}) &rarr;</span>
</div>
