---
title: command line
description: The commands that ship, the loop they form, and where configuration comes from.
---

# command line

<p class="lede">Package <code>cli</code> is the command line, grouped by what it
does. Every command runs against the same store the API and MCP use.</p>

<figure>
<img src="{{ '/img/cli.svg' | relative_url }}" alt="Say what you are hunting for, then crawl, train, and mark what came back wrong, and crawl again. Records leave through stream.">
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
scour start vehicle --depth 10 --max-pages 5000
```

Train on what it cached, and read back what it learned:

```
scour train vehicle
scour rules vehicle
```

Correct what it got wrong. The records worth reviewing are the ones the model
was least sure of, because those are where supervision changes the next round
the most:

```
scour stream vehicle --confidence 0.5 --limit 20
scour mark vehicle 1088 --invalid
```

Then run again against the trained model, and take the records out:

```
scour start vehicle
scour stream vehicle --write csv --to ./out
```

## Commands

Every command that prints a table also accepts `--json` for machine-readable
output, and `--limit <n>` to cap the rows returned.

| Command | Description |
| --- | --- |
| `scour item add <name> -a <word>` | Define an item, or add another word for it |
| `scour item add <name> --template <schema>` | Start from a built-in schema |
| `scour item add <name> -d <domain>` | Add a whole domain as a crawl target |
| `scour item add <name> -d <domain> --subdomains` | Follow its subdomains too |
| `scour item add <name> -u <url>` | Add a single URL as a crawl target |
| `scour item add <name> -p <prop> -e <example>` | Add a property with an example value |
| `scour item add <name> -p <prop> --prop-type <t>` | Its type: string, number, bool, date, url, email |
| `scour item add <name> -p <prop> --regex <pat>` | What a valid value looks like |
| `scour item add <name> -p <prop> --label <pat>` | What the name beside the value must look like |
| `scour item add <name> --type <type>` | Restrict this item's crawls to a content type |
| `scour item tag <name> -p <prop>` | List the words a property might be labelled with |
| `scour item tag <name> -p <prop> -a <word>` | Add a word (repeatable) |
| `scour item tag <name> -p <prop> -d <word>` | Remove a word (repeatable) |
| `scour item tag <name> -p <prop> -u <word>` | Replace the whole set (repeatable) |
| `scour item tag <name> -p <prop> --on <domain>` | Scope the teaching to one site |
| `scour item ls` | A line per item, or everything about one |
| `scour item rm <name> [-d/-u/-p/--rule]` | Remove an item, or one of its parts |
| `scour item templates` | List the built-in schemas `--template` accepts |
| `scour import <name> --urls\|--domains\|--props\|--aliases <file>` | Load targets, properties or aliases from a file |
| `scour export <name> --domains <file>` | Write an item's targets back out |
| `scour start <name> --depth <n>` | Crawl, and rank discovered URLs by probability. `crawl` is an alias |
| `scour start <name> --max-pages <n>` | Stop after this many pages, keeping the frontier |
| `scour start <name> --max-time <d>` | Stop after this long, keeping the frontier |
| `scour start <name> --browser <policy>` | When to render in a browser: never, auto or always |
| `scour start <name> --reset` | Discard the frontier and start over, keeping the cached pages |
| `scour pause <name>` | Pause a crawl, keeping its frontier |
| `scour stop <name> --force` | Stop a crawl, discarding its frontier |
| `scour train <name>` | Train the model and extraction rules on the cached pages |
| `scour rules <name>` | List the extraction rules learned for an item |
| `scour mark <name> <id>... --valid\|--invalid\|--clear` | Mark extracted records right or wrong |
| `scour stream <name>` | The extracted records. `search` is an alias |
| `scour stream <name> --confidence <p>` | Only those at or above a confidence |
| `scour stream <name> --marked <verdict>` | Only those carrying a verdict |
| `scour stream <name> --type <type>` | Only those from a content type, `--format` being an alias |
| `scour stream <name> --follow` | Keep printing records as they are extracted |
| `scour stream <name> --write csv --to <dir>` | Write records out |
| `scour status` | A line per item, or everything about one |
| `scour top` | Monitor engine activity, live |
| `scour server --listen <addr>` | Run as a service, serving the HTTP API and MCP |
| `scour mcp` | Run as an MCP server over stdio |
| `scour join --role <role>` | Join a cluster. `run` is an alias |
| `scour version` | Print the version |

`--depth` has no short form, because `-d` already means domain. On `scour item
tag`, `-d` means delete and a domain is given with `--on`. That collision is one
of the faults a redesign of this surface is meant to remove.

> A redesign around five nouns and one rule, `scour <noun> <verb> [target]
> [flags]`, is worked out in
> [the command surface]({{ '/cli/design.html' | relative_url }}), along with a
> migration table from every command above. The table here is what ships today.
>
> Two of today's aliases are reassigned by that design, and they are the two
> worth knowing about before the change lands. `run` is an alias for `join`
> today and becomes the way to start a crawl; `search` is an alias for `stream`
> today and becomes a query over records rather than a listing of them. Both
> keep working and both come to mean something else, which is the one part of
> the migration that muscle memory gets wrong rather than an error message.

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
scour stream vehicle --confidence 0.5 --limit 20

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
