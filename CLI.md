# The command line

A design for the command surface. [NOTES.md](NOTES.md) is the architecture and
[PLAN.md](PLAN.md) is the build order; this is what a person types.

Nothing here is settled. It is written down so the shape can be argued with
before there are commands nobody wants to rename.

## The shape it already has

The job document is declarative, resubmitting a name diffs against what is
running, and the `mutation` block says what applying a change costs. That is
Terraform's shape, arrived at from the other direction, so the command line
should read like Terraform rather than inventing a third vocabulary for the
same three ideas:

```
scour validate job.hcl     # would this be accepted?
scour plan job.hcl         # what would applying it do?
scour apply job.hcl        # do it
```

`plan` is the one that earns the comparison. The engine already computes a diff
with an effect per change, so a plan can say *raising a page budget is free* and
*narrowing scope will drop 1,204 queued URLs* before anything happens.

## Flat, not nested

`scour validate`, not `scour job validate`.

A job is the only thing the document commands can act on, so naming it adds a
word to every line and distinguishes nothing. Terraform is flat and nobody
wishes it were `terraform config plan`. Where a second noun genuinely exists it
is its own plural command: `scour records`, `scour nodes`.

## The surface

### Reading a document

These need nothing running, and they are what exists today.

```
scour init [name]          Print a starter job document
scour validate <file>      Check it. Report every problem at once
scour show <file>          The resolved job: every default filled in
scour spec <file>          What a spider is handed, as HCL
scour defaults             Every default and its value
```

#### `scour init [name]`

Prints a small, commented job that validates as it stands, so it can be run and
then grown. To stdout, so it composes:

```console
$ scour init news > news.hcl
$ scour validate news.hcl
news.hcl: ok, 1 job(s): news
```

| Flag | Effect |
| --- | --- |
| `-o`, `--out <file>` | Write to a file instead of stdout |
| `--force` | Overwrite the file if it is already there |

Writing to a file refuses to clobber one, because somebody running this twice in
a directory they have been working in should not lose what they wrote the first
time. Printing to stdout has no such problem, which is why it is the default.

A test asserts that what `init` prints validates. A sample that does not work is
worse than none, since the first thing anybody does with it is assume it does.

#### `scour validate <file>`

Parses and validates, reporting everything wrong at once.

It does not reach the network, so it works offline and in CI, and it cannot know
whether a plugin somebody else's node registers exists. `plan` is where that
becomes an error.

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
  chain          cache(900)
...
pipeline: 2 wave(s), 2 at once at the widest
  1. clean.article
  2. rank.article, score.article
```

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

#### `scour train <file>`

Reads the pages already in the cache, works out how to find each property, and
writes the locators back into the document.

**The locators go into the document, as text.** Not into a model file, not into
a database. The reason is that induction is a guess and a guess should be
readable: `xpath = ["//h1[@class='headline']"]` is something a person can look
at, disagree with, correct, and commit. A binary model can only be trusted or
retrained.

```console
$ scour train news.hcl
read 312 pages from the cache

article
  title      //h1[@class='headline']         308/312 pages
  published  //time/@datetime                295/312
  body       //div[@class='article-body']    311/312
  author     //span[@class='byline']          97/312   weak

Writing to news.hcl would change 4 properties. Pass --write to do it.
```

| Flag | Effect |
| --- | --- |
| `--write` | Edit the document in place instead of printing what would change |
| `--item <name>` | Only this shape |
| `--min <n>` | Ignore a locator matching fewer than this share of pages, as a percentage |
| `--replace` | Also replace locators that are already there |

Two rules about writing, and they are the ones worth arguing with:

**A locator a person wrote is never replaced.** Training fills in what is empty
and leaves alone what is not, unless `--replace` says otherwise. The document is
the source of truth and a human's correction outranks a fresh guess, or the loop
becomes: correct it, retrain, lose the correction, correct it again.

**Comments and formatting survive.** The document is edited rather than
regenerated, so the notes somebody left themselves are still there afterwards.
This is why it is written with `hclwrite` and not with a template.

The match count is written beside each locator as a comment, because a locator
that worked on 97 of 312 pages and one that worked on 311 deserve different
amounts of trust, and that number is invisible once the guess is in the file.

### Running

These arrive with the engine.

```
scour plan <file>          What applying it would change, and what that costs
scour apply <file>         Submit it, and apply the changes
scour ls                   The jobs there are, and what they are doing
scour status <job>         One job, in detail
scour logs <job>           What it is saying
scour pause <job>          Stop handing out work, keep the frontier
scour resume <job>
scour stop <job>           Stop, and keep everything
scour rm <job>             Forget it
```

```console
$ scour plan job.hcl
news: 3 changes

  free
    scheduler.max_pages: 500 -> 2000
    downloader.plugin.retry: config changed

  rescope        1,204 queued URLs are no longer in bounds
    domains: removed b.example

