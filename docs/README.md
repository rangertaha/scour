# The scour book

The design of the engine, in ten chapters. Every claim in them is checked
against the code by a test, so a chapter that drifts fails the build rather
than misleading somebody quietly.

Read it as a site at **[rangertaha.github.io/scour](https://rangertaha.github.io/scour)**,
or start here with [the overview](index.md).

## The order it runs in

The book follows a request: what a job is, how the request is made, what comes
back, and what is done with it.

| | Chapter | What it settles |
| --- | --- | --- |
| **Start** | [Four stages and a bus](index.md) | The shape, and why one arrow points backwards |
| | [One document, everything in it](job/index.md) | A job carries its own engine |
| **Fetching** | [Chains run both ways](chains/index.md) | Middleware wraps a stage, in both directions |
| | [Fetching, politely](downloader/index.md) | robots.txt and redirects, outside the chain |
| | [The cache is the corpus](cache/index.md) | Bodies are kept, because fetching is the expensive part |
| **Choosing** | [What to fetch next](frontier/index.md) | The queue is the crawler |
| **Extracting** | [Shapes, entities, measurements](items/index.md) | One declaration, three lives |
| | [A graph, not a list](pipeline/index.md) | Steps run when what they require has run |
| **Running it** | [Local until it has to be shared](cli/index.md) | Twelve commands, and what each one needs |
| | [Where everything lives](storage/index.md) | Eleven stores, one owner each |

## Reading it another way

- **What does a job look like?** [One document](job/index.md), then [the shapes](items/index.md) it extracts.
- **Why is it polite?** [robots.txt and redirects](downloader/index.md), then [the frontier](frontier/index.md), which is where politeness is decided.
- **What does it keep, and where?** [The cache](cache/index.md) for bodies, [storage](storage/index.md) for everything else.
- **How do I run one?** [The command line](cli/index.md).
- **How do I extend it?** [Chains](chains/index.md) for the stages, [the pipeline](pipeline/index.md) for the work on an item.

## How this folder is put together

`index.md` is the cover and each chapter is a directory, so a chapter has a
clean URL on the site and renders when you open the folder here.

`_config.yml` is gone: the site is built by MkDocs from `mkdocs.yml`, which is
what the other projects use. Diagrams live in `img/` and are referenced with
`<img>` rather than inlined, because GitHub strips inline `<svg>` from Markdown
and a referenced file is not inline. They are hand-drawn in GitHub's own palette
and carry a dark-mode block, so they sit in the page in either theme.
