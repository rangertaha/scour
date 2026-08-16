# Local until it has to be shared

*Chapter nine of [the scour book](README.md).*

A crawler is a thing you argue with before you run it. So the commands that
read a document need nothing running at all, and the line between those and
the rest is the first thing worth knowing about the command line.

```mermaid
flowchart TB
  subgraph CLUSTER["needs a cluster: a broker, and whatever it is keeping for you"]
    direction TB
    subgraph DIR["needs a directory here: the cache, and a frontier of its own"]
      direction TB
      subgraph DOC["needs nothing but the document"]
        L["init · validate · show · spec · defaults<br/><br/>offline, in CI, on a plane"]
      end
      M["try · run · train · topic"]
    end
    O["serve · service · secret"]
  end
```

<details>
<summary>What this diagram shows</summary>

Three nested rings of commands. The innermost holds init, validate, show, spec
and defaults, which need nothing but the document. Around it, try, run, train
and topic, which need a directory on this machine for the cache and the
frontier. Around those, serve, service and secret, which need a cluster. A
command in an outer ring needs everything in the rings inside it.

</details>

*What has to exist before a command can work. The innermost ring is the one
you are in while a job is still being written, which is most of the time.*

## The loop

Start from something that already validates, look at one page, then crawl a
few hundred and let induction propose the locators the guessing missed.

```console
$ scour init --list
basic      The plainest job that works. Start here
listing    A directory of entries: jobs, venues, courses
news       Articles: headline, byline, dates, body
product    A shop: name, price, availability, images

$ scour init news > news.hcl
$ scour validate news.hcl
news.hcl: ok, 1 job(s): news
```

**`try` is the one you will type most.** One page, fetched once and cached, so
the second run and the twentieth cost nothing and the site is asked once.
Against each property it prints the value and which of the four ways found it,
which is the only way to tell a locator that works from one that has never
been tested.

```console
$ scour try news.hcl
fetched https://example.com/  200  559 B  text/html  (125ms)

article
  title  "Example Domain"                    <h1>
  url    -                                   required, found nothing

1 of 2 properties found. 1 links.
```

The line with nothing on it is the interesting one, and it is why `try`
exists. Nothing is wrong with the document: the guess simply did not land on
this page, and the answer is an alias, a locator, or an example for training
to work from. What is listed is what was found plus the *required* properties
that were not, so an optional property that found nothing is absent from both
the output and the count, which is worth knowing before you read too much into
the ratio.

```console
$ scour run news.hcl
crawling news: 1 seeded, 1 queued
finished in 156ms
  fetched   1 (1 from the cache)
  dropped   0
  failed    0
  items     1
  exported  1
  wrote     json.article

$ scour train news.hcl
read 1 cached pages
  article.title                h1                           1/1 pages  "Example Domain"
nothing written. Pass --write to edit news.hcl
```

**The crawl reused what `try` had already fetched.** One page, asked for once,
whichever command wanted it. That is the cache doing the thing the whole
corpus argument is about, and it is why the loop above can be run twenty times
over a lunch break without leaning on anybody's server.

**Training proposes and does not commit.** It reads the cache and never the
network, so it is free and repeatable, and it prints what it would write until
`--write` says otherwise. What it does write is marked with a comment, and
only what is marked is ever overwritten: delete the comment and that locator
is yours for good. That one rule is what makes the loop converge instead of
going round.

> **Why the whole crawl runs here**
>
> `scour run` is not a demonstration mode. It is the same four stages the
> cluster wires over the bus, wired directly instead, and a test holds one
> job to producing the same records either way. A laptop is a complete
> deployment, which is what makes the loop above possible at all.

## The surface

### Reading a document

These need nothing running.

```
scour init [name]          Print a starter job document
scour validate <file>      Check it. Report every problem at once
scour show <file>          The resolved job: every default filled in
scour spec <file>          What a spider is handed, as HCL
scour defaults             Every default and its value
```

#### `scour init [name]`

Prints a job that validates as it stands, so it can be run and then grown. To
stdout, so it composes.

