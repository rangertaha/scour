// SPDX-License-Identifier: GPL-3.0-or-later

package engine_test

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/rangertaha/scour/internal/engine"
)

// docs/ is checked against the code, for the same reason NOTES.md is.
//
// The book says of itself that a chapter which drifts from the code fails the
// build rather than misleading somebody quietly. This is what makes that true:
// every job document printed in it is parsed and validated, every plugin
// position quoted in it is compared with the catalogue the code uses, and the
// SQL it prints is compared with the query the frontier runs.
//
// # Markdown, and what that changed
//
// The book was hand-written HTML with inline SVG, and is now Markdown with
// mermaid, read on GitHub rather than served as a site. The checks that were
// about being a website went with it: there is no stylesheet to link, no
// doctype, no viewBox to fall outside of, and no sidebar repeated in ten files.
// What replaced them is smaller and covers the same failures: a link that goes
// nowhere, a chapter nothing reaches, a running order that disagrees with
// itself, and a diagram that is empty.
//
// The checks about the *content* did not change at all, which is the argument
// for having written them against the text rather than against the markup.

const bookDir = "../../docs"

func bookPages(t *testing.T) map[string]string {
	t.Helper()

	paths, err := filepath.Glob(filepath.Join(bookDir, "*.md"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(paths) == 0 {
		t.Fatal("docs/ has no chapters")
	}

	pages := map[string]string{}
	for _, path := range paths {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		pages[filepath.Base(path)] = string(body)
	}
	return pages
}

var (
	// A fenced block, with the language it claims to be.
	fenced = regexp.MustCompile("(?s)```([a-z]*)\n(.*?)\n```")

	// A markdown link to another chapter, which is the only kind that can rot.
	chapterLink = regexp.MustCompile(`\]\((([a-z]+)\.md)\)`)

	// A position and the plugin it belongs to, as the book's tables print them.
	placement = regexp.MustCompile(`\|\s*(\d+)\s*\|\s*` + "`" + `([a-z]+)` + "`" + `\s*\|`)

	exporterOf = regexp.MustCompile(`exporter\s+"[a-z]+"\s+"([a-z_]+)"`)
)

// blocks returns every fenced block in a page that claims the given language.
func blocks(page, language string) []string {
	var out []string
	for _, m := range fenced.FindAllStringSubmatch(page, -1) {
		if m[1] == language {
			out = append(out, m[2])
		}
	}
	return out
}

// TestBookHCLIsReal parses every job document the book prints.
//
// A fragment is wrapped in whatever it needs to stand alone, because a chapter
// prints the part it is talking about rather than a whole document every time,
// and a reader who copies one out expects it to work in place.
func TestBookHCLIsReal(t *testing.T) {
	var checked int

	for name, page := range bookPages(t) {
		for i, fragment := range blocks(page, "hcl") {
			src, ok := standalone(fragment)
			if !ok {
				continue
			}
			checked++

			t.Run(fmt.Sprintf("%s/%d", name, i), func(t *testing.T) {
				doc, err := engine.Parse([]byte(src), name)
				if err != nil {
					t.Fatalf("does not parse:\n%v\n\n%s", err, src)
				}
				if err := doc.Validate(); err != nil {
					t.Fatalf("does not validate:\n%v\n\n%s", err, src)
				}
			})
		}
	}

	if checked == 0 {
		t.Fatal("no HCL was found in the book, so this test proves nothing")
	}
}

// standalone turns a printed fragment into a document that can be parsed, and
// reports whether it was a job document at all. A service document and a labels
// document are neither, and TestNotesAndCliDocumentTypesAreReal has those.
func standalone(fragment string) (string, bool) {
	fragment = strings.TrimSpace(fragment)

	switch {
	case strings.HasPrefix(fragment, `job "`):
		return fragment, true

	case startsWith(fragment, "scheduler", "downloader", "spider", "pipeline", "exporter", "monitoring", "mutation"):
		return around(fragment, itemsIn(fragment)), true

	case startsWith(fragment, `property "`, `relation "`):
		return around(`item "article" {
  property "headline" {
    type = str
  }

`+indent(fragment)+`
}`, nil), true
	}
	return "", false
}

func startsWith(s string, prefixes ...string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(s, prefix) {
			return true
		}
	}
	return false
}

// itemsIn names the items a fragment refers to, so a job built around an
// exporter declares the item that exporter writes. An exporter naming an item
// the job does not extract is refused, which is the point of the rule and would
// otherwise make this test unable to check the chapter that explains it.
func itemsIn(fragment string) []string {
	seen := map[string]bool{}
	var names []string
	for _, m := range exporterOf.FindAllStringSubmatch(fragment, -1) {
		if seen[m[1]] {
			continue
		}
		seen[m[1]] = true
		names = append(names, m[1])
	}
	return names
}

func around(fragment string, extra []string) string {
	var b strings.Builder

	b.WriteString("job \"book\" {\n  start = [\"https://example.com/\"]\n\n")
	if !strings.HasPrefix(fragment, "item ") {
		b.WriteString("  item \"article\" {\n    property \"title\" {\n      type = str\n    }\n  }\n\n")
	}
	for _, name := range extra {
		if name == "article" {
			continue
		}
		fmt.Fprintf(&b, "  item %q {\n    property \"value\" {\n      type = float\n    }\n  }\n\n", name)
	}
	b.WriteString(indent(fragment))
	b.WriteString("\n}\n")
	return b.String()
}

func indent(fragment string) string {
	lines := strings.Split(fragment, "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) != "" {
			lines[i] = "  " + line
		}
	}
	return strings.Join(lines, "\n")
}

