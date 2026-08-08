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
- Decoding is not in this chain at all. The cache holds what the server sent,
  and `Response.Text()` decodes on demand through the same function the spider
  uses when it reads a body back out. See
  [Decoding is not in this table](#downloader) for why.

A link may **short-circuit**: return a result without calling the rest, which is
how a cache hit works. It may **drop**: return `ErrDrop`, which is how offsite
works. Both are in the contract from the start because neither can be added
later without changing every link ever written.

`internal/chain` does this by wrapping rather than by hooking a pair of
`Request` and `Response` methods, which is the shape `net/http` middleware has
and for the same reasons: short-circuit is not calling next, and a link needing
state across both directions keeps it in a local variable.

### From a list of names to a chain that runs

`internal/chain` runs an ordered set and does not care where the set came from.
`internal/engine` reads a document and can say a job wants `cache` at 900.
Neither can answer whether `cache` is a thing that exists, so `internal/plugin`
is the seam that does.

It is the first place a job naming a plugin nothing implements is refused.
Validation deliberately does not do it: `scour validate` runs offline and in CI,
so it cannot know what some other node has compiled in. Building the chain can,
because by then there is a process with the implementations in it. All the
missing names are reported at once, along with what the node does have. A job
loading six plugins on a node with four of them should be told which two, not
sent round the loop twice.

What reaches a plugin is its block's body, undecoded. The plugin decodes it
against its own schema, which is what lets somebody else write one without the
seam knowing its fields, and is why a bad field is an error with a line and a
column rather than a value silently ignored. It is also why `secret("name")`
travels as an unevaluated call through the stored job, the diff and `scour
show`, and becomes a credential exactly once, on the node that builds the
plugin.

## The job document

One HCL file holds one or more jobs, submitted and accepted together. A job
carries everything it needs, so nothing is inherited from whichever server picks
it up and a job resubmitted next month does what it did today.

```hcl
job "news" {
  domains  = ["example.com"]
  start    = ["https://example.com/topic"]
  included = ["*/topic/*", "https://example.com/topic"]
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
  #
  # `rate` is a floor, not the whole answer. A site asking for longer in its
  # own robots.txt `Crawl-delay` gets it, and the wait is the longer of the
  # two: a job cannot use a permissive robots.txt to go faster than it
  # configured, and cannot use its own rate to go faster than the site asked.
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
    robots        = true
    user_agent    = "scour"
    timeout       = "30s"
    max_body      = 33554432
    max_redirects = 10

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
  exporter "json"   "article" { dir     = "./out" }
  exporter "csv"    "article" { dir     = "./out" }
  exporter "nats"   "article" { subject = "items.article" }
  exporter "sqlite" "article" { file    = "./items.db" }
}
```

## Decisions

**Plugins are middleware.** One word for one concept. A plugin is a link in a
stage's chain, ordered by `order`, and the block it sits in says which stage.

**A stage's settings live in the stage's block.** An attribute is what the stage
always does; a nested plugin is what was added to it. That is also the answer to
where caching goes: a cache sits between a request and the network, which is
what a downloader middleware is, so it is `plugin "cache"` inside `downloader`.

**Obligations are attributes, not plugins.** `robots`, `user_agent`, `timeout`,
`max_body` and `max_redirects` were catalogued as middleware and should not have
been. A crawl
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

## Fill rates, measured

Extraction is the first thing here whose quality is a number rather than a pass
or a fail, and for a long time the number had never been taken. `extract.Rates`
takes it. It runs a job over a set of pages and reports, per property, how many
pages produced a value, which of the four ways found it, how many values the
transforms emptied, and how many required properties came back missing.

Measured on 5 August 2026, over the corpus in `internal/extract/testdata`:
fifteen hand-written pages, and the job in `corpus/job.hcl`, which is the news
template scour ships with two class selectors and one date regex added.

An article came out of all fifteen pages. Ten of the fifteen were complete, and
seven required properties were missing across the other five.

| Property | Found | Rate | css | xpath | regex | semantics | empty | missing |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| `url` (required) | 11 | 73.3% | 0 | 0 | 0 | 11 | 0 | 4 |
| `title` (required) | 14 | 93.3% | 0 | 0 | 0 | 14 | 0 | 1 |
| `summary` | 8 | 53.3% | 0 | 0 | 0 | 8 | 0 | 0 |
| `author` | 9 | 60.0% | 0 | 0 | 0 | 9 | 0 | 0 |
| `author.name` | 6 | 40.0% | 0 | 0 | 0 | 6 | 0 | 0 |
| `author.profile` | 2 | 13.3% | 0 | 0 | 0 | 2 | 1 | 0 |
| `published` | 11 | 73.3% | 0 | 0 | 4 | 7 | 0 | 0 |
| `modified` | 3 | 20.0% | 0 | 0 | 0 | 3 | 0 | 0 |
| `section` | 5 | 33.3% | 0 | 0 | 0 | 5 | 0 | 0 |
| `body` (required) | 13 | 86.7% | 9 | 0 | 0 | 4 | 0 | 2 |

Overall 54.7%, counting one opportunity per declared property per page.

**The breakdown by how a value was found is the point of the table.** A property
found by semantics on ninety per cent of pages and one found by a taught
selector on ninety per cent are the same number describing two different
situations. The taught one breaks loudly when a site changes its markup. The
guessed one drifts onto whatever else on the page answers to the name, quietly,
while the rate stays at ninety. On this corpus almost everything is guessed:
every title, every url, and every summary. The two taught selectors carry nine
of the thirteen bodies and the taught regex four of the eleven dates, and that
is the whole of what is not a guess. It is the argument for `scour train` stated
as a measurement rather than as an opinion.

**A found value is not a correct value.** The rate counts a value, and several
of these are imprecise on purpose: the body of the split-body page comes back as
the whole of `<main>`, headline included. Measuring correctness needs expected
values written down per page, which is a larger job and a different one.

**Fifteen hand-written pages are a floor-check, not a claim about the open web.**
Nothing here is a sample of anything. What the corpus is for is a ratchet:
`TestFillRatesOverTheCorpusClearTheFloor` holds each rate to about one page
below what was measured, so a change that makes extraction worse fails the
build. The floors go up when extraction improves and are never lowered to make a
change pass. Real numbers over a real crawl are still owed, and the old
implementation's are on the `main` branch.

## Events are items, converted at the edge

There is no event block. An item is what a job declares, and what leaves the
pipeline is that item rendered as a measurement:

```
price,company=acme,exchange=lse value=178.23,volume=1000000 1754308800
```

The split is derived rather than declared. `of` and every `relation` are entity
references, so they are the **tags**. The properties are the **fields**. `time`
names which property is the event's own time. Nothing in a document says "this
is an event", and one model has two renderings: a record for export, a
measurement for the stream and the archive.

| | |
| --- | --- |
| Measurement | The item's name |
| Tags | `of`, the relations, and any property declared `tag` |
| Fields | Everything else |
| Time | The property `time` names |

**Tag cardinality is the failure mode.** Every distinct tag value is another
series, so tagging by URL or headline destroys a time-series store. Entity
references are safe because entities are bounded by definition, which is why
they are the tags and free text never is. A scalar that genuinely is a dimension
says `tag = true` and wants a bounded set of values; one that cannot be a single
value is refused.

**Time is event time, never ingest time.** A headline published at nine and
crawled at half eleven is an event at nine. Getting that wrong makes replay and
backfill produce series that are wrong in a way nobody notices for months. When
the source gives no time at all, `time` is absent and the moment of observation
is all there is, which is worth being explicit about rather than quietly
inventing.

**Content does not travel.** A five thousand character body is not a field. A
headline event carries its tags, its small fields and a reference to the body,
which is the rule the bus already follows for pages.

### Two shapes, one mechanism

A headline happens once. A price is the same thing measured again. The
difference shows up in the subject, and `of` is what puts it there:

```
events.news.headline                 every headline
events.markets.price.<company>       one subject per company
```

Which is what lets a consumer subscribe to one company rather than filter the
firehose, and what makes the latest value a fetch rather than a scan.

### An exporter does both

There is no archive component. Saving to storage and streaming to a stream are
both deliveries, so both are exporters, and they were both in the list already:

```hcl
job "markets" {
  start = ["https://example.com/quotes"]

  item "price" {
    of   = "company"
    time = "observed"

    property "value" {
      type = float
    }

    property "observed" {
      type = date
    }
  }

  exporter "parquet" "price" {
    dir = "./archive"
  }

  exporter "nats" "price" {
    subject = "events.markets.price"
  }
}
```

Same item, two deliveries, neither privileged. Real-time is what the second one
does, not a property the design has to be built around: the pipeline emits an
item and a streaming exporter publishes it as it goes.

That also leaves the source of truth where it was. The pipeline owns its records
database, because `stale_records = "discard"` has to know what to delete,
re-extraction has to know what it is replacing, and a mark has to attach to
something. Exporters are copies, which is the whole of their job, and a copy in
Parquet is no more the truth than a copy in CSV.

**Parquet suits the measurement shape exactly.** Tags are low cardinality by
construction, so they dictionary-encode to almost nothing, and the fields are
what anybody queries. Correlating price series and counting distinct values per
property become the same kind of scan over the same files.

**On the storage backend the page cache already uses**, so local, S3 and GCS
need nothing new, and where an export lands is a config line. That part is the
design and not yet the code: the exporter as built writes to a `dir` and a
`file` like the other file formats, which is why the block above says `dir`. It
also writes records rather than measurements, which the section below has the
rest of.

**And nothing has to be running.** DuckDB reads Parquet where it lies, so
analytics is pointing something at files rather than importing into a database
that then has to be kept in step.

### An event exporter takes a different shape

They do differ, and in the shape they consume rather than where they put it.

A record is a document: properties, some of them nested, as somebody asked for
them. A measurement is flat: a name, tags, fields and a time. `json`, `csv` and
`sqlite` want the first. `nats` wants the second, and `parquet` would suit it.

**One interface, and the conversion is a method on the record.** The design here
used to be two interfaces, `Exporter` and an `EventExporter` an implementation
opted into, with the pipeline converting once and handing each exporter the
shape it asked for. What was built is simpler and it is worth recording why.

[record.Record.Measure] takes the item's declaration and returns the
measurement. The split it applies is the one the item already declares, in
[engine.Item.Tags] and [engine.Item.Fields], so an exporter never decides that
`of` is a tag or that `time` names the timestamp and two of them cannot disagree
about it. The `nats` exporter calls it and publishes the result; the file
formats do not call it and write the record.

That is the whole of what the second interface would have bought, without a
second interface: a type assertion at the seam would have made the pipeline
carry two shapes and every exporter's contract test carry two paths, for one
line an exporter can call itself. The rule that matters, that the conversion is
written once, is kept by there being one `Measure` and no other way to reach a
measurement.

**Parquet still writes records.** It goes through [exporter.Layout], the same
column derivation the CSV and SQLite exporters use, so an archive of
measurements is not what is on disk today. It is the one format where the change
would pay, since tags are low cardinality by construction, and it is a call to
`Measure` and a different layout rather than a redesign.

The other difference is batching. A streaming exporter sends as it goes; a file
exporter cannot write a Parquet file per row, so rolling by time or by size is
its own business, in its own block, like everything else a plugin decides for
itself. Nothing rolls yet: `json`, `jsonlines`, `csv` and `parquet` each write
one file, named for the item unless the block says otherwise.

## Entities

Extraction is per page and knows nothing. An entity store makes it
corpus-informed: knowing that a publisher has published these authors on this
subject gives a mangled byline something to be resolved against rather than
merely parsed.

```hcl
job "news" {
  start = ["https://example.com/"]

  item "article" {
    property "title" {
      type = str
    }

    property "author" {
      type   = entity
      entity = "person"     # the relation back to the publisher
    }
  }
}
```

It composes with topics in a way that is worth noticing: **"Apple" in a
technology article is a company and in an agriculture article it is a fruit.**
Topic-conditioned linking falls out of having both, and neither was designed
with the other in mind.

### Shared, like a classifier

A publisher means the same thing in every job, so the store is referenced rather
than copied. That is the second thing to be shared and it makes a pattern:
anything whose meaning does not depend on the job belongs outside it.

### It needs a service, and the others do not

The frontier has one owner and the records store has one owner, so both stay
private files. This has two: the pipeline writes resolved entities, and the
spider reads known ones to disambiguate what it is extracting. Two stages
touching one database would break the ownership rule, and a service is what
keeps it: one process owns the file and both stages ask.

### Everything is an assertion

Nothing in the store is true. Everything was **asserted by something, at a time,
with a confidence**, and provenance is carried on every one:

| | |
| --- | --- |
| Job | Which crawl said so |
| URL | Which page it came from |
| Record | Which extraction |
| Spec | Which shape it was extracted under |
| Classifier | Which topic version, when one was involved |
| Confidence | How sure that extraction was |
| Observed | When |

Provenance hangs on the assertions and not on the entity, because an entity's
existence is implied by things being said about it. That has three consequences
worth having.

**Conflict stops being a problem to prevent.** Two pages giving different
founding years is not corruption, it is two assertions, and the current value
becomes a question you ask rather than a field you overwrite: most confident,
most recent, most corroborated, or by source. Materialise that view if it gets
hot; compute it until then.

**Retraction becomes a delete.** One job extracting badly used to mean polluting
a shared store with no way to tell which values came from where. With provenance
it is `where job = ?`, and the store is clean.

**A merge is an assertion too.** When identity resolution decides two entities
are one, recording it beats rewriting rows: it is reversible, it keeps both
provenance trails, and it does not destroy the fact that they were once thought
distinct, which is the thing you need when the merge was wrong. So a merge is
one alias row pointing at a canonical id, carrying its own provenance and the
rule that proposed it, and every read joins through it. Retracting the job that
merged takes the merge back with the rest of what that job said.

### Merging wrongly is worse than not merging

The two failures are not symmetric. Two people wrongly collapsed into one look
exactly like a store working: the rows are there, the counts go up, and every
later answer is confident and wrong. Two spellings left apart are visible, and
somebody says so.

So nothing merges by itself. Proposing a merge and making one are two calls,
and the automatic rule is one: an initial and a surname against **exactly one**
full name. Two candidates means "A. Doe" is Alex or Anna, the evidence cannot
say which, and taking the more asserted one would be a popularity contest
dressed as evidence. There is no edit distance anywhere, because a threshold
that merges "Jon Smith" with "Jan Smith" is one character from a threshold that
does not, and nobody can say which side a given pair should fall on.

### A relation is not a record field

A property is extracted into the record and travels with it. Put the publisher
there and it appears in every exported article whether anybody wanted it or not.
A relation belongs to the graph: it has its own attributes and its own lifetime,
and the record stays what somebody asked for.

```hcl
job "news" {
  start = ["https://example.com/"]

  item "article" {
    property "author" {
      type   = entity
      entity = "person"
    }

    relation "publisher" {
      entity   = "company"
      property = self.domain
      topic    = ["climate@7"]
    }
  }
}
```

A byline is the case that is both: wanted in the record and worth keeping as a
link, so a property typed entity is extracted and asserted. A publisher is not
on the page at all, it *is* the site, so it needs somewhere other than the text
to come from.

**`self` is a small predeclared vocabulary**: `self.url`, `self.domain`,
`self.host`, `self.path`, `self.fetched_at`. Things true of every extraction
without anybody declaring them, resolved the same way the type names are, so
`self.doamin` is a parse error with a line and a column rather than an empty
value nobody notices.

**`topic` says which evidence to attach, not what to assert.** The page was
already scored while it was crawled, so naming a classifier records that score
on the edge. Forty articles then give the edge a distribution rather than a
number somebody typed once and never revisited. Writing a topic as a literal
would be the assertion this store exists to avoid.

### The relation is the property's name

`property "author"` on `item "article"` already says everything:
`(article) --author--> (person)`. There is nothing else to write.

Deliberately nothing. A relation name each document chose for itself would give
a shared graph two words for one thing: one job writing `writes_for` and another
`authored_by`, and neither question answerable without knowing both. Naming is a
decision pushed onto whoever is least equipped to make it, so it is not asked
for. It also lands on the vocabulary sites already publish, since the aliases
are full of `datePublished` and `articleBody` and `author` is the same word
schema.org uses.

### The interesting relations are derived, not declared

"Monbiot writes for the Guardian about environment" was never a fact for anybody
to type. It is what forty articles whose `author` was him and whose `publisher`
was them already imply, on topics the classifier scored while they were crawled.

So it is a query over assertions, and it comes back with a count, a spread of
topics and the pages that evidence it, rather than being asserted by whoever
wrote a document and believed at face value afterwards. That is the database of
publishers and their authors per topic: computed, evidenced, and never
maintained.

Which is what makes relations carrying properties matter. The topic on
`writes_for` is not an attribute somebody set; it is an aggregate of the
articles underneath it. A relation therefore has an identity of its own and can
be the subject of assertions, the same as an entity.

That part is built. A `relation` block takes `property` blocks like an item
does, `Relate` returns the edge's id so `Describe` can hang a value on it, and
the extraction plan reaches inside the relation so the value comes off the page
rather than out of a map somebody filled in. It was the second half that was
missing for a while: the store took relation properties and no valid document
could express one, which is the shape of unreachable feature this repository
keeps rediscovering. A feature needs a test that starts where a person starts,
at the document.

### Explicit tables, not triples

Everything here could be one table of subject-predicate-object with provenance
columns, which is the elegant answer and handles entity properties, aliases,
relations and relation properties uniformly.

It is not the one to take. In SQLite a triple store turns every question into a
chain of self-joins, and being able to answer a question with a readable SELECT
is the argument that won the frontier decision. Explicit tables are more of them
and less cleverness, and they stay legible at three in the morning.

### The loop has a failure mode

Known entities improving extraction is also known entities **crowding out new
ones**. If a byline only resolves cleanly when it matches something already
stored, discovery stops and the store converges on what it already believed,
looking exactly like rising accuracy the whole time.

The guard is the one topics needed: **known entities raise confidence, they
never gate extraction.** A novel author must come out as easily as a familiar
one, so the store makes the crawl surer rather than able. That is a test rather
than an intention: a byline belonging to nobody in the store extracts as easily
as a familiar one, in `internal/pipeline/entities`. The step that feeds the
store returns every record exactly as it was handed them, so there is nowhere
for a byline to be dropped, held back or scored differently for being new.

### Named entity recognition is a plugin

Go has no good NER, and the honest choices are a Python service or a language
model: expensive, shared, wanted by some jobs and not others. That is the remote
middleware contract exactly, the same as a classifier, and for the same reasons.

Open: **staging.** The property-to-entity reference and a store of typed
entities with typed relations is contained and useful alone, since it answers
"which authors has this publisher published" for nothing. Identity resolution is
the middle piece, and it is built in the only shape that does not put the first
one at risk: nothing merges by itself, a merge is a row pointing at a canonical
id rather than a rewrite, and the one automatic rule refuses to guess when two
candidates could be meant. Recognition and linking is the large one, and the
feedback into extraction needs all three. Building them as one thing is how this
becomes a year of work that never ships.

## Topics are optional

Most jobs crawl a site because they want that site. Some want a subject
wherever it turns up, and those need to know whether a page is about it.

**It is a plugin, so a job that does not want it has none of it.** No classifier
loaded, no model storage opened, nothing asked of a service that need not be
running. That is the same property that keeps a build which never wanted S3
from carrying the AWS SDK, and it is why this is not an attribute on the job: an
attribute would make every job carry a concept almost none of them use.

```hcl
job "climate" {
  start = ["https://example.com/"]

  item "article" {
    property "title" {
      type = str
    }
  }

  scheduler {
    plugin "topic" {
      subject = "climate@7"
      least   = 0.05         # not worth following
    }
  }

  spider {
    plugin "topic" {
      subject = "climate@7"
      least   = 0.4          # not worth extracting
    }
  }
}
```

### Two placements, because they are two questions

Before a fetch there is only the URL, the anchor text and the page the link was
found on. That is a handful of words, which is enough to *rank* and not enough
to *decide*. After a fetch there is the whole page, which is enough for both and
costs far more.

So the scheduler's copy orders the frontier and the spider's copy gates
extraction, and you often want one without the other. Ranking without gating is
the common case: focus the crawl, keep everything it finds. Gating without
ranking is a known-good site crawled exhaustively where only some pages matter.

**Rank, do not filter, by default.** A hard topical filter breaks a focused
crawl in a specific way: hubs, indexes and navigation are off-topic themselves
while being the only route to on-topic content, so filtering on them strangles
the crawl at depth one. The topic feeds the score, the ordering policy already
consumes it, and the two thresholds exist for the cases where dropping is
genuinely right.

That is also why there are two thresholds and not one. "Not an article about
this" and "not worth traversing" are far apart, and one number for both is the
mistake.

Both are spelled `least`, because the block a plugin is written in is what says
which question it is answering, and a job that wants only ranking leaves it out:
zero drops nothing. The scheduler's copy also takes a `weight`, which is how a
subject is blended against whatever else scored the same request, and both take
either a `dir` of trained classifiers or the `url` of a cluster to fetch one
from.

### A classifier is shared, and jobs reference it

Locators are per site because markup is per site. A subject is not: it means the
same thing on a UK site and a US one, and a classifier trained across every page
ever fetched beats one trained on a single job's.

So a classifier is a named thing with its own lifecycle, trained over the corpus
and referenced by jobs rather than copied into them. It is the one learned
artefact that fails the readable test: a weight table or a topic-word matrix is
not something anybody corrects by hand, so it lives as an artefact rather than
in a document.

**A job pins a version.** `climate@7`, not `climate`. Otherwise retraining
changes what every job crawls with nothing in any document to show why, which is
the same trap that made [Job.Resolved] the stored form and gave the spec a
fingerprint. Retraining produces a new version; moving to it is a change
somebody applies, under the `mutation` rules that already exist.

### The classifier may as well be a remote middleware

It is expensive, shared, wanted by some jobs and not others, and reasonably
written in a language that is not Go. That is the shape the remote middleware
contract was designed for. A small setup runs it in process; anybody serious
runs it once and points several nodes at it.

Open: **what a topic is defined by.** Terms are easy to write and crude.
Example pages are far better and mean the crawl has to have fetched them.
Discovery over the corpus finds subjects nobody thought to name, and then wants
naming. These are not exclusive, and which is the required one decides what
`scour topic train` takes.

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
| 850 | `stats` | Counts requests, responses and failures |
| 900 | `cache` | Reads and writes the page cache |

`robots`, `user_agent`, `timeout`, `max_body` and `max_redirects` are not here.
They are `downloader` attributes, for the reason under Decisions.

**robots is not in the table for a second reason too.** There is exactly one
correct position for it, and a position that can be configured is a position
that can be configured wrongly. It wraps the entire chain, so a refused URL is
refused before the cache is consulted and before a retry is scheduled. A job can
turn it off, which is sometimes legitimate against a site you own; it cannot
quietly move it behind the cache, which never is.

robots.txt is fetched with the bare fetcher rather than through the chain.
Through the chain it would be checked against robots.txt, which is a loop, and
it would land in the page cache, where it would be served back long after the
site changed its mind. What a site permits has to be current. A success is kept
for the life of the job on this node; a failure is not kept at all, so a network
blip costs one request rather than a host.

What each answer means is RFC 9309 §2.3.1: 2xx is the rules, 4xx is a site with
nothing to say, and anything else is a site that could not tell us, which is not
the same as a site that said yes.

**Redirects left the table for the same reason.** A redirect is a different URL,
on a host that may have its own robots.txt, and a follower anywhere inside the
robots check would fetch it without ever asking. So following is a
`max_redirects` attribute, and it wraps everything: each hop re-enters the whole
downloader from the top, is checked against its own host's robots.txt, and is
cached under its own key. `max_redirects = 0` follows none and hands the 3xx
back.

The HTTP client is told not to follow anything itself, because a redirect it
followed would be invisible to all of this.

When the frontier exists, a redirect that leaves the host should become a
frontier request rather than an inline hop, so that dedup, scope and politeness
all get a say in it. Following inline is right for the same-host case, which is
nearly all of them, and is what this does today.

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

**Two keys per page.** The body under the URL's key, and the status, the final
URL and the response headers under the same key with `.meta` on the end.

The sidecar exists because a body on its own is not re-readable. A page in
windows-1251 that declares its encoding in the `Content-Type` header and nowhere
else decodes correctly on the way in and into mojibake on the way back out, and
nothing about the resulting text says it went wrong. The headers are what makes
a hit the response that was received rather than a body with its provenance
filed off.

This could have lived in the records database instead. It lives in the cache
because a corpus that cannot be read without a second database that happens to
still exist is not a corpus. `cache.Store` stays what it was, a key holding
bytes; using two of them is the middleware's business and none of the store's.

### Spider

| Order | Name | What it does |
| --- | --- | --- |
| 50 | `httperror` | Drops non-2xx before anything parses them |
| 300 | `topic` | Scores a page against a topic, and drops what is off it |
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
| 300 | `cron` | Defers a URL until it is due again |
| 450 | `topic` | Scores a URL against a topic, before the policy orders it |
| 500 | `priority` | Best first, by score |
| 500 | `breadth` | Level by level, for an archival crawl |
| 500 | `depth` | Follows a spur down before returning |
| 500 | `random` | Samples without the sample being shaped by the scorer |

**The scheduler enforces the job's own attributes itself,** rather than through
plugins, which is why `offsite` and `budget` are not in this table. `domains`,
`included`, `excluded`, `max_depth` and `max_pages` are all attributes, and an
attribute's enforcement cannot be optional: a plugin that could be turned off is
a boundary that can be crossed by deleting a line. The check is on the way into
the frontier, outside the chain, the same way robots.txt sits outside the
downloader's.

`offsite` stays in the spider's and the downloader's tables, and there it really
is optional. Those two are catching work that did not come through this
scheduler, which on a single node is pure redundancy and across a cluster with a
spider somebody else wrote is not.

The ordering policies at 500 are alternatives rather than a chain, and
`scheduler.policy` is the attribute that picks one. They are catalogued because
a job may want to bring its own.

### Pipeline

Not a plugin stage. A step is a node in a graph, written as `step <kind> <name>`
and ordered by `requires` rather than by a number.

| Kind | What it does | State |
| --- | --- | --- |
| `clean` | Rule-driven tidying | Built |
| `validate` | Enforces `required` and types | Built |
| `dedupe` | Drops items already seen | Built |
| `rank` | Scores and orders | Built |
| `entities` | Asserts what the records refer to, into the entity store | Built |
| `python` | Runs a Python script, inline or from a file | Catalogued |
| `rhai` | Runs a Rhai script, inline or from a file | Catalogued |
| `nodejs` | Runs a Node script, inline or from a file | Catalogued |
| `bash` | Runs a shell script, inline or from a file | Catalogued |

**Catalogued means a position, not a part**, the same as the plugin tables
above: the name is what a step of that kind would be called, and a job naming
one is refused when the pipeline is built rather than when the document is
validated. The state column is compared with the registry by a test, so a kind
that gets built and is still written down as catalogued fails.

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

**What is built, and what is not.** `internal/node` joins a cluster, watches the
jobs and serves the stages it offers, and `internal/bus` carries a request to
one and an answer back. Both are tested, and the equivalence test holds one job
to the same records through either wiring.

What has no caller is the other end. `run.Options` has a `Fetch` and a `Read`,
which is the whole of what running over a bus changes, and only the bus's own
tests supply them. So a document saying `external = true` is refused by name
rather than crawled locally: the setting is reported back by `scour show` as
"yes, waiting 5m0s", and a run that quietly ignored it would leave somebody
believing their pages were being fetched on a machine they were not. That also
leaves `external_timeout` a setting nothing reads yet, which is recorded where
the check for those lives rather than left to be rediscovered.

## Storage

Several kinds of thing get kept, and they want different stores. Gathered here
because the decisions were argued out one at a time and the map is the thing
worth being able to read at once.

| What | Where | Why |
| --- | --- | --- |
| Page bodies | The cache: a directory, S3 or GCS | Large, immutable, and shared between machines |
| Jobs, as desired state | NATS KV, `SCOUR_JOBS` | The only store every node already has |
| Run state | NATS KV, `SCOUR_RUNS`, no history | Changes constantly and must not churn a job's revisions |
| Secrets | NATS KV, `SCOUR_SECRETS`, sealed, no history | Per job, so the environment cannot carry them |
| Nodes | NATS KV, `SCOUR_NODES`, with a TTL | Not durable state. A row outliving its process is a lie |
| Frontier and hosts | SQLite, one shared database | Politeness is shared, so this cannot be partitioned |
| Records and marks | SQLite, one database per job | Unbounded, unshared, and deleted by unlinking |
| The entity graph | SQLite, behind a service | Shared between jobs, and two stages touch it |
| The event log | SQLite, behind a service | The same, and the one most likely to want another backend |
| Trained classifiers | Files, one per version, `.scour/topics` | A few hundred kilobytes of counts that never change once written |
| Learned locators | The job document itself, marked | A guess should be readable and correctable |
| Exports | Whatever the exporters write | Copies. Not the record of truth |

The two behind a service are the ones that broke the ownership rule, and the
service is what restores it: one process owns the file, everything else asks.
Both are an interface with a registry, `internal/entity` and `internal/event`,
so a backend that is not SQLite is a registration rather than a rewrite, and
`entitytest` and `eventtest` are what make two of them interchangeable in fact
rather than in principle. The SQLite implementations are in those packages
rather than in a subpackage each, because the driver is in every build already;
anything bringing a driver of its own belongs beside them the way the cloud
caches do.

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
and a SQLite file per store that a crawl actually asks for. A job with no
entities and no events opens neither.

**The SQL is written once per question, not once per dialect.**
`internal/storage` is the seam the entity graph and the event log are written
against: `Greatest`, `Least`, `MergeJSON` and `Rebind`, which is the whole of
what differs between SQLite and Postgres in the queries here. It was measured
rather than assumed, and the answer was eight scalar `MAX`/`MIN` calls, one JSON
merge, the DSN and the placeholders. That is small enough that no ORM earns its
place, and the queries stay the design.

**SQLite is the decision, and the Postgres dialect is a measurement of the
distance rather than a backend.** Nothing constructs it: it exists so that the
size of the port is a fact in the repository instead of an estimate in
somebody's head, and it is held to that by `internal/storage`'s own tests. A
second backend gets built when there is a workload that needs one, and the
event log is where that will come from first, because events are the one thing
here that is not bounded.

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

**Correction, from building it.** The note here used to say the lease was
written as `SELECT ... FOR UPDATE`, which SQLite ignores and Postgres needs.
SQLite does not ignore it: it rejects the syntax outright. What it has instead
is `BEGIN IMMEDIATE`, taking the write lock when the transaction opens rather
than when it first writes, which is what the implementation uses. A port to
Postgres adds `FOR UPDATE SKIP LOCKED` at that point, because there the readers
are concurrent and the row has to be locked rather than the database. Still a
line rather than a rewrite, but worth knowing which line.

**There is no `leased` status,** and that is a performance decision rather than
a modelling one. A leased row is a waiting row that is not ready yet, which it
says with `ready_at`. The obvious schema, a second status, makes the lease ask
for one status OR another, and an OR over the leading column of an index is an
index the query cannot use. Measured: that shape scanned the whole frontier on
every lease, 47 ms at a hundred thousand URLs against the memory
implementation's 4.6.

The working shape is `(job, status)` equality and then the ordering columns, and
nothing else. Whether a row is ready, and whether its host is cooling, are
residuals checked per row as SQLite walks the index in policy order until one
passes.

| Lease | 1,000 URLs | 100,000 URLs |
| --- | --- | --- |
| Memory, the floor | 43 µs | 4.7 ms |
| SQLite, sorting | 965 µs | 47.9 ms |
| SQLite, walking an index | 357 µs | 369 µs |

Flat, which is the property that matters: a crawl leases once per page, so this
query is the ceiling on how fast anything can go. `EXPLAIN QUERY PLAN` is
asserted in a test rather than left to a benchmark somebody remembers to read.
`random` is exempt and always sorts, because sampling without regard to score is
a shuffle of everything waiting and no index can express one.

**That table is measured with politeness off, and this note used to stop here.**
`Config{}` has no rate, so no host ever cools, so the residual never skips
anything. Turn politeness on, which every real job does, and the walk is what
pays. Two cases, and they are different problems:

**Nothing is due.** A crawl of one site spends most of its life here: the host is
cooling, every waiting row is behind it, and finding that out by walking the urls
table means reading all of it, because SQLite cannot know a row fails the check
until it looks. That is 0.55 ms at a thousand URLs, 5.3 ms at ten thousand and
69 ms at a hundred thousand, and it is not asked once: every worker asks again
every `run.Idle` for the length of the delay, holding the write lock each time,
which also blocks the `Add` that queues what the crawl is finding. At a hundred
thousand URLs a one-site crawl spends more than a core proving it has nothing to
do.

Fixed by asking the right table. `hosts` holds a row for every host in the
frontier, so "is any host free at all" is one seek, and a lease that cannot
possibly succeed says so without reading the queue. 17 µs at a hundred thousand
URLs, and flat. `hosts_next_at` makes it flat in the number of hosts too: with
5,000 hosts all cooling it is 10 µs against 238 µs without the index. Held by a
test that asserts the cost as a ratio, because the plan was already right when
it was 69 ms and the plan assertion could not see it.

**Something is due, but the best rows are cooling.** Not fixed. Under `priority`
the lease hands out the best-scoring row and then cools that row's host, so the
head of the index becomes a run of cooling rows that grows by one host's worth
per lease, and every later lease in the same window walks it. Per-lease cost is
linear in leases-per-politeness-window and total work inside a window is
quadratic: at 50,000 URLs over 5,000 hosts with a 30-second delay, 1.7 ms after
250 leases, 3.8 ms after 500, 8 ms after 1,000, 17 ms after 2,000, dropping back
when the window turns over. Honouring `Crawl-delay` makes this more pressing
rather than less, because the delays sites actually ask for are tens of seconds.

The fix is Heritrix's shape and not an index: one queue per host, a heap of the
ready ones and a delay queue of the snoozed, so a cooling host leaves the
candidate set instead of sitting at the head of it. That makes the host the unit
of scheduling rather than the URL, which is what politeness has been saying all
along.

What is given up, and it is worth being honest: ad-hoc analytics over records
are not this store's job, and cross-machine writes to one job's frontier are not
possible without sharding first.

### Reconsidered, and kept

Weighed against Postgres, an embedded key-value store, Redis, DuckDB and
Parquet, and kept.

The frontier is not close. Politeness already forces one writer per host, the
lease is a transaction with an ordering in it rather than a row fetched by id,
and being able to answer "why was that URL never fetched" with a SELECT is worth
more during development than any of the alternatives offer.

Records are the piece most likely to move, and saying so is the point. Nothing
forces the choice the way politeness forces the frontier's: it is an append-and-
query workload, and columnar storage would suit the analytics better. What holds
it here for now is that marks are updated in place and `stale_records =
"discard"` is a delete, both of which columnar formats are bad at, so the
mutable half would need a relational store beside it anyway. DuckDB would also
cost the clean cross-compile, and one binary with nothing installed is worth
more than a query nobody has run yet.

**The pipeline owning that database privately is what keeps changing this
cheap.** Nothing else touches it, so swapping what is underneath is contained
rather than a migration.

**What would change it:** the distinctness query becoming hot. Counting how many
distinct values a property takes across the corpus is how a locator that found
the site's name rather than the headline is caught, and it is genuinely
analytical. At a few million records SQLite does it comfortably. As a live view
over tens of millions it would not, and then the records store moves and the
frontier stays exactly where it is.

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

### Secrets

A per-job secret has to travel from whoever submitted the job to whichever node
happens to run it. The environment cannot express that: there is one environment
and many jobs, and a shared node running work for two owners needs two sets of
credentials.

So the document holds a reference and never a value:

```hcl
job "acme" {
  start = ["https://acme.example/"]

  item "article" {
    property "title" {
      type = str
    }
  }

  downloader {
    plugin "cache" {
      backend    = "s3"
      bucket     = "acme-pages"
      access_key = secret("acme-s3-key")
      secret_key = secret("acme-s3-secret")
    }
  }
}
```

That is the strongest case for this, and it is stronger than site authentication:
a cache bucket is per job, ambient cloud credentials are per node, and only a
reference bridges the two. The ambient chain stays as the fallback when no
secret is named, so a laptop still needs nothing configured.

#### It is safe because plugin config is opaque

`secret()` is an HCL function, so what matters is when it is evaluated. Plugin
and exporter bodies are deliberately left undecoded and are decoded against the
plugin's own schema when the plugin is built. That decision was made so somebody
else could write a plugin without this package knowing its fields, and it gives
secret-safety as a side effect:

| | Sees |
| --- | --- |
| Parse | An unevaluated expression |
| [Job.Resolved] into KV | The expression. KV never holds the value |
| [Diff] | Raw source, so rotating a secret correctly reads as no change |
| `scour show` | The reference as written, redacted by construction |
| Plugin build, on the node | The value, resolved then and there |

Nothing had to be added to any of them.

**Which gives one rule: secrets live only in the opaque bodies.** Not in
`scheduler.rate` or `downloader.user_agent`, which are decoded eagerly and would
resolve into stored state. Those do not want secrets, so the rule costs nothing
and can be enforced rather than remembered.

#### The bucket has different rules from every other

Kept in `SCOUR_SECRETS`, and three things make that real rather than a file on
every node with extra steps.

**Values are sealed before they go in.** JetStream writes to disk in plaintext,
so a stream directory, a backup or a replica on a machine you would rather it
were not on would otherwise all be readable. AES-GCM with a cluster key that
comes from outside KV, an environment variable or a file, so the bucket holds
ciphertext. One root secret living outside the system is unavoidable: every
secret manager has one, and pretending otherwise only hides where it is.

**History is one.** Every other bucket wants revisions, and jobs keep ten so a
change can be seen. For secrets, history means retaining rotated credentials,
which is the opposite of what rotating is for. This is the one bucket that
breaks the rule, and it is written down here so nobody makes it consistent.

**Subject permissions.** A bucket is a stream, and NATS can restrict who reads
`$KV.SCOUR_SECRETS.>`. A node running only unauthenticated jobs with a local
cache has no business reading it.

#### Setting one, and not reading it back

```
scour secret set acme-s3-key      reads from stdin
scour secret ls                   names and when they were set
scour secret rm acme-s3-key
```

**There is no `get`.** You can set a secret and rotate it, and if you have lost
it you rotate it. Read-back exists mostly to be misused: pasted into a terminal,
kept in scrollback, attached to a ticket. `set` reads from stdin rather than an
argument for the same reason, since an argument is in the shell history and in
`ps` the moment it runs.

**A missing secret fails at submission.** A job naming `secret("acme-s3-key")`
when no such key exists should be refused when it is submitted, not three hours
into a crawl. The server checks the key is there without reading it, which is
one lookup and no decryption. Local `validate` cannot do this, which is the
split [CLI.md](CLI.md) already draws between what needs a server and what never
will.

**Rotation is deliberately unlike a classifier version.** A job pins
`climate@7` because retraining changes what the word means. A secret rotates
under a stable name because rotating does not change what it is for. The watch
drops the cached value, the next use resolves the new one, and no document
changes.

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
may be leased, a response in flight, and an item mid-graph. In one process
`internal/run` answers it with a stall bound: nothing making progress for longer
than `run.StallFor`, which is a constant longer than a lease plus the job's
politeness rate plus its fetch timeout, and the run ends as `Stalled` rather
than hanging. Both of the last two terms are there because a crawl legitimately
waits for each, and each was missing once. That bound is arithmetic over
settings this one process can see. Quiescence across four stages on four
machines is the genuinely new distributed-systems problem here, and it is still
open.

## Status

| Piece | State |
| --- | --- |
| `internal/cache` interface, registry, keying | Built, tested |
| Backends: local, s3, gcs | Built, one shared contract suite, all passing |
| `internal/registry`, generic, shared by every extension point | Built, cache runs on it |
| `internal/chain`, middleware that wraps, with drop and short-circuit | Built, tested |
| `internal/plugin`, the seam from a job's plugin list to a chain | Built, tested |
| `internal/urls`: what makes two addresses one page | Built, tested |
| `internal/scope`: domains, included, excluded, one implementation | Built, tested |
| The scheduler: the chain, scope, budget, politeness | Built, tested |
| The `dupefilter` middleware | Built, tested |
| `internal/extract`: four ways to find a value, provenance on each | Built, tested |
| Fill rates, measured over a corpus and held to a floor | Built, tested |
| The spider: the chain, link discovery, the spec fingerprint | Built, tested |
| The `httperror` middleware | Built, tested |
| `internal/record`: the flat form, and the measurement rendering | Built, tested |
| The pipeline: waves, concurrency, clean, validate, dedupe, rank | Built, tested |
| The `entities` step: a crawl feeding the entity store | Built, tested |
| Exporters: the registry, json, jsonlines, csv, parquet, nats, sqlite | Built, tested |
| `internal/run`: the whole crawl, four stages wired directly | Built, tested |
| `scour try` and `scour run` | Built, tested |
| `internal/bus`: NATS, embedded or joined, downloader and spider as services | Built, tested |
| The equivalence: same job, same records, either wiring | Held to a test |
| `internal/downloader`: the core fetch, agent, timeout, body limit | Built, tested |
| The `cache` middleware: hits, sidecar, ttl, statuses | Built, tested |
| `internal/robots`: RFC 9309, written rather than imported | Built, tested |
| robots.txt obeyed, outside the whole chain | Built, tested |
| `Crawl-delay` carried back to the frontier and paced against | Built, tested |
| Redirects followed, every hop checked and cached on its own | Built, tested |
| HCL job document, stage blocks, nested plugins, multiple jobs | Built, tested |
| Vocabulary: bare types and transforms | Built, tested |
| Validation, every problem reported at once | Built, tested |
| Chain ordering, catalogued defaults | Built, tested |
| Pipeline graph: references, cycles, topological order, waves | Built, tested |
| Extraction spec: separable, renders to HCL, fingerprinted | Built, tested |
| Defaults for every setting, in one file, with a resolved view | Built, tested |
| Resubmission diff and mutation policy | Built, tested |
| Classifiers: the contract, terms and bayes | Built, tested |
| `internal/classify/store`: shared, versioned, one file per training | Built, tested |
| The `topic` middleware in the spider | Built, tested |
| Secrets in a sealed KV bucket, resolved at plugin build | Built, tested |
| S3 and GCS taking an explicit credential from one | Built, tested |
| Entity store: typed entities, relations, assertions with provenance | Built, tested |
| Entity identity resolution: aliases, one conservative rule | Built, tested |
| Entity and relation properties, declared and extracted | Built, tested |
| `internal/entity`, `internal/event`: an interface, a registry, a backend | Built, one contract suite each, every registered backend run through it |
| `internal/storage`: the SQL dialect seam | Built, tested. Only SQLite is constructed |
| Events as items: tags, fields, time, and the nats exporter publishing them | Built, tested |
| Parquet as the measurement archive | Designed. The exporter writes records |
| Entity recognition and linking | Designed, not started |
| `internal/bus`: the entity, event and topic services on one connection | Built, tested |
| `engine.ParseService`: a service document, separate from a job | Built, tested |
| `scour service`: all three stores from one file | Built, tested |
| `internal/classify/source`: a topic from a directory or from the cluster | Built, tested |
| `scour topic ls / show / rm / propose / train`, from a labels document | Built, tested |
| The `topic` middleware, in the spider and in the scheduler | Built, tested |
| Plugin implementations: `cache`, `dupefilter`, `httperror`, `topic` twice | Built, tested. The rest of the catalogue is positions, not parts |
| Jobs and nodes in NATS KV, watched | Built, tested |
| `internal/node`: join, watch, serve, drain and stop. Two nodes, one job, work on both | Built, tested |
| A crawl driven over the bus | Not built. `run.Options.Fetch` and `Read` have no caller but the bus's tests |
| `internal/train`: locators induced from the corpus, written back | Built, tested |
| `scour serve`, `scour secret`, `scour train` | Built, tested |
| Frontier in SQLite, one shared database | Built, tested, benchmarked |
| Records in SQLite, one database per job | The sqlite exporter writes one. Marks, `stale_records` and re-extraction not built |
| Run state in a KV bucket of its own | Decided, not started |
| Applying a change to a running job | `engine.Diff` and every `Effect` are built and tested, and nothing calls them |

`internal/engine/notes_test.go` and `internal/engine/bodies_test.go` read this
file, and between them they are why it can be trusted:

- **The job documents in it are parsed and validated**, so an example that has
  drifted from the schema is a failing build rather than something somebody
  discovers by typing it.
- **The service and labels documents are parsed too**, by their own parsers,
  which decode strictly.
- **Every plugin and exporter block is decoded** against the schema the
  implementation itself uses. Those bodies are opaque to the engine by design,
  which made them the one part of an example nothing had ever checked, and two
  wrong field names had been sitting here for a while when the check was added.
- **Every number in the catalogue tables** is compared with the catalogue the
  code uses, and the pipeline kinds with the kinds it ships.
- **Every bare word in an example** is checked against the vocabulary the parser
  resolves against.
- **The prose about what may be replaced and what may be extended** is held to
  what the stages actually allow.

If the notes and the code disagree, the tests fail, which is the only way a
document like this stays true. It is not a guarantee that everything here is
right: a claim nothing covers can still be wrong, and the way to add one is to
add the check with it.