| Flag | Effect |
| --- | --- |
| `-t`, `--template <name>` | Which starting point. Defaults to `basic` |
| `--list` | List the templates and what they are for |
| `-o`, `--out <file>` | Write to a file instead of stdout |
| `--force` | Overwrite the file if it is already there |

**The templates differ in what they extract, not in how they crawl.** All of
them are polite, budgeted and cached, because a starting point gets copied
without being read and its defaults are what most crawls will actually run at.
Tests hold them to that: robots on, no faster than the default rate, a page
ceiling, and a concurrency nobody would notice.

**None of them contains a locator.** No `xpath`, no `css`, because the ones that
work depend on the site and a wrong selector shipped in a template is worse than
an absent one. They carry `aliases` instead, which is how a property is found
before anything has been learned, and `scour train` proposes the rest once there
are pages to look at.

**And none of them starts outside its own scope.** Three of the four shipped
with `included = ["*.example.com"]` beside `domains = ["example.com"]`, written
meaning "this site and below it", which is what `domains` already says. As an
inclusion pattern it means something else, and it refused the apex the job
started from, so the first thing anybody did crawled nothing and said it was
fine. That is now refused outright rather than warned about.

Writing to a file refuses to clobber one, because somebody running this twice in
a directory they have been working in should not lose what they wrote the first
time. Printing to stdout has no such problem, which is why it is the default.

A test renders every template, validates it, and checks it extracts something
and has somewhere to put it. A sample that does not work is worse than none,
since the first thing anybody does with it is assume it does.

#### `scour validate <file>`

Parses and validates, reporting everything wrong at once.

It does not reach the network, so it works offline and in CI, and it cannot know
whether a plugin somebody else's node registers exists. That answer arrives when
a chain is built, on the machine that has the implementations.

```console
$ scour validate job.hcl
job.hcl: ok, 1 job(s): news

$ scour validate broken.hcl
broken.hcl: refused
  job "news": start[0] "file:///etc/passwd": only http and https are crawled
  job "news": scheduler.concurrency: 999 is more than 64 against a single host
  job "news": item article.title: has nested properties but is typed str, which only object can hold
$ echo $?
1
```

#### `scour show <file>`

What the document means once every default is filled in: the settings, the
chains in the order they run, the items, and the pipeline as the waves it will
run in.

Deliberately not called `plan`. A plan is a comparison against something
running; a resolved job is what a document means on its own. Giving one name to
both would mean the command changed meaning the day the server arrived.

| Flag | Effect |
| --- | --- |
| `--json` | Print as JSON |
| `--job <name>` | Which job, if the document holds several |

```console
$ scour show news.hcl
job "news"

scope
  start          https://example.com/
  domains        example.com

scheduler
  policy         priority
  rate           2s
  concurrency    2
  max_depth      3
  max_pages      100
  max_time       no limit
  chain          empty

downloader
  robots         true
  user_agent     scour
  timeout        30s
  max_body       33554432
  max_redirects  10
  chain          cache(900)
...
pipeline: 2 wave(s), 2 at once at the widest
  1. clean.article
  2. rank.article, score.article
```

`rate` here is what the job asked for, and it is a floor rather than the whole
answer. With `robots` on, a site asking for longer in its own `Crawl-delay` gets
it: the wait is whichever of the two is longer, so this crawl would leave a host
requesting thirty seconds alone for thirty and one requesting one alone for two.
The number a site asked for is learnt on the first fetch of that host and applies
from then on, including to the crawl that learnt it.

The chain is printed in the order it runs, with the order numbers, because that
is the whole reason the numbers exist. The pipeline is printed as waves rather
than as a list, because a list hides the concurrency that is the point of having
a graph.

#### `scour spec <file>`

What a spider is handed: the shapes to extract and nothing else. Not where
bodies are cached, not the budget, not the exporters.

| Flag | Effect |
| --- | --- |
| `--job <name>` | Which job, if the document holds several |

The spec goes to stdout alone and the fingerprint to stderr, so
`scour spec job.hcl > spec.hcl` writes a spec rather than a spec with a note in
the middle of it.

#### `scour defaults`

Every default and its value, which is otherwise answerable only by reading the
source.

| Flag | Effect |
| --- | --- |
| `--json` | Print as JSON |