// TestBookPlacementsMatchTheCatalogue. Every order the book prints is a number
// somebody could change in one place and not the other, which is exactly the
// kind of drift nobody notices by reading.
func TestBookPlacementsMatchTheCatalogue(t *testing.T) {
	stages := []engine.Stage{engine.StageDownloader, engine.StageSpider, engine.StageScheduler}

	var checked int
	for name, page := range bookPages(t) {
		for _, m := range placement.FindAllStringSubmatch(page, -1) {
			order, err := strconv.Atoi(m[1])
			if err != nil {
				t.Fatalf("%s: %q is not a number", name, m[1])
			}
			plugin := m[2]
			checked++

			var found bool
			var elsewhere []string
			for _, stage := range stages {
				got, ok := engine.DefaultOrder(stage, plugin)
				if !ok {
					continue
				}
				if got == order {
					found = true
					break
				}
				elsewhere = append(elsewhere, fmt.Sprintf("%s has it at %d", stage, got))
			}

			if !found {
				if len(elsewhere) == 0 {
					t.Errorf("%s: the book places %q at %d, and the catalogue has no such plugin",
						name, plugin, order)
					continue
				}
				t.Errorf("%s: the book places %q at %d, but %s",
					name, plugin, order, strings.Join(elsewhere, "; "))
			}
		}
	}

	if checked == 0 {
		t.Fatal("no placements were found in the book, so this test proves nothing")
	}
}

// TestBookMentionsNoPluginTheCatalogueDropped is the other direction: robots,
// redirects and decoding were each taken out of the catalogue because there is
// only one correct position for them, and a table that still lists one would be
// telling a reader to write something the engine now refuses.
func TestBookMentionsNoPluginTheCatalogueDropped(t *testing.T) {
	gone := []string{"robots", "redirect", "charset", "compression", "useragent", "timeout"}

	for name, page := range bookPages(t) {
		for _, m := range placement.FindAllStringSubmatch(page, -1) {
			for _, dropped := range gone {
				if m[2] == dropped {
					t.Errorf("%s lists %q as a plugin with a position, and it is an attribute", name, dropped)
				}
			}
		}
	}
}

