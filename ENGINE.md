# The engine

scour is a generic crawler. The engine does not know what you are looking for,
and is not allowed to: what specializes it is trained, not compiled in. This
document is what the parts are, how they fit, and where to add your own.

The one rule that governs the rest: **if a behaviour cannot be expressed by
teaching, and it still changes the answer, it is in the wrong place.** Teaching
means the schema, the examples, the aliases, the value and name patterns, the
per-domain overrides, and the marks a person puts on records.

## The path a page takes

```
   targets                                          records
      |                                                ^
      v                                                |
  [ frontier ] --url--> [ transport ] --body--> [ cache ]
      ^                       |                        |
      |                       v                        v
   [ score ] <--roles-- [ nodeclass ]            [ parse ] --graph--> [ wom ]
                                                                         |
                                                          [ matcher ] <--+
                                                                         v
                                                                   [ locators ]
```

A crawl takes a URL from the frontier, fetches it through a transport, and
stores the body in the cache. Training reads the cache back, parses every body
into one graph, and induces the locators that find each property. Scoring
decides what the frontier hands out next, and is the only part of the loop that
learns from the last run.

## The parts

| Package | What it owns |
| --- | --- |
| `store` | Persistence. Owns the gorm models, the frontier, and every query. |
| `crawl` | Drives colly: the fetch loop, budgets, politeness, escalation. |
| `transport` | How a request reaches the network. |
| `cache` | Fetched page bodies, so a re-crawl and a retrain cost nothing twice. |
| `parse` | Turns cached pages into one wom graph. |
| `wom` | The Web Object Model: HTML, JSON, XML, PDF as one addressable graph, and induction over it. |
| `matcher` | How strongly one node satisfies one property. |
| `score` | How likely a URL is to lead to a match. |
| `nodeclass` | What a node of the crawl graph is: its role, its topic, its recency. |
| `classify` | What a fetched page is about, per page. |
| `train` | Induces an item's rules from its cached pages, and fits the chains. |
| `export` | Writes extracted records somewhere useful. |
| `bus` | How components talk when they are not in one process. |
| `service` | Runs those components, in any combination. |
| `server` | The HTTP API and MCP. |
| `cli` | The command line, grouped by what it does. |
| `registry` | The extension point the pluggable parts share. |
| `ai` | Access to language models, for the parts that consult one. |
| `config` | config.toml, environment, flag precedence. |
| `content` | Which content types a crawl may traverse. |
| `defaults` | The schemas scour ships with. |
| `tui` | What the live view shows, apart from how it is drawn. |

## Extension points

Every pluggable part is a registry. They differ in what they produce and in what
they are built from, and in nothing else, so they share one implementation in
`internal/registry`.

| Kind | Interface | Ships with | Selected by |
| --- | --- | --- | --- |
| transport | `http.RoundTripper` | `http`, `webdriver` | `[[host]] transport` |
| score | `Scorer` | `bayes`, `embed` | `[model] scorer` |
| matcher | `wom.Matcher` | `heuristic`, `llm` | `[model] matcher` |
| classify | `Classifier` | `llm` | `[model] classifier` |
| nodeclass | `Classifier` | `recency`*, `topic`* | not yet wired |
| cache | `Store` | `local`, `s3`†, `gcs`† | `[cache] driver` |
| export | `Exporter` | `csv`, `json`, `webhook` | `scour stream --write` |
| ai | provider | `anthropic`, `ollama` | `[[ai]] provider` |

\* registered, not written yet: they answer `ErrNotImplemented` so a name in a
config file resolves to "this is planned" rather than to "unknown", which reads
as a typo.
† compiled in only with `-tags cloud`, which costs 41MB of SDK.

### Adding an implementation

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
name is looked up.

### Adding a kind of extension

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

## Classifying the crawl graph

`nodeclass` is the newest point and the one with the most room, so it is worth
saying what it is for.

A node is a URL, and the graph is how the crawl reached it. Whether a page holds
records is a fact about the URL and its place in the site, not about any element
on it: answering it from the markup means asking the same question of every node
in a document instead of once per page, and "this page links to records but
holds none" cannot be seen from the markup at all.

A node carries more than one answer. What role it plays, what it is about, how
fresh it is: different questions, different vocabularies, different evidence. So
a classifier declares its `Kind`, and verdicts are held per kind. Two
classifiers of one kind are alternatives; two of different kinds both run.

The role question already has an answer that nothing reads. `internal/score/hmm`
decodes six roles over the parent path of every URL, stores them, and `scour
item ls` prints them. Outside that package `hmm.Detail` has no callers, so
extraction runs over every fetched page whatever the crawl graph concluded. That
is why 118 of 867 titles on the second corpus are `"Page A1"`, `"Ads"`,
`"Community"`. Moving that decoder behind `nodeclass` and reading its answer is
the next piece of work, and it is reading a decision already made.

## What is not extensible, and why

The six roles, the inference constants, and the tuned thresholds in `wom` are
the engine's own. They are not configuration, because a knob is a way of asking
someone to tune what should have been measured. They are changed by measuring
against the corpora and re-measuring, which is what `README.md` records.

Entries like `semanticTags` are allowed to name HTML elements because their
meaning comes from the specification rather than from a corpus: `<time>` is a
date on a Greek page as much as an English one. A table of words that only
holds for news would not be allowed there, and belongs in a shipped schema
under `defaults`, where it is a starting point somebody can replace.