### Developing a job

The loop a person is actually in: change a selector, see what it pulls out, and
do it again. It has to be fast, which means it must not touch the network twice
for the same page.

```
scour try <file> [url]     Run one page and show what came out
scour run <file>           Crawl a job here, without a server
scour train <file>         Read the cache, propose locators, write them back
```

#### `scour try <file> [url]`

Fetches one page, caches it, and runs it through extraction, printing what each
property found.

**The cache is used first, always.** A URL already in the cache is not fetched
again, so the second run and the two hundredth cost nothing and the site is
asked once. That is the whole reason this is usable as a development loop: the
edit-run cycle is against bytes on disk, not against somebody's server.

| Flag | Effect |
| --- | --- |
| `--job <name>` | Which job, if the document holds several |
| `--url <url>` | The page, if not given positionally. Defaults to the job's first start URL |
| `--refresh` | Fetch even if it is cached, and replace what is there |
| `--item <name>` | Only this shape |
| `--strict` | Exit non-zero if a required property found nothing |
| `--json` | Print as JSON |

```console
$ scour try news.hcl https://example.com/story/1
fetched  https://example.com/story/1  200  48.2 kB  text/html  (cached)

article
  url        https://example.com/story/1        <link rel=canonical>
  title      "Something happened yesterday"     <h1 class=headline>
  published  2026-08-04T09:15:00Z               <time datetime>
  body       4,812 characters                   <div class=article-body>

4 of 4 properties found.
```

Showing *where* each value came from is the point. A value on its own does not
tell you whether the locator will hold on the next page; the node it came from
does.

`--strict` is for CI: a job whose `required` properties stopped matching has
broken, and that should fail a build rather than quietly export nothing.

#### `scour run <file>`

Runs the whole engine in this process: scheduler, downloader, spider, pipeline
and exporters, wired to each other directly. No server, no cluster, no broker.

**It resumes.** The frontier is a file under `.scour` beside the document, so a
crawl that was stopped, or that hit its budget, continues where it left off.

| Flag | Effect |
| --- | --- |
| `--job <name>` | Which job, if the document holds several |
| `--dir <path>` | Where to keep the frontier and the cache |
| `--verbose`, `-v` | Log every page |
| `--fresh` | Forget what a previous run queued |

```console
$ scour run news.hcl
crawling news: 1 seeded, 1 queued
finished in 2.4s
  fetched   48 (12 from the cache)
  dropped   9
  failed    0
  items     41
  exported  41
  wrote     jsonlines.article
```

The summary says *why* it ended, because a crawl that finished a site and one
that ran out of budget look identical otherwise and mean opposite things.

The exporters are flushed and closed before that summary is printed, so what it
says was written has been. Reporting first and closing afterwards meant a write
that failed on the way out was invisible: the count was the number of records
handed over, not the number that landed, and a truncated export exited zero.

This is also the thing a cluster has to be equivalent to: the same job on
several nodes should produce the same records, and being able to run both is
what makes that checkable rather than merely claimed.

#### `scour train <file>`

Reads the pages already in the cache, works out how to find each property, and
writes the locators back into the document.

**The locators go into the document, as text.** Not into a model file, not into
a database. The reason is that induction is a guess and a guess should be
readable: `css = [".article-body h1"]` is something a person can look at,
disagree with, correct, and commit. A binary model can only be trusted or
retrained. It also means the crawl has no runtime dependency on anything
training produced: the document is complete.

**It reads the cache and never the network,** so training is free, repeatable
and offline. The same corpus produces the same locators, which is what makes a
change to induction measurable rather than merely different.

```console
$ scour train news.hcl
read 312 cached pages
  article.title                .headline                    308/312 pages  "Something happened yesterday"
  article.published_time       time[itemprop="datePublished"] 295/312 pages  "2026-08-04T09:15:00Z"
  article.body                 .article-body                311/312 pages  4812 characters
  article.author               kept, and never replaced
nothing written. Pass --write to edit news.hcl
```

