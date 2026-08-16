# The scour book

The design of the engine, in ten chapters. Every claim in them is checked
against the code by a test, so a chapter that drifts fails the build rather
than misleading somebody quietly.

Read it as a site at **[rangertaha.github.io/scour](https://rangertaha.github.io/scour)**,
or start here with [the overview](index.md).

| Chapter | |
| --- | --- |
| 1 | [Four stages and a bus](index.md) |
| 2 | [One document, everything in it](job/) |
| 3 | [Chains run both ways](chains/) |
| 4 | [Fetching, politely](downloader/) |
| 5 | [The cache is the corpus](cache/) |
| 6 | [What to fetch next](frontier/) |
| 7 | [Shapes, entities, measurements](items/) |
| 8 | [A graph, not a list](pipeline/) |
| 9 | [Local until it has to be shared](cli/) |
| 10 | [Where everything lives](storage/) |

`_config.yml` and `_layouts/` are what GitHub Pages builds the site from. The
diagrams in `img/` are rendered from the chapters and referenced with `<img>`,
so they survive both readings: GitHub strips inline `<svg>` from Markdown, and
a referenced file is not inline.
