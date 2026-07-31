// SPDX-License-Identifier: GPL-3.0-or-later

package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// Align says which way a Table column is padded.
type Align int

// Column alignments.
const (
	AlignLeft Align = iota
	AlignRight
)

// Table renders the aligned, dash-underlined tables the CLI prints, matching
// the layout documented in the README.
type Table struct {
	headers []string
	aligns  []Align
	rows    [][]string
}

// NewTable starts a Table. One alignment per header is required.
func NewTable(headers []string, aligns ...Align) *Table {
	if len(aligns) != len(headers) {
		panic("Table: one alignment per header")
	}
	return &Table{headers: headers, aligns: aligns}
}

// add appends a row. Short rows are padded with empty cells.
func (t *Table) Add(cells ...string) {
	row := make([]string, len(t.headers))
	copy(row, cells)
	t.rows = append(t.rows, row)
}

// render writes the Table, or nothing at all when there are no rows, so an
// empty result does not print a header over a void.
func (t *Table) Render(w io.Writer) error {
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
		return fmt.Errorf("write Table: %w", err)
	}
	return nil
}

func (t *Table) writeRow(b *strings.Builder, widths []int, cells []string) {
	for i, cell := range cells {
		if i > 0 {
			b.WriteString("  ")
		}
		pad := widths[i] - utf8.RuneCountInString(cell)
		if t.aligns[i] == AlignRight {
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

// WriteJSON prints v as indented JSON, which is what --json produces.
func WriteJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return fmt.Errorf("write json: %w", err)
	}
	return nil
}

// Truncate shortens s for display, keeping the result at most n runes
// including the ellipsis.
func Truncate(s string, n int) string {
	if n <= 0 || utf8.RuneCountInString(s) <= n {
		return s
	}
	runes := []rune(s)
	if n <= 3 {
		return string(runes[:n])
	}
	return string(runes[:n-3]) + "..."
}

func FormatBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1fGB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1fKB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%dB", n)
	}
}