| Flag | Effect |
| --- | --- |
| `--job <name>` | Which job, if the document holds several |
| `--item <name>` | Only this shape |
| `--dir <path>` | Where the cache is |
| `--pages <n>` | How many cached pages to learn from |
| `--min <n>` | Ignore a locator matching fewer than this share of pages, as a percentage |
| `--write` | Edit the document instead of printing what would change |

**Teaching by example goes in the document,** not on the command line. A
property's `examples` are values it is known to have taken, and given the answer
induction can look for the node that produces it and generalise across the
corpus. Written down, an example survives; typed into a shell, it is gone when
the terminal is. This is built: `train.Learn` looks for each example on the page
before it falls back to what extraction already finds.

There is no `--replace`. What may be overwritten is decided by the document
rather than by a flag, and the rule below is the whole of it.

##### Teaching it an answer

**Designed, not built.** The `examples` in the document are what training reads
today. What follows is the command-line shortcut for putting one there, and
nothing implements it yet: `scour train` takes no `--url` and no `-i`.

Induction over a corpus can find what is *consistent*. It cannot know which
consistent thing you wanted, and on a page with three plausible headings it will
pick one and be confidently wrong. So you would hand it the answer.

The address is `<item>.<property>[.<part>]`, where the part says which bit of the
node to compare against:

| Part | Compares against |
| --- | --- |
| `text` | The node's text. The default, so it can be left out |
| `html` | The node's markup, for a property that keeps the formatting |
| `attr=<name>` | An attribute, for a URL in an `href` or a date in a `datetime` |

Given a value, training looks through the cached page for nodes that produce it,
generalises a locator across the rest of the corpus, and reports how far it got.
Three outcomes are worth naming because they are all useful:

- **One node produces it.** The locator is induced from there and checked
  against every other cached page.
- **Nothing produces it.** An error, and a good one: either the value is not on
  that page, or a transform is changing it before the comparison. Both are worth
  knowing before a crawl runs on the assumption.
- **Several nodes produce it.** The candidates are listed with their locators,
  and a more specific example is asked for. Guessing here is how a locator ends
  up pinned to the wrong one of two identical headings.

##### Where an example lives

In the document:

```hcl
property "title" {
  type     = str
  aliases  = ["headline"]
  examples = ["Hello World"]
}
```

**Examples belong in the job, not in a flag.** A flag teaches once and is gone,
so the next person to run `train` gets a different answer for no visible reason.
In the document they are evidence that travels with the shape, they are
reviewed in a diff like everything else, and re-running training is
reproducible.

They are evidence rather than configuration, which has one consequence worth
stating: **an example is not part of the spec's fingerprint.** Adding one does
not change what is being extracted, so it must not read as a schema change and
force a re-extraction of records that are still correct. An example that stops
matching is a signal that the site changed, not a setting that has gone stale.

Two rules about writing, and they are the ones worth arguing with:

**A locator a person wrote is never replaced.** Not by a flag: what training
wrote is marked, with `# induced by scour train; delete this comment to keep
your own`, and only what is marked is ever overwritten. Deleting the comment is
how a person says this one is theirs now, so the state lives in the document and
nowhere else. The alternative, a `--replace` somebody remembers to leave off,
loses a correction the first time they forget. The loop has to converge instead
of going in circles: correct it, retrain, lose the correction, correct it again.

**Comments and formatting survive.** The document is edited rather than
regenerated, so the notes somebody left themselves are still there afterwards.
That is why `internal/train/write.go` edits the lines a locator belongs on
rather than reprinting the file from its parse tree: a round trip through a
parser and a printer returns something equivalent and unrecognisable, and a diff
nobody can read is a diff nobody reviews. Reviewing what induction proposed is
the entire point, so the less clever approach is the right one.

The match count is written beside each locator as a comment, because a locator
that worked on 97 of 312 pages and one that worked on 311 deserve different
amounts of trust, and that number is invisible once the guess is in the file.

**Three hundred pages is enough.** Cost is linear in bytes at roughly half a
second a page, and 800 pages was not meaningfully better than 300. Crawl a few
hundred, train, and spend the time you saved looking at what it proposed.

**Train across several sites if the job crawls several sites.** A locator
induced from one site will be pinned to that site's markup, and the failure is
invisible until the second site quietly extracts nothing.

