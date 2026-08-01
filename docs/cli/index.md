---
title: command line
description: The commands that ship, the loop they form, and where configuration comes from.
---

# command line

<p class="lede">Package <code>cli</code> is the command line, grouped by what it
does. Every command runs against the same store the API and MCP use.</p>

<figure>
<img src="{{ '/img/cli.svg' | relative_url }}" alt="Say what you are hunting for, then run the crawl, train a model, and mark what came back wrong, and run again. Records leave through record ls.">
<figcaption>Everything else exists to set that loop up or to watch it.</figcaption>
</figure>

## The loop

Say what you are looking for. A template fills in the properties, the words
pages label them with, and a generic example of each:

```
scour item templates
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
scour item add vehicle -d 'example.com' --subdomains
scour run vehicle --depth 10 --max-pages 5000
```

Train on what it cached, and read back what it learned:

```
scour model train vehicle
scour model rules vehicle
```

Correct what it got wrong. The records worth reviewing are the ones the model
was least sure of, because those are where supervision changes the next round
the most:

```
scour record ls vehicle --confidence 0.5 --limit 20
scour record mark vehicle 1088 --invalid
```

Then run again against the trained model, and take the records out:

```
scour run vehicle
scour record ls vehicle --write csv --to ./out
```

## Commands

    scour <noun> <verb> [target] [flags]

Five nouns, and everything scour stores is one of them. Every command that
prints a table also accepts `--json`, `--limit <n>` to cap the rows, and
`--strict` to exit non-zero when the result is empty.

### item

What you are hunting: its aliases, its properties, and the words a page might
label those properties with.

| Command | Description |
| --- | --- |
| `scour item add <name> -a <word>` | Define an item, or add another word for it |
| `scour item add <name> --template <schema>` | Start from a built-in schema |
| `scour item add <name> -d <domain> [--subdomains]` | Add a domain as a crawl target |
| `scour item add <name> -u <url>` | Add a single URL as a crawl target |
| `scour item add <name> -p <prop> -e <example>` | Add a property with an example value |
| `scour item add <name> -p <prop> --prop-type <t>` | Its type: string, number, bool, date, url, email |
| `scour item add <name> -p <prop> --regex <pat>` | What a valid value looks like |
| `scour item add <name> -p <prop> --label <pat>` | What the name beside the value must look like |
| `scour item tag <name> -p <prop>` | List the words a property might be labelled with |
| `scour item tag <name> -p <prop> -a/-d/-u <word>` | Add, remove, or replace the whole set |
| `scour item tag <name> -p <prop> --on <domain>` | Scope the teaching to one site |
| `scour item ls [<name>]` | A line per item, or everything about one |
| `scour item show <name>` | Everything known about one item |
| `scour item rm <name> [-d/-u/-p/--rule]` | Remove an item, or one of its parts |
| `scour item rm <name> -p <prop> --clear <detail>` | Clear one detail, keeping the property |
| `scour item templates` | List the built-in schemas `--template` accepts |

### job

Where to look, and the run itself. `remove` and `list` are accepted as aliases
for `rm` and `ls` under every noun.

| Command | Description |
| --- | --- |
| `scour job add <name> -i <item>` | Add a job, or a target to one |
| `scour job add <name> -d <domain> [--subdomains]` | Add a domain target |
| `scour job set <name> --depth <n>` | Change a bound: also `--max-pages`, `--max-time` |
| `scour job rm <name> [-d/-u]` | Remove a job, or one of its targets |
| `scour job ls` | A line per job: item, targets, progress, state |
| `scour job show <name>` | Everything about one job |
| `scour job start <name>` | Start a search. `crawl` is an alias |
| `scour job pause <name>` | Pause it, keeping the frontier |
| `scour job stop <name> --force` | Stop it, discarding the frontier |
| `scour job import <name> --urls\|--domains\|--props\|--aliases <file>` | Load from files |
| `scour job export <name> --domains <file>` | Write targets back out |

### record

What came out. `ls` orders best first, and `stream` is an alias for it.

| Command | Description |
| --- | --- |
| `scour record ls <item>` | The extracted records, best first |
| `scour record ls <item> --confidence <p>` | Only those at or above a confidence |
| `scour record ls <item> --verdict <v>` | Only those carrying a verdict |
| `scour record ls <item> --type <t>` | Only those from a content type, `--format` being an alias |
| `scour record ls <item> --follow` | Keep printing records as they are extracted |
| `scour record ls <item> --write csv --to <dir>` | Write records out |
| `scour record ls <item> --write webhook --token-env <var>` | Post them, with the token from an env var |
| `scour record mark <item> <id>... [--invalid]` | Mark records right or wrong |

### model

What was learned. Training is per item, because the model describes the item and
every job of that item feeds it.

