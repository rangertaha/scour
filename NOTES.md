# Notes

Working notes for the rewrite. Not documentation: this is where the shape gets
argued out before it is built.

## The mental model

Four stages, borrowed from Scrapy, talking over NATS instead of calling each
other, and the exporters at the end.

```
                    ┌──────────────┐
        ┌──────────►│  SCHEDULER   │◄─────────┐
        │           │  (frontier)  │          │
        │           └──────┬───────┘          │
   new requests            │ request     new requests
        │                  ▼                  │
        │           ┌──────────────┐          │
        │           │  DOWNLOADER  │          │
        │           │  ┌────────┐  │          │
        │           │  │ chain  │  │          │
        │           │  └────────┘  │          │
        │           └──────┬───────┘          │
        │                  │ response         │
        │                  ▼                  │
        │           ┌──────────────┐          │
        └───────────┤    SPIDER    ├──────────┘
                    │  ┌────────┐  │
                    │  │ chain  │  │
                    │  └────────┘  │
                    └──────┬───────┘
                           │ items
                           ▼
                    ┌──────────────┐      ┌───────────┐
                    │   PIPELINE   ├─────►│ EXPORTERS │
                    │   (a DAG)    │      └───────────┘
                    └──────────────┘
```

The scheduler owns the frontier and is the only stage a job may not replace.
Two schedulers handing out the same host cannot honour a crawl delay between
them, so politeness forces one decision point per host.

## Chains run both ways

This is the part that makes `order` mean something, and the part that is easy
to get wrong.

A middleware chain is not a list of steps executed once. It wraps the stage, so
every link sees the request on the way out **and** the response on the way
back, in opposite orders:

```
order:      100        550        900
         ┌────────┐ ┌────────┐ ┌────────┐
request  │ robots │→│ retry  │→│ cache  │→ ─┐
         │        │ │        │ │        │   │ network
response │        │←│        │←│        │← ─┘
         └────────┘ └────────┘ └────────┘
         first out              last out
         last back              first back
```

Consequences worth stating, because they are the reason the numbers are what
they are:

- **Low order is nearest the spider, high order is nearest the network.**
- `robots` at 100 refuses a request before anything else pays for it.
- `cache` at 900 is the last thing before the network, so a hit short-circuits
  the fetch only after every other request middleware has had its say. This is
  Scrapy's `HttpCacheMiddleware` placement, and the reasoning is the same.
- `charset` at 600 sits after `compression` at 590 and before `cache` at 900,
  so what lands in the cache is decompressed and UTF-8.

A link may **short-circuit**: return a response without calling the rest, which
is how a cache hit works. It may **drop**: refuse the request, which is how
`robots` and `offsite` work. Both need to be in the plugin contract from the
start, because they cannot be added to it later without changing every plugin.

## The job document

One HCL file holds one or more jobs, submitted and accepted together. A job
carries everything it needs, so nothing is inherited from whichever server
picks it up and a job resubmitted next month does what it did today.

