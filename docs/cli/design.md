---
title: The command surface
description: A design for the command line around five nouns and one rule, and how every command that ships today maps onto it.
---

# The command surface

<p class="lede">This is a design for the command surface, not a description of
the one that ships today.</p>

The last section maps every command that exists now onto the one proposed here.
For what ships, see [the command line]({{ '/cli/' | relative_url }}).

<figure>
<img src="{{ '/img/nouns.svg' | relative_url }}" alt="An item has many jobs, a job has many runs. Records and the model belong to the item rather than to the job, because two jobs hunting one item fill one table and train one model.">
<figcaption>Five things scour knows about, and everything it stores is one of them.</figcaption>
</figure>

## The fault

Today's surface names the same thing three ways and three things one way.

`scour start vehicle` starts a crawl. `scour item add vehicle -d example.com`
says where that crawl goes. `scour status vehicle` reports on it. All three
take an item name, so the item is carrying a definition, a target list, a
budget, a frontier and a run state at once. There is nowhere to put a second
crawl of the same item against a different site set, and nowhere to say that
one of them is paused while the other is not.

The flags record the strain. `--depth` has no short form because `-d` is
already domain, except on `scour item tag`, where `-d` is delete and a domain
is `--on`. A short flag that means two things is a symptom, not a problem to be
worked around.

And the verbs are split between two shapes with no rule behind the split:
`scour item add` is noun then verb, `scour start` is a bare verb, and both
operate on the same argument.

## The nouns

Five things scour knows about, and everything it stores is one of them.

**item** is what you are hunting. Its aliases, its properties, the other words
a page might label those properties with, and one example value for each. An
item knows nothing about where it might be found.

**job** is a crawl: one item, a set of targets, and a policy. Targets are
domains and urls. Policy is depth, content types, budgets and politeness. A job
is saved, named, and re-runnable. Two jobs can share an item.

**run** is one execution of a job. It has a start, an end reason, counters and
a log. A job accumulates runs; a run is never edited.

**record** is what came out. A record belongs to an item, carries the url it
came from, a confidence, and a verdict once someone marks it.

**model** is what was learned: the link scorer and the extraction rules for one
item, induced from the pages its jobs cached.

The relationships are fixed. An item has many jobs. A job has many runs. A run
produces records, which belong to the item rather than the job, because two
jobs hunting the same item are filling the same table. A model belongs to an
item, and is trained from every page every job of that item has cached.

Four of the five get a command group. **run** does not, because a run is never
created or edited by hand: it is listed and read through the job that produced
it, as `scour job runs` and `scour job log`. A noun with no verbs of its own
does not need a group to hold them.

**node** is a command group without being one of the five, for the opposite
reason. It is the engine process rather than something the engine stores, so it
has verbs and no rows.

## Do you need to define a job?

You need the job. You do not need to define one.

Something has to hold the targets, the depth, the budgets, the frontier and the
run state, and today the item holds them, which is the fault above. So a job
exists whether or not it is named. The question is only whether the user has to
say the word before their first crawl, and the answer is no. Making someone
create a job before they can crawl anything is a tax paid by every beginner to
buy a feature only heavy users need.

So a job is created by the first crawl and named after the item:

```
scour item add vehicle --template vehicle
scour run vehicle -d example.com --depth 10
```

That second line creates a job called `vehicle`, saves the target and the
depth on it, and runs it. `scour run vehicle` from then on resumes that same
job, with that same frontier, and nobody has typed or read the word "job".

The word appears the first time it earns its place, which is when one item
needs two different target sets or two different policies:

```
scour job add uk -i vehicle -d example.co.uk --depth 4
scour run uk
```

Both jobs feed one model, because the model belongs to the item.

Three tiers, and you only climb when the tier you are on runs out:

| Tier | You type | You get |
| --- | --- | --- |
| Implicit | `scour run vehicle -d example.com` | One job per item, named after it |
| Named | `scour job add uk -i vehicle -d ...` | Many jobs per item |
| File | `scour job add -f uk.toml` | Jobs under version control |

The item is the one thing that stays mandatory, because ranking links by how
likely they are to hold what you want requires knowing what you want. There is
no useful crawl to run before that is said.

### Flags on run

Flags given to `run` are saved on the job and reported, rather than applied
silently and forgotten:

