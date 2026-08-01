---
title: config
description: Every setting, where it is read from, and which of the four sources wins.
---

# config

<p class="lede">Package <code>config</code> loads scour's configuration. Settings
resolve in a fixed order, so a flag always wins and a packaged default never
overwrites a local one.</p>

<figure>
<img src="{{ '/img/config.svg' | relative_url }}" alt="Settings resolve in four steps: command line flags, then environment variables, then a config file, then built-in defaults. Paths differ between a per-user install and a packaged one.">
</figure>

## Precedence

1. command line flags
2. environment variables: `SCOUR_CONFIG`, `SCOUR_LISTEN`, `SCOUR_DATA_DIR`, `SCOUR_CACHE_DIR`
3. `/etc/scour/config.toml` if it exists, otherwise `~/.config/scour/config.toml`
4. built-in defaults

The fourth step is a complete configuration on its own, which is why an empty
config file behaves identically to no file at all, and why a missing file is not
an error.

The third step is also what decides whether scour thinks it is a service. If
`/etc/scour/config.toml` exists, data and cache default under `/var`; otherwise
they follow the user's XDG directories. That is a property of the file
existing rather than a flag, so a packaged install and a per-user one cannot
disagree about where they put things.

## The file

The same format in both locations. Everything below is the default, so you only
write the lines you are changing.

### `[server]`

```toml
[server]
listen     = "127.0.0.1:8080"   # HTTP API and MCP endpoint
mcp        = true               # serve MCP at /mcp as well as over stdio
metrics    = "/metrics"         # Prometheus endpoint, empty to disable
token_file = ""                 # path to a bearer token; empty means no auth
```