```hcl
job "news" {
  domains  = ["example.com"]
  start    = ["https://example.com/topic"]
  included = ["*.example.com"]
  excluded = []

  # What to extract.
  item "article" {
    type        = object
    description = "A news article"

    property "url" {
      type       = str
      required   = true
      aliases    = ["uri", "link"]
      transforms = [absurl]
      regexes    = []
      xpath      = []
      css        = []
    }

    # Properties nest, but only under object: a property with children is an
    # object whatever it says, and a mismatch is refused rather than inferred.
    property "author" {
      type = object

      property "name" {
        type       = str
        transforms = [text, trim]
      }

      property "profile" {
        type = url
      }
    }

    property "title" {
      type       = str
      required   = true
      aliases    = ["headline"]
      transforms = [text, trim]
    }

    property "summary" {
      type    = str
      aliases = ["description", "excerpt", "standfirst"]
    }

    property "pubdate" {
      type       = date
      aliases    = ["published", "datePublished"]
      transforms = [datetime]
    }

    property "moddate" {
      type       = date
      aliases    = ["modified", "dateModified"]
      transforms = [datetime]
    }

    property "body" {
      type       = str
      required   = true
      aliases    = ["content", "articleBody"]
      transforms = [text, normalise_space]
    }
  }

  # How the crawl behaves, as opposed to what it is looking for.
  engine {
    limits {
      max_pages = 500
      max_depth = 4
      max_time  = "90m"
    }

    politeness {
      rate        = "2s"
      concurrency = 2
      robots      = true
    }
  }

  monitoring {
    metrics = false
    logging = false
    level   = "info"
  }

  # Middleware. Two labels: the stage, then the implementation.
  plugin "downloader" "cache" {
    order   = 900
    backend = "s3"
    bucket  = "pages"
  }

  plugin "downloader" "retry" {
    order = 550
    times = 3
  }

  plugin "spider" "depth" {
    order = 900
  }

  plugin "scheduler" "priority" {}

  # Item processing, as a dependency graph.
  pipelines "clean" "article" {
    rule {}
    rule {}
  }

  pipelines "rank" "article" {
    requires = [clean.article]
  }

  pipelines "python" "enrich" {
    requires = [clean.article, rank.article]
    script   = "./enrich.py"
  }

  pipelines "bash" "notify" {
    requires = [python.enrich]
    script   = "./notify.sh"
  }

  # Exporters are per item. The second label says which.
  exporter "json"   "article" { dir    = "./out" }
  exporter "csv"    "article" { dir    = "./out" }
  exporter "nats"   "article" { stream = "ITEMS" }
  exporter "sqlite" "article" { file   = "./items.db" }
}
```

## Decisions

**Plugins are middleware.** One word for one concept. A plugin is a link in a
stage's chain, ordered by `order`, and the stage is the first label.

**Caching is a downloader middleware.** Not an engine setting. A cache sits
between a request and the network, which is what a downloader middleware is.
Making it a setting would have made it the one part of the fetch path that
could not be reordered, replaced or turned off.

**`order` is explicit, never positional.** A chain whose order depends on where
a block was written changes when somebody tidies the file.

**Bare words are predeclared, not strings.** `type = str` and
`transforms = [datetime]` are HCL variable references resolved against a
vocabulary the parser knows. A typo becomes
`job.hcl:14,16-19: Unknown variable` with a line and a column, instead of a
string carried until something later fails to make sense of it. Quoted forms
still work, because `"str"` and `str` decode to the same value.

**`requires = [clean.article]` is a reference, not a string.** Read as a
traversal, so a dependency on a step that does not exist is caught when the job
is submitted rather than when the graph runs. Cycles too.

**A plugin's own fields stay undecoded.** Common fields are read centrally; the
rest is kept as an opaque body and handed to the plugin's schema when it is
built. That is what makes a plugin something somebody else can write, and it is
also when a bad field gets an error with a line number on it.

**Defaults are applied at submission, not at run.** A stored job records what it
will actually do, rather than inheriting whatever the server meant that day.

**Every stage chains the same way,** the scheduler included. One mechanism, one
set of rules about order, one thing to learn. A scheduler plugin is a link in a
chain exactly as a downloader plugin is.

**Exporters are per item, not per job.** A job extracting articles and comments
wants them in different files, and an exporter handed both would have to be told
which was which anyway. The item is the second label, matching how `plugin` and
`pipelines` already read.

**A pipeline step is a node in a graph, not a link in a chain.** One stage, one
spelling: `pipelines <kind> <name>` with `requires`. Giving it an `order` as
well would be two ways of saying the same thing, and they would disagree.

**Pipelines run concurrently.** The graph exists so independent work happens at
the same time. `Waves()` groups the steps into sets whose dependencies are
already satisfied and which do not depend on each other; a runner starts a wave,
waits, and starts the next. `Width()` is the widest wave, which is what a runner
sizes its pool against. `Order()` flattens the same graph for showing a plan.

**Resubmitting a job name mutates it and applies the changes.** The name is the
identity: a client resubmitting the same crawl means the same crawl. But what
"applies" means is not the same for every change, so a diff reports an effect
per change and whoever accepts the submission decides what to do about the ones
that are not free.

**Nesting requires `object`.** A property with children is an object whatever it
says, and a mismatch is refused rather than inferred, because silently changing
a declared type is how a document stops meaning what it reads as.

## Plugin catalogue

