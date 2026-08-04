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
                    │   (a graph)  │      └───────────┘
                    └──────────────┘
```

The scheduler owns the frontier and is the only stage a job may not replace.
Two schedulers handing out the same host cannot honour a crawl delay between
them, so politeness forces one decision point per host.

## A stage is a block, its plugins are inside it

Each stage is a block in the job holding two different kinds of thing:

```hcl
job "example" {
  start = ["https://example.com/"]

  item "article" {
    property "title" {
      type = str
    }
  }

  downloader {
    robots = true            # what the downloader is

    plugin "cache" {         # what has been added to it
      backend = "s3"
      bucket  = "pages"
    }
  }
}
```

**An attribute is behaviour the stage always has.** No meaningful "off", no
meaningful position in an order, and nowhere else it could have been written.

**A nested plugin is something you added.** It reorders, it turns off, and
somebody else can write it.

That division is what stops a setting drifting away from whatever enforces it. A
`max_body` kept in a different block would be a number the downloader might or
might not be reading, and the way you would find out is by downloading four
gigabytes.

It also removes the stage label from plugins: the block a plugin is written in
says which chain it joins, so the two cannot disagree. And the scheduler block
simply has no `external` attribute, which makes writing one a parse error with a
line and a column rather than a rule buried in a validator.

## Chains run both ways

This is what makes `order` mean something, and the part that is easy to get
wrong.

A chain wraps its stage, so every link sees the request on the way out **and**
the response on the way back, in opposite orders:

```
order:      500        550        900
         ┌────────┐ ┌────────┐ ┌────────┐
request  │offsite │→│ retry  │→│ cache  │→ ─┐
         │        │ │        │ │        │   │ network
response │        │←│        │←│        │← ─┘
         └────────┘ └────────┘ └────────┘
         first out              last out
         last back              first back