```
$ scour run vehicle --depth 12 -d another.example.com
job vehicle: depth 10 -> 12, +1 domain
resuming: 4,182 fetched, 918 queued
```

The alternative, where flags apply to one run and vanish, produces a job whose
saved policy is not the policy it last ran under, and then a resumed crawl
behaves differently from the crawl it is resuming. If you want a throwaway,
say so, and nothing is written back:

```
scour run vehicle --depth 3 --once
```

## One rule

    scour <noun> <verb> [target] [flags]

Every command that manages something takes that shape. There are exactly four
exceptions, and they are the four things you type all day:

| Shortcut | Canonical |
| --- | --- |
| `scour run <job>` | `scour job start <job>` |
| `scour search <item> <query>` | `scour record search <item> <query>` |
| `scour status` | `scour job ls` |
| `scour top` | `scour node top` |

Plus three that act on the install rather than on any one noun, and so have
no noun to sit under: `scour server`, `scour mcp` and `scour version`.

Four is a number you can memorise. The rule is what you fall back on when you
have not.

### The verbs

The same verb means the same thing under every noun. That is the whole of the
rule, and it is what today's surface breaks when `add` creates an item but
`--append` adds a word to one:

| Verb | Means |
| --- | --- |
| `add` | Add the thing, or a member of a set on it |
| `rm` | Remove the thing, or a member of a set on it |
| `set` | Change a value on it |
| `ls` | List them, a line each |
| `show` | One of them, in full |
| `tag` | Show or edit the words that name something |

`add` and `rm` are the pair, and one rule covers both: given only a name they
act on the noun, given a member flag they act on that member.

```
scour item add vehicle                    # the item
scour item add vehicle -p make -e Ford    # a property on it
scour item rm  vehicle -p make            # that property
scour item rm  vehicle                    # the item

scour job add uk -i vehicle               # the job
scour job add uk -d example.com           # a domain on it
scour job rm  uk -d example.com           # that domain
scour job rm  uk                          # the job
```

That is why there is no `create`. A `create` alongside `add` would mean the
same act had two names depending on which noun it acted on, which is the fault
this design exists to remove. `scour item add` also stays exactly as it is
today, so the most-typed command in the tool does not move.

`set` is the third because it overwrites where `add` accumulates. `scour job
set uk --depth 12` replaces the depth that was there; `scour job add uk -d
example.com` leaves the domains that were there and adds one more. That is the
whole test, and it is about what happens to the existing value rather than
about how many values you are allowed to give:

```
scour item tag vehicle -p make --add marque              # add one more
scour item tag vehicle -p make --set make --set marque   # these and no others
```

Both take one word per flag and both repeat, because a word is often a phrase.
They still differ, because the first adds to the set and the second declares
it.

Of those six, `add`, `rm`, `ls` and `show` are genuinely shared today. `set`
appears only on jobs and `tag` only on items, and they sit in that table rather
than the next one because they are the general words for what they do: a second
noun that needed to overwrite a value or edit a set of words would reach for
them rather than invent a name. The verbs below are the opposite case, the ones
no other noun could want, and this is all of them:

| Noun | Its own verbs |
| --- | --- |
| `item` | `templates` |
| `job` | `start`, `pause`, `stop`, `runs`, `log`, `config`, `validate`, `import`, `export` |
| `record` | `search`, `mark`, `write` |
| `model` | `train`, `rules` |
| `node` | `top`, `join`, `leave` |

Whichever of the two tables a verb is in, it means one thing. There is no word
that means something here and something else there.

## The workflow

Say what you are looking for. A template fills in the properties, the words
pages label them with, and a generic example of each:

```
scour item add vehicle --template vehicle
```

Its examples are generic, and examples are what bootstrap the first round of
labels, so replace one with a real value off the site you are about to crawl:

```
scour item add vehicle -p price -e '$42,000'
```

Say where to look, and run it. On the first run there is no trained model, so
links are scored from the aliases and property examples alone:

```
scour run vehicle -d example.com --subdomains --depth 10 --max-pages 5000
```

That saved a job named `vehicle`. Later runs need only the name, and pick the
frontier back up where the last one left it:

```
scour run vehicle
```

