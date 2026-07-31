// SPDX-License-Identifier: MIT

package graph

import (
	"bytes"
	"mime"
	"path"
	"strings"
)

// Format identifies the document dialect of a response body. It selects both
// the parser used to build the node tree and the locator dialect used to
// address nodes inside it.
type Format uint8

// The document formats wom can ingest.
const (
	FormatUnknown Format = iota
	FormatHTML
	FormatXML
	FormatSVG
	FormatFeed // RSS or Atom
	FormatJSON
	FormatJS
	FormatCSS
	FormatPDF
)

// unknownName is the label reported for any unrecognized format or kind.
const unknownName = "unknown"

var formatNames = [...]string{
	FormatUnknown: unknownName,
	FormatHTML:    "html",
	FormatXML:     "xml",
	FormatSVG:     "svg",
	FormatFeed:    "feed",
	FormatJSON:    "json",
	FormatJS:      "js",
	FormatCSS:     "css",
	FormatPDF:     "pdf",
}

// String returns the lowercase name of the format, e.g. "html".
func (f Format) String() string {
	if int(f) >= len(formatNames) {
		return unknownName
	}
	return formatNames[f]
}

// formatByName is the reverse of formatNames, for decoding.
var formatByName = func() map[string]Format {
	m := make(map[string]Format, len(formatNames))
	for i, name := range formatNames {
		m[name] = Format(i)
	}
	return m
}()

// MarshalText encodes the format as its lowercase name, so a saved model
// stays readable and survives a renumbering of the constants.
func (f Format) MarshalText() ([]byte, error) {
	return []byte(f.String()), nil
}

// UnmarshalText decodes a format written by MarshalText. An unrecognized name
// decodes to FormatUnknown rather than failing, so a model written by a newer
// build still loads its other fields.
func (f *Format) UnmarshalText(b []byte) error {
	*f = formatByName[string(b)]
	return nil
}

// Markup reports whether the format is an element tree addressable by XPath
// and CSS selectors.
func (f Format) Markup() bool {
	switch f {
	case FormatHTML, FormatXML, FormatSVG, FormatFeed:
		return true
	case FormatUnknown, FormatJSON, FormatJS, FormatCSS, FormatPDF:
		return false
	}
	return false
}

// mediaFormats maps a bare media type to a format. Types that need to be
// told apart by their payload (application/xml may be a feed, SVG, or plain
// XML) resolve to FormatXML here and are refined by sniffing.
var mediaFormats = map[string]Format{
	"text/html":                    FormatHTML,
	"application/xhtml+xml":        FormatHTML,
	"text/xml":                     FormatXML,
	"application/xml":              FormatXML,
	"image/svg+xml":                FormatSVG,
	"application/rss+xml":          FormatFeed,
	"application/atom+xml":         FormatFeed,
	"application/feed+json":        FormatJSON,
	"application/json":             FormatJSON,
	"text/json":                    FormatJSON,
	"application/javascript":       FormatJS,
	"text/javascript":              FormatJS,
	"application/x-javascript":     FormatJS,
	"application/ecmascript":       FormatJS,
	"module/javascript":            FormatJS,
	"text/css":                     FormatCSS,
	"application/pdf":              FormatPDF,
	"application/x-pdf":            FormatPDF,
	"application/octet-stream":     FormatUnknown,
	"application/ld+json":          FormatJSON,
	"application/manifest+json":    FormatJSON,
	"application/vnd.api+json":     FormatJSON,
	"application/problem+json":     FormatJSON,
	"application/schema+json":      FormatJSON,
	"application/geo+json":         FormatJSON,
	"application/vnd.geo+json":     FormatJSON,
	"application/json-patch+json":  FormatJSON,
	"application/merge-patch+json": FormatJSON,
}

// extFormats maps a URL path extension to a format, used when the
// Content-Type is missing or uselessly generic.
var extFormats = map[string]Format{
	".html":  FormatHTML,
	".htm":   FormatHTML,
	".xhtml": FormatHTML,
	".xml":   FormatXML,
	".svg":   FormatSVG,
	".rss":   FormatFeed,
	".atom":  FormatFeed,
	".json":  FormatJSON,
	".jsonl": FormatJSON,
	".js":    FormatJS,
	".mjs":   FormatJS,
	".cjs":   FormatJS,
	".css":   FormatCSS,
	".pdf":   FormatPDF,
}

// DetectFormat resolves the format of a body from its Content-Type header,
// the extension of its URL, and finally the leading bytes of the body. Later
// steps only run when the earlier ones are inconclusive, except that a
// generic XML type is always refined by sniffing so feeds and SVG are not
// flattened into plain XML.
func DetectFormat(contentType, uri string, body []byte) Format {
	f := formatFromContentType(contentType)
	if f == FormatXML {
		if sniffed := sniff(body); sniffed != FormatUnknown && sniffed != FormatHTML {
			return sniffed
		}
		return FormatXML
	}
	if f != FormatUnknown {
		return f
	}
	if f = formatFromURI(uri); f != FormatUnknown {
		return f
	}
	return sniff(body)
}

func formatFromContentType(contentType string) Format {
	if contentType == "" {
		return FormatUnknown
	}
	media, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		// Tolerate malformed headers such as "text/html;charset" by keeping
		// everything before the first parameter.
		media = strings.TrimSpace(strings.ToLower(contentType))
		if i := strings.IndexByte(media, ';'); i >= 0 {
			media = strings.TrimSpace(media[:i])
		}
	}
	media = strings.ToLower(media)
	if f, ok := mediaFormats[media]; ok {
		return f
	}
	// Unregistered vendor types still carry a usable suffix, e.g.
	// application/vnd.example+json.
	if i := strings.LastIndexByte(media, '+'); i >= 0 {
		switch media[i+1:] {
		case "json":
			return FormatJSON
		case "xml":
			return FormatXML
		}
	}
	return FormatUnknown
}

func formatFromURI(uri string) Format {
	if uri == "" {
		return FormatUnknown
	}
	// Trim any query or fragment before looking at the extension.
	if i := strings.IndexAny(uri, "?#"); i >= 0 {
		uri = uri[:i]
	}
	return extFormats[strings.ToLower(path.Ext(uri))]
}

// sniff inspects the leading bytes of a body. It is deliberately conservative:
// it only reports a format when the payload carries an unambiguous marker.
func sniff(body []byte) Format {
	// The cutset includes U+FEFF so a byte-order mark does not hide the
	// marker that identifies the payload.
	trimmed := bytes.TrimLeft(body, " \t\r\n\uFEFF")
	if len(trimmed) == 0 {
		return FormatUnknown
	}
	if bytes.HasPrefix(trimmed, []byte("%PDF-")) {
		return FormatPDF
	}

	// Look past an XML declaration, doctype, or comments for the root element.
	head := trimmed
	if len(head) > 2048 {
		head = head[:2048]
	}
	lower := bytes.ToLower(head)
	switch {
	case bytes.Contains(lower, []byte("<!doctype html")), bytes.Contains(lower, []byte("<html")):
		return FormatHTML
	case bytes.Contains(lower, []byte("<svg")):
		return FormatSVG
	case bytes.Contains(lower, []byte("<rss")), bytes.Contains(lower, []byte("<feed")),
		bytes.Contains(lower, []byte("<rdf:rdf")):
		return FormatFeed
	case bytes.HasPrefix(trimmed, []byte("<?xml")), bytes.HasPrefix(trimmed, []byte("<")):
		return FormatXML
	case trimmed[0] == '{', trimmed[0] == '[':
		return FormatJSON
	}
	return FormatUnknown
}
