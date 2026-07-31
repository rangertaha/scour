// SPDX-License-Identifier: MIT

package parse

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/rangertaha/scour/internal/wom/internal/graph"
)

// parseXML builds an element tree under doc from an XML body. It serves plain
// XML, SVG, and RSS/Atom feeds alike, since all three are element trees
// addressed the same way.
//
// Element names keep only their local part so that XPath and selectors stay
// readable; the namespace, when present, is preserved as an "xmlns" attribute
// on the element.
func parseXML(doc *graph.Node, body []byte) error {
	dec := xml.NewDecoder(bytes.NewReader(body))
	// Real-world feeds are full of unescaped entities, so relax strictness and
	// accept the HTML entity table. AutoClose is deliberately not set: its
	// HTML rules treat <link> as void, which would break every RSS item.
	dec.Strict = false
	dec.Entity = xml.HTMLEntity

	stack := []*graph.Node{doc}
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			// Truncated or malformed feeds are common in the wild. Keep
			// whatever was decoded rather than discarding the document.
			if len(stack) > 1 {
				break
			}
			return fmt.Errorf("parse xml: %w", err)
		}

		switch t := tok.(type) {
		case xml.StartElement:
			parent := stack[len(stack)-1]
			el := parent.Append(graph.New(graph.KindElement, t.Name.Local, ""))
			if t.Name.Space != "" {
				el.Append(graph.New(graph.KindAttribute, "xmlns", t.Name.Space))
			}
			for _, a := range t.Attr {
				name := a.Name.Local
				if a.Name.Space != "" {
					name = a.Name.Space + ":" + a.Name.Local
				}
				el.Append(graph.New(graph.KindAttribute, name, a.Value))
			}
			stack = append(stack, el)
		case xml.EndElement:
			if len(stack) > 1 {
				stack = stack[:len(stack)-1]
			}
		case xml.CharData:
			if text := strings.TrimSpace(string(t)); text != "" {
				stack[len(stack)-1].Append(graph.New(graph.KindText, "", text))
			}
		}
	}
	return nil
}
