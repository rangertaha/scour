// SPDX-License-Identifier: GPL-3.0-or-later

package engine_test

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"

	"github.com/rangertaha/scour/internal/downloader"
	"github.com/rangertaha/scour/internal/downloader/httpcache"
	"github.com/rangertaha/scour/internal/engine"
	"github.com/rangertaha/scour/internal/exporter"
	"github.com/rangertaha/scour/internal/exporter/files"
	natsexport "github.com/rangertaha/scour/internal/exporter/nats"
	"github.com/rangertaha/scour/internal/exporter/parquet"
	sqliteexport "github.com/rangertaha/scour/internal/exporter/sqlite"
	_ "github.com/rangertaha/scour/internal/plugins"
	"github.com/rangertaha/scour/internal/scheduler"
	"github.com/rangertaha/scour/internal/scheduler/dupefilter"
	schedtopic "github.com/rangertaha/scour/internal/scheduler/topic"
	"github.com/rangertaha/scour/internal/spider"
	"github.com/rangertaha/scour/internal/spider/httperror"
	spidertopic "github.com/rangertaha/scour/internal/spider/topic"
)

// The documented blocks nothing else looks inside.
//
// A plugin's and an exporter's fields are undecoded by design. The engine keeps
// the block body opaque and hands it to the implementation, which reads it
// against its own schema, and that is what lets somebody else write one without
// this package knowing its fields.
//
// It is also a hole in every check there was. Parsing a documented job does not
// touch those bodies and neither does validating it, so `plugin "topic" { use =
// "climate@7" }` and `exporter "nats" "article" { stream = "ITEMS" }` both sat
// in NOTES.md passing every test while no binary would have accepted either:
// the fields are `subject`, `least` and `subject`, `url`. Somebody following
// the notes got "Unsupported argument" on the first run.
//
// So this decodes them, against the same schema the implementation decodes them
// with. The table below is the seam, and TestNotesEveryBuiltBlockHasASchema is
// what stops it going stale: a new plugin or exporter with no entry fails.

// schemas is what a documented block body is decoded against: a value of the
// type the implementation itself reads its block into.
//
// Keyed by stage, because "topic" is two different plugins with two different
// schemas, and a job writes each in the block that says which one it is.
var schemas = map[engine.Stage]map[string]any{
	engine.StageDownloader: {
		httpcache.Name: httpcache.Config{},
	},
	engine.StageScheduler: {
		dupefilter.Name: dupefilter.Config{},
		schedtopic.Name: schedtopic.Config{},
	},
	engine.StageSpider: {
		httperror.Name:   httperror.Config{},
		spidertopic.Name: spidertopic.Config{},
	},
	// Exporters are not a stage, but they are the same shape of problem: a
	// format's block is opaque until the format decodes it.
	stageExporter: {
		"json":      files.Config{},
		"jsonlines": files.Config{},
		"csv":       files.Config{},
		"parquet":   parquet.Config{},
		"nats":      natsexport.Config{},
		"sqlite":    sqliteexport.Config{},
	},
}

// stageExporter keys the exporters. Not an [engine.Stage] in the code, because
// an exporter is not a stage: it is a key here so that one table can hold both
// kinds of opaque block.
const stageExporter = engine.Stage("exporter")

// documents are the files whose examples somebody types. The book's chapters
// are read the same way, from bookPages, because a chapter's examples are the
// ones most likely to be copied out.
var documents = []string{"../../NOTES.md", "../../CLI.md"}

// TestNotesEveryBuiltBlockHasASchema keeps the table above honest. A plugin or
// an exporter this build ships and this file does not know about is a block
// that would go back to being checked by nothing.
func TestNotesEveryBuiltBlockHasASchema(t *testing.T) {
	for _, tc := range []struct {
		stage engine.Stage
		built []string
	}{
		{engine.StageDownloader, downloader.Registered()},
		{engine.StageScheduler, scheduler.Registered()},
		{engine.StageSpider, spider.Registered()},
		{stageExporter, exporter.Registered()},
	} {
		if len(tc.built) == 0 {
			t.Fatalf("%s: nothing is registered, so this check is not checking anything", tc.stage)
		}
		for _, name := range tc.built {
			if _, ok := schemas[tc.stage][name]; !ok {
				t.Errorf("%s/%s is built and has no schema in bodies_test.go, so the docs may say anything about it", tc.stage, name)
			}
		}
	}
}

// A package or a file the documents point at.
var namedPath = regexp.MustCompile(`internal/[a-z0-9_]+(?:/[a-z0-9_]+)*(?:\.go)?`)

// TestNotesNamesNothingThatMoved.
//
// The documents send a reader to the code by name, and a rename is the one kind
// of drift that happens without anybody editing prose at all. Cheap to check,
// and the alternative is a reader concluding the thing described does not
// exist.
func TestNotesNamesNothingThatMoved(t *testing.T) {
	checked := 0

	for _, path := range append(documents, "../../PLAN.md") {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}

		seen := map[string]bool{}
		for _, named := range namedPath.FindAllString(string(src), -1) {
			if seen[named] {
				continue
			}
			seen[named] = true
			checked++

			if _, err := os.Stat(filepath.Join("../..", named)); err != nil {
				t.Errorf("%s names %s, which is not there", filepath.Base(path), named)
			}
		}
	}

	if checked == 0 {
		t.Fatal("the documents name no packages, so this check is not checking anything")
	}
}

