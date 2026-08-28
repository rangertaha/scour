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
  pipeline, wired directly by `scour crawl` and over NATS by `scour server`, held
  to producing the same records either way.
- Eight commands, in two levels: a command takes a file when it acts on a
  document and a name when it acts on a job the cluster holds. Help is grouped
  by what somebody is doing rather than alphabetically.
- A job service. It owns the job store, so a submission is parsed, validated
  and read through the job's own `mutation` policy before it is stored, and it
  drives the crawls, so starting one and asking how far it has got are
  questions to the same process. `scour job` create, list, show, spec, update,
  delete, start, stop, pause, resume, status, stats and watch.
- `scour cluster join` remembers a cluster, so the address is typed once rather
  than on every command, and `scour cluster list` says who is in it.
- A focused frontier in SQLite: leases, politeness per host, and a site's own
  `Crawl-delay` carried back from the downloader.
- Entity graph, event log and trained topics, shared behind `scour server`.
- Exporters for json, jsonlines, csv, parquet, nats and sqlite.
- The book, in ten chapters, checked against the code by tests.
- CI, release and documentation workflows, golangci-lint, GoReleaser and a
  Makefile.

### Changed

- The command tree gained a second level and the names moved with it. `try` is
  `scrape`, `run` is `crawl`, `serve` and `service` are one `server`, and
  `init`, `valid`, `show`, `spec`, `train` and `run` live under `job`. `ls` and
  `rm` are `list` and `delete` wherever they appeared. The old names are gone
  rather than aliased: two names for one command is a tree to learn twice.
- The marker written beside an induced locator is matched by a prefix that
  carries no command name, so a document written by an older scour is still
  recognised and the next rename cannot reach it.

[Unreleased]: https://github.com/rangertaha/scour/commits/main
