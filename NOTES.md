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

  # Optional. What to do when this job is resubmitted under a name that is
  # already running. Leaving it out is the cautious answer to every question.
  mutation {
    costly         = "apply"
    out_of_scope   = "drop"
    stale_records  = "reextract"
    orphaned_cache = "refuse"
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

**A job gets exactly the chain it lists.** Nothing is added that the document
did not ask for, so a chain can be read off the job without knowing a list kept
somewhere else. `enabled = false` therefore means precisely what leaving the
block out means. Both spellings exist because they are written for different
reasons: deleting a block throws away its configuration, and turning it off
keeps it, which is what you want when the setting took an afternoon to work out.

**A spider is handed the spec, not the job.** It has no business knowing where
bodies are cached, what the budget is or which exporters are attached, and
handing it the whole job would make every one of those look like something it
might depend on. The spec renders back to HCL, so a spider in another language
gets the text a person would have written, and an author can check it against
what they meant.

**The spec is fingerprinted.** Resubmitting mutates a job, so the shape can
change while the crawl runs, and a record extracted under one shape and
attributed to another is wrong in a way nothing downstream can detect. The
fingerprint changes exactly when the shape does: not when properties are
reordered and not when the document is reformatted, because a fingerprint that
moved for cosmetics would force a re-extraction nobody needed.

**Everything has a default, and they are all in one file.** A default written
next to the field it fills is impossible to review: nobody can answer "what does
an empty job do?" without reading every file. `internal/engine/defaults.go` is
the whole list, `Defaults()` prints it, and `Resolved()` returns the job with
every one filled in, which is what should be stored so that resubmitting next
month behaves the way it did today.

**A job says what to do about its own mutation.** The diff knows what a
resubmission would cost; the `mutation` block says what should happen about it,
so the answer travels with the job rather than living in a flag somebody has to
remember or a server default that differs between machines. The defaults are
cautious throughout: refuse what is expensive, drop what is out of bounds, never
delete anything.

**Nesting requires `object`.** A property with children is an object whatever it
says, and a mismatch is refused rather than inferred, because silently changing
a declared type is how a document stops meaning what it reads as.

## Plugin catalogue

**A list, not a commitment.** These are positions, not working parts. A name
here says where something would go if it existed, and nothing more. Four are
needed to crawl anything; the rest are a queue to work through when there is
something to work through them with.

What exists is what a registry says exists, and that is asked when a chain is
built, not when a document is validated. If this table decided what a job may
name, a catalogue of intentions would validate as a set of working parts and
the failure would arrive at run time on somebody else's machine.

The numbers are Scrapy's, because copying a known-good ordering is cheaper than
rediscovering it. They live in `internal/engine/catalogue.go` and a test holds
them to what is written here.

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

A link is given the next handler and returns a replacement. Whatever it does
before calling next is the way out; whatever it does after is the way back.
This is the shape `net/http` middleware has, for the reasons it has it.

```go
func timing(next chain.Handler[*Request, *Response]) chain.Handler[*Request, *Response] {
    return chain.Func(func(ctx context.Context, req *Request) (*Response, error) {
        started := time.Now()               // on the way out
        resp, err := next.Handle(ctx, req)
        log.Println(time.Since(started))    // on the way back
        return resp, err
    })
}
```

A pair of `Request` and `Response` methods was the obvious alternative and is
worse in three ways: it needs a convention for a link that wants to
short-circuit, it makes a link that needs state across both directions stash it
somewhere, and it cannot express "run this on the way back even though the way
out failed", which is what a timer and a stats counter both want.

Short-circuit and drop fall out of wrapping rather than needing anything added.
Return a result without calling next and you have a cache hit; return `ErrDrop`
and you have robots.txt refusing a URL. A drop is a sentinel rather than an
ordinary error because a crawl that obeys robots drops requests all day, and
counting those as failures would make a working crawl look broken.

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

**`politeness.robots` and `plugin "downloader" "robots"` are two mechanisms for
one thing.** A job gets exactly the chain it lists, so robots.txt is only obeyed
if the job lists the plugin, while `politeness.robots` defaults to true and
reads as though it were the switch. By the argument that moved the cache out of
the engine block, robots belongs to its plugin and `politeness` should be
configuration the plugins read rather than settings the engine holds. Unresolved,
and it is the same question for `rate` and `concurrency`.

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
| Mutation policy: what a resubmission does to work already done | Built, tested |
| Extraction spec: separable, renders to HCL, fingerprinted | Built, tested |
| Defaults for every setting, in one file, with a resolved view | Built, tested |
| `internal/registry`, generic, shared by every extension point | Built, cache runs on it |
| `internal/chain`, middleware that wraps, with drop and short-circuit | Built, tested |
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
