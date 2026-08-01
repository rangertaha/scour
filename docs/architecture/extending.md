---
title: Extending it
description: One generic registry, ten extension points, and how to add an implementation or a whole new kind.
---

# Extending it

<p class="lede">Every pluggable part of scour is a registry. They differ in what
they produce and in what they are built from, and in nothing else, so they share
one implementation.</p>

<figure>
<img src="{{ '/img/registry.svg' | relative_url }}" alt="One generic registry underlies ten extension points: transport, score, matcher, classify, schedule, cache, export, ai, nodeclass and refresh.">
<figcaption>Dashed boxes are registered and not written yet: a name in a config file resolves to "this is planned" rather than to "unknown", which reads as a typo.</figcaption>
</figure>

Each of these used to carry its own copy of the same forty lines, which meant
six places to keep in step and a seventh to write by hand every time a new kind
of extension appeared. One generic registry serves them all.

## The extension points

| Kind | Interface | Ships with | Selected by |
| --- | --- | --- | --- |
| [transport]({{ '/transport/' | relative_url }}) | `http.RoundTripper` | `http`, `webdriver` | `[[host]] transport` |
| [score]({{ '/score/' | relative_url }}) | `Scorer` | `bayes`, `embed` | `[model] scorer` |
| [matcher]({{ '/matcher/' | relative_url }}) | `wom.Matcher` | `heuristic`, `embed`, `llm` | `[model] matcher` |
| [classify]({{ '/classify/' | relative_url }}) | `Classifier` | `llm` | `[model] classifier` |
| [nodeclass]({{ '/classify/' | relative_url }}#nodeclass) | `Classifier` | `recency`*, `topic`* | not yet wired |
| [cache]({{ '/cache/' | relative_url }}) | `Store` | `local`, `s3`†, `gcs`† | `[cache] driver` |
| [export]({{ '/export/' | relative_url }}) | `Exporter` | `csv`, `json`, `webhook` | `scour record ls --write` |
| [schedule]({{ '/schedule/' | relative_url }}) | `Policy` | `best`, `breadth`, `depth`, `random`, `warmup` | `[crawl] scheduler` |
| [refresh]({{ '/schedule/' | relative_url }}#refresh) | `Refresh` | `cron`* | not yet selectable |
| ai | `Provider` | `anthropic`, `ollama` | `[[ai]] provider` |

\* registered, not written yet: they answer `ErrNotImplemented`.
† compiled in only with `-tags cloud`, which adds 29MB to the stripped binary.

`ai` is the same shape holding its own map, because a provider is chosen by a
field inside a named `[[ai]]` block rather than by a bare name, so its lookup
key is not the same thing as a registry name.

## Adding an implementation

Register it from `init` in its own package, and have whatever selects it import
that package for its side effects:

```go
func init() {
    transport.Register("myproxy", func(cfg transport.Config) (http.RoundTripper, error) {
        return &myProxy{timeout: cfg.Timeout}, nil
    })
}
```

Registration is not import-order sensitive: nothing reads a registry until a
name is looked up. Two implementations claiming one name is a build that
imported both, which is a decision somebody made on purpose, so the last one
wins rather than panicking from an `init` that has nowhere to return an error
to.

## Adding a kind of extension

Declare the interface and the config it is built from, make a registry for the
pair, and export four wrappers:

```go
var reg = registry.New[Config, Thinger]("thinger").Default("simple")

func Register(name string, f registry.Factory[Config, Thinger]) { reg.Register(name, f) }
func New(name string, cfg Config) (Thinger, error)              { return reg.New(name, cfg) }
func Names() []string                                           { return reg.Names() }
func Has(name string) bool                                      { return reg.Has(name) }
```

The wrappers are kept rather than exposing the registry value, because they are
what callers import: scour's own packages say `score.New`, and an implementation
registers itself without knowing `registry` exists.

The string given to `registry.New` is the word that appears in errors, so an
unknown name reports which registry it was not found in, and reports what is in
that registry:

```
unknown scorer "bays", have [bayes embed]
```

`Default` names what an empty string resolves to, which is how an unconfigured
scour gets a working implementation without every call site repeating which
default that is.

## Deciding where something belongs

Three questions, in this order.

**Can it be taught instead?** A behaviour expressible as a schema, an alias, an
example, a pattern or a mark belongs there rather than in code. This is the rule
the whole engine is built on, and it disqualifies most of what looks at first
like it wants a plugin.

**What does it rank, or what does it produce?** If something already ranks that
input, it is an implementation of an existing kind. If nothing does, it is a new
kind. The two scoring registries stayed separate for exactly this reason: they
rank different graphs, and folding them into one would have meant a cast at
every call site.

**Is a name in a config file enough to choose it?** If choosing it needs more
than a name, it wants a config block of its own rather than a registry entry,
which is why `ai` reads a `[[ai]]` block and everything else reads a word.

## What deliberately has no seam

The six page roles, the inference constants, and the tuned thresholds in `wom`.
They are not configuration, because a knob is a way of asking someone to tune
what should have been measured. See
[what is not extensible]({{ '/architecture/' | relative_url }}#what-is-not-extensible-and-why).

<div class="pager" markdown="1">
<span markdown="1">&larr; [plan]({{ '/plan/' | relative_url }})</span>
<span markdown="1">[The hierarchies]({{ '/architecture/hierarchies.html' | relative_url }}) &rarr;</span>
</div>
