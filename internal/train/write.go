// SPDX-License-Identifier: GPL-3.0-or-later

package train

import (
	"fmt"
	"os"
	"strings"

	"github.com/rangertaha/scour/internal/safefile"
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
// # Why the job name is a parameter
//
// Because two jobs in one document may each declare an item of the same name,
// crawling different sites, and the search for where a locator goes was
// line-oriented with no notion of a job at all: it took the first `item "x"` in
// the file. Training the second job wrote its induced selector into the first,
// so one job started extracting with a selector induced from a site it had
// never fetched and the other still had no locator. Both silently.
func Write(document []byte, job string, proposals []Proposal) ([]byte, int, error) {
	lines := strings.Split(string(document), "\n")

	from, to := bounds(lines, job)
	if from < 0 {
		return document, 0, fmt.Errorf("train: no job %q in the document", job)
	}

	var written int
	for _, proposal := range proposals {
		if proposal.Kept || proposal.Selector == "" {
			continue
		}

		at, indent, existing := find(lines, from, to, proposal)
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
			// An inserted line moves the job's closing brace down with it, and
			// the next proposal is searched for in the same range.
			to++
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
func find(lines []string, from, to int, proposal Proposal) (insertAt int, indent string, replace int) {
	item := `item "` + proposal.Item + `"`
	property := `property "` + proposal.Property + `"`

	inItem, depth := false, 0

	// relation is the brace depth inside a relation block, zero when outside one.
	relation := 0
	for i := from; i < to; i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)

		if !inItem {
			if strings.HasPrefix(trimmed, item) {
				inItem = true
			}
			continue
		}

		// A relation is skipped whole, never looked inside.
		//
		// An item holds property blocks and relation blocks, and a relation
		// holds property blocks of its own, spelled identically. So a scan
		// looking for the item's `role` found `relation "author"`'s `role`
		// whenever the relation was written first, and the induced locator went
		// onto the edge. Nothing failed: the item's property still had none, so
		// extraction went on missing it on every page while the edge quietly
		// gained a selector induced for a different field.
		//
		// Skipped rather than descended into because induction does not propose
		// locators for relation properties at all: [Learn] walks item.Properties
		// and nothing else, so anything found in there is a name collision and
		// never the thing being looked for.
		if relation > 0 {
			relation += strings.Count(line, "{") - strings.Count(line, "}")
			continue
		}
		if strings.HasPrefix(trimmed, "relation ") {
			relation = strings.Count(line, "{") - strings.Count(line, "}")
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

			for j := i + 1; j < to; j++ {
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

// bounds is the half-open line range of a job block, or -1 if the document has
// no such job.
//
// Line-oriented and conservative for the same reason the rest of this file is:
// the alternative is a parser and a printer, and that returns somebody a file
// they do not recognise. A job whose block cannot be delimited is one this
// gives up on rather than guesses at.
func bounds(lines []string, job string) (int, int) {
	want := `job "` + job + `"`

	for i, line := range lines {
		if !strings.HasPrefix(strings.TrimSpace(line), want) {
			continue
		}
		depth := strings.Count(line, "{") - strings.Count(line, "}")
		if depth <= 0 {
			// Opened and closed on one line, so there is nothing inside it to
			// edit.
			return i, i + 1
		}
		for j := i + 1; j < len(lines); j++ {
			depth += strings.Count(lines[j], "{") - strings.Count(lines[j], "}")
			if depth <= 0 {
				return i, j + 1
			}
		}
		// Unbalanced: the document would not parse either, so say so by
		// finding nothing rather than editing to the end of the file.
		return -1, -1
	}
	return -1, -1
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
func WriteFile(path, job string, proposals []Proposal) (int, error) {
	document, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("train: %w", err)
	}

	edited, written, err := Write(document, job, proposals)
	if err != nil || written == 0 {
		return written, err
	}

	info, err := os.Stat(path)
	if err != nil {
		return 0, fmt.Errorf("train: %w", err)
	}

	// A failure halfway must leave the original: this is somebody's job
	// document, and half of one is worse than none. [internal/safefile] is
	// shared with the two other places that rewrite a file, because the third
	// of them wrote in place and truncated a live file that had readers.
	if err := safefile.Replace(path, edited, info.Mode().Perm()); err != nil {
		return 0, fmt.Errorf("train: %w", err)
	}
	return written, nil
}

// MarkInduced reads a document and says which properties of one job hold a
// locator this wrote, which is what tells [Learn] what it may replace.
//
// Scoped to a job for the reason [Write] is: a marker in another job's item of
// the same name used to report this job's property as induced, so a locator a
// person had written by hand was proposed for replacement.
func MarkInduced(document []byte, job string) map[string]bool {
	induced := map[string]bool{}

	lines := strings.Split(string(document), "\n")
	from, to := bounds(lines, job)
	if from < 0 {
		return induced
	}

	// Relations are skipped whole, for the reason [find] skips them: they hold
	// property blocks of their own, spelled identically to the item's. A marker
	// inside `relation "author"`'s `role` reported the ITEM's `role` as
	// induced, so a locator a person had written by hand was offered for
	// replacement on the strength of a marker belonging to a different field.
	var item, property string
	relation := 0
	for _, line := range lines[from:to] {
		trimmed := strings.TrimSpace(line)

		if relation > 0 {
			relation += strings.Count(line, "{") - strings.Count(line, "}")
			continue
		}
		if strings.HasPrefix(trimmed, "relation ") {
			relation = strings.Count(line, "{") - strings.Count(line, "}")
			continue
		}

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
