// SPDX-License-Identifier: GPL-3.0-or-later

// Package content decides which content types a crawl may traverse.
//
// Filtering happens twice, so unwanted content costs as little as possible.
// Before a request, [Set.AllowsPath] rejects links whose extension clearly
// disagrees with the allowed types. After the response headers arrive,
// [Set.AllowsMIME] checks the real Content-Type so the body can be abandoned
// before it is downloaded.
package content

import (
	"fmt"
	"mime"
	"net/http"
	"path"
	"strings"
)

// The shorthand names, as constants because four tables have to agree on them
// and nothing was checking that they did. Shorthands says what each expands to,
// extensions maps filenames onto them, Extractable says which can be parsed,
// and callers name them in config. A misspelling in any one of those is a
// silent hole rather than an error, which is how .xml came to name only half of
// what it can be.
const (
	HTML    = "html"
	PDF     = "pdf"
	JSON    = "json"
	XML     = "xml"
	Feed    = "feed"
	Text    = "text"
	Image   = "image"
	CSS     = "css"
	JS      = "js"
	Archive = "archive"
	Video   = "video"
	Audio   = "audio"
)

// Shorthands are the friendly names accepted wherever a content type is given,
// and the MIME types each expands to.
var Shorthands = map[string][]string{
	HTML: {"text/html", "application/xhtml+xml"},
	PDF:  {"application/pdf"},
	JSON: {"application/json", "application/ld+json"},
	XML:  {"application/xml", "text/xml"},
	// Feeds get their own shorthand because almost nothing serves them as
	// plain xml. Sampled across a real list of news feeds, eight in twelve
	// arrived as application/rss+xml and only one as application/xml, so a
	// crawl restricted to "xml" would have skipped most of the feeds it was
	// pointed at.
	Feed: {
		"application/rss+xml", "application/atom+xml",
		"application/rdf+xml", "application/feed+json",
		"text/rss+xml", "text/atom+xml",
	},
	Text: {"text/plain", "text/markdown", "text/csv"},
	Image: {
		"image/*",
	},
}

// extensions maps a file extension to the shorthands it could belong to. It is
// used only to skip a request that is certain to be unwanted; an unknown
// extension is never grounds for skipping, since the header is the authority.
//
// Most extensions name one type. .xml names two, and getting that wrong costs
// more than anywhere else on this list: feed.xml, rss.xml, atom.xml and
// index.xml are how the web actually publishes feeds, and reading .xml as
// plain xml alone meant a crawl asked for feeds skipped them by their filename
// before it ever saw a Content-Type saying application/rss+xml. That is the
// same mistake the feed shorthand exists to correct, made one layer earlier.
var extensions = map[string][]string{
	".html": {HTML}, ".htm": {HTML}, ".xhtml": {HTML},
	".pdf":  {PDF},
	".json": {JSON}, ".jsonl": {JSON}, ".ndjson": {JSON},
	".xml": {XML, Feed}, ".rss": {Feed}, ".atom": {Feed}, ".rdf": {Feed},
	".txt": {Text}, ".md": {Text}, ".csv": {Text},
	".jpg": {Image}, ".jpeg": {Image}, ".png": {Image}, ".gif": {Image},
	".webp": {Image}, ".bmp": {Image}, ".ico": {Image}, ".svg": {Image},
	".css": {CSS}, ".js": {JS}, ".mjs": {JS},
	".zip": {Archive}, ".gz": {Archive}, ".tar": {Archive},
	".mp4": {Video}, ".webm": {Video}, ".mp3": {Audio},
}

// Extractable lists the shorthands scour can pull text out of. Types outside
// it are recorded with their status and size but never parsed, so allowing
// them costs bandwidth without adding matches.
var Extractable = map[string]bool{
	HTML: true, PDF: true, JSON: true, XML: true, Feed: true, Text: true,
}

// Set is a resolved allow and deny list of content types.
type Set struct {
	allow []pattern
	deny  []pattern
	names []string
}

// pattern is one MIME type, possibly with a wildcard subtype such as image/*.
type pattern struct {
	typ, sub string
}

func parsePattern(s string) (pattern, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	typ, sub, ok := strings.Cut(s, "/")
	if !ok || typ == "" || sub == "" {
		return pattern{}, fmt.Errorf("%q is not a MIME type or a known shorthand", s)
	}
	return pattern{typ: typ, sub: sub}, nil
}

func (p pattern) matches(typ, sub string) bool {
	if p.typ != "*" && p.typ != typ {
		return false
	}
	return p.sub == "*" || p.sub == sub
}