| Command | Description |
| --- | --- |
| `scour model train <item>` | Train the scorer and extraction rules on the cached pages |
| `scour model show <item>` | What it learned, and from how much |
| `scour model rules <item>` | The extraction rules, per property |
| `scour model rm <item>` | Discard the model, keeping the pages and the marks |

### node

The engine process rather than something the engine stores, so it has verbs and
no rows.

| Command | Description |
| --- | --- |
| `scour node top` | Monitor engine activity, live |
| `scour node join --role <role> --bus-url <url>` | Join a cluster. `run` is an alias here |

### Shortcuts, and the three that act on the install

| Command | Canonical |
| --- | --- |
| `scour run <item>` | `scour job start`, creating the job if the item has no targets yet. `crawl` and `start` are aliases |
| `scour status` | `scour job ls` |
| `scour top` | `scour node top` |
| `scour server --listen <addr>` | Run as a service, serving the HTTP API and MCP |
| `scour mcp` | Run as an MCP server over stdio |
| `scour version` | Print the version |

`--depth` has no short form, because `-d` already means domain.

> This surface is the [command surface design]({{ '/cli/design.html' | relative_url }})
> as far as it has been built. The five nouns, the one rule and the shortcuts are
> in place; several verbs from the design are not yet, among them `record
> search`, `record write`, `job runs`, `job log`, `job config`, `job validate`
> and `node ls`. The tables above are what the binary answers to today.
>
> One collision the design called out has now resolved in both directions at
> once: `scour run` starts a job, while `run` under `node` is an alias for
> `join`. They are different commands at different levels, and the second is the
> one that will surprise anybody who learned it as a top-level word.

## Templates
{: #templates }

A template is a schema scour ships with, compiled into the binary so it cannot
go missing from a package, a container image or a `go install`:

```
scour item templates

TEMPLATE   PROPS  FIELDS
---------  -----  ------------------------------------------------------------
article        7  title, author, published, modified, section, summary, link
job            6  title, company, location, salary, employment, posted
microdata      9  headline, author, datePublished, dateModified, descriptio...
product        7  title, brand, price, currency, sku, availability, rating
vehicle        9  make, model, year, price, mileage, vin, body, fuel, trans...
```

**What ships is only what transfers between sites.** A schema describes what a
vehicle is, which is true everywhere; an XPath describes where one site put it,
which is true nowhere else. So a shipped model carries a schema, and may carry a
field-order chain, and never carries located items.

A template is a starting point, not an answer. Its example values are generic,
and examples are what bootstrap the first round of labels, so replace one with a
real value off the site you are about to crawl.

## Watching a crawl
{: #top }

```
scour top
```

One row per item, refreshed live: targets, queued, visited, records, rules, the
rate, and what state it is in. The rate is measured over a fifteen-second
window, so it reflects what the crawl is doing now rather than its average since
it started.

What the view shows is kept separate from how it is drawn, so the part worth
asserting anything about can be tested: rendering a table into a terminal is
hard to make claims about, and deciding what belongs in the table is not.

Rules beside records is the pairing to watch. A rule count holding while records
fall is a site changing under a model that has not noticed.

## Reading a record listing

```
scour record ls vehicle --confidence 0.5 --limit 20

  ID  CONF  FORMAT  MAKE       MODEL      YEAR  TYPE
----  ----  ------  ---------  ---------  ----  ----------------
1042   .99  html    Ford       F-Series   2026  Full-Size Pickup
1043   .97  html    Chevrolet  Silverado  2025  Full-Size Pickup
1088   .54  pdf     no way                2000  On my way home..

showing 3 of 100 matches
```

One row per match, one column per property you defined. `FORMAT` is the content
type the record came from. IDs are stable, so they stay valid across queries and
reruns, and a mark put on one survives retraining.

Record 1088 is a false positive: scour read prose off the page as if it were a
spec table. Marking it wrong is how it stops happening, and
[training]({{ '/train/' | relative_url }}) is what that mark changes.

## Where configuration comes from

A flag beats the environment, which beats a config file, which beats the
built-in defaults. Anything a flag can say once, `config.toml` can say for
every run, and every value naming an implementation is a registry name.

[The whole of it, key by key]({{ '/config/' | relative_url }}).

## Global flags

| Flag | Meaning |
| --- | --- |
| `--config <file>` | Configuration file |
| `--json` | Machine-readable output |
| `--limit <n>` | Cap the rows printed (0 for no cap) |
| `--verbose`, `-v` | Log at debug level |

<div class="pager" markdown="1">
<span markdown="1">&larr; [server &amp; MCP]({{ '/server/' | relative_url }})</span>
<span markdown="1">[config]({{ '/config/' | relative_url }}) &rarr;</span>
</div>