mutation.costly is "refuse", so this will not be applied.
Set costly = "apply" to allow it, and out_of_scope to say what happens
to those 1,204.
```

That last paragraph is the whole argument for the `mutation` block being in the
document. The command does not ask a question at a prompt, because the answer
belongs to the job and has to survive being submitted by a machine at three in
the morning.

### Output

```
scour records <job>                  The extracted records
scour records <job> --item article   One shape
scour records <job> --follow         As they arrive
```

### The install

```
scour serve                          Run a node
scour serve --join nats://host:4222  Join a cluster
scour nodes                          Who is running
scour version
```

## Rules

**Every problem at once, never the first.** A person fixing a document one
error per run gives up, and so does a build script.

**Exit codes mean something.**

| Code | Meaning |
| --- | --- |
| 0 | It worked |
| 1 | The document was read and refused |
| 2 | The command line itself was wrong |
| 3 | scour could not do it, for a reason that is not the document's fault |

A wrong document and a broken tool are different things, and a script needs to
tell them apart: the first means fix your file, the second means the tool needs
looking at. Conflating them is how a broken build gets retried forever.

**Machine output on stdout, commentary on stderr.** So `scour spec job.hcl >
spec.hcl` writes a spec and not a spec with a progress line in the middle of it.

**`--format json` wherever there is structure.** Human by default, because the
common case is a person. Never a third format nobody maintains.

**A file argument is positional.** `scour validate job.hcl`, not
`scour validate --file job.hcl`. It is the subject of the sentence.

**One document at a time.** A document can hold several jobs, and they are
accepted or refused together. Two documents at once would need rules about what
happens when the first is accepted and the second is not.

**A job is named when it is ambiguous, and not otherwise.** A document holding
one job needs no name on the command line. A document holding three and a
command given no name is refused rather than guessed at.

## Decisions worth arguing with

**There is no `--auto-approve`.** Terraform has one; this should not. The
`mutation` block already says whether a costly change may be applied, and it
travels with the job, so a machine submitting it gets the same answer as a
person. A flag that overrode it would make the document's own statement
advisory, and the first thing anybody would do is put the flag in a script and
forget it is there.

The cost is real: changing a policy for one run means editing the document.
That is the intent, and it is the part most likely to be wrong.

**`apply` is not `run`.** Applying a document makes the running state match it,
which for a new job means starting it. There is no separate verb for "start",
because a job that exists and is not running is a state the document should be
able to describe.

Open: whether the document describes that. Something like `paused = true` in
the job, or an explicit `scour pause`, but not both.

**`validate` does not reach the network.** It parses, resolves and checks. It
does not ask a server whether a plugin exists, so it works offline and in CI,
and it can be wrong about a plugin somebody else's node registers. `plan` is
where that becomes an error.

## What exists today

`init`, `validate`, `show`, `spec` and `defaults` are built and tested. They
need only the engine package. Everything under Running, Output and The install
waits on the stages.

`cmd/scour/main_test.go` drives the whole command line, arguments and all,
through `cli.Run`, and reads what it printed. That is why `App` carries its
streams rather than reaching for the process's: a command that wrote to
`os.Stdout` directly could only be tested by starting a process.

`cmd/scour/cli_doc_test.go` reads this file and checks that the commands and
flags documented above are the ones the binary has. If they disagree, the tests
fail, which is the only way a document like this stays true.