### Running a node

```
scour serve                 Serve stages for whatever jobs the cluster has
scour service <file.hcl>    Run the entity graph, the event store and the topics
```

#### `scour serve`

A node joins a cluster, watches the jobs and serves the stages it offers for
every job that appears. Nothing elects anything and nothing assigns anything:
work is distributed by queue group, so adding a machine is a matter of starting
one.

With no `--join` it starts a broker in this process and prints the address the
next node should join, which is what makes a single node need nothing installed.

| Flag | Effect |
| --- | --- |
| `--join <url>` | A node to join, as `nats://host:port` |
| `--name <name>` | What to call this node. Defaults to the hostname |
| `--dir <path>` | Where to keep the cache and the cluster's state |
| `--stages <list>` | Which stages to serve: `download`, `read`, or both |
| `--quiet` | Say nothing but failures |

A stage nothing serves is refused before the node announces itself, naming what
the stages are. A typo is the likeliest thing to be wrong with that flag, and
until it was checked at the door `--stages downlaod` connected, announced the
capacity into the registry, printed that it was serving, and then answered
nothing for the rest of its life while logging one warning per job.

```console
$ scour serve
node-a is serving, and is the broker
join it with: scour serve --join nats://127.0.0.1:41923

$ scour serve --join nats://127.0.0.1:41923 --name node-b
node-b joined nats://127.0.0.1:41923
```

**One node per job still drives the crawl.** It owns the frontier, and the
frontier cannot be shared: two schedulers handing out the same host cannot
honour a crawl delay between them. Every other node serves stages. That
asymmetry is the politeness rule rather than a limitation to be lifted.

#### `scour service <file.hcl>`

The stores a cluster shares, on the bus, until interrupted.

| Flag | Effect |
| --- | --- |
| `--join <url>` | The cluster, as `nats://host:port` |

A service document is not a job document. A job says it wants entities; it does
not say where they live, and the difference matters more than it looks. The
entity graph is shared between jobs, which is the whole of its value: two jobs
crawling different sites should agree about who Acme is. A job document carries
everything one crawl needs so that a job resubmitted next month does what it did
today, so an address in it would mean whichever job was submitted last silently
decided where every other job's entities went, and a job moved between clusters
would carry the old cluster's address with it.

```hcl
entity {
  dir = "./graph"
}

event {
  dir = "./events"
}

topic {
  dir = "./topics"
}
```

| Field | Effect |
| --- | --- |
| `dir` | Where the store lives. Required: a store that vanishes on restart is one every writer believes it wrote to |
| `url` | The bus to answer on. Empty starts one in this process |
| `timeout` | How long one request may take. Default `30s` |

```
$ scour service service.hcl
entities: serving ./graph on scour.entity.*
events: serving ./events on scour.event.*
topics: serving ./topics on scour.topic.*
listening on nats://127.0.0.1:41923
ready. Interrupt to stop
```

**A node fetches topics from here.** A job's `topic` middleware takes a `url`
instead of a `dir`, and a node that has joined a cluster has no trained topics
on its disk:

```hcl
plugin "topic" {
  subject = "climate@7"
  url     = "nats://127.0.0.1:4222"
}
```

**A topic travels; a page does not.** A client fetches a trained topic once,
when the chain is built, and scores locally from then on. The scheduler scores
every URL it is offered and the spider every page it reads, so a request per
page would put the network in the hottest loop in the crawl, which is the same
reason a fetched body never crosses the bus and a cache key goes instead.

**The stores have one writer each**, because they are SQLite. That is why they
are behind a service rather than a file each node opens: a cluster where every
node opened the file would be one where the answer depended on which node you
asked, and where two nodes writing at once failed. Running a second
`scour service` against the same document joins the queue group as a standby
that shares the load, not as a second writer.

### Teaching it a subject

```
scour topic ls                      What has been trained
scour topic propose <labels.hcl>    Label the cached corpus from seed terms
scour topic train <labels.hcl>      Learn from the labels, writing the next version
scour topic show <name@version>     What it learned
scour topic rm <name@version>
```

#### `scour topic`