// TestBookSQLIsTheQuery.
//
// The frontier chapter prints the lease, which is the query everything else in
// that chapter is an argument about: the indexes, the residuals, why there is no
// leased status. A printed query that has quietly stopped being the query is
// worse than none, because the whole chapter is then reasoning about something
// that is not there. It had already lost a column when this was written.
//
// Compared piece by piece rather than whole, because the source builds the
// query by concatenation: the status and the ordering are values spliced in,
// and both halves either side of a splice are in the file verbatim.
func TestBookSQLIsTheQuery(t *testing.T) {
	source, err := os.ReadFile("../frontier/sqlite/sqlite.go")
	if err != nil {
		t.Fatalf("read the frontier: %v", err)
	}
	src := string(source)

	var checked int
	for name, page := range bookPages(t) {
		for _, printed := range blocks(page, "sql") {
			for _, line := range strings.Split(printed, "\n") {
				for _, piece := range strings.FieldsFunc(line, func(r rune) bool { return r == '\'' }) {
					piece = strings.TrimSpace(piece)
					piece = strings.TrimSpace(strings.TrimPrefix(piece, "ORDER BY"))
					// Short pieces are the spliced values themselves and the
					// odd LIMIT 1, which say nothing about drift.
					if len(piece) < 16 {
						continue
					}
					checked++
					if !strings.Contains(src, piece) {
						t.Errorf("%s prints SQL the frontier does not have:\n  %s", name, piece)
					}
				}
			}
		}
	}

	if checked == 0 {
		t.Fatal("no SQL was found in the book, so this check is not checking anything")
	}
}

var (
	// A count written out, as the book writes it: "Eleven kinds of thing get
	// kept", "Eleven stores, each with one owner".
	counted = regexp.MustCompile(`([A-Za-z]+) (?:kinds of thing|stores)`)

	// The rows of the storage map, which is the table it is counting.
	storeRow = regexp.MustCompile(`(?m)^\| [^|]+ \| (?:The cache|NATS KV|SQLite|Files|Whatever)`)
)

var numberWords = map[string]int{
	"one": 1, "two": 2, "three": 3, "four": 4, "five": 5, "six": 6,
	"seven": 7, "eight": 8, "nine": 9, "ten": 10, "eleven": 11,
	"twelve": 12, "thirteen": 13, "fourteen": 14, "fifteen": 15,
}

// TestBookCountsTheStoresItLists.
//
// The storage map is the one table in the book that is also counted in prose,
// in two chapters, and a count in words is exactly the kind of claim that goes
// stale silently: a store is added to the table, the sentence above it still
// says nine, and nothing anywhere disagrees. It had already happened twice when
// this was written.
func TestBookCountsTheStoresItLists(t *testing.T) {
	pages := bookPages(t)

	rows := len(storeRow.FindAllString(pages["storage.md"], -1))
	if rows == 0 {
		t.Fatal("storage.md has no store rows, so this check is not checking anything")
	}

	var found int
	for name, page := range pages {
		for _, m := range counted.FindAllStringSubmatch(page, -1) {
			said, ok := numberWords[strings.ToLower(m[1])]
			if !ok {
				continue
			}
			found++
			if said != rows {
				t.Errorf("%s says %q, and the storage map lists %d", name, m[0], rows)
			}
		}
	}
	if found == 0 {
		t.Fatal("no written-out count of the stores was found, so this check is not checking anything")
	}
}

// TestBookLinksGoSomewhere. Ten files linking to each other by hand, with
// nothing that resolves them, is exactly the kind of thing that rots.
func TestBookLinksGoSomewhere(t *testing.T) {
	pages := bookPages(t)

	var checked int
	for name, page := range pages {
		for _, m := range chapterLink.FindAllStringSubmatch(page, -1) {
			checked++
			if _, ok := pages[m[1]]; !ok {
				t.Errorf("%s links to %s, which is not a chapter", name, m[1])
			}
		}
	}
	if checked == 0 {
		t.Fatal("the chapters link to each other nowhere, so this check is not checking anything")
	}
}

// chapterOrder is the book's running order, read from the cover's contents.
func chapterOrder(t *testing.T, pages map[string]string) []string {
	t.Helper()

	contents, _, ok := strings.Cut(pages["index.md"], "\n---\n")
	if !ok {
		contents = pages["index.md"]
	}

	var order []string
	seen := map[string]bool{}
	for _, m := range chapterLink.FindAllStringSubmatch(contents, -1) {
		if m[1] == "index.md" || seen[m[1]] {
			continue
		}
		seen[m[1]] = true
		order = append(order, m[1])
	}
	if len(order) < 2 {
		t.Fatal("the cover lists fewer than two chapters, so this check is not checking anything")
	}
	return append([]string{"index.md"}, order...)
}