Train on what it cached. Training is per item, not per job, because the model
and the rules describe the item and every job of that item feeds them:

```
scour model train vehicle
scour model rules vehicle
```

Correct what it got wrong. The records worth reviewing are the unmarked ones
the model was least sure of, because those are where supervision changes the
next round the most:

```
scour record ls vehicle --confidence ..0.5 --verdict none
scour record mark vehicle 41 42 43 --verdict invalid
```

Run again against the trained model, then find what you came for and take the
rest out:

```
scour run vehicle
scour search vehicle make:Ford 'crew cab'
scour record write vehicle --format csv --to ./out
```

The loop is run, train, mark, run. Everything else on this page exists to set
that loop up or to watch it.

## Jobs

Everything in this section is the second and third tier. The implicit job the
first crawl creates is an ordinary job in every respect, so it shows up in
`scour job ls`, it can be edited by the commands below, and it can be written
out as a file. Nothing here has to be learned before a first crawl, and nothing
learned here is a different mechanism from the one already running.

A job is created from flags or from a file, and the two are the same thing.
`scour job config` prints a commented sample; `scour job show <name> --toml`
prints an existing job back in that form, so anything built by flags can be put
under version control and anything under version control can be applied:

```
scour job config > uk.toml
scour job validate -f uk.toml
scour job add -f uk.toml
scour job show uk --toml > uk.toml
```

Targets are edited after the fact without recreating the job, and bulk lists
load from files:

```
scour job add uk -d another.example.com
scour job rm  uk -u https://www.example.co.uk/others/
scour job import uk --domains domains.txt --urls urls.txt
scour job export uk --domains domains.txt --urls urls.txt
```

A domain that covers its subdomains writes as `*.example.com`, which is how
import reads it back, so a round trip does not quietly narrow the target.

### States

A job is in exactly one state, and the state says what a `start` would do:

| State | Means | `start` does |
| --- | --- | --- |
| `ready` | Created, never run | Begins from the seeds |
| `running` | A run is in flight | Nothing, reports the run |
| `paused` | Frozen, frontier kept | Resumes where it stopped |
| `budget` | Hit `--max-pages` or `--max-time`, frontier kept | Resumes, on a fresh budget |
| `done` | Frontier exhausted | Begins from the seeds again |
| `stopped` | Frontier discarded | Begins from the seeds |
| `failed` | The last run died | Resumes, frontier permitting |

`budget` is a separate state from `done` on purpose. Both end a run with the
frontier intact, but one means there is more to fetch and the other means there
is not, and a script that cannot tell them apart cannot decide whether to run
again.

### What survives

Each row is the whole-thing form of the command, `scour job rm uk` rather than
`scour job rm uk -d example.com`. Removing one target only drops that target
and the frontier entries below it.

| | Definition | Cached pages | Frontier | Records | Model |
| --- | --- | --- | --- | --- | --- |
| `job pause` | kept | kept | kept | kept | kept |
| `job stop` | kept | kept | dropped | kept | kept |
| `job rm` | dropped | kept | dropped | kept | kept |
| `job rm --pages` | dropped | dropped | dropped | kept | kept |
| `item rm` | dropped | dropped | dropped | dropped | dropped |
| `model rm` | kept | kept | kept | kept | dropped |

`stop` asks for `--force` when there is a frontier to lose, because on a large
site that frontier is hours of deciding what to fetch next, and it is the one
thing here that cannot be recomputed cheaply.

`job rm` leaves the cached pages, because they belong to the item's corpus and
the next job over the same site should not refetch them. `scour job rm uk
--pages` is how they go, and it is a flag rather than the default because a
removed job is a common thing to do and a refetched site is an expensive one.

### Runs and logs

```
scour job runs uk
scour job log  uk            # the last run
scour job log  uk --run 7
scour job log  uk --follow
```

`scour job runs` is the history: when each ran, how many pages, how it ended.
`scour job log` is the detail for one of them, defaulting to the most recent,
which is what you want the moment a run ends badly.

## Commands

### item