From Scrapy's built-in middleware and its ordering logic, adapted where scour
needs something Scrapy gets elsewhere. These numbers live in
`internal/engine/catalogue.go` and a test holds them to what is written here.

### Downloader

| Order | Name | What it does |
| --- | --- | --- |
| 100 | `robots` | Refuses what robots.txt forbids |
| 400 | `useragent` | Sets the User-Agent |
| 500 | `offsite` | Drops URLs outside `domains` / `included` / `excluded` |
| 520 | `contenttype` | Refuses by extension and MIME before the body is read |
| 540 | `timeout` | Per-request deadline |
| 543 | `cookies` | Session cookies, per host |
| 544 | `auth` | HTTP authentication |
| 550 | `retry` | Retries the temporarily failed |
| 560 | `headers` | Default request headers |
| 580 | `metarefresh` | Follows meta-refresh redirects |
| 590 | `compression` | gzip, deflate, br, zstd |
| **600** | **`charset`** | **Transcodes the body to UTF-8** |
| 610 | `proxy` | Routes through a proxy |
| 630 | `redirect` | Follows HTTP redirects |
| 700 | `maxsize` | Refuses bodies over the limit |
| 850 | `stats` | Counts requests, responses and failures |
| 900 | `cache` | Reads and writes the page cache |

`charset` has no Scrapy equivalent, because Scrapy decodes in its response
object rather than in a middleware. It is not optional here, and it must sit
after `compression` and before `cache`: bodies are cached transcoded, so the
corpus is UTF-8 whatever the site served. Getting this wrong does not merely
score badly, it poisons the evidence every later measurement is taken against.

### Spider

| Order | Name | What it does |
| --- | --- | --- |
| 50 | `httperror` | Drops non-2xx before anything parses them |
| 500 | `offsite` | Drops discovered links outside scope |
| 700 | `referer` | Sets Referer from the page a link was found on |
| 800 | `urllength` | Drops absurdly long URLs |
| 900 | `depth` | Tracks depth and enforces `max_depth` |

### Scheduler

No Scrapy equivalent: its scheduler is configured rather than extended. scour
chains it exactly as the downloader is chained, because ordering the frontier
is most of what a focused crawl is.

A request passes through on its way into the frontier and back out on its way
to the downloader, so low order is nearest the spider that discovered it and
high order is nearest the queue.

| Order | Name | What it does |
| --- | --- | --- |
| 100 | `dupefilter` | Decides what counts as already seen |
| 200 | `offsite` | Drops URLs outside `domains` / `included` / `excluded` |
| 300 | `cron` | Defers a URL until it is due again |
| 400 | `budget` | Refuses a URL the job can no longer pay for |
| 500 | `priority` | Best first, by score. The default |
| 500 | `breadth` | Level by level, for an archival crawl |
| 500 | `depth` | Follows a spur down before returning |
| 500 | `random` | Samples without the sample being shaped by the scorer |

`offsite` appears in three chains and that is deliberate, not duplication. The
spider's keeps an out-of-scope link out of the frontier, the scheduler's catches
entries that were in scope when they were queued and are not any more, and the
downloader's is the last check before the network. One concept, one word, three
places it has to hold.

`dupefilter` at 100 drops a URL already seen before anything else pays to think
about it. The ordering policies sit at 500, against the queue, because deciding
what comes out next is the last thing that happens on the way in and the first
on the way out. They share an order because they are alternatives: loading two
is legal and the later one wins, but it is almost certainly a mistake.

### Pipeline

Not a plugin stage. A pipeline step is a node in a graph, so it is written as
`pipelines <kind> <name>` and ordered by `requires` rather than by a number.
Writing `plugin "pipeline" ...` is refused, with a message saying what to write
instead.

| Kind | What it does |
| --- | --- |
| `clean` | Rule-driven tidying |
| `validate` | Enforces `required` and types |
| `dedupe` | Drops items already seen |
| `rank` | Scores and orders |
| `python`, `rhai`, `nodejs`, `bash` | Runs a script, inline or from a file |

### Exporters

`json`, `jsonlines`, `csv`, `parquet`, `nats`, `sqlite`.

Exporters are per item, named `exporter "<format>" "<item>"`. Every exporter
naming an item receives a copy of each of those items, so writing one item to
both json and sqlite is two blocks. An exporter naming an item the job does not
extract is refused, because silently writing nothing is the failure mode nobody
notices until they go looking for the output.