// example is one ```hcl fence, parsed, and knowing where in its file it began.
type example struct {
	path string
	line int
	src  []byte
	body *hclsyntax.Body
}

// examples parses every hcl fence in the documents.
func examples(t *testing.T) []example {
	t.Helper()

	var out []example
	for _, path := range documents {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}

		text := string(src)
		for _, at := range hclBlock.FindAllStringSubmatchIndex(text, -1) {
			// Positioned at the line the example starts on in the file, so a
			// failure names somewhere somebody can go and look.
			line := 1 + strings.Count(text[:at[2]], "\n")
			fence := text[at[2]:at[3]]

			file, diags := hclsyntax.ParseConfig([]byte(fence), path, hcl.Pos{Line: line, Column: 1, Byte: at[2]})
			if diags.HasErrors() {
				t.Errorf("%s:%d: the example does not parse:\n%s", path, line, diags.Error())
				continue
			}

			body, ok := file.Body.(*hclsyntax.Body)
			if !ok {
				t.Fatalf("%s:%d: the example is not native syntax", path, line)
			}
			out = append(out, example{path: path, line: line, src: []byte(fence), body: body})
		}
	}

	if len(out) == 0 {
		t.Fatal("the documents have no hcl examples, so these checks are not checking anything")
	}
	return out
}

// blockOfInterest matches a printed fragment that declares one of the blocks
// nothing else decodes.
var blockOfInterest = regexp.MustCompile(`(?m)^\s*(plugin|exporter)\s+"`)

// bookExamples are the book's code blocks, which are the ones a reader is most
// likely to copy.
//
// Only the fragments the book fenced as hcl and that declare a plugin or an
// exporter. One that names such a block and does not parse is a real failure,
// which is why it is reported rather than skipped: the fence already claimed
// it was HCL.
func bookExamples(t *testing.T) []example {
	t.Helper()

	var out []example
	for name, page := range bookPages(t) {
		for i, fragment := range blocks(page, "hcl") {
			if !blockOfInterest.MatchString(fragment) {
				continue
			}

			file, diags := hclsyntax.ParseConfig([]byte(fragment), name, hcl.InitialPos)
			if diags.HasErrors() {
				t.Errorf("%s: block %d declares a plugin or an exporter and does not parse:\n%s", name, i, diags.Error())
				continue
			}
			out = append(out, example{path: name, line: i, body: file.Body.(*hclsyntax.Body)})
		}
	}

	if len(out) == 0 {
		t.Fatal("the book prints no plugin or exporter blocks, so this check is not checking anything")
	}
	return out
}

// TestNotesAndCliBlockBodiesDecode reads every hcl example in the documents and
// holds each plugin and exporter block to the schema of whatever decodes it.
func TestNotesAndCliBlockBodiesDecode(t *testing.T) {
	checked := 0
	for _, ex := range append(examples(t), bookExamples(t)...) {
		checked += checkBlocks(t, ex.path, ex.body, "")
	}

	// The whole point is that these bodies are opaque to everything else. A
	// walk that found none of them would report nothing and look like a pass.
	if checked == 0 {
		t.Fatal("no plugin or exporter blocks were found in the documents, so this check is not checking anything")
	}
}

// TestNotesAndCliDocumentTypesAreReal parses each example as whatever kind of
// document it is.
//
// There are three, and only the job document was ever checked. A service
// document and a labels document are read by their own parsers, which decode
// strictly, so an example naming a field that has been renamed is a file the
// binary refuses and every test passes.
func TestNotesAndCliDocumentTypesAreReal(t *testing.T) {
	seen := map[string]int{}

	for _, ex := range examples(t) {
		kind := documentKind(ex.body)
		seen[kind]++

		var err error
		switch kind {
		case "job":
			var doc *engine.Document
			if doc, err = engine.Parse(ex.src, ex.path); err == nil {
				err = doc.Validate()
			}
		case "service":
			var doc *engine.Service
			if doc, err = engine.ParseService(ex.src, ex.path); err == nil {
				err = doc.Validate()
			}
		case "topics":
			var doc *engine.Topics
			if doc, err = engine.ParseTopics(ex.src, ex.path); err == nil {
				err = doc.Validate()
			}
		default:
			// A fragment: a single property or plugin block, shown to explain
			// one thing. It belongs to no document type on its own, and the
			// body check above is what covers those.
			continue
		}
		if err != nil {
			t.Errorf("%s:%d: the documented %s document is not one:\n%v", ex.path, ex.line, kind, err)
		}
	}

	for _, kind := range []string{"job", "service", "topics"} {
		if seen[kind] == 0 {
			t.Errorf("no %s document is documented, so nothing here is checking one", kind)
		}
	}
}