| Command | Description |
| --- | --- |
| `scour item add <name>` | Define an item |
| `scour item add <name> --template <template>` | Define it from a built-in schema |
| `scour item add <name> -p <prop> -e <example>` | Add a property, with an example value |
| `scour item tag <name> --add <word>` | Add an alias for the item itself |
| `scour item tag <name> -p <prop>` | List the words a property might be labelled with |
| `scour item tag <name> -p <prop> --add <word>` | Add a word (repeatable) |
| `scour item tag <name> -p <prop> --rm <word>` | Remove a word (repeatable) |
| `scour item tag <name> -p <prop> --set <word>` | Declare the whole set (repeatable) |
| `scour item tag <name> -p <prop> --on <domain>` | Scope the teaching to one site |
| `scour item ls` | A line per item: properties, jobs, records, whether it is trained |
| `scour item show <name>` | Everything known about one item |
| `scour item rm <name>` | Remove an item and everything derived from it |
| `scour item rm <name> -p <prop>` | Remove one property |
| `scour item rm <name> -p <prop> --clear <detail>` | Clear one detail, keeping the property |
| `scour item templates` | List the built-in schemas `--template` accepts |

Aliases and property labels are the same act, so they are one command. `-p`
selects what is being tagged: with it, a property; without it, the item.

Each tag flag carries one word and repeats, because a word is often a phrase:
`'pickup truck'`, `'model year'`, `'asking price'`. Splitting one argument on
spaces would eventually cut one of those in half.

Teaching writes only what you give it, so adding a label does not cost a
property the example it was taught with. An empty value means "not given"
rather than "make it empty", which is why clearing has its own form.

That form is `--clear`, which takes `example`, `label` or `regex` and repeats,
so clearing is one flag with an argument rather than three boolean flags that
shadow `-e`. A `--example` that means "set this example" in one command and
"throw the example away" in another is the same fault as `-d` meaning domain
and delete.

### job

| Command | Description |
| --- | --- |
| `scour job add <name> -i <item>` | Add a job |
| `scour job add -f <file>` | Add it from a config file |
| `scour job add <name> -d <domain>` | Add a domain target |
| `scour job add <name> -d <domain> --subdomains` | Follow its subdomains too |
| `scour job add <name> -u <url>` | Add a url target |
| `scour job add <name> -t <type>` | Allow a content type |
| `scour job add <name> --exclude-type <type>` | Skip a content type |
| `scour job set <name> --depth <n>` | How deep to follow links |
| `scour job set <name> --max-pages <n>` | Stop a run after this many pages |
| `scour job set <name> --max-time <d>` | Stop a run after this long |
| `scour job rm <name> -d <domain>` | Remove one target |
| `scour job rm <name> -t <type>` | Stop allowing a content type |
| `scour job rm <name>` | Remove the job, keeping its cached pages |
| `scour job rm <name> --pages` | Remove it and its cached pages |
| `scour job config` | Print a commented sample config |
| `scour job validate -f <file>` | Check a config without applying it |
| `scour job import <name> --domains <file>` | Load targets from a file |
| `scour job export <name> --urls <file>` | Write targets back out |
| `scour job ls` | A line per job: item, targets, state, progress, last run |
| `scour job ls -i <item>` | Only the jobs of one item |
| `scour job show <name>` | Everything about one job |
| `scour job show <name> --toml` | The same, as a config file |
| `scour job start <name>` | Run it, resuming a paused one |
| `scour job pause <name>` | Freeze it, keeping the frontier |
| `scour job stop <name>` | Stop it, discarding the frontier |
| `scour job stop <name> --force` | The same, when there is a frontier to lose |
| `scour job runs <name>` | The run history |
| `scour job log <name> --run <n>` | One run's log, defaulting to the last |
| `scour job log <name> --follow` | Follow the running one |

The flags that describe a job, the targets on `add` and `rm` and the policy on
`set`, are all accepted by `scour run` too, which applies them before starting.
The ones that act on the job as a whole are not: `scour run` never takes
`--pages`, and removing a job is not something a command that starts one should
be able to do. `scour job set` is the same edit as `run` makes, without the
crawl, for changing a budget on something that is not running.

### record

