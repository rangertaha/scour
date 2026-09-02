// SPDX-License-Identifier: GPL-3.0-or-later

package train

import (
	"fmt"
	"os"
	"strings"

	"github.com/rangertaha/scour/internal/safefile"

	"github.com/rangertaha/scour/internal/engine"
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
			// HCL quoting, not Go's. A selector induced from a page carrying
			// an unrendered template attribute contains `${`, which HCL reads
			// as an interpolation: written with %q it went into somebody's job
			// document and the document stopped parsing.
			fmt.Sprintf("%scss = [%s] %s", indent, engine.HCLString(proposal.Selector), Marker),
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

// stepping tracks a block a line scan is stepping over whole.
//
// An item holds property blocks and relation blocks; a relation holds property
// blocks of its own, and a property may hold nested ones, all spelled
// identically. A scan that does not step over them acts on the wrong field:
// an induced locator went onto a relation's `role` instead of the item's, and
// then into `author`'s `name` instead of the item's, whenever the containing
// block was written first. Nothing failed either time - the item's property
// still had none, so extraction went on missing it on every page, while some
// other field quietly gained a selector induced for a different one.
//
// Stepped over rather than descended into because induction does not propose
// locators for anything but an item's own properties: [Learn] walks
// item.Properties and no deeper, so a name found inside another block is a
// collision and never the thing being looked for.
type stepping struct{ depth int }

// over reports whether this line is inside a block being stepped over, and
// keeps count. Ask it before looking at the line for anything else.
func (s *stepping) over(line string) bool {
	if s.depth <= 0 {
		return false
	}
	s.depth += strings.Count(line, "{") - strings.Count(line, "}")
	return true
}

// start steps over the block this line opens.
func (s *stepping) start(line string) {
	s.depth = strings.Count(line, "{") - strings.Count(line, "}")
}

// find locates where a property's locator goes: the line to replace if there is
// an induced one, and otherwise the line to insert before.// find locates where a property's locator goes: the line to replace if there is
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

	// inner is any block below the item's own level: a relation, or one of the
	// item's other properties.
	var inner stepping
	for i := from; i < to; i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)

		if !inItem {
			if strings.HasPrefix(trimmed, item) {
				inItem = true
			}
			continue
		}

		// A relation or another property is stepped over whole, never looked
		// inside. See [stepping].
		if inner.over(line) {
			continue
		}
		if strings.HasPrefix(trimmed, "relation ") {
			inner.start(line)
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
					if strings.Contains(lines[j], Mark) {
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

		// One of the item's other properties, stepped over whole so that a
		// nested block inside it is never mistaken for the item's own. See
		// [stepping].
		if strings.HasPrefix(trimmed, `property "`) {
			inner.start(line)
			continue
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

	// A relation, and a property below the item's own level, are stepped over
	// whole. See [stepping]: both hold property blocks spelled identically to
	// the item's, so a marker inside `relation "author"`'s `role`, or inside
	// `author`'s `name`, reported the ITEM's property as induced - and a
	// locator a person had written by hand was then overwritten rather than
	// learned from, on the strength of a marker belonging to a different
	// field.
	var item, property string
	var inner stepping

	for _, line := range lines[from:to] {
		trimmed := strings.TrimSpace(line)

		if inner.over(line) {
			continue
		}
		if strings.HasPrefix(trimmed, "relation ") {
			inner.start(line)
			continue
		}

		switch {
		case strings.HasPrefix(trimmed, `item "`):
			item, property = quoted(trimmed), ""
		case strings.HasPrefix(trimmed, `property "`):
			if property != "" {
				// Nested: the item's own property is still open. Only an
				// item's own properties are ever induced for, so a marker
				// under this one belongs to nothing this reports.
				inner.start(line)
				continue
			}
			property = quoted(trimmed)
		case strings.Contains(line, Mark) && item != "" && property != "":
			induced[item+"."+property] = true
		case trimmed == "}":
			// The end of the item's own property, so the next one is at the
			// item's level again.
			property = ""
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
