// SPDX-License-Identifier: GPL-3.0-or-later

package engine_test

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/rangertaha/scour/internal/engine"
)

// NOTES.md is checked against the code rather than trusted.
//
// A design document that has drifted is worse than none: it is confidently
// wrong, and everybody who reads it believes it. So the job document in the
// notes is parsed and validated here, and every number in its catalogue tables
// is compared with the catalogue the code actually uses.
//
// This is the whole reason "clean" can mean something. A human reading a file
// five times finds fewer mistakes each pass because they remember what they
// meant; a test finds the same ones every time.

const notesPath = "../../NOTES.md"

func notes(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(notesPath)
	if err != nil {
		t.Fatalf("read notes: %v", err)
	}
	return string(b)
}

var hclBlock = regexp.MustCompile("(?s)```hcl\n(.*?)```")

// TestNotesJobDocumentIsReal parses the HCL in NOTES.md itself, so the notes
// and the parser cannot disagree.
func TestNotesJobDocumentIsReal(t *testing.T) {
	blocks := hclBlock.FindAllStringSubmatch(notes(t), -1)
	if len(blocks) == 0 {
		t.Fatal("NOTES.md has no hcl block; the example is what everything else is checked against")
	}

	for i, block := range blocks {
		t.Run(fmt.Sprintf("block%d", i), func(t *testing.T) {
			doc, err := engine.Parse([]byte(block[1]), "NOTES.md")
			if err != nil {
				t.Fatalf("the documented job does not parse:\n%v", err)
			}
			if err := doc.Validate(); err != nil {
				t.Fatalf("the documented job does not validate:\n%v", err)
			}
		})
	}
}

// tableRow matches | 100 | `robots` | ... | and | `clean` | ... |.
var tableRow = regexp.MustCompile("^\\|\\s*\\**([0-9]*)\\**\\s*\\|\\s*\\**`([a-z_]+)`\\**\\s*\\|")

// sectionOf returns the lines under a heading, up to the next heading.
func sectionOf(t *testing.T, src, heading string) []string {
	t.Helper()

	lines := strings.Split(src, "\n")
	start := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == heading {
			start = i + 1
			break
		}
	}
	if start < 0 {
		t.Fatalf("NOTES.md has no %q section", heading)
	}

	var out []string
	for _, line := range lines[start:] {
		if strings.HasPrefix(line, "#") {
			break
		}
		out = append(out, line)
	}
	return out
}

// documentedOrders reads a catalogue table into name -> order.
func documentedOrders(t *testing.T, src, heading string) map[string]int {
	t.Helper()

	out := map[string]int{}
	for _, line := range sectionOf(t, src, heading) {
		m := tableRow.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		name, raw := m[2], m[1]
		if raw == "" {
			out[name] = -1 // a table with no order column, such as the kinds
			continue
		}
		order, err := strconv.Atoi(raw)
		if err != nil {
			t.Fatalf("%s: %q is not a number", heading, raw)
		}
		out[name] = order
	}
	if len(out) == 0 {
		t.Fatalf("%s: no table rows found, so this check is not checking anything", heading)
	}
	return out
}

// TestNotesCatalogueMatchesTheCode is the check that keeps the tables honest.
func TestNotesCatalogueMatchesTheCode(t *testing.T) {
	src := notes(t)

	for _, tc := range []struct {
		heading string
		stage   engine.Stage
	}{
		{"### Downloader", engine.StageDownloader},
		{"### Spider", engine.StageSpider},
		{"### Scheduler", engine.StageScheduler},
	} {
		t.Run(string(tc.stage), func(t *testing.T) {
			documented := documentedOrders(t, src, tc.heading)

			built := map[string]int{}
			for _, b := range engine.Placements[tc.stage] {
				built[b.Name] = b.Order
			}

			for name, order := range documented {
				got, ok := built[name]
				if !ok {
					t.Errorf("NOTES.md documents %s/%s, which the code does not ship", tc.stage, name)
					continue
				}
				if got != order {
					t.Errorf("%s/%s: NOTES.md says %d, code says %d", tc.stage, name, order, got)
				}
			}
			for name := range built {
				if _, ok := documented[name]; !ok {
					t.Errorf("the code ships %s/%s, which NOTES.md does not document", tc.stage, name)
				}
			}
		})
	}
}

func TestNotesPipelineKindsMatchTheCode(t *testing.T) {
	documented := sectionOf(t, notes(t), "### Pipeline")
	text := strings.Join(documented, "\n")

	for _, kind := range engine.PipelineKindNames() {
		if !strings.Contains(text, "`"+kind+"`") {
			t.Errorf("the code ships pipeline kind %q, which NOTES.md does not document", kind)
		}
	}
}

// TestNotesVocabularyIsReal catches a type or transform named in the prose that
// the parser would refuse.
func TestNotesVocabularyIsReal(t *testing.T) {
	src := notes(t)

	known := map[string]bool{}
	for _, ty := range engine.TypeNames() {
		known[ty] = true
	}
	for _, tr := range engine.TransformNames() {
		known[tr] = true
	}

	// Only inside the hcl examples, where a word has to be real.
	for _, block := range hclBlock.FindAllStringSubmatch(src, -1) {
		for _, line := range strings.Split(block[1], "\n") {
			field, values, ok := bareWords(line)
			if !ok {
				continue
			}
			for _, word := range values {
				if !known[word] {
					t.Errorf("NOTES.md uses %s = %s, which is not in the vocabulary", field, word)
				}
			}
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

// TestNotesStageListsMatchTheCode keeps the prose about what can be replaced
// and what can be extended true.
func TestNotesStageListsMatchTheCode(t *testing.T) {
	src := notes(t)

	// The pipeline is not a plugin stage, and the notes say so in two places.
	if engine.StagePipeline.ValidPlugin() {
		t.Error("the code allows plugin \"pipeline\", which NOTES.md says is refused")
	}
	if !strings.Contains(src, `Writing `+"`"+`plugin "pipeline" ...`+"`"+` is refused`) {
		t.Error("NOTES.md no longer says that plugin \"pipeline\" is refused")
	}

	// The scheduler cannot be replaced, only extended.
	if engine.StageScheduler.ValidExternal() {
		t.Error("the code allows an external scheduler, which NOTES.md says it does not")
	}
	if !engine.StageScheduler.ValidPlugin() {
		t.Error("the code refuses scheduler plugins, which NOTES.md documents a table of")
	}
}