| Command | Description |
| --- | --- |
| `scour record search <item> <query>` | Records matching a query, best first |
| `scour record search <item> <field>:<value>` | Match one field rather than any |
| `scour record search <item> <query> --follow` | Keep matching as records arrive |
| `scour record ls <item>` | The extracted records, newest first |
| `scour record ls <item> --confidence <p>` | Only those at or above a confidence |
| `scour record ls <item> --confidence <lo>..<hi>` | Only those inside a band |
| `scour record ls <item> --verdict <verdict>` | Only those carrying a verdict |
| `scour record ls <item> -j <job>` | Only those a given job produced |
| `scour record ls <item> -t <type>` | Only those from a content type |
| `scour record ls <item> --exclude-type <type>` | Everything except a content type |
| `scour record ls <item> --follow` | Keep printing as they are extracted |
| `scour record show <item> <id>` | One record, with the page it came from |
| `scour record mark <item> <id>... --verdict <verdict>` | Mark records right, wrong, or neither |
| `scour record write <item> --format csv --to <dir>` | Write records out |
| `scour record write <item> <query> --format csv` | Write out only what matches |
| `scour record rm <item> -j <job>` | Drop records from one job |

The records are the point of the crawl, so finding one has its own verb rather
than a flag on a listing:

```
scour search vehicle 'f-150'
scour search vehicle make:Ford year:2026
scour search vehicle make:Ford 'crew cab' --confidence 0.8
scour search vehicle url:example.com --verdict none
```

A bare word matches any field of the record and its url. `field:value` matches
that one field, where the field is a property of the item, or `url`. Several
terms narrow, quotes hold a phrase together, and every `record ls` filter works
here too, so a search can be pinned to one job, one content type, one
confidence band or one verdict.

`search` and `ls` are not two names for one thing. `ls` enumerates: it takes no
query, orders newest first, and answers "what has this crawl produced". `search`
requires a query, orders by how well each record matches it, and shows which
field matched. One is for watching a crawl fill up, the other is for finding
the row you came for.

`--follow` on a search is the useful pairing of the two: it waits, and prints
only the records that match as they are extracted, which is how you watch a
long crawl for the thing you actually wanted.

The same query is accepted by `scour record write`, so a search that found the
right rows on screen exports exactly those rows without being rewritten as a
set of filter flags.

`--confidence` takes a floor or a band, and either end of a band may be left
off: `0.8` is everything at or above, `..0.5` everything below, `0.4..0.6`
everything between. A floor alone would have been enough for export, where you
want the good rows, but not for review, where you want the doubtful ones, and
those are the two things anybody does with a confidence.

A verdict is `valid`, `invalid` or `none`, and the same word both filters and
sets: `--verdict invalid` on `ls` finds them, `--verdict invalid` on `mark`
makes them. Three boolean flags would have needed a rule about what happens
when two are given, and `--clear` as a boolean here would have collided with
`--clear <detail>` on `scour item rm`.

Records are the product, so they belong wherever the rest of your pipeline
reads. Written out, they are grouped by the domain they came from, one file per
site, so an export is diffable and a site that changed is a changed file.

`--format` takes `csv`, `json`, `jsonl` or `webhook`; with `webhook`, `--to` is
a url. The columns are the union of every record's fields, not the first
record's, so a field only some pages carry still gets a column, and the verdict
travels with the record, because an export is also how records get corrected
outside scour.

### model

| Command | Description |
| --- | --- |
| `scour model train <item>` | Train the scorer and extraction rules on the cached pages |
| `scour model show <item>` | What it learned, and from how much |
| `scour model rules <item>` | The extraction rules, per property |
| `scour model rules <item> --on <domain>` | The rules for one site |
| `scour model rm <item>` | Discard the model, keeping the pages and the marks |

`scour model rm` followed by `scour model train` is a clean retrain, and it is
worth having as two commands rather than a `--force` flag, because the first
one is the recoverable half and the second is the expensive half.

### node

The engine, whether that is this process or a cluster of them.

| Command | Description |
| --- | --- |
| `scour node top` | Monitor engine activity, live |
| `scour node ls` | A line per node: role, health, queue depth, throughput |
| `scour node show <node>` | Everything about one node |
| `scour node join --role <role>` | Join a cluster |
| `scour node leave` | Leave it, draining first |

There is no `scour node status`, because there is already one `status` in the
surface and it reports jobs. `scour node ls` carries the health columns that a
node status would have printed, which is one command less and one meaning of
the word `status` less.

### server

| Command | Description |
| --- | --- |
| `scour server --listen <addr>` | Run as a service, serving the HTTP API and MCP |
| `scour mcp` | Run as an MCP server over stdio |

