// SPDX-License-Identifier: GPL-3.0-or-later

package e2e

import (
	"bytes"
	"net/http"
	"strings"
)

// BaseToken is what a static file writes where it wants the server's own
// address.
//
// A fixture served on a random port cannot know its own host, and a link that
// has to be root-relative is a link that never exercises the absolute case.
// Real pages carry all three forms, often on the same page: an absolute URL to
// the canonical host, a root-relative one to a section, and a relative one to a
// sibling. Resolving all three is the crawler's job, so all three are here, and
// this token is how the first of them gets written down.
const BaseToken = "{{BASE}}"

// textual is the set of content types worth rewriting. A PNG containing the
// bytes of the token by coincidence must not be edited, and a PDF's byte
// offsets would stop matching its xref table if it were.
var textual = []string{"text/html", "text/plain", "application/xml", "text/xml",
	"application/json", "application/rss+xml", "application/atom+xml",
	"application/rdf+xml", "text/javascript", "application/javascript"}

// rewriteBase replaces [BaseToken] with the scheme and host the request came
// in on, so a file can name an absolute URL without knowing the port.
func rewriteBase(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &captured{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		body := rec.body.Bytes()
		if rewritable(rec.Header().Get("Content-Type")) {
			scheme := "http"
			if r.TLS != nil {
				scheme = "https"
			}
			body = bytes.ReplaceAll(body, []byte(BaseToken), []byte(scheme+"://"+r.Host))
		}

		// Content-Length is set after rewriting, because the replacement is
		// longer than the token and a stale length truncates the page.
		rec.Header().Del("Content-Length")
		rec.ResponseWriter.WriteHeader(rec.status)
		_, _ = rec.ResponseWriter.Write(body)
	})
}

func rewritable(contentType string) bool {
	ct := strings.ToLower(contentType)
	for _, t := range textual {
		if strings.Contains(ct, t) {
			return true
		}
	}
	return false
}

// captured buffers a response so its body can be edited before it is sent.
type captured struct {
	http.ResponseWriter
	body   bytes.Buffer
	status int
	wrote  bool
}

func (c *captured) WriteHeader(status int) {
	if !c.wrote {
		c.status, c.wrote = status, true
	}
}

func (c *captured) Write(p []byte) (int, error) { return c.body.Write(p) }
