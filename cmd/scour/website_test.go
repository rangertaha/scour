// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rangertaha/scour/internal/mocksite"
)

// website is [mocksite.Site] behind a real listener, which is what a crawl
// needs and what a test can point one at.
//
// The site itself is a program: see cmd/mocksite. The tests use its handler
// rather than a copy, so the thing a person debugs against by hand and the
// thing the suite runs against are the same site.
type website struct {
	*httptest.Server
	*mocksite.Site
}

func newWebsite(t *testing.T, opts mocksite.Options) *website {
	t.Helper()

	site := mocksite.New(opts)
	server := httptest.NewServer(site)
	t.Cleanup(server.Close)
	return &website{Server: server, Site: site}
}

// host is the site's authority, which is what a job's `domains` wants.
func (w *website) host() string { return strings.TrimPrefix(w.URL, "http://") }

// crawl writes a job document pointed at the site, with whatever extra blocks
// a test needs folded in.
func (w *website) crawl(t *testing.T, extra string) string {
	t.Helper()

	return document(t, fmt.Sprintf(`
job "news" {
  domains = ["%s"]
  start   = ["%s/"]

  item "article" {
    property "title" {
      type     = str
      required = true
    }
  }

  scheduler {
    rate = "1ms"
  }
%s
}
`, w.host(), w.URL, extra))
}

// asking reports what the site was asked for, for a failure message that says
// what happened rather than leaving somebody to guess.
func (w *website) asking() string {
	var b strings.Builder
	for path, count := range w.Paths() {
		fmt.Fprintf(&b, "\n  %-28s %d", path, count)
	}
	return b.String()
}

// slowly is the option a timeout test wants and nothing else does.
func slowly(d time.Duration) mocksite.Options { return mocksite.Options{Slow: d} }
