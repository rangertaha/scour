// SPDX-License-Identifier: MIT

package parse

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/ledongthuc/pdf"

	"github.com/rangertaha/scour/internal/wom/internal/graph"
)

// parsePDF builds a page and line tree under doc from a PDF body. A PDF has no
// element structure to induce, so it produces the flattest tree wom supports:
// one graph.KindPage per page, one graph.KindLine per row of text. Values inside are
// located by page[n].line[n] plus a regex, and are the main reason the HMM
// decoder exists — field order along the line sequence is the only structural
// signal available here.
func parsePDF(doc *graph.Node, body []byte) (err error) {
	// The reader indexes into raw object tables and panics on malformed or
	// unusually encoded files rather than returning an error.
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("parse pdf: %v", r)
		}
	}()

	r, err := pdf.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return fmt.Errorf("parse pdf: %w", err)
	}

	for i := 1; i <= r.NumPage(); i++ {
		page := r.Page(i)
		if page.V.IsNull() {
			continue
		}
		pageNode := doc.Append(graph.New(graph.KindPage, "", ""))

		rows, rerr := page.GetTextByRow()
		if rerr != nil {
			// A page that will not decode should not lose the whole document.
			continue
		}
		for _, row := range rows {
			var b strings.Builder
			for _, word := range row.Content {
				b.WriteString(word.S)
			}
			if text := strings.Join(strings.Fields(b.String()), " "); text != "" {
				pageNode.Append(graph.New(graph.KindLine, "", text))
			}
		}
	}
	return nil
}