```

- **Low order is nearest the spider, high order is nearest the network.**
- `cache` at 900 is the last thing before the network, so a hit short-circuits
  the fetch only after every other request middleware has had its say. This is
  Scrapy's `HttpCacheMiddleware` placement and the reasoning is the same.
- `charset` at 600 sits after `compression` at 590 and before `cache` at 900, so
  what lands in the cache is decompressed and UTF-8.

A link may **short-circuit**: return a result without calling the rest, which is
how a cache hit works. It may **drop**: return `ErrDrop`, which is how offsite
works. Both are in the contract from the start because neither can be added
later without changing every link ever written.

`internal/chain` does this by wrapping rather than by hooking a pair of
`Request` and `Response` methods, which is the shape `net/http` middleware has
and for the same reasons: short-circuit is not calling next, and a link needing
state across both directions keeps it in a local variable.

## The job document

One HCL file holds one or more jobs, submitted and accepted together. A job
carries everything it needs, so nothing is inherited from whichever server picks
it up and a job resubmitted next month does what it did today.

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

    property "pubdate" {
      type       = date
      aliases    = ["published", "datePublished"]
      transforms = [datetime]
    }

    property "body" {
      type       = str
      required   = true
      aliases    = ["content", "articleBody"]
      transforms = [text, normalise_space]
    }
  }

  # The frontier: what is queued, in what order, and how hard one host may be
  # leaned on. Politeness is here rather than in the downloader because pacing
  # is decided when work is handed out, and because a rate is per host and
  # shared between jobs.
  scheduler {
    policy      = "priority"
    rate        = "2s"
    concurrency = 2
    max_depth   = 4
    max_pages   = 500
    max_time    = "90m"

    plugin "cron" {
      schedule = "0 */6 * * *"
    }
  }

  downloader {
    robots     = true
    user_agent = "scour"
    timeout    = "30s"
    max_body   = 33554432

    plugin "cache" {
      order   = 900
      backend = "s3"
      bucket  = "pages"
    }

    plugin "retry" {
      order = 550
      times = 3
    }
  }

  spider {
    plugin "depth" {
      order = 900
    }
  }

  # Item processing, as a dependency graph.
  pipeline {
    step "clean" "article" {
      rule {}
      rule {}
    }

    step "rank" "article" {
      requires = [clean.article]
    }

    step "python" "enrich" {
      requires = [clean.article, rank.article]
      script   = "./enrich.py"
    }

    step "bash" "notify" {
      requires = [python.enrich]
      script   = "./notify.sh"
    }
  }

  monitoring {
    metrics = false
    logging = true
    level   = "info"
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
stage's chain, ordered by `order`, and the block it sits in says which stage.

**A stage's settings live in the stage's block.** An attribute is what the stage
always does; a nested plugin is what was added to it. That is also the answer to
where caching goes: a cache sits between a request and the network, which is
what a downloader middleware is, so it is `plugin "cache"` inside `downloader`.

**Obligations are attributes, not plugins.** `robots`, `user_agent`, `timeout`
and `max_body` were catalogued as middleware and should not have been. A crawl
with no cache is a valid crawl that costs you money; a crawl with no robots
handling harms somebody else's server. A thing whose absence hurts a third party
must not be opt-in through a mechanism that defaults to absent. A knob you can
deliberately turn off is fine; a mechanism that is silently missing is not.

**Politeness cannot be middleware,** for a structural reason rather than a
tasteful one. A rate limit is per host and shared: two jobs crawling one site
must not each get their own allowance. Middleware is per job chain, so it could
not express that. It lives in the scheduler, which is the one decision point per
host.

**`order` is explicit, never positional.** A chain whose order depends on where
a block was written changes when somebody tidies the file.

**Bare words are predeclared, not strings.** `type = str` and
`transforms = [datetime]` are HCL variable references resolved against a
vocabulary the parser knows. A typo becomes `job.hcl:14,16-19: Unknown variable`
with a line and a column, instead of a string carried until something later
fails to make sense of it. Quoted forms still work.

**`requires = [clean.article]` is a reference, not a string.** Read as a
traversal, so a dependency on a step that does not exist is caught when the job
is submitted rather than when the graph runs. Cycles too.

**A plugin's own fields stay undecoded.** Common fields are read centrally; the
rest is kept as an opaque body and handed to the plugin's schema when it is
built. That is what makes a plugin something somebody else can write, and it is
also when a bad field gets an error with a line number on it.

**A job gets exactly the chain it lists.** Nothing is added that the document did
not ask for, so a chain can be read off the job without knowing a list kept
somewhere else. `enabled = false` therefore means precisely what leaving the
block out means. Both spellings exist because deleting a block throws away its
configuration and turning it off keeps it, which is what you want when the
setting took an afternoon to work out.

**A pipeline step is a node in a graph, not a link in a chain.** One stage, one
spelling: `step <kind> <name>` with `requires`. Giving it an `order` as well
would be two ways of saying the same thing, and they would disagree.

**Pipelines run concurrently.** The graph exists so independent work happens at
the same time. `Waves()` groups the steps whose dependencies are already
satisfied and which do not depend on each other; a runner starts a wave, waits,
and starts the next. `Width()` is the widest wave, which is what a runner sizes
its pool against. `Order()` flattens the same graph for showing a plan.

**Exporters are per item, not per job.** A job extracting articles and comments
wants them in different files, and an exporter handed both would have to be told
which was which anyway.

**A spider is handed the spec, not the job.** It has no business knowing where
bodies are cached, what the budget is or which exporters are attached, and
handing it the whole job would make every one of those look like something it
might depend on. The spec renders back to HCL, so a spider in another language
gets the text a person would have written.

**The spec is fingerprinted.** Resubmitting mutates a job, so the shape can
change while the crawl runs, and a record extracted under one shape and
attributed to another is wrong in a way nothing downstream can detect. The
fingerprint changes exactly when the shape does: not when properties are
reordered and not when the document is reformatted, because a fingerprint that
moved for cosmetics would force a re-extraction nobody needed.

**Resubmitting a job name mutates it and applies the changes.** The name is the
identity. What "applies" means is not the same for every change, so a diff
reports an effect per change and the `mutation` block says what should happen
about the ones that are not free.

**Everything has a default, and they are all in one file.** A default written
next to the field it fills is impossible to review: nobody can answer "what does
an empty job do?" without reading every file. `internal/engine/defaults.go` is
the whole list, `Defaults()` prints it, and `Resolved()` returns the job with
every one filled in, which is what should be stored so that resubmitting next
month behaves the way it did today.

**Nesting requires `object`.** A property with children is an object whatever it
says, and a mismatch is refused rather than inferred, because silently changing
a declared type is how a document stops meaning what it reads as.

## Plugin catalogue

**A list, not a commitment.** These are positions, not working parts. A name
here says where something would go if it existed, and nothing more. What exists
is what a registry says exists, and that is asked when a chain is built, not
when a document is validated. If this table decided what a job may name, a
catalogue of intentions would validate as a set of working parts and the failure
would arrive at run time on somebody else's machine.

The numbers are Scrapy's, because copying a known-good ordering is cheaper than
rediscovering it. They live in `internal/engine/catalogue.go` and a test holds
them to what is written here.

### Downloader

| Order | Name | What it does |
| --- | --- | --- |
| 500 | `offsite` | Drops URLs outside `domains` / `included` / `excluded` |
| 520 | `contenttype` | Refuses by extension and MIME before the body is read |
| 543 | `cookies` | Session cookies, per host |
| 544 | `auth` | HTTP authentication |
| 550 | `retry` | Retries the temporarily failed |
| 560 | `headers` | Default request headers |
| 580 | `metarefresh` | Follows meta-refresh redirects |
| 610 | `proxy` | Routes through a proxy |
| 630 | `redirect` | Follows HTTP redirects |
| 850 | `stats` | Counts requests, responses and failures |
| 900 | `cache` | Reads and writes the page cache |

`robots`, `user_agent`, `timeout` and `max_body` are not here. They are
`downloader` attributes, for the reason under Decisions.

**Decoding is not in this table, and that is deliberate.**

Turning bytes into text is what reading a body means, not a position in a chain.
Two things read a body: the downloader on its way to the cache, and the spider,
which reads the cache directly by key and never passes through this chain at
all. A decode that lived here would apply to one of them and not the other, and
the corpus would be UTF-8 only when it happened to be read the long way round.

So it is `internal/decode`, a function both call, and there is one
implementation of it.

**The cache holds what the server sent.** Not decoded output. That is the more
useful archive: detection improves, and original bytes can be decoded again for
a better answer, while a corpus decoded on the way in has its mistakes baked in
until somebody re-crawls. It is smaller, too, and faithful enough to
revalidate.

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
chains it exactly as the downloader is chained, because ordering the frontier is
most of what a focused crawl is.

A request passes through on its way into the frontier and back out on its way to
the downloader, so low order is nearest the spider that discovered it and high
order is nearest the queue.

| Order | Name | What it does |
| --- | --- | --- |
| 100 | `dupefilter` | Decides what counts as already seen |
| 200 | `offsite` | Drops URLs outside `domains` / `included` / `excluded` |
| 300 | `cron` | Defers a URL until it is due again |
| 400 | `budget` | Refuses a URL the job can no longer pay for |
| 500 | `priority` | Best first, by score |
| 500 | `breadth` | Level by level, for an archival crawl |
| 500 | `depth` | Follows a spur down before returning |
| 500 | `random` | Samples without the sample being shaped by the scorer |

`offsite` appears in three chains and that is deliberate, not duplication. The
spider's keeps an out-of-scope link out of the frontier, the scheduler's catches
entries that were in scope when they were queued and are not any more, and the
downloader's is the last check before the network.

The ordering policies at 500 are alternatives rather than a chain, and
`scheduler.policy` is the attribute that picks one. They are catalogued because
a job may want to bring its own.

### Pipeline

Not a plugin stage. A step is a node in a graph, written as `step <kind> <name>`
and ordered by `requires` rather than by a number.

| Kind | What it does |
| --- | --- |
| `clean` | Rule-driven tidying |
| `validate` | Enforces `required` and types |
| `dedupe` | Drops items already seen |
| `rank` | Scores and orders |
| `python`, `rhai`, `nodejs`, `bash` | Runs a script, inline or from a file |

### Exporters

`json`, `jsonlines`, `csv`, `parquet`, `nats`, `sqlite`.

Named `exporter "<format>" "<item>"`. An exporter naming an item the job does
not extract is refused, because silently writing nothing is the failure mode
nobody notices until they go looking for the output.

## Writing a plugin

A link is given the next handler and returns a replacement. Whatever it does
before calling next is the way out; whatever it does after is the way back.

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

A plugin is registered by name and built from its undecoded body:

```go
func init() {
    downloader.Register("cache", func(ctx context.Context, body hcl.Body) (Middleware, error) {
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
is the point. What it cannot do is stay silent about where it goes: a catalogued
name has a default `order`, and anything else has to say.

## Across machines

Servers join to form a cluster, and jobs run across it.

- Each node embeds a NATS server. `--join` adds routes, so the embedded servers
  cluster natively and a laptop still needs nothing installed.
- Jobs live in a NATS key-value bucket. Storage, below, has the whole map.
- Work distributes by queue group, so downloaders on different nodes pull from
  one subject with no coordinator.
- Bodies never travel on the bus. The cache holds them; messages carry the key.
- **Bring your own stage.** Because stages talk over the bus rather than calling
  each other, a spider somebody else wrote in another language is a subscriber,
  not a fork. The stage says so itself:

```hcl
job "news" {
  start = ["https://example.com/"]

  item "article" {
    property "title" {
      type = str
    }
  }

  spider {
    external         = true
    external_timeout = "10m"
  }
}
```

## Storage

Five kinds of thing get kept, and they want different stores. Gathered here
because the decisions were argued out one at a time and the map is the thing
worth being able to read at once.

| What | Where | Why |
| --- | --- | --- |
| Page bodies | The cache: a directory, S3 or GCS | Large, immutable, and shared between machines |
| Jobs, as desired state | NATS KV, `SCOUR_JOBS` | The only store every node already has |
| Run state | NATS KV, `SCOUR_RUNS`, no history | Changes constantly and must not churn a job's revisions |
| Nodes | NATS KV, `SCOUR_NODES`, with a TTL | Not durable state. A row outliving its process is a lie |
| Frontier and hosts | SQLite, one shared database | Politeness is shared, so this cannot be partitioned |
| Records and marks | SQLite, one database per job | Unbounded, unshared, and deleted by unlinking |
| Learned locators | The job document itself | A guess should be readable and correctable |
| Exports | Whatever the exporters write | Copies. Not the record of truth |

### What that buys

**The two stages that touch a database touch different ones.** The scheduler
holds the frontier; the pipeline holds the records. They share no file, so they
can run on different machines without coordinating, and neither needs what the
other has.

**The downloader and the spider touch no database at all.** A fetching node
needs the network and the cache. A parsing node needs the cache and the spec,
and the spec comes from KV like everything else. That is the property the whole
split exists to protect, and it survives this map.

**A laptop needs nothing installed.** An embedded broker, a directory of bodies,
and two SQLite files.

### Where the frontier lives

In SQLite, in one database, with hand-written SQL and no ORM.

Bodies are in the cache and the job is in KV. The frontier needs a database
because of what it has to do at once: dedup by URL, hand out the highest-scoring entry whose host
is not cooling, lease it with a timeout, and survive a restart with all of that
intact.

**NATS cannot do it.** JetStream is a work queue and work queues are FIFO. A
focused crawl is ranking, and the ranking changes as the model learns, so the
one thing the frontier must do is the one thing a broker does not.

**Politeness decides the rest.** A rate limit is per host and shared between
jobs, and two schedulers handing out the same host cannot honour a crawl delay
between them. So the frontier is single-writer per host by construction. A
multi-writer database buys nothing until there are more hosts than one process
can schedule, and at that point the answer is to shard by host, which makes each
shard single-writer again. SQLite is what a single writer wants.

That politeness is shared also settles the layout: **one database, not one per
job.** Per-job files would be tidier and would make dropping a job a delete, but
host state cannot be partitioned per job without two jobs on one site each
getting their own allowance, which is exactly what must not happen.

**One writer, many readers**, which is what WAL is for. Pure-Go driver, so it
cross-compiles and installs with nothing.

**Hand-written SQL, no ORM.** There are perhaps a dozen queries and every one is
shaped by an index. An ORM would hide the thing most worth looking at, and the
lease is a transaction with an ordering in it rather than a row fetched by id.

The escape hatch is deliberate: the lease is written as `SELECT ... FOR UPDATE`,
which SQLite ignores because it serialises writers anyway and Postgres needs.
The day multi-writer is genuinely required, it is the same SQL against a
different dialect rather than a rewrite. The old implementation had this shape
and it held at 150,000 rows.

What is given up, and it is worth being honest: ad-hoc analytics over records
are not this store's job, and cross-machine writes to one job's frontier are not
possible without sharding first.

What would change the answer: a crawl whose hosts genuinely exceed what one
scheduler can pace. Then Postgres and `FOR UPDATE SKIP LOCKED`, which is built
for exactly this, and the migration is a dialect rather than a redesign.

### Where records are kept

In SQLite, one database per job.

The asymmetry with the frontier is deliberate and has a reason on both sides.
The frontier is one shared database because politeness is shared. Records are
one database per job because they are the opposite: nothing about a job's
records is shared with another job's, they grow without bound, and `scour rm`
should be a deletion rather than a query.

Keeping them out of the frontier's file also keeps the frontier's file small,
which matters because it is the hot one: the frontier is written on every
discovery and every lease, and a growing table of records alongside would drag
a vacuum through the busiest thing in the system.

**There is a record store at all**, rather than the exporters being the only
output, because three things need to find a record again after it was written.
`mutation.stale_records = "discard"` has to know which records to delete.
`reextract` has to know what it is replacing. And a mark somebody puts on a
record has to attach to something. None of that works if the only copy left the
building as a CSV.

Exporters are copies. That is the whole of their job.

### Where run state is kept

In a KV bucket of its own, with no history.

Paused, pages fetched, what the current run is doing: this changes constantly
and is read by anybody asking `scour status`. Putting it in the job's entry
would churn revisions until the history that entry keeps is worthless, and
putting it in the database would mean a node needs one to answer a question
about itself.

No history because there is nothing to look back at: the interesting record of
what a run did is its log and its records, not a thousand revisions of a page
counter.

### Where a job is kept

In a NATS key-value bucket, not the database.

The argument that decides it: **KV is the only store every node already has.**
If jobs lived in the database, every node would need database access to know
what it is running, and the property that only the scheduler and the pipeline
touch it would be gone. A downloader node that needs no database at all is a
real deployment win and it evaporates the moment the job lives in one.

Three things follow that are worth having rather than merely tolerable.

**A revision is a compare-and-swap.** Resubmitting a name mutates the job, so
two clients submitting the same name at once need to not overwrite each other.
`Update(key, value, revision)` is exactly that, and doing it in SQL would mean a
transaction on a database most nodes should not have.

**A watch is how a change propagates.** Submit, and every node running that job
is told. No polling, no interval to tune, nothing to get wrong.

**History is a diff for free.** A bucket keeping ten revisions answers "what
changed, when" without anybody designing an audit log, and it pairs with the
diff the engine already computes.

Four conditions, because each is a way to get this wrong:

**Store the resolved job, not the submitted one.** The design already requires
it: defaults are applied when a job is accepted, so what is stored has to be
what will run, or a change to a default silently alters a job that is already
going. Keeping the submitted form alongside is worth it for showing a person
what they wrote.

**Run state does not go in the job's entry.** Paused, pages fetched, frontier
depth: those change constantly, and writing them here would churn revisions
until the history is worthless. The entry is desired state. What is actually
happening belongs somewhere with different semantics.

**Job names are sanitised into keys.** A job called `a.b` or `a*` would split a
key or widen a watch, which is the same problem subjects have and wants the same
answer.

**Large `inline` scripts do not fit.** A value is capped by the broker's maximum
payload, a megabyte by default, and a pipeline step with a few hundred lines of
inline Python is how somebody reaches it. Either such a step uses `script` and a
path, or scripts get the treatment bodies already have: content in the cache,
key in the entry.

The one thing given up is querying. KV answers "this key" and "these keys", not
"every job crawling example.com", which would mean listing and filtering. For
tens or hundreds of jobs that is not a constraint worth designing around.

### Only the server writes

The command line is a client. It holds no frontier, no records and no idea what
is running, so anything about running state is a question it asks.

That means the server validates, diffs, applies the `mutation` policy and
writes; the CLI never writes the bucket directly. One place enforces the rules,
or an old client bypasses a newer one. It also means exactly one writer per job,
which is what makes the compare-and-swap above sufficient rather than hopeful.

A diff cannot be computed by the client at all, since it is the submitted
document against the running job and the client has only the first. So `plan`
sends the document and renders the answer. [CLI.md](CLI.md) has the split of
which commands need a server and which never will.

The `scheduler` block has no `external` attribute, so it cannot be handed over.

Open: **job placement.** A job naming a local cache directory can only run where
that directory is, so either placement is constrained by config or a job with a
local cache is a single-node job by definition.

## Open questions

**What happens to work already done, in detail?** The `mutation` block says
`drop` or `keep` or `reextract`. Whether a dropped frontier entry is deleted or
merely marked, and whether a re-extraction re-runs the whole corpus or only what
the changed property touches, is not decided.

**Does an external stage get the spec pushed, or pull it?** The spec is
separable and fingerprinted, so either works. Pushing it with every response
wastes bandwidth; pulling it needs a request-reply the stage has to implement.

**How does a run know it is finished?** An empty frontier is not enough: work
may be leased, a response in flight, and an item mid-graph. Quiescence across
four stages is the genuinely new distributed-systems problem here.

## Status

| Piece | State |
| --- | --- |
| `internal/cache` interface, registry, keying | Built, tested |
| Backends: local, s3, gcs | Built, one shared contract suite, all passing |
| `internal/registry`, generic, shared by every extension point | Built, cache runs on it |
| `internal/chain`, middleware that wraps, with drop and short-circuit | Built, tested |
| HCL job document, stage blocks, nested plugins, multiple jobs | Built, tested |
| Vocabulary: bare types and transforms | Built, tested |
| Validation, every problem reported at once | Built, tested |
| Chain ordering, catalogued defaults | Built, tested |
| Pipeline graph: references, cycles, topological order, waves | Built, tested |
| Extraction spec: separable, renders to HCL, fingerprinted | Built, tested |
| Defaults for every setting, in one file, with a resolved view | Built, tested |
| Resubmission diff and mutation policy | Built, tested |
| Plugin implementations, all of them | Not started |
| The stages themselves | Not started |
| Exporters, all formats | Not started |
| Cluster join, distributed jobs | Not started |
| Jobs in a KV bucket, server-side writes | Decided, not started |
| Frontier in SQLite, one shared database | Decided, not started |
| Records in SQLite, one database per job | Decided, not started |
| Run state and nodes in KV buckets of their own | Decided, not started |

`internal/engine/notes_test.go` reads this file. It parses and validates the job
documents in it, and compares every number in the catalogue tables with the
catalogue the code uses. If the notes and the code disagree, the tests fail,
which is the only way a document like this stays true.