// documentKind says which parser an example belongs to, from its top-level
// blocks. The three document types have no block type in common except topic,
// and there a label is what tells them apart: a labels document names the
// subject it is teaching, a service document says where the store lives.
func documentKind(body *hclsyntax.Body) string {
	kind := ""
	for _, block := range body.Blocks {
		var this string
		switch {
		case block.Type == "job":
			this = "job"
		case block.Type == "entity" || block.Type == "event":
			this = "service"
		case block.Type == "topic" && len(block.Labels) == 0:
			this = "service"
		case block.Type == "topic":
			this = "topics"
		default:
			return ""
		}
		if kind != "" && kind != this {
			return ""
		}
		kind = this
	}
	return kind
}

// checkBlocks walks a body and checks the plugin and exporter blocks in it,
// returning how many it checked. stage is the enclosing stage block, which is
// what says which "topic" a plugin means.
func checkBlocks(t *testing.T, path string, body *hclsyntax.Body, stage engine.Stage) int {
	t.Helper()

	checked := 0
	for _, block := range body.Blocks {
		inner := stage
		switch block.Type {
		case "plugin":
			if len(block.Labels) == 1 {
				checked += checkBody(t, path, block, stage, block.Labels[0])
			}
		case "exporter":
			if len(block.Labels) == 2 {
				checked += checkBody(t, path, block, stageExporter, block.Labels[0])
			}
		case string(engine.StageDownloader), string(engine.StageSpider), string(engine.StageScheduler):
			inner = engine.Stage(block.Type)
		}
		checked += checkBlocks(t, path, block.Body, inner)
	}
	return checked
}

// checkBody holds one block's attributes to the schema for it.
//
// A block whose stage cannot be told from the document, because the example is
// a fragment rather than a whole job, is checked against every schema that
// answers to the name and passes if any of them accepts it. That is weaker on
// purpose: refusing a fragment for a field its own stage does have would make
// the check wrong more often than the docs are.
func checkBody(t *testing.T, path string, block *hclsyntax.Block, stage engine.Stage, name string) int {
	t.Helper()

	var candidates []any
	if stage == "" {
		for _, byName := range schemas {
			if schema, ok := byName[name]; ok {
				candidates = append(candidates, schema)
			}
		}
	} else if schema, ok := schemas[stage][name]; ok {
		candidates = append(candidates, schema)
	}
	if len(candidates) == 0 {
		// A catalogued position with nothing behind it yet. The catalogue
		// tables say which those are, and TestNotesCatalogueMatchesTheCode is
		// what holds them to the code.
		return 0
	}

	refused := 0
	var problems []string
	for _, schema := range candidates {
		err := accepts(schema, block)
		if err == nil {
			continue
		}
		refused++
		// The two `topic` plugins take nearly the same fields, so a fragment
		// checked against both would otherwise report one complaint twice.
		if !slices.Contains(problems, err.Error()) {
			problems = append(problems, err.Error())
		}
	}
	if refused == len(candidates) {
		where := block.TypeRange.String()
		t.Errorf("%s: %s %q at %s: %s", path, block.Type, name, where, strings.Join(problems, "; "))
	}
	return 1
}

// accepts reports whether a schema would take the block as written.
//
// The fields are read off the hcl tags rather than by calling gohcl, because
// building a plugin opens what it configures: an s3 cache block would go to the
// network for a check about spelling.
func accepts(schema any, block *hclsyntax.Block) error {
	known, required := fields(schema)

	var unknown []string
	for name := range block.Body.Attributes {
		// Read centrally, by the engine, for every plugin.
		if block.Type == "plugin" && (name == "order" || name == "enabled") {
			continue
		}
		if !known[name] {
			unknown = append(unknown, name)
		}
	}
	sort.Strings(unknown)

	var missing []string
	for _, name := range required {
		if _, ok := block.Body.Attributes[name]; !ok {
			missing = append(missing, name)
		}
	}

	switch {
	case len(unknown) > 0 && len(missing) > 0:
		return &fieldError{"has no field " + strings.Join(unknown, ", ") + ", and needs " + strings.Join(missing, ", ")}
	case len(unknown) > 0:
		return &fieldError{"has no field " + strings.Join(unknown, ", ") + " (it has " + strings.Join(names(known), ", ") + ")"}
	case len(missing) > 0:
		return &fieldError{"needs " + strings.Join(missing, ", ")}
	}
	return nil
}

type fieldError struct{ msg string }

func (e *fieldError) Error() string { return e.msg }

// fields reads a schema's hcl tags: everything it takes, and the ones it must
// be given.
func fields(schema any) (known map[string]bool, required []string) {
	known = map[string]bool{}

	ty := reflect.TypeOf(schema)
	for i := range ty.NumField() {
		tag, ok := ty.Field(i).Tag.Lookup("hcl")
		if !ok {
			continue
		}
		parts := strings.Split(tag, ",")
		name := parts[0]
		if name == "" {
			continue
		}
		known[name] = true
		if len(parts) == 1 || parts[1] == "" {
			required = append(required, name)
		}
	}
	sort.Strings(required)
	return known, required
}

func names(known map[string]bool) []string {
	out := make([]string, 0, len(known))
	for name := range known {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
