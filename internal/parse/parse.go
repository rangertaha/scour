// SPDX-License-Identifier: GPL-3.0-or-later

// Package parse turns an entity's cached pages into a wom graph.
//
// Everything downstream of the fetch works on that graph: induction reads it to
// locate fields, extraction applies a model to it. Building it from the cache
// rather than the network is what lets training and re-extraction run without
// crawling again.
package parse

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"

	"github.com/rangertaha/scour/internal/cache"
	"github.com/rangertaha/scour/internal/content"
	"github.com/rangertaha/scour/internal/store"
	"github.com/rangertaha/scour/internal/wom"
)

// ErrNoPages is returned when an entity has nothing cached to work from.
var ErrNoPages = errors.New("no cached pages: run scour crawl first")

// Result is a built graph and what went into it.
type Result struct {
	Graph   *wom.WOM
	Pages   int
	Bytes   int64
	Skipped int
	URLs    []store.URL
}

// Options controls which cached pages are loaded.
type Options struct {
	// Limit caps how many pages are read, newest first. Induction over a few
	// hundred pages is usually as good as over thousands and much faster.
	Limit int
	// Types restricts loading to formats scour can extract text from. Nil
	// means every extractable format.
	Types *content.Set
	// WOM configures the graph itself: the matcher that scores candidates and
	// the chain that orders fields. Options belong here rather than at the
	// call site because a graph's options are fixed when it is built.
	WOM []wom.Option
}

// Load reads an entity's cached pages into a graph.
//
// A page whose body has fallen out of the cache is skipped rather than fatal:
// the cache is disposable by design, so a partial corpus is a smaller corpus,
// not a failure.
func Load(ctx context.Context, s *store.Store, pages *cache.Cache, entityID uint, opts Options) (*Result, error) {
	rows, err := s.FetchedURLs(ctx, entityID)
	if err != nil {
		return nil, err
	}

	res := &Result{Graph: wom.New(opts.WOM...)}
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if opts.Limit > 0 && res.Pages >= opts.Limit {
			break
		}
		if !extractable(row.ContentType, opts.Types) {
			res.Skipped++
			continue
		}

		body, err := pages.Get(row.URL)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				res.Skipped++
				continue
			}
			return nil, err
		}

		if err := res.Graph.AddBody(row.URL, mimeOf(row.ContentType), body); err != nil {
			// One unparseable document should not stop the others; wom already
			// reports the format it could not read.
			slog.Debug("page not parsed", "url", row.URL, "err", err)
			res.Skipped++
			continue
		}

		res.Pages++
		res.Bytes += int64(len(body))
		res.URLs = append(res.URLs, row)
	}

	if res.Pages == 0 {
		return nil, fmt.Errorf("entity has %d fetched pages, none of them parseable: %w", len(rows), ErrNoPages)
	}
	return res, nil
}

// extractable reports whether a stored format is worth parsing.
func extractable(format string, types *content.Set) bool {
	if format == "" {
		return false
	}
	if !content.Extractable[format] {
		return false
	}
	if types != nil && !types.AllowsMIME(mimeOf(format)) {
		return false
	}
	return true
}

// mimeOf turns a stored shorthand back into a MIME type, which is what wom
// wants for format detection.
func mimeOf(format string) string {
	if mimes, ok := content.Shorthands[format]; ok && len(mimes) > 0 {
		return mimes[0]
	}
	return format
}
