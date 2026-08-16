# Changelog

All notable changes to this project are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

Nothing has been released yet. `scour` is on its second implementation: the
engine is built and tested, and what is not built is listed in the last chapter
of [the book](docs/index.md).

### Added

- The four stages and the bus between them: scheduler, downloader, spider and
  pipeline, wired directly by `scour run` and over NATS by `scour serve`, held
  to producing the same records either way.
- Twelve commands. The five that read a document need nothing running.
- A focused frontier in SQLite: leases, politeness per host, and a site's own
  `Crawl-delay` carried back from the downloader.
- Entity graph, event log and trained topics, shared behind `scour service`.
- Exporters for json, jsonlines, csv, parquet, nats and sqlite.
- The book, in ten chapters, checked against the code by tests.
- CI, release and documentation workflows, golangci-lint, GoReleaser and a
  Makefile.

[Unreleased]: https://github.com/rangertaha/scour/commits/main