## Writing a plugin

The contract every downloader link satisfies. Sketched, not built.

```go
type Downloader interface {
    // Request runs on the way out. It may return a response to
    // short-circuit the rest of the chain, which is how a cache hit works,
    // or ErrDrop to refuse the request, which is how robots does.
    Request(ctx context.Context, req *Request) (*Response, error)

    // Response runs on the way back, in reverse order.
    Response(ctx context.Context, req *Request, resp *Response) (*Response, error)
}
```

A plugin is registered by name, built from its undecoded body:

```go
func init() {
    downloader.Register("cache", func(body hcl.Body) (Downloader, error) {
        var cfg struct {
            Backend string `hcl:"backend,optional"`
            Bucket  string `hcl:"bucket,optional"`
        }
        // gohcl reports a bad field with the line it was written on.
        return &Cache{...}, nil
    })
}
```

A plugin scour does not ship is accepted, because a plugin somebody else wrote
is the point. What it cannot do is stay silent about where it goes: a built-in
has a default `order`, and anything else has to say, so a submission naming an
unknown plugin with no order is refused.

## Across machines

Servers join to form a cluster, and jobs run across it.

- Each node embeds a NATS server. `--join` adds routes, so the embedded servers
  cluster natively and a laptop still needs nothing installed.
- Jobs live in JetStream so any node can see them. Work distributes by queue
  group, so downloaders on different nodes pull from one subject with no
  coordinator.
- **Bring your own stage.** Because stages talk over the bus rather than
  calling each other, a spider somebody else wrote in another language is a
  subscriber, not a fork. A job declares which stages it is bringing, and scour
  publishes that stage's input and waits for its output instead of running its
  own.
- Bodies never travel on the bus. The cache holds them; messages carry the key.

A job that brings its own spider:

```hcl
job "news" {
  start = ["https://example.com/"]

  item "article" {
    property "title" {
      type = str
    }
  }

  engine {
    components {
      external = ["spider"]
      timeout  = "10m"
    }
  }
}
```

The scheduler cannot appear in that list. It is extended with plugins, never
replaced, because two schedulers handing out the same host cannot honour a
crawl delay between them.

Open: **job placement.** A job naming a local cache directory can only run
where that directory is, so either placement is constrained by config or a job
with a local cache is a single-node job by definition.

## Open questions

**Does a costly change need consent, or just a warning?** A diff knows which
changes are not free. Whether submitting one is refused without a flag, applied
with a warning, or queued for confirmation is a decision about the client, not
about the engine.

**What happens to work already done when a change is applied?** Narrowing scope
leaves entries in the frontier that are now out of bounds, and changing a schema
leaves records that no longer match the shape. Dropping them and keeping them
are both defensible, and the answer probably differs per effect.

## Status

| Piece | State |
| --- | --- |
| `internal/cache` interface, registry, keying | Built, tested |
| Backends: local, s3, gcs | Built, one shared contract suite, all passing |
| HCL job document, nested blocks, multiple jobs | Built, tested |
| Vocabulary: bare types and transforms | Built, tested |
| Validation, every problem reported at once | Built, tested |
| Plugin blocks, undecoded bodies, chain ordering | Built, tested. No plugin *implementations* yet |
| Pipelines DAG: references, cycles, topological order | Built, tested |
| Pipeline waves, for running independent steps at once | Built, tested |
| Resubmission diff, with an effect per change | Built, tested |
| Plugin implementations, all of them | Not started |
| Scheduler, downloader, spider, pipeline stages | Not started |
| Exporters, all formats | Not started |
| Cluster join, distributed jobs | Not started |

`internal/engine/notes_test.go` reads this file. It parses and validates the
job document above, and compares every number in the catalogue tables with the
catalogue the code uses. If the notes and the code disagree, the tests fail,
which is the only way a document like this stays true.

## Corrections applied to the original sketch

`pipelines "bash"` had one label where every other block has two, and HCL
requires a fixed label count. `pubdata` read as `pubdate`, `publised` as
`published`. `summary` and `body` carried copy-pasted aliases belonging to
other properties. The `http://` start URL became `https://`.
