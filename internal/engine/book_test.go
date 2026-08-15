// SPDX-License-Identifier: GPL-3.0-or-later

package engine_test

import (
	"fmt"
	"html"
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
// and the spider are printed side by side in chains.html, and the scheduler's own
// in frontier.html, because ordering the queue is what that chapter is about.
//
// # HTML, and what that changed
//
// The book was Markdown with mermaid for a while, and is hand-written HTML with
// inline SVG again, served as a site with no build step. The checks that only a
// website needs came back with it: a stylesheet that is linked, a doctype, a
// viewBox nothing falls outside of, and a sidebar that is the same in every
// chapter. GitHub's Markdown sanitiser strips inline <svg>, and Pages with
// .nojekyll serves a .md file as raw text, so the two forms cannot both be rich
// and there is no third option without a build step.
//
// The checks about the *content* did not change at all, which is the argument
// for having written them against the text rather than against the markup: a
// table is a table whether its cells are pipes or <td>.

const bookDir = "../../docs"

func bookPages(t *testing.T) map[string]string {
	t.Helper()

	paths, err := filepath.Glob(filepath.Join(bookDir, "*.html"))
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
	// A printed block, and the language it claims to be. An untagged block is
	// a console transcript or a listing, which no parser should be handed.
	codeBlock = regexp.MustCompile(`(?s)<pre><code(?: class="([a-z]+)")?>(.*?)</code></pre>`)

	// A link to another chapter, which is the only kind that can rot.
	chapterLink = regexp.MustCompile(`href="(([a-z]+)\.html)"`)

	// A position and the plugin it belongs to, as the book's tables print them.
	placement = regexp.MustCompile(`<td class="num">(\d+)</td><td><code>([a-z]+)</code></td>`)

	exporterOf = regexp.MustCompile(`exporter\s+"[a-z]+"\s+"([a-z_]+)"`)
)

// blocks returns every printed block in a page that claims the given language,
// as a reader would copy it: unescaped, because &lt; in the markup is < on the
// page and a parser handed the markup would refuse it.
//
// The class is the language tag. Markdown had one on every fence and HTML has
// none, so it is written on the blocks that are meant to parse; the rest are
// console transcripts and command listings, which are not anybody's syntax.
func blocks(page, language string) []string {
	var out []string
	for _, m := range codeBlock.FindAllStringSubmatch(page, -1) {
		if m[1] == language {
			out = append(out, html.UnescapeString(m[2]))
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
	storeRow = regexp.MustCompile(`<tr><td>[^<]+</td><td>(?:The cache|NATS KV|SQLite|Files|Whatever)`)
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

	rows := len(storeRow.FindAllString(pages["storage.html"], -1))
	if rows == 0 {
		t.Fatal("storage.html has no store rows, so this check is not checking anything")
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

	contents, _, ok := strings.Cut(pages["index.html"], "\n---\n")
	if !ok {
		contents = pages["index.html"]
	}

	var order []string
	seen := map[string]bool{}
	for _, m := range chapterLink.FindAllStringSubmatch(contents, -1) {
		if m[1] == "index.html" || seen[m[1]] {
			continue
		}
		seen[m[1]] = true
		order = append(order, m[1])
	}
	if len(order) < 2 {
		t.Fatal("the cover lists fewer than two chapters, so this check is not checking anything")
	}
	return append([]string{"index.html"}, order...)
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

// The pager, as the book prints it:
// <a class="prev" href="cache.html"><span class="dir">Back</span>...
var pagerRel = regexp.MustCompile(`<a class="(?:prev|next)" href="([a-z]+\.html)"><span class="dir">(Back|Next)</span>`)

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
			links[m[2]] = m[1]
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

// stageTable is the chapter each stage's catalogue is printed in, and the cell
// its order column starts at. chains.html prints the downloader and the spider
// side by side as one table with two order columns; frontier.html prints the
// scheduler's own, because ordering the queue is what that chapter is about.
var stageTable = map[engine.Stage]struct {
	page string
	at   int
}{
	engine.StageDownloader: {"chains.html", 0},
	engine.StageSpider:     {"chains.html", 1},
	engine.StageScheduler:  {"frontier.html", 0},
}

// tableRow is one row of any table in the book.
var tableRow = regexp.MustCompile(`(?s)<tr>(.*?)</tr>`)

// placedAt reads the nth order-and-plugin pair out of a row.
//
// chains.html prints two catalogues side by side, so a row there carries a
// downloader pair at 0 and a spider pair at 1. It reports false for anything
// that is not such a pair, which is how the other tables in these chapters are
// skipped without naming them: a header, the lease benchmark, the exit codes. A
// table that stops being a catalogue stops being read rather than being read
// wrongly.
func placedAt(row string, at int) (string, bool) {
	pairs := placement.FindAllStringSubmatch(row, -1)
	if at >= len(pairs) {
		return "", false
	}
	return pairs[at][2], true
}

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
			for _, row := range tableRow.FindAllStringSubmatch(page, -1) {
				if name, ok := placedAt(row[1], where.at); ok {
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

// <tr><td><code>clean</code></td><td>Rule-driven tidying</td><td>Built</td></tr>
var kindRow = regexp.MustCompile(`<tr><td><code>([a-z]+)</code></td><td>.*?</td><td>(Built|Catalogued)</td></tr>`)

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
	page := bookPages(t)["pipeline.html"]

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
			t.Errorf("pipeline.html says the %q step is built, and nothing registers one", kind)
		case state == "Catalogued" && built[kind]:
			t.Errorf("pipeline.html calls the %q step catalogued, and this build runs it", kind)
		}
	}
	if len(said) == 0 {
		t.Fatal("pipeline.html has no kinds table with a state column, so this check is not checking it")
	}

	for kind := range built {
		if !said[kind] {
			t.Errorf("pipeline.html leaves the %q step out of its kinds table", kind)
		}
	}
	catalogued := map[string]bool{}
	for _, kind := range engine.PipelineKindNames() {
		catalogued[kind] = true
		if !said[kind] {
			t.Errorf("the code catalogues pipeline kind %q, which pipeline.html does not document", kind)
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
	if !strings.Contains(pages["pipeline.html"], "Not a plugin stage") {
		t.Error("pipeline.html no longer says the pipeline is not a plugin stage")
	}
	if !strings.Contains(pages["pipeline.html"], "step &lt;kind&gt; &lt;name&gt;") {
		t.Error("pipeline.html no longer documents the step spelling")
	}

	// The scheduler may be extended and not replaced, because politeness is per
	// host and shared. It is the one asymmetry in the whole engine.
	if engine.StageScheduler.ValidExternal() {
		t.Error("the code allows an external scheduler, which the book says it does not")
	}
	if !strings.Contains(pages["index.html"], "one stage a job may not replace") {
		t.Error("index.html no longer says the scheduler cannot be replaced")
	}
	if !engine.StageScheduler.ValidPlugin() {
		t.Error("the code refuses scheduler plugins, which the book documents a table of")
	}
	if !strings.Contains(pages["frontier.html"], "Ordering is a plugin") {
		t.Error("frontier.html no longer documents the scheduler's plugins")
	}
}

// TestEveryPageIsWholeAndAccessible.
//
// These checks came back with the HTML. A Markdown book had no stylesheet to
// link and no doctype to forget, and GitHub rendered whatever it was given; a
// site with no build step is ten hand-written files that have to agree about
// their own scaffolding, and the way they stop agreeing is that somebody copies
// a chapter to start a new one and edits the middle.
func TestEveryPageIsWholeAndAccessible(t *testing.T) {
	for name, page := range bookPages(t) {
		t.Run(name, func(t *testing.T) {
			for _, want := range []string{
				`<!doctype html>`,
				`<html lang="en">`,
				`<meta name="viewport"`,
				`<link rel="stylesheet" href="book.css">`,
				`<title>`,
				`<meta name="description"`,
			} {
				if !strings.Contains(page, want) {
					t.Errorf("no %s", want)
				}
			}

			// Every diagram has to say what it shows for a reader who cannot
			// see it, and a book that is mostly diagrams cannot skip this.
			for i, svg := range strings.Split(page, "<svg")[1:] {
				head, _, _ := strings.Cut(svg, ">")
				if !strings.Contains(head, `role="img"`) || !strings.Contains(head, "aria-label=") {
					t.Errorf("diagram %d has no role and label", i+1)
				}
			}

			// The house style has no em dashes.
			if strings.Contains(page, "—") {
				t.Error("an em dash got in")
			}

			// One sidebar, the same in every chapter, because a chapter that
			// lists nine of the ten is a chapter you cannot get out of.
			//
			// Read from the <nav> alone. The cover repeats every chapter in its
			// contents list, so a check over the whole page cannot tell a
			// sidebar that is complete from a sidebar that is missing an entry
			// the contents happen to carry: dropping the CLI chapter from
			// index.html's nav left this passing.
			sidebar, _, _ := strings.Cut(page, "</nav>")
			for _, chapter := range []string{
				"index.html", "job.html", "chains.html", "downloader.html",
				"cache.html", "frontier.html", "items.html", "pipeline.html",
				"cli.html", "storage.html",
			} {
				if !strings.Contains(sidebar, `<li><a href="`+chapter+`"`) {
					t.Errorf("the sidebar leaves out %s", chapter)
				}
			}
		})
	}
}

var (
	// The root element's own box. Read from the opening tag alone: a <marker>
	// inside the defs has a viewBox too, and a regex over the whole diagram
	// finds that one when the root has none, which reads as a tiny box that
	// everything is outside of, or as an empty diagram that is fine.
	viewBox = regexp.MustCompile(`viewBox="(-?[\d.]+) (-?[\d.]+) ([\d.]+) ([\d.]+)"`)
	element = regexp.MustCompile(`(?s)<(rect|line|text|path)\s([^>]*?)/?>`)
	attr    = regexp.MustCompile(`([a-zA-Z0-9-]+)="([^"]*)"`)
	number  = regexp.MustCompile(`-?[\d.]+`)
)

type point struct {
	what string
	x, y float64
}

// TestEveryDiagramFitsItsViewBox.
//
// The diagrams are hand-authored, which means every coordinate in them was
// worked out by arithmetic somebody did in their head. A shape placed outside
// the viewBox is not a wrong drawing, it is an invisible one, and it looks
// exactly like a drawing that was never added.
func TestEveryDiagramFitsItsViewBox(t *testing.T) {
	var checked int

	for name, page := range bookPages(t) {
		for i, svg := range strings.Split(page, "<svg")[1:] {
			svg, _, _ = strings.Cut(svg, "</svg>")

			head, body, _ := strings.Cut(svg, ">")
			box := viewBox.FindStringSubmatch(head)
			if box == nil {
				t.Errorf("%s: diagram %d has no viewBox on its root element", name, i+1)
				continue
			}
			width, height := atof(t, box[3]), atof(t, box[4])
			checked++

			for _, point := range points(t, body) {
				if point.x < 0 || point.x > width || point.y < 0 || point.y > height {
					t.Errorf("%s: diagram %d has %s at (%g, %g), outside its %g by %g box",
						name, i+1, point.what, point.x, point.y, width, height)
				}
			}
		}
	}

	if checked == 0 {
		t.Fatal("no diagrams were found, so this test proves nothing")
	}
}

// points lists every coordinate a diagram places something at. Text is measured
// at its anchor rather than its extent, because how wide a word renders is the
// font's business and not something a test can know.
func points(t *testing.T, svg string) []point {
	t.Helper()

	var out []point
	for _, el := range element.FindAllStringSubmatch(svg, -1) {
		tag, rest := el[1], el[2]

		attrs := map[string]string{}
		for _, a := range attr.FindAllStringSubmatch(rest, -1) {
			attrs[a[1]] = a[2]
		}

		switch tag {
		case "rect":
			x, y := atof(t, attrs["x"]), atof(t, attrs["y"])
			out = append(out,
				point{"a rect corner", x, y},
				point{"a rect corner", x + atof(t, attrs["width"]), y + atof(t, attrs["height"])})

		case "line":
			out = append(out,
				point{"a line end", atof(t, attrs["x1"]), atof(t, attrs["y1"])},
				point{"a line end", atof(t, attrs["x2"]), atof(t, attrs["y2"])})

		case "text":
			out = append(out, point{"a label", atof(t, attrs["x"]), atof(t, attrs["y"])})

		case "path":
			// Absolute M and L only, which is all these diagrams use, so the
			// numbers in d are x and y in turn.
			d := number.FindAllString(attrs["d"], -1)
			for i := 0; i+1 < len(d); i += 2 {
				out = append(out, point{"a path point", atof(t, d[i]), atof(t, d[i+1])})
			}
		}
	}
	return out
}

func atof(t *testing.T, s string) float64 {
	t.Helper()

	if s == "" {
		return 0
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		t.Fatalf("%q is not a number", s)
	}
	return v
}
