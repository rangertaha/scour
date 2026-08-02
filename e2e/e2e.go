// SPDX-License-Identifier: GPL-3.0-or-later

// Package e2e serves a site built out of the cases that have actually broken
// extraction.
//
// Extraction is measured against live corpora: 808 pages from 19 news sites,
// ten live feeds, and 1,267 pages from 30 more. That is the right way to find a
// fault and the wrong way to keep it fixed. A live site needs the network, is
// slow, and changes underneath the measurement, so a regression shows up as a
// number moving rather than as a test failing, and only when somebody reruns
// the corpus.
//
// Every page here exists because something real went wrong on it. The comment
// at the top of each says what, in the words of README's "What the corpora
// exposed" table, so a fixture cannot quietly become decoration.
//
// The pages are files rather than string constants because a fixture you can
// open in a browser is a fixture people will add to. They are embedded, so a
// test needs no working directory and the site travels with the binary.
package e2e

import (
	"embed"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"testing"
)

//go:embed site
var content embed.FS

// FS is the site as a file system, rooted at the site directory.
func FS() fs.FS {
	sub, err := fs.Sub(content, "site")
	if err != nil {
		// Unreachable: the directory is embedded above, so this can only fail
		// if the embed itself is wrong, which is a compile-time fact.
		panic(err)
	}
	return sub
}

// Handler serves the site.
//
// The static files are served by the standard file server, and the routes that
// cannot be a file, because they turn on headers, status, a query or the clock,
// are registered in front of it. Anything that can be a file is one.
//
// There are two muxes because [rewriteBase] has to buffer a response to edit
// it, and buffering is exactly wrong for a stream: an event stream that is held
// until it ends is not a stream. So the streaming routes are matched first, on
// an outer mux that does not rewrite, and everything else goes through one that
// does.
func Handler() http.Handler {
	pages := http.NewServeMux()
	registerDynamic(pages)
	registerAPI(pages)
	registerLongform(pages)
	registerSearch(pages)
	registerLive(pages)
	registerPrivate(pages)
	pages.Handle("/", http.FileServerFS(FS()))

	mux := http.NewServeMux()
	registerStreams(mux)
	mux.Handle("/", rewriteBase(pages))
	return mux
}

// readFile reads one of the embedded files by its path under site.
func readFile(name string) ([]byte, error) { return fs.ReadFile(FS(), name) }

// Server starts the site on a random port and stops it when the test ends.
func Server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(Handler())
	t.Cleanup(srv.Close)
	return srv
}