There is no separate client. Every command in this document runs against a
local store by default and against a service when one is configured, chosen by
`--server` or by the config file. That is why the design has no CLIENT group:
a client is an address, not a set of commands, and `scour job ls` should mean
the same thing either way.

This also settles the collision in the current surface, where `start` would
have meant both "start a crawl" and "start the daemon". Only `server` starts a
daemon, and it is never called `start`.

## Flags

### Reserved shorts

One meaning each, everywhere, no exceptions:

| Short | Long | Meaning |
| --- | --- | --- |
| `-d` | `--domain` | A domain target |
| `-u` | `--url` | A url target |
| `-i` | `--item` | Which item |
| `-j` | `--job` | Which job |
| `-p` | `--prop` | A property |
| `-e` | `--example` | An example value |
| `-t` | `--type` | A content type |
| `-f` | `--file` | A config file |

Most long flags have no short form, and these are the ones you might have
expected to: `--depth`, `--template`, `--add`, `--rm`, `--set`, `--clear` and
`--verdict`. They are typed once per job or once per lesson, and reaching for a
short form for them is what forced `-d` to mean delete in the first place.

Three long flags appear under more than one noun, and each means one thing in
every place it appears: `--on` scopes to a single site, on `item tag` and
`model rules`; `--follow` keeps printing as new lines arrive, on `job log`,
`record ls` and `record search`; `--exclude-type` skips a content type, on
`job add` and `record ls`.

`add`, `rm` and `set` mean one thing whether they arrive as a verb or as a
flag. `scour job add -d` and `scour item tag --add` both add a member to a set;
`scour job set --depth` and `scour item tag --set` both overwrite what was
there. They differ only in what they are reached through. Targets are the job's
own content, so they are edited by a verb. A tag set hangs off a property, so
it is reached by naming the property first and edited by a flag.

That is why `item tag` gets `--add` / `--rm` / `--set` rather than today's
`--append` / `--delete` / `--update`, and why the third one is not a fourth
word like `--replace`. Inventing one would have given the surface two names for
overwriting, which is the fault this design exists to remove.

### Global

| Flag | Meaning |
| --- | --- |
| `--config <file>` | Configuration file |
| `--server <addr>` | Run against a service instead of the local store |
| `--json` | Machine-readable output |
| `--limit <n>` | Cap the rows printed, 0 for no cap |
| `--strict` | Exit 4 when the result is empty |
| `--verbose`, `-v` | Log at debug level |
| `--quiet`, `-q` | Errors only |
| `--yes`, `-y` | Do not prompt |
| `--no-color` | Plain output |

`--json`, `--limit` and `--strict` work on every command that prints a table,
including the shortcuts, so `scour --json status` is a valid way to poll and
`scour --strict record ls vehicle --confidence 0.9` is a valid way to wait for
one.

There is no `--wide`. A listing prints the columns it has, and `scour job ls`
prints all of them, which is what makes `scour status` an exact alias rather
than an alias plus a flag. It also keeps the two listings honest with each
other, since `scour node ls` already prints its health columns unasked.

`--once`, on `scour run` alone, keeps that run's flags out of the saved job.

## Output

A command prints a table to stdout and everything else to stderr, so a pipe
carries data only. `--json` prints one object per row, `--json --limit 0` is a
complete dump, and `--follow` streams rows as they happen in whichever format
was asked for.

Exit codes, so a script can branch without parsing text:

| Code | Meaning |
| --- | --- |
| 0 | Success |
| 1 | The command failed |
| 2 | Usage error: bad flag, unknown command |
| 3 | Not found: no such item, job, record, run or node |
| 4 | Empty result, and `--strict` was given |
| 5 | The service was unreachable |

Code 4 only fires under `--strict`. An empty result is normally success,
because "no records above 0.9 yet" is an answer, not a failure.

## Migration

