---
title: A graph, not a list
description: Steps run when what they require has run, and exporters each write one item.
---

# A graph, not a list

*Chapter eight of [the scour book](../).*

Every other stage is a chain, because a request has one path through it. The
pipeline is not, because the work on an item is a dependency graph and
pretending otherwise costs concurrency for nothing.

<figure>
<img src="{{ '/img/pipeline.svg' | relative_url }}" alt="Five pipeline steps arranged by dependency, in four waves. Clean runs first; validate and dedupe both require clean and run at the same time, which is the widest wave; rank requires both of them; a python step requires rank.">
<figcaption>The waves are computed, not written. Four of them here, two steps wide at the widest, and the width is what a run can actually do at the same time.</figcaption>
</figure>

## Why not an order number

A step is a node in a graph, written as `step <kind> <name>` and ordered by
`requires`. Giving it a number as well would be two ways of saying the same
thing, and they would disagree.

It would also throw away what the graph is for. The reason to know that
`validate` and `dedupe` both depend on `clean` and not on each other is so
they run at the same time. A list cannot express that; a number would flatten
it back into one.

```hcl
pipeline {
  step "clean" "article" {}

  step "validate" "article" {
    requires = [clean.article]
  }

  step "dedupe" "article" {
    requires = [clean.article]
  }

  step "rank" "article" {
    requires = [validate.article, dedupe.article]
  }
}
```

References are bare, not strings, so a step requiring something that does not
exist is caught with a line and a column. Cycles are refused, and the error
names the steps stuck in one rather than saying that a cycle exists somewhere.

**Not a plugin stage.** Every other stage takes a chain of middleware; this one
does not, and the difference is not a rule a validator enforces. The `pipeline`
block holds `step` blocks and nothing else, so writing `plugin` in it is a parse
error with a line and a column rather than something refused later. A step is
already the unit of extension here, and a stage with two of them would be two
ways to say one thing.

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

## Exporters are per item

An exporter is named `exporter "<format>" "<item>"`, and one that names an
item the job does not extract is refused. Silently writing nothing is the
failure mode nobody notices until they go looking for the output.

```hcl
exporter "parquet" "price" {
  dir = "./archive"
}

exporter "nats" "price" {
  subject = "events.markets.price"
}
```

There is no archive component and no event component. Saving to storage and
streaming to a stream are both deliveries, so both are exporters. The same
item, declared once, is written as Parquet for whatever reads the archive and
published as a measurement for whatever is listening now.

> **What that buys**
>
> Parquet on disk is a table without a database: DuckDB reads it in place,
> and so does anything else that speaks the format. An archive that needs a
> running service to be readable is an archive with an expiry date.

---

[Back: Shapes, entities, measurements](../items/) · [Next: Local until it has to be shared](../cli/)
