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
)

// docs/ is checked against the code, for the same reason NOTES.md is.
//
// The book says of itself that a chapter which drifts from the code fails the
// build rather than misleading somebody quietly. This is what makes that true:
// every job document printed in it is parsed and validated, and every plugin
// position quoted in it is compared with the catalogue the code uses.
//
// The pages are hand-written HTML with no build step, so the checks that
// matter most are the ones a person cannot do by eye: whether a number still
// matches, and whether a link still goes anywhere.

const bookDir = "../../docs"

func bookPages(t *testing.T) map[string]string {
	t.Helper()

	paths, err := filepath.Glob(filepath.Join(bookDir, "*.html"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(paths) == 0 {
		t.Fatal("docs/ has no pages")
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
	codeBlock = regexp.MustCompile("(?s)<pre><code>(.*?)</code></pre>")
	hrefs     = regexp.MustCompile(`href="([^"]+)"`)

	// A position and the plugin it belongs to, as the book's tables print them.
	placement = regexp.MustCompile(`<td class="num">(\d+)</td><td><code>([a-z]+)</code></td>`)

	exporterOf = regexp.MustCompile(`exporter\s+"[a-z]+"\s+"([a-z_]+)"`)
)

// TestBookHCLIsReal parses every job document the book prints.
//
// A fragment is wrapped in whatever it needs to stand alone, because a chapter
// prints the part it is talking about rather than a whole document every time,
// and a reader who copies one out expects it to work in place.
func TestBookHCLIsReal(t *testing.T) {
	var checked int

	for name, page := range bookPages(t) {
		for i, block := range codeBlock.FindAllStringSubmatch(page, -1) {
			fragment := html.UnescapeString(block[1])

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
// reports whether it was HCL at all. The book also prints Go, SQL, robots.txt
// and NATS subjects, none of which this has any business trying to parse.
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

// TestBookLinksGoSomewhere. A book with no build step has its navigation
// repeated on every page, which is exactly the kind of thing that rots.
func TestBookLinksGoSomewhere(t *testing.T) {
	pages := bookPages(t)

	for name, page := range pages {
		for _, m := range hrefs.FindAllStringSubmatch(page, -1) {
			target := m[1]
			switch {
			case strings.HasPrefix(target, "http"), strings.HasPrefix(target, "#"):
				continue
			case strings.HasSuffix(target, ".css"):
				if _, err := os.Stat(filepath.Join(bookDir, target)); err != nil {
					t.Errorf("%s links to %s, which is not there", name, target)
				}
			default:
				if _, ok := pages[target]; !ok {
					t.Errorf("%s links to %s, which is not a page", name, target)
				}
			}
		}
	}
}

// TestEveryChapterIsReachable, so a page cannot be added and left orphaned, or
// removed and left linked.
func TestEveryChapterIsReachable(t *testing.T) {
	pages := bookPages(t)

	cover, ok := pages["index.html"]
	if !ok {
		t.Fatal("the book has no index.html")
	}

	linked := map[string]bool{}
	for _, m := range hrefs.FindAllStringSubmatch(cover, -1) {
		if strings.HasSuffix(m[1], ".html") {
			linked[m[1]] = true
		}
	}

	var orphans []string
	for name := range pages {
		if name != "index.html" && !linked[name] {
			orphans = append(orphans, name)
		}
	}
	sort.Strings(orphans)

	if len(orphans) > 0 {
		t.Errorf("the cover links to no way of reaching %s", strings.Join(orphans, ", "))
	}
}

// TestEveryPageIsWholeAndAccessible covers the handful of things that are easy
// to leave out of one page and impossible to see without opening all of them.
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
		})
	}
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

			box := viewBox.FindStringSubmatch(svg)
			if box == nil {
				t.Errorf("%s: diagram %d has no viewBox", name, i+1)
				continue
			}
			width, height := atof(t, box[3]), atof(t, box[4])
			checked++

			for _, point := range points(t, svg) {
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

var (
	viewBox = regexp.MustCompile(`viewBox="(-?[\d.]+) (-?[\d.]+) ([\d.]+) ([\d.]+)"`)
	element = regexp.MustCompile(`(?s)<(rect|line|text|path)\s([^>]*?)/?>`)
	attr    = regexp.MustCompile(`([a-zA-Z0-9-]+)="([^"]*)"`)
	number  = regexp.MustCompile(`-?[\d.]+`)
)

type point struct {
	what string
	x, y float64
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