A topic is a trained classifier a job refers to by name and version, as
`climate@7`. The scheduler scores every URL against it and puts the promising
ones at the front of the frontier; the spider scores every page and can drop the
ones that are not the subject. That is what makes a focused crawl focused.

| Flag | Effect |
| --- | --- |
| `--dir <path>` | Where the trained topics live. Default `.scour/topics` |
| `--corpus <path>` | Where the cached pages are. Default `.scour/cache` |
| `--pages <n>` | How many cached pages to look at |
| `--write` | `propose` only: edit the document instead of printing what would change |

**A topic is learned from a labels document**, which is a file you own:

```hcl
topic "climate" {
  terms = ["emissions", "decarbonisation", "carbon budget"]

  about = [
    "https://example.com/story/1",
  ]

  not = [
    "https://example.com/sport/2",
  ]
}
```

The loop is propose, correct, train:

```
$ scour topic propose labels.hcl --write
read 412 cached pages
  climate              38 proposed as the subject, 374 as not
wrote 412 proposals into labels.hcl. Correct them, then `scour topic train labels.hcl`

$ scour topic train labels.hcl
climate@1 trained on 412 examples
  strongest emissions, decarbonisation, climate, carbon, warming, targets, fuels, coal
  use it with: subject = "climate@1"
```

**The seed terms are a bootstrap, not the classifier.** A page holding enough of
them is proposed as an example, which is a worse classifier than the one being
trained and is meant to be: it is a first pass somebody corrects, and what they
correct is what wins. A model that only ever learned what it was already told
would have been a term list all along.

**The labels are a file for the same reason locators are.** A classifier trained
from state inside the tool says a page is about climate and there is nowhere to
go and look at why. `propose` never overwrites a decision somebody already made,
so a correction stays corrected across runs.

**Training writes the next version and never replaces one.** A job pins
`climate@7` precisely so that somebody else retraining cannot change what it
does, which is why the version is required in a job and why `rm` takes one
version rather than a subject.

A model is replaced by writing a new file beside the old one and renaming it
over the top, so a reader either gets the whole of one version or the whole of
the other. Writing in place truncated a file that `scour service` was serving
from, and a node asking for a model while somebody corrected it got half of one.

### Secrets

```
scour secret key            Print a new sealing key, once
scour secret set <name>     Store one, read from stdin
scour secret ls             The names that have been set
scour secret rm <name>
```

#### `scour secret`

| Flag | Effect |
| --- | --- |
| `--join <url>` | The cluster, as `nats://host:port` |
| `--key-file <path>` | The sealing key, if it is not in `SCOUR_SECRET_KEY` |

On the subcommands rather than on `secret` itself, which takes none: every one
of them talks to the cluster, and `key` is the exception that needs neither.

A job holds a reference, never a value: `access_key = secret("acme-s3-key")`.
The document stays safe to commit, diff and paste into an issue, and the value
is resolved on the node that needs it, when it needs it.

**There is no `scour secret get`.** You can set one and rotate it, and if you
have lost it you rotate it. Read-back exists mostly to be misused: pasted into a
terminal, left in scrollback, attached to a ticket.

`set` reads from stdin rather than taking an argument, for the same reason. An
argument is in the shell history and in `ps` output the moment it runs.

```console
$ pbpaste | scour secret set acme-s3-key
stored acme-s3-key

$ scour secret ls
acme-s3-key       set 2026-08-04
acme-s3-secret    set 2026-08-04
```

## A setting belongs in the document, not in a flag

Every flag here is about *this invocation*: which job, which item, where the
cache is, whether to write. Nothing that decides what a crawl does is a flag,
and that is a rule rather than an accident.

A flag is typed once and gone. The next person runs the same command and gets
a different answer with nothing to show why, and the crawl that ran at three
in the morning cannot be reconstructed from anything. The document is the
whole of what a job does, so what a job does is written down, reviewed in a
diff, and the same next month.

```hcl
mutation {
  costly = "refuse"
}
```

**So there is no `--auto-approve`.** Whether a costly change may be applied is
the job's own statement, travelling with the job, so a machine submitting it
gets the same answer as a person. A flag that overrode it would make the
document advisory, and the first thing anybody would do is put the flag in a
script and forget it is there.

