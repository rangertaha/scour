// SPDX-License-Identifier: MIT

package wom_test

import (
	"testing"

	"github.com/rangertaha/scour/internal/wom"
)

func TestDetectFormat(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		contentType string
		uri         string
		body        string
		want        wom.Format
	}{
		{"html by content type", "text/html; charset=utf-8", "", "", wom.FormatHTML},
		{"json by content type", "application/json", "", "", wom.FormatJSON},
		{"vendor json suffix", "application/vnd.example+json", "", "", wom.FormatJSON},
		{"css by content type", "text/css", "", "", wom.FormatCSS},
		{"js by content type", "application/javascript", "", "", wom.FormatJS},
		{"pdf by content type", "application/pdf", "", "", wom.FormatPDF},

		// A generic XML type is refined by the payload, so feeds and SVG do
		// not collapse into plain XML.
		{"rss under generic xml", "application/xml", "", `<?xml version="1.0"?><rss><channel/></rss>`, wom.FormatFeed},
		{"atom under generic xml", "text/xml", "", `<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom"/>`, wom.FormatFeed},
		{"svg under generic xml", "application/xml", "", `<svg xmlns="http://www.w3.org/2000/svg"/>`, wom.FormatSVG},
		{"plain xml stays xml", "application/xml", "", `<?xml version="1.0"?><catalog/>`, wom.FormatXML},

		// Falling back to the URL, then to the bytes.
		{"by extension", "", "https://example.com/a/style.css", "", wom.FormatCSS},
		{"extension ignores query", "", "https://example.com/app.js?v=2", "", wom.FormatJS},
		{"generic type falls through to extension", "application/octet-stream", "https://example.com/d.pdf", "", wom.FormatPDF},
		{"sniff html", "", "https://example.com/page", "<!DOCTYPE html><html></html>", wom.FormatHTML},
		{"sniff json object", "", "https://example.com/data", `{"a":1}`, wom.FormatJSON},
		{"sniff json array", "", "https://example.com/data", `[1,2]`, wom.FormatJSON},
		{"sniff pdf", "", "https://example.com/file", "%PDF-1.7\n", wom.FormatPDF},
		{"sniff past a bom", "", "https://example.com/data", "\ufeff{\"a\":1}", wom.FormatJSON},

		{"nothing to go on", "", "", "", wom.FormatUnknown},
		{"unrecognized binary", "", "https://example.com/x", "\x00\x01\x02", wom.FormatUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := wom.DetectFormat(tt.contentType, tt.uri, []byte(tt.body))
			if got != tt.want {
				t.Errorf("DetectFormat(%q, %q, %q) = %v, want %v",
					tt.contentType, tt.uri, tt.body, got, tt.want)
			}
		})
	}
}

func TestFormatString(t *testing.T) {
	t.Parallel()

	for format, want := range map[wom.Format]string{
		wom.FormatUnknown: "unknown",
		wom.FormatHTML:    "html",
		wom.FormatFeed:    "feed",
		wom.FormatPDF:     "pdf",
		wom.Format(200):   "unknown",
	} {
		if got := format.String(); got != want {
			t.Errorf("Format(%d).String() = %q, want %q", format, got, want)
		}
	}
}
