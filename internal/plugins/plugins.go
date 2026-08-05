// SPDX-License-Identifier: GPL-3.0-or-later

// Package plugins is the list of what this build can do.
//
// Every extension point in scour is a registry, and a registry is filled by an
// `init` in the implementation's own package. That is what keeps a build which
// never wanted S3 from carrying its SDK, and it means a plugin exists only if
// something imported it for its side effect.
//
// # Why this package exists
//
// Because "something imported it" was a thing to remember, and nothing failed
// when it was forgotten. The `topic` middleware was written, tested and
// committed, and no binary could use it: a job naming it was refused with
// "nothing on this node implements topic". Nothing was broken, so nothing
// complained. The imports were also spread across two command files with no
// principle saying which belonged where, so there was nowhere to look and
// notice the gap.
//
// So the list lives here, in a package named for the job, and the command tree
// imports this and nothing else. **A new plugin is added here**, and the test
// beside this file fails if it is not: it walks the repository for packages
// that register something and checks each one is on the list. Forgetting is now
// a failing build rather than a feature that quietly does not exist.
package plugins

import (
	// Cache backends. Each carries its own SDK, so a build gets the ones it
	// asks for.
	_ "github.com/rangertaha/scour/internal/cache/gcs"
	_ "github.com/rangertaha/scour/internal/cache/local"
	_ "github.com/rangertaha/scour/internal/cache/s3"

	// Downloader middleware.
	_ "github.com/rangertaha/scour/internal/downloader/httpcache"

	// Scheduler middleware.
	_ "github.com/rangertaha/scour/internal/scheduler/dupefilter"
	_ "github.com/rangertaha/scour/internal/scheduler/topic"

	// Spider middleware.
	_ "github.com/rangertaha/scour/internal/spider/httperror"
	_ "github.com/rangertaha/scour/internal/spider/topic"

	// Pipeline step kinds.
	_ "github.com/rangertaha/scour/internal/pipeline/steps"

	// Exporters.
	_ "github.com/rangertaha/scour/internal/exporter/files"
	_ "github.com/rangertaha/scour/internal/exporter/nats"
	_ "github.com/rangertaha/scour/internal/exporter/sqlite"

	// Classifier kinds, which the topic middleware builds from a stored model.
	_ "github.com/rangertaha/scour/internal/classify/bayes"
	_ "github.com/rangertaha/scour/internal/classify/terms"
)
