// SPDX-License-Identifier: MIT

// Package parse turns response bodies into graph node trees. Every format ends
// up in the same shape, which is what lets one locator address a value in any
// of them.
package parse

import (
	"errors"

	"github.com/rangertaha/scour/internal/wom/internal/graph"
)

// ErrUnknownFormat is returned for a body whose format could not be
// determined.
var ErrUnknownFormat = errors.New("unrecognized document format")

// Into parses body into doc according to format. doc is expected to be a
// detached document node, so a failed parse can be discarded without ever
// touching the graph.
func Into(doc *graph.Node, format graph.Format, body []byte) error {
	switch format {
	case graph.FormatHTML:
		return parseHTML(doc, body)
	case graph.FormatXML, graph.FormatSVG, graph.FormatFeed:
		return parseXML(doc, body)
	case graph.FormatJSON:
		return parseJSON(doc, body)
	case graph.FormatJS:
		return parseJS(doc, body)
	case graph.FormatCSS:
		return parseCSS(doc, body)
	case graph.FormatPDF:
		return parsePDF(doc, body)
	case graph.FormatUnknown:
		return ErrUnknownFormat
	}
	return ErrUnknownFormat
}
