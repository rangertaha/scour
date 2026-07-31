// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// align says which way a table column is padded.
type align int

// Column alignments.
const (
	alignLeft align = iota
	alignRight
)

// table renders the aligned, dash-underlined tables the CLI prints, matching
// the layout documented in the README.
type table struct {
	headers []string
	aligns  []align
	rows    [][]string
}

// newTable starts a table. One alignment per header is required.
func newTable(headers []string, aligns ...align) *table {
	if len(aligns) != len(headers) {
		panic("table: one alignment per header")
	}
	return &table{headers: headers, aligns: aligns}
}

// add appends a row. Short rows are padded with empty cells.
func (t *table) add(cells ...string) {
	row := make([]string, len(t.headers))
	copy(row, cells)
	t.rows = append(t.rows, row)
}

// render writes the table, or nothing at all when there are no rows, so an
// empty result does not print a header over a void.
func (t *table) render(w io.Writer) error {
	if len(t.rows) == 0 {
		return nil
	}

	widths := make([]int, len(t.headers))
	for i, h := range t.headers {
		widths[i] = utf8.RuneCountInString(h)
	}
	for _, row := range t.rows {
		for i, cell := range row {
			if n := utf8.RuneCountInString(cell); n > widths[i] {
				widths[i] = n
			}
		}
	}

	var b strings.Builder
	t.writeRow(&b, widths, t.headers)
	rule := make([]string, len(t.headers))
	for i, n := range widths {
		rule[i] = strings.Repeat("-", n)
	}
	t.writeRow(&b, widths, rule)
	for _, row := range t.rows {
		t.writeRow(&b, widths, row)
	}

	if _, err := io.WriteString(w, b.String()); err != nil {
		return fmt.Errorf("write table: %w", err)
	}
	return nil
}

func (t *table) writeRow(b *strings.Builder, widths []int, cells []string) {
	for i, cell := range cells {
		if i > 0 {
			b.WriteString("  ")
		}
		pad := widths[i] - utf8.RuneCountInString(cell)
		if t.aligns[i] == alignRight {
			b.WriteString(strings.Repeat(" ", pad))
			b.WriteString(cell)
			continue
		}
		b.WriteString(cell)
		if i < len(cells)-1 {
			b.WriteString(strings.Repeat(" ", pad))
		}
	}
	b.WriteString("\n")
}

// writeJSON prints v as indented JSON, which is what --json produces.
func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return fmt.Errorf("write json: %w", err)
	}
	return nil
}

// truncate shortens s for display, keeping the result at most n runes
// including the ellipsis.
func truncate(s string, n int) string {
	if n <= 0 || utf8.RuneCountInString(s) <= n {
		return s
	}
	runes := []rune(s)
	if n <= 3 {
		return string(runes[:n])
	}
	return string(runes[:n-3]) + "..."
}