The cost is real: changing a policy for one run means editing the document.
That is the intent, and it is the part most likely to be wrong.

The same argument retires two flags that sound useful. **There is no
`--replace`** for training: what may be overwritten is decided by the marker
comment in the document, so a correction cannot be lost by forgetting a flag.
And **examples are a property's own field**, not something you teach at a
prompt, so they survive, they are reviewed like everything else, and re-
training is reproducible.

## Rules

**Every problem at once, never the first.** A person fixing a document one
error per run gives up, and so does a build script.

**A file argument is positional.** `scour validate job.hcl`, not
`scour validate --file job.hcl`. It is the subject of the sentence.

**One document at a time.** A document can hold several jobs, and they are
accepted or refused together. Two documents at once would need rules about what
happens when the first is accepted and the second is not.

**A job is named when it is ambiguous, and not otherwise.** A document holding
one job needs no name on the command line. A document holding three and a
command given no name is refused rather than guessed at.

### Exit codes mean something

| Code | Meaning |
| --- | --- |
| 0 | It worked |
| 1 | The document was read and refused |
| 2 | The command line itself was wrong |
| 3 | scour could not do it, for a reason that is not the document's fault |

A wrong document and a broken tool are different things, and a script needs to
tell them apart: the first means fix your file, the second means the tool or
the machine needs looking at. Conflating them is how a broken build gets
retried forever.

```console
$ scour validate broken.hcl
scour: config: broken.hcl:4,27-31: Unknown variable; There is no variable
named "strr". Did you mean "str"?, and 1 other diagnostic(s)

$ scour validate nope.hcl
scour: open nope.hcl: no such file or directory
```

The first exits 1 and the second exits 3, and the difference is the whole
point: one of those files is wrong and the other was never read. A remote
command that cannot reach a server fails with 3 for the same reason: the
document was not refused, nothing looked at it.

### Machine output on stdout, commentary on stderr

So `scour spec job.hcl > spec.hcl` writes a spec and not a spec with a
progress line in the middle of it. Anything with structure takes `--json`, and
human output is the default because the common case is a person. There is
never a third format for somebody to maintain.

## What exists today

Built and tested. This is the whole of what the binary has:

| Command | Needs |
| --- | --- |
| `scour init` | Nothing |
| `scour validate` | Nothing |
| `scour show` | Nothing |
| `scour spec` | Nothing |
| `scour defaults` | Nothing |
| `scour try` | The cache on disk |
| `scour run` | A directory for the frontier and the cache |
| `scour train` | The cache on disk |
| `scour topic` | A directory of trained topics, or a cluster |
| `scour serve` | Nothing, or `--join` to a cluster |
| `scour service` | A directory per store, and a cluster to answer on |
| `scour secret` | A cluster, and a sealing key |

The first five need only the engine package, which is why they work offline, in
CI and on a plane. `run` is the whole crawl in one process: the same four stages
the cluster wires over the bus, wired directly, and held to producing the same
records either way.

> **Not built yet**
>
> `plan`, `apply`, `ls`, `status`, `logs`, `pause`, `resume`, `stop`, `rm`,
> `records`, `nodes` and `version`. Every one of them is a question about
> running state, which the command line deliberately does not hold, so each
> needs a server that does, and the `--server` and `--timeout` flags arrive
> with them. `plan` is the one that earns the comparison with Terraform: the
> engine already computes a diff with an effect per change, so it can say that
> raising a page budget is free and that narrowing scope will drop 1,204
> queued URLs, before anything happens. The diff is built and tested; nothing
> calls it yet.

> **How this chapter is held to the binary**
>
> `cmd/scour/cli_doc_test.go` reads this file. It checks the table above
> against the commands the binary has, in both directions, and every flag
> against the command it is written under, also in both directions: a flag
> that exists and is undocumented fails, and so does a flag documented here
> that nothing takes. The second direction was added after this text spent a
> while promising `scour train --url`, `-i` and `--replace`, none of which
> were ever built, and after it said five commands were built when there were
> twelve.

---

[Back: A graph, not a list](08-pipeline.md) · [Next: Where everything lives](10-storage.md)
