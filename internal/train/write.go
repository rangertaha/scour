// SPDX-License-Identifier: GPL-3.0-or-later

package train

import (
	"fmt"
	"os"
	"strings"
)

// Write puts proposals back into a job document.
//
// # Why the text and not the parsed document
//
// Because the document is a person's file. It has their comments in it, their
// spacing, the order they chose to write things in, and a round trip through a
// parser and a printer would return something equivalent and unrecognisable. A
// diff nobody can read is a diff nobody reviews, and reviewing what induction
// proposed is the entire point.
//
// So this edits lines. It is less clever than rewriting the tree and it is the
// only approach that leaves the rest of the file alone.
func Write(document []byte, proposals []Proposal) ([]byte, int, error) {
	lines := strings.Split(string(document), "\n")

	var written int
	for _, proposal := range proposals {
		if proposal.Kept || proposal.Selector == "" {
			continue
		}

		at, indent, existing := find(lines, proposal)
		if at < 0 {
			continue
		}

		replacement := []string{
			fmt.Sprintf("%scss = [%q] %s", indent, proposal.Selector, Marker),
		}
		if existing >= 0 {
			lines = append(lines[:existing], append(replacement, lines[existing+1:]...)...)
		} else {
			lines = append(lines[:at], append(replacement, lines[at:]...)...)
		}
		written++
	}

	if written == 0 {
		return document, 0, nil
	}
	return []byte(strings.Join(lines, "\n")), written, nil
}

// find locates where a property's locator goes: the line to replace if there is
// an induced one, and otherwise the line to insert before.
//
// Line-oriented and deliberately conservative. It looks for the property block
// inside the item block, and gives up rather than guessing if the document is
// shaped in a way it does not recognise: a tool that edits a file it has
// misread is worse than one that says it could not.
func find(lines []string, proposal Proposal) (insertAt int, indent string, replace int) {
	item := `item "` + proposal.Item + `"`
	property := `property "` + proposal.Property + `"`

	inItem, depth := false, 0
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)

		if !inItem {
			if strings.HasPrefix(trimmed, item) {
				inItem = true
			}
			continue
		}

		if strings.HasPrefix(trimmed, property) {
			// A property written on one line has no body to insert into, and
			// the scan below would latch onto the closing brace of whatever
			// block came next: the locator landed outside the property, the
			// document stopped decoding, and every later command on it failed
			// with "Unsupported argument". Giving up is what this function's
			// documentation already promised, and it was not doing it.
			if closes(trimmed) {
				return -1, "", -1
			}

			// The property's body: its closing brace, the line to insert
			// before, and any induced locator already in it.
			indent = leading(line) + "  "
			replace = -1

			for j := i + 1; j < len(lines); j++ {
				body := strings.TrimSpace(lines[j])
				switch {
				case strings.HasPrefix(body, "css ") || strings.HasPrefix(body, "css="):
					if strings.Contains(lines[j], Marker) {
						replace = j
					} else {
						// A locator somebody wrote, which is never touched.
						return -1, "", -1
					}
				case body == "}":
					return j, indent, replace
				case strings.HasPrefix(body, "property "):
					// A nested property, which this does not descend into.
					return -1, "", -1
				}
			}
			return -1, "", -1
		}

		// Track the item block so a property of the same name in another item
		// is not edited by mistake.
		depth += strings.Count(line, "{") - strings.Count(line, "}")
		if inItem && depth < 0 {
			inItem = false
			depth = 0
		}
	}
	return -1, "", -1
}

// closes reports whether a block opens and closes on one line.
//
// Counted rather than matched on a suffix, because `property "a" {}` and
// `property "a" { type = str }` are both one-line blocks and only the first
// ends in a brace.
func closes(line string) bool {
	var depth int
	for _, r := range line {
		switch r {
		case '{':
			depth++
		case '}':
			depth--
		}
	}
	return depth <= 0 && strings.Contains(line, "{")
}

func leading(line string) string {
	return line[:len(line)-len(strings.TrimLeft(line, " \t"))]
}

// WriteFile edits a document in place.
//
// A copy is written and renamed, so a failure halfway leaves the original
// rather than half a file: this is somebody's job document and it may be the
// only copy.
func WriteFile(path string, proposals []Proposal) (int, error) {
	document, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("train: %w", err)
	}

	edited, written, err := Write(document, proposals)
	if err != nil || written == 0 {
		return written, err
	}

	info, err := os.Stat(path)
	if err != nil {
		return 0, fmt.Errorf("train: %w", err)
	}

	temporary := path + ".scour-train"
	if err := os.WriteFile(temporary, edited, info.Mode().Perm()); err != nil {
		return 0, fmt.Errorf("train: %w", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		os.Remove(temporary)
		return 0, fmt.Errorf("train: %w", err)
	}
	return written, nil
}

// MarkInduced reads a document and says which properties hold a locator this
// wrote, which is what tells [Learn] what it may replace.
func MarkInduced(document []byte) map[string]bool {
	induced := map[string]bool{}

	var item, property string
	for _, line := range strings.Split(string(document), "\n") {
		trimmed := strings.TrimSpace(line)

		switch {
		case strings.HasPrefix(trimmed, `item "`):
			item, property = quoted(trimmed), ""
		case strings.HasPrefix(trimmed, `property "`):
			property = quoted(trimmed)
		case strings.Contains(line, Marker) && item != "" && property != "":
			induced[item+"."+property] = true
		}
	}
	return induced
}

func quoted(line string) string {
	first := strings.Index(line, `"`)
	if first < 0 {
		return ""
	}
	rest := line[first+1:]
	last := strings.Index(rest, `"`)
	if last < 0 {
		return ""
	}
	return rest[:last]
}
