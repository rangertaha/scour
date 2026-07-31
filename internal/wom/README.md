# internal/wom

The Web Object Model: scour's document engine. It folds HTTP exchanges and the
documents they carry (HTML, XML, SVG, RSS/Atom, JSON, JavaScript, CSS, PDF)
into one graph, then induces the XPath, CSS selector, native path and
extraction regex that locate a described field, each with a probability
attached.

Everything downstream of the fetch runs on it: parsing, induction
(`scour train`), the learned locators (`scour rules`), and extraction
(`scour search`).

This is scour's own code, developed here. It is held to the same standards as
the rest of the tree: formatted, vetted, linted, and tested in CI with
everything else. Change it directly, the way you would change any other
package.

## Why it is a package rather than a dependency

The engine is separable from the crawler on purpose. wom knows nothing about
crawling, scoring, queues, or storage, and scour's other packages reach it only
through the aliases in `wom.go`. That boundary is what keeps induction testable
against fixed documents rather than a live site, and it is why the matcher and
the field-order chain are replaceable without touching graph construction.

It lives under `internal/` because scour ships a command, not a library. If it
ever needs to be consumed on its own, the seam is already in the right place.

## Layout

| Path | What it does |
| --- | --- |
| `wom.go`, `option.go` | The public surface: `WOM`, `Prop`, `Item`, `Model`, and the aliases the rest of scour uses |
| `internal/graph` | The document graph: nodes, kinds, paths, XPath and selector synthesis |
| `internal/parse` | Format detection and the per-dialect parsers |
| `internal/match` | Scoring how strongly a node satisfies a property. The single seam where semantic judgement lives |
| `internal/infer` | Turning scored nodes into located items |
| `internal/pattern` | Regex and URI pattern synthesis from examples |
| `internal/seq` | The field-order hidden Markov chain |
| `internal/schema` | Props, types, locators, items |
| `internal/model` | The reusable product of induction, and extraction with it |

## Licence

The sources carry `SPDX-License-Identifier: MIT` headers and `LICENSE` holds
the MIT notice. scour as a whole is GPL-3.0, which MIT is compatible with.
Keep `LICENSE` and the headers: the notice has to travel with the code that was
released under it, and that obligation does not change with how the directory
is organised.

## A fix worth knowing about

**`internal/pattern/regex.go`, `SynthesizeURI`: keep the trailing slash.** The
function split the path on `strings.Trim(u.Path, "/")` and rebuilt the pattern
anchored with `$`, dropping a trailing slash the input URLs had. For
directory-style URLs, which is most sites, the result was a pattern matching
none of the URLs it was synthesized from, so every locator carrying a URI
pattern matched nothing and extraction returned no records at all. The pattern
now ends in `/` when every input had one, `/?` when only some did, and is
unchanged otherwise. Covered by `TestSynthesizeURIKeepsTrailingSlash` in
`internal/pattern/pattern_test.go`.
