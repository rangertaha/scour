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
	"github.com/rangertaha/scour/internal/pipeline"
)

// docs/ is checked against the code rather than trusted.
//
// A design document that has drifted is worse than none: it is confidently
// wrong, and everybody who reads it believes it. The book says of itself that a
// chapter which drifts from the code fails the build rather than misleading
// somebody quietly. This is what makes that true: every job document printed in
// it is parsed and validated, every plugin position quoted in it is compared
// with the catalogue the code uses, and the SQL it prints is compared with the
// query the frontier runs.
//
// This is the whole reason "clean" can mean something. A human reading a file
// five times finds fewer mistakes each pass because they remember what they
// meant; a test finds the same ones every time.
//
// # These checks used to read NOTES.md
//
// The working notes were the document of record and carried the catalogue
// tables, the pipeline kinds and the vocabulary, with a test per claim. NOTES.md
// is gone and the book is what is left, so the checks were repointed here rather
// than deleted with it. The tables they read are the same tables: the downloader
// and the spider are printed side by side in chains.md, and the scheduler's own
// in frontier.md, because ordering the queue is what that chapter is about.
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

	// The cover, and one directory per chapter. A chapter is a directory so
	// that Pages serves it at /frontier/ and GitHub renders it when somebody
	// opens the folder, which is the same reason the cover is index.md.
	paths, err := filepath.Glob(filepath.Join(bookDir, "*/index.md"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	paths = append(paths, filepath.Join(bookDir, "index.md"))

	pages := map[string]string{}
	for _, path := range paths {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		_, prose := frontMatter(string(body))
		pages[chapterOf(path)] = prose
	}
	if len(pages) < 2 {
		t.Fatal("docs/ has no chapters")
	}
	return pages
}

// frontMatter splits a page's YAML header from its prose. Jekyll reads the
// header for the title and description of every page; everything that checks
// what a chapter says wants the prose under it.
func frontMatter(page string) (head, prose string) {
	if !strings.HasPrefix(page, "---\n") {
		return "", page
	}
	rest := page[len("---\n"):]
	end := strings.Index(rest, "\n---\n")
	if end < 0 {
		return "", page
	}
	return rest[:end], strings.TrimLeft(rest[end+len("\n---\n"):], "\n")
}

// chapterOf names a chapter by its directory, which is how the book links to
// one: docs/frontier/index.md is "frontier", and the cover is "index".
func chapterOf(path string) string {
	dir := filepath.Base(filepath.Dir(path))
	if dir == "docs" {
		return "index"
	}
	return dir
}

var (
	// A fenced block, with the language it claims to be.
	fenced = regexp.MustCompile("(?s)```([a-z]*)\n(.*?)\n```")

	// A markdown link to another chapter, which is the only kind that can rot.
	chapterLink = regexp.MustCompile(`\]\((?:\.\./)?([a-z]+)/index\.md\)`)

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
				doc, err := parseExample(t, []byte(src), name)
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
// document are neither, and TestBookAndCliDocumentTypesAreReal has those.
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

// leaseQuery is the raw literal the frontier assigns to `query` in Lease. The
// closing backtick is the end of it, and there is no way to write a backtick
// inside one, so the first is the last.
var leaseQuery = regexp.MustCompile("(?s)\tquery := `(.*?)`")

// TestBookSQLIsTheWholeQuery walks the other way: every line the frontier's
// lease really runs has to appear in the book.
//
// [TestBookSQLIsTheQuery] only asks that what the book prints exists in the
// source, which cannot see a column ADDED to the query. That is not a
// hypothetical direction to have missed. The book printed the lease without
// `COALESCE(h.delay, 0)` for as long as crawl delays had been honoured, and
// every line it did print was still a substring of the real query, so the check
// stayed green while the chapter described a lease that stopped existing.
//
// A missing line is the case that matters: it makes the chapter's argument
// about a query nobody runs. The pieces are matched the same way as above,
// because the source splices the status and the ordering in.
func TestBookSQLIsTheWholeQuery(t *testing.T) {
	source, err := os.ReadFile("../frontier/sqlite/sqlite.go")
	if err != nil {
		t.Fatalf("read the frontier: %v", err)
	}

	m := leaseQuery.FindStringSubmatch(string(source))
	if m == nil {
		t.Fatal("the frontier has no `query :=` literal, so this check cannot find the lease")
	}

	// Every SQL block in the book, as one haystack: the chapter prints the
	// guard and the lease as separate statements in one fence, and a later
	// chapter may quote a line of either.
	var printed strings.Builder
	for _, page := range bookPages(t) {
		for _, block := range blocks(page, "sql") {
			printed.WriteString(block)
			printed.WriteString("\n")
		}
	}
	book := printed.String()

	var checked int
	for _, line := range strings.Split(m[1], "\n") {
		for _, piece := range strings.FieldsFunc(line, func(r rune) bool { return r == '\'' }) {
			piece = strings.TrimSpace(piece)
			piece = strings.TrimSpace(strings.TrimPrefix(piece, "ORDER BY"))
			if len(piece) < 16 {
				continue
			}
			checked++
			if !strings.Contains(book, piece) {
				t.Errorf("the lease runs SQL the book does not print:\n  %s", piece)
			}
		}
	}

	if checked == 0 {
		t.Fatal("nothing was taken from the lease, so this check is not checking anything")
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

	rows := len(storeRow.FindAllString(pages["storage"], -1))
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

	contents, _, ok := strings.Cut(pages["index"], "\n---\n")
	if !ok {
		contents = pages["index"]
	}

	var order []string
	seen := map[string]bool{}
	for _, m := range chapterLink.FindAllStringSubmatch(contents, -1) {
		if m[1] == "index" || seen[m[1]] {
			continue
		}
		seen[m[1]] = true
		order = append(order, m[1])
	}
	if len(order) < 2 {
		t.Fatal("the cover lists fewer than two chapters, so this check is not checking anything")
	}
	return append([]string{"index"}, order...)
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

var pagerRel = regexp.MustCompile(`\[(Back|Next): [^\]]+\]\(([^)]+)\)`)

// chapterAt resolves a link written from one chapter into the chapter it names.
//
// Links carry the index.md rather than stopping at the directory, which reads
// the same on GitHub and lets MkDocs resolve them: written as "../frontier/"
// the site builds with nineteen "unrecognized relative link" notices and
// validates none of them, so a chapter renamed out from under a link would go
// unnoticed by the build. The three shapes are "job/index.md",
// "../items/index.md" and "../index.md".
func chapterAt(link string) string {
	link = strings.TrimPrefix(link, "../")
	link = strings.TrimSuffix(link, "index.md")
	link = strings.TrimSuffix(link, "/")
	if link == "" {
		return "index"
	}
	return link
}

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
			links[m[1]] = chapterAt(m[2])
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

// figure is a drawing as the book references one, with the text that stands in
// for it.
var figure = regexp.MustCompile(`<img src="(?:\.\./)?img/([a-z0-9-]+\.svg)" alt="([^"]*)">`)

// TestEveryFigureHasItsPicture.
//
// The diagrams are files now rather than fenced mermaid, referenced with <img>
// rather than inlined, because GitHub's Markdown sanitiser strips inline <svg>
// and a referenced file is not inline. That buys the pictures at the cost of a
// new way to be wrong: a reference and a file are two things that can disagree,
// and a missing one renders as a broken image rather than as nothing.
//
// Both directions. A reference with no file is a hole in the page, and a file
// nothing references is a drawing nobody sees, which is how a diagram survives
// the chapter it belonged to being rewritten.
func TestEveryFigureHasItsPicture(t *testing.T) {
	drawn := map[string]bool{}
	paths, err := filepath.Glob(filepath.Join(bookDir, "img", "*.svg"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	for _, path := range paths {
		drawn[filepath.Base(path)] = true
	}
	if len(drawn) == 0 {
		t.Fatal("docs/img holds no drawings, so this check is not checking anything")
	}

	shown := map[string]bool{}
	var checked int
	for name, page := range bookPages(t) {
		for _, m := range figure.FindAllStringSubmatch(page, -1) {
			checked++
			shown[m[1]] = true

			if !drawn[m[1]] {
				t.Errorf("%s shows img/%s, which is not there", name, m[1])
			}
			// The picture carries the whole of what a diagram says, so a
			// reader who cannot see it gets nothing without this.
			if strings.TrimSpace(m[2]) == "" {
				t.Errorf("%s shows img/%s with nothing said about it", name, m[1])
			}
		}

		// A path Jekyll resolves and GitHub does not is a picture that renders
		// on the site and is broken in the repository. These were written as
		// {{ '/img/x.svg' | relative_url }}, which is Liquid: Pages processes
		// it, github.com serves it verbatim, and every diagram in the book came
		// out as a broken image for anybody reading it where it lives. A plain
		// relative path resolves in both.
		if strings.Contains(page, `src="{{`) {
			t.Errorf("%s builds an image path with Liquid, which only Pages resolves", name)
		}
	}
	if checked == 0 {
		t.Fatal("no figures were found, so this check is not checking anything")
	}

	for name := range drawn {
		if !shown[name] {
			t.Errorf("img/%s is drawn and no chapter shows it", name)
		}
	}
}

// stageTable is the chapter each stage's catalogue is printed in, and the cell
// its order column starts at. chains.md prints the downloader and the spider
// side by side as one table with two order columns; frontier.md prints the
// scheduler's own, because ordering the queue is what that chapter is about.
var stageTable = map[engine.Stage]struct {
	page string
	at   int
}{
	engine.StageDownloader: {"chains", 0},
	engine.StageSpider:     {"chains", 2},
	engine.StageScheduler:  {"frontier", 0},
}

// tableCells splits a Markdown table row into its trimmed cells, or returns nil
// if the line is not one.
func tableCells(line string) []string {
	line = strings.TrimSpace(line)
	if len(line) < 2 || !strings.HasPrefix(line, "|") || !strings.HasSuffix(line, "|") {
		return nil
	}
	cells := strings.Split(strings.TrimSuffix(strings.TrimPrefix(line, "|"), "|"), "|")
	for i := range cells {
		cells[i] = strings.TrimSpace(cells[i])
	}
	return cells
}

// placedAt reads the plugin named in the cell after `at`, when the cell at `at`
// is an order.
//
// It reports false for anything that is not that pair, which is how the other
// tables in these chapters are skipped without naming them: a separator row, the
// lease benchmark, the exit codes. A table that stops being a catalogue stops
// being read rather than being read wrongly.
func placedAt(cells []string, at int) (string, bool) {
	if at+1 >= len(cells) {
		return "", false
	}
	m := backticked.FindStringSubmatch(cells[at+1])
	if m == nil {
		return "", false
	}
	if _, err := strconv.Atoi(cells[at]); err != nil {
		return "", false
	}
	return m[1], true
}

var backticked = regexp.MustCompile("^`([a-z_]+)`$")

// TestBookCataloguesEveryPluginTheCodeShips is the direction the other two
// catalogue checks cannot walk.
//
// [TestBookPlacementsMatchTheCatalogue] holds every position the book prints to
// the code, and [TestBookMentionsNoPluginTheCatalogueDropped] refuses ones that
// became attributes. Neither can see a plugin the code ships and the book has
// never heard of, because both start from what is written. A reader takes these
// tables for the whole catalogue, so one missing row is a plugin nobody knows
// they can write.
//
// This check came off NOTES.md, which carried a per-stage table and had this
// direction from the start.
func TestBookCataloguesEveryPluginTheCodeShips(t *testing.T) {
	pages := bookPages(t)

	for stage, where := range stageTable {
		t.Run(string(stage), func(t *testing.T) {
			page, ok := pages[where.page]
			if !ok {
				t.Fatalf("the book has no %s, so the %s catalogue cannot be checked", where.page, stage)
			}

			said := map[string]bool{}
			for _, line := range strings.Split(page, "\n") {
				if name, ok := placedAt(tableCells(line), where.at); ok {
					said[name] = true
				}
			}
			if len(said) == 0 {
				t.Fatalf("%s has no %s catalogue table, so this check is not checking anything",
					where.page, stage)
			}

			for _, b := range engine.Placements[stage] {
				if !said[b.Name] {
					t.Errorf("the code ships %s/%s at %d, which %s does not list",
						stage, b.Name, b.Order, where.page)
				}
			}
		})
	}
}

// | `clean` | Rule-driven tidying | Built |
var kindRow = regexp.MustCompile("(?m)^\\|\\s*`([a-z]+)`\\s*\\|[^|]*\\|\\s*(Built|Catalogued)\\s*\\|")

// TestBookSaysWhichKindsAreBuilt holds the pipeline's kinds table to both lists
// that could disagree with it.
//
// A kinds table with no state column reads as a list of working parts, and four
// of the nine are not: `python`, `rhai`, `nodejs` and `bash` are catalogued
// positions, and a job naming one is refused when the pipeline is built.
//
// The catalogue is what a document may name, in principle. The registry is what
// this build can actually run, and they are not the same list: `entities` was
// registered, documented and reachable while the catalogue had never heard of
// it, so a check that read only one of them could not have noticed the other
// going stale.
func TestBookSaysWhichKindsAreBuilt(t *testing.T) {
	page := bookPages(t)["pipeline"]

	built := map[string]bool{}
	for _, kind := range pipeline.Registered() {
		built[kind] = true
	}
	if len(built) == 0 {
		t.Fatal("no pipeline kinds are registered, so this check is not checking anything")
	}

	said := map[string]bool{}
	for _, row := range kindRow.FindAllStringSubmatch(page, -1) {
		kind, state := row[1], row[2]
		said[kind] = true

		switch {
		case state == "Built" && !built[kind]:
			t.Errorf("pipeline.md says the %q step is built, and nothing registers one", kind)
		case state == "Catalogued" && built[kind]:
			t.Errorf("pipeline.md calls the %q step catalogued, and this build runs it", kind)
		}
	}
	if len(said) == 0 {
		t.Fatal("pipeline.md has no kinds table with a state column, so this check is not checking it")
	}

	for kind := range built {
		if !said[kind] {
			t.Errorf("pipeline.md leaves the %q step out of its kinds table", kind)
		}
	}
	catalogued := map[string]bool{}
	for _, kind := range engine.PipelineKindNames() {
		catalogued[kind] = true
		if !said[kind] {
			t.Errorf("the code catalogues pipeline kind %q, which pipeline.md does not document", kind)
		}
	}
	for kind := range built {
		if !catalogued[kind] {
			t.Errorf("this build runs pipeline kind %q, which engine.PipelineKinds does not list", kind)
		}
	}
}

var bareAssign = regexp.MustCompile(`^\s*(type|transforms)\s*=\s*(.+?)\s*(#.*)?$`)

func bareWords(line string) (field string, values []string, ok bool) {
	m := bareAssign.FindStringSubmatch(line)
	if m == nil {
		return "", nil, false
	}
	field = m[1]
	raw := strings.Trim(m[2], "[]")
	if raw == "" {
		return field, nil, true
	}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		part = strings.Trim(part, `"`)
		if part != "" {
			values = append(values, part)
		}
	}
	return field, values, true
}

// TestBookVocabularyIsReal catches a type or transform named in an example that
// the parser would refuse.
//
// [TestBookHCLIsReal] parses what it can, but it wraps a fragment to make it
// stand alone and skips what will not wrap, so a bare word in a fragment nothing
// could parse reaches nobody. These are the words a reader is most likely to
// copy, being the whole vocabulary a property is written in.
func TestBookVocabularyIsReal(t *testing.T) {
	known := map[string]bool{}
	for _, ty := range engine.TypeNames() {
		known[ty] = true
	}
	for _, tr := range engine.TransformNames() {
		known[tr] = true
	}
	if len(known) == 0 {
		t.Fatal("the vocabulary is empty, so this check is not checking anything")
	}

	var checked int
	for name, page := range bookPages(t) {
		for _, block := range blocks(page, "hcl") {
			for _, line := range strings.Split(block, "\n") {
				field, values, ok := bareWords(line)
				if !ok {
					continue
				}
				for _, word := range values {
					checked++
					if !known[word] {
						t.Errorf("%s uses %s = %s, which is not in the vocabulary", name, field, word)
					}
				}
			}
		}
	}

	if checked == 0 {
		t.Fatal("no bare words were found in the book, so this check is not checking anything")
	}
}

// TestBookStageListsMatchTheCode keeps the prose about what may be replaced and
// what may be extended true.
//
// Each of these is a pair: an invariant the code has to hold, and the sentence
// in the book that tells somebody about it. Checking only the first lets the
// book stop saying it; checking only the second lets the code stop doing it.
func TestBookStageListsMatchTheCode(t *testing.T) {
	pages := bookPages(t)

	// The pipeline is not a plugin stage, and cannot even be written as one: the
	// block holds step blocks and nothing else, so it is a parse error rather
	// than a rule. The book has to keep saying which spelling wins.
	if engine.StagePipeline.ValidPlugin() {
		t.Error("the code allows pipeline plugins, which the book says it does not")
	}
	if !strings.Contains(pages["pipeline"], "Not a plugin stage") {
		t.Error("pipeline.md no longer says the pipeline is not a plugin stage")
	}
	if !strings.Contains(pages["pipeline"], "step <kind> <name>") {
		t.Error("pipeline.md no longer documents the step spelling")
	}

	// The scheduler may be extended and not replaced, because politeness is per
	// host and shared. It is the one asymmetry in the whole engine.
	if engine.StageScheduler.ValidExternal() {
		t.Error("the code allows an external scheduler, which the book says it does not")
	}
	if !strings.Contains(pages["index"], "one stage a job may not replace") {
		t.Error("index.md no longer says the scheduler cannot be replaced")
	}
	if !engine.StageScheduler.ValidPlugin() {
		t.Error("the code refuses scheduler plugins, which the book documents a table of")
	}
	if !strings.Contains(pages["frontier"], "Ordering is a plugin") {
		t.Error("frontier.md no longer documents the scheduler's plugins")
	}
}

// chapterCount is the cover's claim about how long the book is.
var chapterCount = regexp.MustCompile(`in ([a-z]+) chapters`)

// TestTheCoverCountsItsOwnChapters.
//
// The cover opens by saying how many chapters there are, in words, and a count
// in words is the kind of claim that goes stale in silence: a chapter is added,
// the sentence above the contents still says nine, and nothing anywhere
// disagrees. It had already happened when this was written, the CLI chapter
// having been folded in from the repository root while the cover went on saying
// nine.
//
// The same shape as TestBookCountsTheStoresItLists, which exists because that
// one had gone stale twice.
func TestTheCoverCountsItsOwnChapters(t *testing.T) {
	pages := bookPages(t)

	m := chapterCount.FindStringSubmatch(pages["index"])
	if m == nil {
		t.Fatal("the cover no longer says how many chapters there are")
	}

	said, ok := numberWords[m[1]]
	if !ok {
		t.Fatalf("the cover says %q chapters, which is not a number this knows", m[1])
	}
	if said != len(pages) {
		t.Errorf("the cover says %s chapters and docs/ holds %d", m[1], len(pages))
	}
}

// bookLink is a link from the repository's README into the book.
var bookLink = regexp.MustCompile(`\]\(docs/([a-z]+)/index\.md\)`)

// TestTheReadmeLinksIntoTheBook.
//
// The README is the first page anybody sees and it is outside docs/, so nothing
// that checks the book checks it. It names every chapter by filename, which is
// the one thing about a chapter guaranteed to change: the chapters were
// renamed once already, to put the folder in reading order, and every link to
// them had to move at the same time.
//
// Both directions. A link that goes nowhere is a broken front page, and a
// chapter the README leaves out is a chapter nobody arrives at.
func TestTheReadmeLinksIntoTheBook(t *testing.T) {
	src, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatalf("read the README: %v", err)
	}

	pages := bookPages(t)
	linked := map[string]bool{}

	for _, m := range bookLink.FindAllStringSubmatch(string(src), -1) {
		linked[m[1]] = true
		if _, ok := pages[m[1]]; !ok {
			t.Errorf("the README links to docs/%s/, which is not a chapter", m[1])
		}
	}
	if len(linked) == 0 {
		t.Fatal("the README links to no chapter, so this check is not checking anything")
	}

	for name := range pages {
		if name == "index" {
			continue // the cover is linked as docs/ rather than by name
		}
		if !linked[name] {
			t.Errorf("docs/%s/ is a chapter the README does not link to", name)
		}
	}
}

// TestEveryChapterHasItsFrontMatter.
//
// Jekyll reads the title and the description of every page from its YAML
// header. Without one the page still builds, and it builds wrong: the browser
// tab says "scour" for all ten chapters and the meta description falls back to
// the site's, so search results and link previews describe the book rather than
// the chapter. Nothing about the rendered page looks broken, which is what
// makes it worth a check rather than a glance.
func TestEveryChapterHasItsFrontMatter(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join(bookDir, "*/index.md"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	paths = append(paths, filepath.Join(bookDir, "index.md"))

	var checked int
	for _, path := range paths {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		head, prose := frontMatter(string(body))
		name := chapterOf(path)
		checked++

		for _, want := range []string{"title:", "description:"} {
			if !strings.Contains(head, want) {
				t.Errorf("%s has no %s in its front matter", name, want)
			}
		}
		// The header is the site's; the prose still has to open with the
		// chapter's own title for anybody reading it on GitHub.
		if !strings.HasPrefix(prose, "# ") {
			t.Errorf("%s does not open with its title", name)
		}
	}

	if checked == 0 {
		t.Fatal("no chapters were read, so this check is not checking anything")
	}
}

// The list files the book's examples read.
//
// `domains = lines("domains.txt")` is a document that only means something
// beside the file it names, so an example using it has to be parsed somewhere
// that file exists. These are that somewhere, and keeping them here rather than
// inventing content per filename is what makes the check strict: a chapter that
// reads a list file this map does not know about fails, with the name it used,
// instead of being quietly skipped.
var bookLists = map[string]string{
	"domains.txt":  "example.com\n",
	"seeds.txt":    "https://example.com/\n",
	"included.txt": "/topic/\n",
	"excluded.txt": "/admin\n",
}

// readsLines finds the list files an example names.
var readsLines = regexp.MustCompile(`lines\("([^"]+)"\)`)

// parseExample parses one of the book's job documents.
//
// A document that reads no files is parsed as a stored job is, with no
// directory, which is the stricter of the two and what most examples are. One
// that does read files is given a directory holding them, because that is what
// it means: the example is the authoring form, and the entries are expanded
// into the document before it is submitted.
func parseExample(t *testing.T, src []byte, name string) (*engine.Document, error) {
	t.Helper()

	named := readsLines.FindAllStringSubmatch(string(src), -1)
	if len(named) == 0 {
		return engine.Parse(src, name)
	}

	dir := t.TempDir()
	for _, m := range named {
		body, known := bookLists[m[1]]
		if !known {
			t.Fatalf("%s reads a list from %q, and the book's fixtures have no such file. "+
				"Add it to bookLists, or use one of the names already there", name, m[1])
		}
		if err := os.WriteFile(filepath.Join(dir, m[1]), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return engine.ParseIn(src, name, dir)
}
