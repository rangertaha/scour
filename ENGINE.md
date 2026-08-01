# The engine

The documentation lives in [docs/](docs/) and is published with its diagrams at
<https://rangertaha.github.io/scour/>.

One directory per top-level component, each page opening with the diagram of
what that component decides:

| Page | Covers |
| --- | --- |
| [architecture](docs/architecture/index.md) | What the parts are, how a page moves through them, the two graphs, what is deliberately not extensible |
| [plan](docs/plan/index.md) | Why each part is where it is, and what is built against what is still designed |
| [extending it](docs/architecture/extending.md) | Every extension point, what ships in it, how to add an implementation and how to add a kind |
| [the hierarchies](docs/architecture/hierarchies.md) | What an item owns, what a schema becomes, what the crawl discovers |
| [crawl](docs/crawl/index.md) · [transport](docs/transport/index.md) · [schedule](docs/schedule/index.md) · [cache](docs/cache/index.md) | Fetching |
| [parse & wom](docs/parse/index.md) · [matcher](docs/matcher/index.md) · [train](docs/train/index.md) · [classify](docs/classify/index.md) · [score](docs/score/index.md) | Understanding |
| [the algorithms](docs/algorithms/index.md) · [the markup](docs/algorithms/markup.md) | Every algorithm in one place, and the corpus evidence behind them |
| [store](docs/store/index.md) · [export](docs/export/index.md) | Keeping |
| [bus & service](docs/bus/index.md) · [server & MCP](docs/server/index.md) · [command line](docs/cli/index.md) | Interfaces |
| [measured results](docs/results/index.md) | What extraction achieves on live corpora |
| [the command surface](docs/cli/design.md) · [the HTTP API](docs/server/api.md) | Designed ahead of what ships, with migration tables |