| Today | Proposed |
| --- | --- |
| `scour item add <n> --alias <w>` | `scour item tag <n> --add <w>` |
| `scour item add <n> -p <p> -e <v>` | `scour item add <n> -p <p> -e <v>`, unchanged |
| `scour item add <n> -d <domain>` | `scour job add <n> -d <domain>` |
| `scour item add <n> -u <url>` | `scour job add <n> -u <url>` |
| `scour item add <n> --type <type>` | `scour job add <n> -t <type>` |
| `scour item tag <n> -p <p> --append <w>` | `scour item tag <n> -p <p> --add <w>` |
| `scour item tag <n> -p <p> --delete <w>` | `scour item tag <n> -p <p> --rm <w>` |
| `scour item tag <n> -p <p> --update <w>` | `scour item tag <n> -p <p> --set <w>` |
| `scour item ls <n>` | `scour item show <n>` |
| `scour item rm <n> -p <p> --example` | `scour item rm <n> -p <p> --clear example` |
| `scour item templates` | `scour item templates`, unchanged |
| `scour import <n> --urls <f>` | `scour job import <n> --urls <f>` |
| `scour export <n> --domains <f>` | `scour job export <n> --domains <f>` |
| `scour start <n> --depth 10` | `scour run <n> --depth 10` |
| `scour start <n> --max-pages <k>` | `scour run <n> --max-pages <k>` |
| `scour pause <n>` | `scour job pause <n>` |
| `scour stop <n> --force` | `scour job stop <n> --force` |
| `scour status` | `scour status`, unchanged, now a line per job |
| `scour status <n>` | `scour item show <n>` or `scour job show <n>` |
| `scour top` | `scour top`, unchanged |
| `scour train <n>` | `scour model train <n>` |
| `scour rules <n>` | `scour model rules <n>` |
| `scour mark <n> <id>... --valid` | `scour record mark <n> <id>... --verdict valid` |
| `scour stream <n>` | `scour record ls <n>` |
| `scour stream <n> --follow` | `scour record ls <n> --follow` |
| `scour stream <n> --marked <v>` | `scour record ls <n> --verdict <v>` |
| `scour stream <n> --confidence <p>` | `scour record ls <n> --confidence <p>` |
| `scour stream <n> --write csv --to <d>` | `scour record write <n> --format csv --to <d>` |
| no equivalent | `scour search <n> <query>` |
| `scour server --listen <a>` | `scour server --listen <a>`, unchanged |
| `scour join --role <r>` | `scour node join --role <r>` |
| `scour mcp` | `scour mcp`, unchanged |

Existing crawls migrate without being described twice. Every item that has
targets today becomes an item plus one job named after it, so `scour start
vehicle` and `scour run vehicle` do the same thing to the same frontier, and
nothing has to be re-seeded.

### Names not taken

`scour search` meant the crawl in the notes this design replaces. It is kept,
but for the other thing it could have meant: a query over the records. That is
the reading someone arrives with, and the crawl already has two good names in
`scour job start` and `scour run`. A word that pulls in two directions should
be given to whichever one has no other name.

`scour domain` and `scour url` are not nouns here. A domain is not a thing you
manage on its own; it is a target belonging to a job, and giving it commands
would mean it also needs an owner, a state and a lifecycle it does not have.
`scour job add -d` is the whole of what `scour domain add` would have been.

`scour cluster` is `scour node`, because the commands act on one node at a
time: this node joins, this node leaves, these nodes are up.

`scour stream` is not kept. It was `scour record ls --follow` under another
name, and a shortcut is only worth a top-level word when it saves you something
you type all day. Following a crawl is what `scour top` is for, and following
the records you actually want is `scour search --follow`, so the plain listing
did not need a second name of its own.

## Help

The help lists the nouns in the order you meet them, not alphabetically:

```
COMMANDS:
   item:     Define what you are hunting for
   job:      Define where to look, and run it
   record:   Read, mark and export what was found
   model:    Train and inspect what was learned
   node:     Monitor the engine, and cluster it

   run       Start a job, creating it if it has no targets yet
   search    Find records matching a query
   status    A line per job
   top       Monitor engine activity, live

   server    Run as a service
   mcp       Run as an MCP server over stdio
   version   Print the version
```

Three blocks: the nouns, the four shortcuts, and the three that act on the
install. Someone who reads only the first line of each block can still get from
an idea to a crawl.

<div class="pager" markdown="1">
<span markdown="1">&larr; [config]({{ '/config/' | relative_url }})</span>
<span markdown="1">[The HTTP API]({{ '/server/api.html' | relative_url }}) &rarr;</span>
</div>