// New resolves allow and deny lists, each entry being a shorthand such as
// "html", a MIME type such as "application/pdf", or a wildcard such as
// "text/*". An empty allow list permits everything the deny list does not
// forbid.
func New(allow, deny []string) (*Set, error) {
	s := &Set{names: normaliseNames(allow)}
	var err error
	if s.allow, err = expand(allow); err != nil {
		return nil, err
	}
	if s.deny, err = expand(deny); err != nil {
		return nil, err
	}
	return s, nil
}

func expand(names []string) ([]pattern, error) {
	var out []pattern
	for _, name := range names {
		name = strings.ToLower(strings.TrimSpace(name))
		if name == "" {
			continue
		}
		if mimes, ok := Shorthands[name]; ok {
			for _, m := range mimes {
				p, err := parsePattern(m)
				if err != nil {
					return nil, err
				}
				out = append(out, p)
			}
			continue
		}
		p, err := parsePattern(name)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

func normaliseNames(names []string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		if n = strings.ToLower(strings.TrimSpace(n)); n != "" {
			out = append(out, n)
		}
	}
	return out
}

// Names returns the allow list as it was given, for display.
func (s *Set) Names() []string { return s.names }

// AllowsMIME reports whether a response with this Content-Type may be read.
// Parameters such as "; charset=utf-8" are ignored. An unparseable or absent
// type is allowed, since rejecting on a malformed header would drop pages that
// are otherwise fine.
func (s *Set) AllowsMIME(contentType string) bool {
	ct := strings.TrimSpace(contentType)
	if ct == "" {
		return true
	}
	if parsed, _, err := mime.ParseMediaType(ct); err == nil {
		ct = parsed
	} else if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	ct = strings.ToLower(ct)

	typ, sub, ok := strings.Cut(ct, "/")
	if !ok {
		return true
	}

	for _, p := range s.deny {
		if p.matches(typ, sub) {
			return false
		}
	}
	if len(s.allow) == 0 {
		return true
	}
	for _, p := range s.allow {
		if p.matches(typ, sub) {
			return true
		}
	}
	return false
}

// AllowsPath reports whether a URL path is worth requesting, judged by its
// extension alone. Only a known extension can rule a URL out; anything else
// gets the benefit of the doubt and is decided by [Set.AllowsMIME].
func (s *Set) AllowsPath(urlPath string) bool {
	ext := strings.ToLower(path.Ext(urlPath))
	if ext == "" {
		return true
	}
	shorthands, known := extensions[ext]
	if !known {
		return true
	}

	// An extension that could be more than one type is ruled out only when
	// every type it could be is unwanted. Skipping on the strength of the
	// likelier reading would drop the case the ambiguity exists for.
	var crawlable bool
	for _, shorthand := range shorthands {
		mimes, ok := Shorthands[shorthand]
		if !ok {
			continue
		}
		crawlable = true
		for _, m := range mimes {
			if s.AllowsMIME(m) {
				return true
			}
		}
	}
	if !crawlable {
		// A type we recognise but never crawl, such as css or an archive.
		// Allowed only if the operator asked for it by MIME type.
		return len(s.allow) == 0
	}
	return false
}

// Shorthand returns the friendly name for a Content-Type, or the bare MIME
// type when there is no shorthand for it. This is the FORMAT column.
// ShorthandOf names a response's format, sniffing the body when the server did
// not say.
//
// The header is the authority everywhere else in this package, and this is what
// happens when there is no authority to defer to. A response with no
// Content-Type is fetched, cached and counted, and then nothing reads it,
// because an empty format is not extractable: the page is lost in silence,
// which is the worst way to lose one. Servers omit the header often enough that
// this is not a curiosity.
//
// Sniffing is what a browser does in the same position, and
// [http.DetectContentType] implements the algorithm browsers agreed on, so the
// guess made here is the guess the rest of the web is already making. It is
// only ever a fallback: a header that says anything at all still wins.
func ShorthandOf(contentType string, body []byte) string {
	if s := Shorthand(contentType); s != "" {
		return s
	}
	if len(body) == 0 {
		return ""
	}
	return Shorthand(http.DetectContentType(body))
}

func Shorthand(contentType string) string {
	ct := strings.TrimSpace(contentType)
	if ct == "" {
		return ""
	}
	if parsed, _, err := mime.ParseMediaType(ct); err == nil {
		ct = parsed
	} else if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	ct = strings.ToLower(ct)

	typ, sub, ok := strings.Cut(ct, "/")
	if !ok {
		return ct
	}
	for name, mimes := range Shorthands {
		for _, m := range mimes {
			p, err := parsePattern(m)
			if err != nil {
				continue
			}
			if p.matches(typ, sub) {
				return name
			}
		}
	}
	return ct
}