// TestEveryChapterIsReachable, so a chapter cannot be added and left orphaned,
// or removed and left linked.
func TestEveryChapterIsReachable(t *testing.T) {
	pages := bookPages(t)
	linked := map[string]bool{}
	for _, name := range chapterOrder(t, pages) {
		linked[name] = true
	}

	var orphans []string
	for name := range pages {
		if !linked[name] {
			orphans = append(orphans, name)
		}
	}
	sort.Strings(orphans)

	if len(orphans) > 0 {
		t.Errorf("the cover's contents leave out %s", strings.Join(orphans, ", "))
	}
}

var pagerRel = regexp.MustCompile(`\[(Back|Next): [^\]]+\]\(([a-z]+\.md)\)`)

// TestThePagerChainIsWhole.
//
// Back and Next are the way most people move through a book, and they are the
// part that goes wrong when a chapter is inserted: the new page is wired in,
// and the two either side of it still point past it at each other.
func TestThePagerChainIsWhole(t *testing.T) {
	pages := bookPages(t)
	order := chapterOrder(t, pages)

	for i, name := range order {
		links := map[string]string{}
		for _, m := range pagerRel.FindAllStringSubmatch(pages[name], -1) {
			links[m[1]] = m[2]
		}

		want := map[string]string{}
		if i > 0 {
			want["Back"] = order[i-1]
		}
		if i < len(order)-1 {
			want["Next"] = order[i+1]
		}

		for _, dir := range []string{"Back", "Next"} {
			switch {
			case want[dir] == "" && links[dir] != "":
				t.Errorf("%s has a %s to %s, and it is at the end of the book", name, dir, links[dir])
			case want[dir] != "" && links[dir] == "":
				t.Errorf("%s has no %s, and %s is beside it", name, dir, want[dir])
			case links[dir] != want[dir]:
				t.Errorf("%s's %s goes to %s, and %s is beside it", name, dir, links[dir], want[dir])
			}
		}
	}
}

// TestEveryChapterIsWholeAndAccessible covers the handful of things that are
// easy to leave out of one chapter and impossible to see without opening all of
// them.
func TestEveryChapterIsWholeAndAccessible(t *testing.T) {
	for name, page := range bookPages(t) {
		t.Run(name, func(t *testing.T) {
			if !strings.HasPrefix(page, "# ") {
				t.Error("does not open with its title")
			}

			// The house style has no em dashes.
			if strings.Contains(page, "—") {
				t.Error("an em dash got in")
			}

			// A mermaid diagram renders as a picture and nothing else, so
			// somebody who cannot see it gets nothing unless the chapter says
			// what it shows. That description is the alt text this book used to
			// carry in aria-label, and it must not be lost.
			drawings := len(blocks(page, "mermaid"))
			described := strings.Count(page, "<summary>What this diagram shows</summary>")
			if drawings != described {
				t.Errorf("%d diagrams and %d descriptions of one", drawings, described)
			}
		})
	}
}

// TestEveryDiagramIsADiagram.
//
// A mermaid block that does not begin with a diagram type renders as an error
// box on GitHub, in the place where the picture should be. It is the one
// failure that looks worse to a reader than having no diagram at all, and it
// cannot be seen by reading the Markdown.
func TestEveryDiagramIsADiagram(t *testing.T) {
	kinds := []string{"flowchart", "graph", "sequenceDiagram", "erDiagram",
		"classDiagram", "stateDiagram", "journey", "gantt", "pie"}

	var checked int
	for name, page := range bookPages(t) {
		for i, drawing := range blocks(page, "mermaid") {
			checked++
			first, _, _ := strings.Cut(strings.TrimSpace(drawing), "\n")

			var ok bool
			for _, kind := range kinds {
				if strings.HasPrefix(first, kind) {
					ok = true
					break
				}
			}
			if !ok {
				t.Errorf("%s: diagram %d starts with %q, which is not a mermaid diagram", name, i+1, first)
			}
			if len(strings.Split(strings.TrimSpace(drawing), "\n")) < 3 {
				t.Errorf("%s: diagram %d has nothing in it", name, i+1)
			}
		}
	}

	if checked == 0 {
		t.Fatal("no diagrams were found, so this test proves nothing")
	}
}
