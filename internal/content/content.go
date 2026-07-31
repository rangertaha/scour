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
	"path"
	"strings"
)

// Shorthands are the friendly names accepted wherever a content type is given,
// and the MIME types each expands to.
var Shorthands = map[string][]string{
	"html": {"text/html", "application/xhtml+xml"},
	"pdf":  {"application/pdf"},
	"json": {"application/json", "application/ld+json"},
	"xml":  {"application/xml", "text/xml"},
	// Feeds get their own shorthand because almost nothing serves them as
	// plain xml. Sampled across a real list of news feeds, eight in twelve
	// arrived as application/rss+xml and only one as application/xml, so a
	// crawl restricted to "xml" would have skipped most of the feeds it was
	// pointed at.
	"feed": {
		"application/rss+xml", "application/atom+xml",
		"application/rdf+xml", "application/feed+json",
		"text/rss+xml", "text/atom+xml",
	},
	"text": {"text/plain", "text/markdown", "text/csv"},
	"image": {
		"image/*",
	},
}

// extensions maps a file extension to the shorthand it belongs to. It is used
// only to skip a request that is certain to be unwanted; an unknown extension
// is never grounds for skipping, since the header is the authority.
var extensions = map[string]string{
	".html": "html", ".htm": "html", ".xhtml": "html",
	".pdf":  "pdf",
	".json": "json", ".jsonl": "json", ".ndjson": "json",
	".xml": "xml", ".rss": "feed", ".atom": "feed", ".rdf": "feed",
	".txt": "text", ".md": "text", ".csv": "text",
	".jpg": "image", ".jpeg": "image", ".png": "image", ".gif": "image",
	".webp": "image", ".bmp": "image", ".ico": "image", ".svg": "image",
	".css": "css", ".js": "js", ".mjs": "js",
	".zip": "archive", ".gz": "archive", ".tar": "archive",
	".mp4": "video", ".webm": "video", ".mp3": "audio",
}

// Extractable lists the shorthands scour can pull text out of. Types outside
// it are recorded with their status and size but never parsed, so allowing
// them costs bandwidth without adding matches.
var Extractable = map[string]bool{
	"html": true, "pdf": true, "json": true, "xml": true, "feed": true, "text": true,
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
	shorthand, known := extensions[ext]
	if !known {
		return true
	}
	mimes, ok := Shorthands[shorthand]
	if !ok {
		// A type we recognise but never crawl, such as css or an archive.
		// Allowed only if the operator asked for it by MIME type.
		return len(s.allow) == 0
	}
	for _, m := range mimes {
		if s.AllowsMIME(m) {
			return true
		}
	}
	return false
}

// Shorthand returns the friendly name for a Content-Type, or the bare MIME
// type when there is no shorthand for it. This is the FORMAT column.
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