The token is read from a file rather than from this file, because a config is
often world-readable and checked into configuration management while a secret
should be neither. See [server &amp; MCP]({{ '/server/' | relative_url }}#auth).

### `[crawl]`

```toml
[crawl]
concurrency   = 8               # in-flight requests across all hosts
rate          = "1s"            # delay between requests to one host
timeout       = "30s"           # per-request timeout
max_size      = "10MB"          # abandon bodies larger than this
user_agent    = "scour/0.1 (+https://github.com/Rangertaha/scour)"
robots        = true            # honour robots.txt
content_types = ["html"]        # see the crawl page
depth         = 10
scheduler     = "best"          # best · breadth · depth · random · warmup
```

`rate` and `concurrency` are per host, not global, so raising `concurrency`
widens how many hosts are in flight at once rather than how hard any one of them
is hit. [crawl]({{ '/crawl/' | relative_url }}#politeness),
[schedule]({{ '/schedule/' | relative_url }}).

### `[browser]`

```toml
[browser]
enabled   = true                # allow rendering at all
policy    = "auto"              # never · auto · always
pool      = 2                   # tabs rendering at once
timeout   = "45s"               # per render, not per request
exec_path = ""                  # browser binary; empty means find one
```

[transport]({{ '/transport/' | relative_url }}).

### `[model]`

```toml
[model]
scorer     = "bayes"            # URL scoring: bayes or embed
vectors    = ""                 # word vectors, for the embed scorer
matcher    = "heuristic"        # heuristic or llm
classifier = ""                 # "" is off, or llm
ai         = ""                 # which [[ai]] block the llm parts use
budget     = 0                  # model calls per training run, 0 for the default
holdout    = 0.2                # share of pages reserved for accuracy
min_score  = 0.1                # do not follow links below this
```

[score]({{ '/score/' | relative_url }}),
[matcher]({{ '/matcher/' | relative_url }}),
[classify]({{ '/classify/' | relative_url }}),
[ai]({{ '/ai/' | relative_url }}).

### `[store]`, `[cache]`, `[paths]`

```toml
[store]
driver = "sqlite"               # the only driver today
dsn    = ""                     # empty means the default path below

[cache]
driver = "local"                # local · s3 · gcs
url    = ""                     # empty means the default pages directory
# [cache.options]               # whatever the driver needs beyond the location
# region = "us-east-1"

[paths]
data  = ""                      # empty means /var/lib/scour or the XDG data dir
cache = ""                      # empty means /var/cache/scour or the XDG cache dir
```

The sqlite driver is the pure-Go one, so scour cross-compiles and installs
without a C toolchain. `s3` and `gcs` need a `-tags cloud` build; a build
without them still knows the names and says so rather than pretending they do
not exist. [cache]({{ '/cache/' | relative_url }}),
[store]({{ '/store/' | relative_url }}).

### `[bus]`

```toml
[bus]
url       = ""                  # NATS server; empty runs an embedded one in-process
store_dir = ""                  # where the embedded broker keeps JetStream data;
                                # empty keeps streams in memory
```

[bus &amp; service]({{ '/bus/' | relative_url }}).

### `[[ai]]`, repeated

One block per model, referred to by name from `[model]`. The key itself is never
written here, only the name of the variable holding it.
[ai]({{ '/ai/' | relative_url }}).

```toml
[[ai]]
name        = "claude"
provider    = "anthropic"
model       = "claude-opus-5"
effort      = "low"
endpoint    = ""
api_key_env = "ANTHROPIC_API_KEY"
timeout     = "60s"
```

### `[[host]]`, repeated

Per-host overrides. The first block whose host matches wins, so put the specific
ones first. Use these to be politer to fragile servers than the global default,
or faster against ones you own.

```toml
[[host]]
host        = "example.com"
rate        = "5s"
concurrency = 2

[[host]]
host      = "*.internal.example.com"
rate      = "100ms"
robots    = false

[[host]]
host      = "app.example.com"
transport = "webdriver"
```

The `RATE` column in `scour run` output shows the value actually applied to
each URL, which is where an override becomes visible.

## Every implementation is a name

`scheduler`, `scorer`, `matcher`, `classifier`, `driver`, `provider`,
`transport` and `--write` all take a registry name. That is the whole of how a
scour is specialised without recompiling one, and
[extending it]({{ '/architecture/extending.html' | relative_url }}) is where the
names come from.

An empty name resolves to the registry's default rather than to an error, which
is why `Default()` does not bother setting `scheduler` or the cache driver: the
registries already know that `best` and `local` are the fallbacks, and repeating
it in a second place would be a second place to get it wrong.

A name that does not resolve reports what is registered rather than only that it
failed:

```
unknown scorer "bays", have [bayes embed]
```

## What is safe to delete

Only the config file lives in the config directory. Everything scour writes
goes to the data directory or the cache directory, which is what lets the
working data be cleared without losing the setup.

| Path | Contents | Safe to delete |
| --- | --- | --- |
| `~/.config/scour/config.toml` | The config file, and nothing else | It is your setup |
| `~/.local/share/scour/scour.db` | Items, jobs, targets, frontier, rules, records, marks | No |
| `~/.local/share/scour/models/` | Two model files per item, `.score.json` and `.extract.json` | No, though training rebuilds them |
| `~/.local/share/scour/exports/` | Written-out records | Yes, they are a copy |
| `~/.cache/scour/pages/` | Fetched page bodies | Yes, at the cost of re-crawling |

The data directory is `$XDG_DATA_HOME/scour` when that is set, and the cache
directory follows the platform's own convention, so neither is guaranteed to be
the path above. `scour --json status` reports what they resolved to.

Under a packaged install, with `/etc/scour/config.toml` present, the data
directory is `/var/lib/scour` and the cache is `/var/cache/scour`, and the four
rows move with them. Only the cache is safe to delete, and the marks on records
are the one thing in the data directory that could not be rebuilt by crawling
again.

<div class="pager" markdown="1">
<span markdown="1">&larr; [command line]({{ '/cli/' | relative_url }})</span>
<span markdown="1">[The command surface]({{ '/cli/design.html' | relative_url }}) &rarr;</span>
</div>
