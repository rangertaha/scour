// SPDX-License-Identifier: MIT

package parse

import (
	"bytes"
	"fmt"
	"strings"

	"golang.org/x/net/html"

	"github.com/rangertaha/scour/internal/wom/internal/graph"
)

// parseHTML builds an element tree under doc from an HTML body. Comments and
// doctypes are dropped; everything else, including the text inside <script>
// and <style>, is kept, because inline JSON-LD is one of the richest sources
// of structured data a page has.
func parseHTML(doc *graph.Node, body []byte) error {
	root, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("parse html: %w", err)
	}
	buildHTML(doc, root)
	return nil
}

// embedJSONLD turns a <script type="application/ld+json"> block into a real
// JSON subtree rather than one opaque run of text. Schema.org metadata is
// where sites put the data they most want machines to read — publication
// dates, authors, prices — so leaving it as a single text node would hide the
// richest structured source on the page behind a wall of punctuation.
//
// The JSON hangs off the script element as its own nested document, which is
// what makes it addressable by JSONPath while still sitting at a known XPath
// inside the page.
func embedJSONLD(el *graph.Node, hn *html.Node) bool {
	if hn.Data != "script" {
		return false
	}
	var isLD bool
	for _, a := range hn.Attr {
		if strings.EqualFold(a.Key, "type") && strings.Contains(strings.ToLower(a.Val), "ld+json") {
			isLD = true
			break
		}
	}
	if !isLD {
		return false
	}

	var raw strings.Builder
	for c := hn.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.TextNode {
			raw.WriteString(c.Data)
		}
	}
	if strings.TrimSpace(raw.String()) == "" {
		return false
	}

	nested := graph.NewDocument(graph.FormatJSON)
	if err := parseJSON(nested, []byte(raw.String())); err != nil {
		// Not valid JSON after all; let the caller keep it as plain text.
		return false
	}
	el.Append(nested)
	return true
}

func buildHTML(parent *graph.Node, hn *html.Node) {
	for c := hn.FirstChild; c != nil; c = c.NextSibling {
		switch c.Type {
		case html.ElementNode:
			el := parent.Append(graph.New(graph.KindElement, c.Data, ""))
			for _, a := range c.Attr {
				name := a.Key
				if a.Namespace != "" {
					name = a.Namespace + ":" + a.Key
				}
				el.Append(graph.New(graph.KindAttribute, name, a.Val))
			}
			if embedJSONLD(el, c) {
				continue
			}
			buildHTML(el, c)
		case html.TextNode:
			if text := strings.TrimSpace(c.Data); text != "" {
				parent.Append(graph.New(graph.KindText, "", text))
			}
		case html.ErrorNode, html.DocumentNode, html.CommentNode,
			html.DoctypeNode, html.RawNode:
			// Not addressable content; skip but keep descending where the
			// parser nests real nodes underneath.
			buildHTML(parent, c)
		}
	}
}
