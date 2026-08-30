// SPDX-License-Identifier: GPL-3.0-or-later

package engine_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/rangertaha/scour/internal/engine"
)

// The reporting surface: what a client is shown when something changes or goes
// wrong. Rendering is not incidental here, because these strings are the whole
// of what a resubmission tells somebody.

func TestEffectNames(t *testing.T) {
	for effect, want := range map[engine.Effect]string{
		engine.EffectImmediate:  "immediate",
		engine.EffectReseed:     "reseed",
		engine.EffectRescope:    "rescope",
		engine.EffectReextract:  "reextract",
		engine.EffectCacheMoved: "cache moved",
	} {
		if got := effect.String(); got != want {
			t.Errorf("effect %d = %q, want %q", effect, got, want)
		}
	}
	if !engine.EffectImmediate.Free() {
		t.Error("immediate is not free")
	}
	if engine.EffectRescope.Free() {
		t.Error("rescope reports itself free")
	}
}

func TestChangeReadsAsASentence(t *testing.T) {
	for name, tc := range map[string]struct {
		change engine.Change
		want   string
	}{
		"added":   {engine.Change{Path: "domains", To: "b.example"}, "domains: added b.example"},
		"removed": {engine.Change{Path: "domains", From: "b.example"}, "domains: removed b.example"},
		"changed": {engine.Change{Path: "scheduler.max_pages", From: "100", To: "500"}, "scheduler.max_pages: 100 -> 500"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := tc.change.String(); got != tc.want {
				t.Errorf("got  %q\nwant %q", got, tc.want)
			}
		})
	}
}

func TestActionReadsAsASentence(t *testing.T) {
	a := engine.Action{
		Effect: engine.EffectRescope,
		Do:     engine.ScopeDrop,
		Why:    engine.Change{Path: "domains", From: "b.example"},
	}
	got := a.String()
	for _, want := range []string{"rescope", "drop", "domains"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q does not mention %q", got, want)
		}
	}
}

func TestDefaultNamesAreSorted(t *testing.T) {
	names := engine.DefaultNames()
	if len(names) == 0 {
		t.Fatal("no defaults are named")
	}
	for i := 1; i < len(names); i++ {
		if names[i-1] > names[i] {
			t.Fatalf("not sorted at %d: %q then %q", i, names[i-1], names[i])
		}
	}
	if len(names) != len(engine.Defaults()) {
		t.Errorf("%d names for %d defaults", len(names), len(engine.Defaults()))
	}
}

// Durations are strings in the document, so every one of them can be written
// wrongly and every one has to say which field it was.

func TestBadDurationsNameTheirField(t *testing.T) {
	for field, extra := range map[string]string{
		"scheduler.rate":              "\n  scheduler {\n    rate = \"whenever\"\n  }\n",
		"scheduler.max_time":          "\n  scheduler {\n    max_time = \"a while\"\n  }\n",
		"downloader.timeout":          "\n  downloader {\n    timeout = \"soon\"\n  }\n",
		"downloader.external_timeout": "\n  downloader {\n    external_timeout = \"eventually\"\n  }\n",
		"spider.external_timeout":     "\n  spider {\n    external_timeout = \"eventually\"\n  }\n",
	} {
		t.Run(field, func(t *testing.T) {
			refuses(t, extra, field)
		})
	}
}

func TestNegativeNumbersAreRefused(t *testing.T) {
	for name, extra := range map[string]string{
		"max_depth":  "\n  scheduler {\n    max_depth = -1\n  }\n",
		"max_pages":  "\n  scheduler {\n    max_pages = -1\n  }\n",
		"rate":       "\n  scheduler {\n    rate = \"-2s\"\n  }\n",
		"max_time":   "\n  scheduler {\n    max_time = \"-1h\"\n  }\n",
		"concurrent": "\n  scheduler {\n    concurrency = -1\n  }\n",
		"max_body":   "\n  downloader {\n    max_body = -1\n  }\n",
		"timeout":    "\n  downloader {\n    timeout = \"-30s\"\n  }\n",
		"external":   "\n  spider {\n    external_timeout = \"-10m\"\n  }\n",
		"order":      "\n  downloader {\n    plugin \"cache\" {\n      order = -1\n    }\n  }\n",
	} {
		t.Run(name, func(t *testing.T) { refuses(t, extra, "negative") })
	}
}

func TestPipelineExternalWait(t *testing.T) {
	j := mustValidate(t, `
  pipeline {
    external         = true
    external_timeout = "3m"
  }
`)
	if !j.IsExternal(engine.StagePipeline) {
		t.Error("pipeline was not marked external")
	}
	d, err := j.Pipeline.ExternalWait()
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if d.String() != "3m0s" {
		t.Errorf("wait = %s", d)
	}

	// The scheduler has no external attribute, so asking is always false.
	if j.IsExternal(engine.StageScheduler) {
		t.Error("the scheduler reported itself external")
	}
}

func TestExternalWaitDefaults(t *testing.T) {
	var d *engine.Downloader
	var s *engine.Spider
	var p *engine.Pipeline

	for name, get := range map[string]func() (interface{ String() string }, error){
		"downloader": func() (interface{ String() string }, error) { return d.ExternalWait() },
		"spider":     func() (interface{ String() string }, error) { return s.ExternalWait() },
		"pipeline":   func() (interface{ String() string }, error) { return p.ExternalWait() },
	} {
		got, err := get()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got.String() != engine.DefaultExternalTimeout.String() {
			t.Errorf("%s wait = %s, want the default", name, got)
		}
	}
}

// Duplicates, which are always a mistake and always worth naming.

func TestDuplicatesAreRefused(t *testing.T) {
	for name, tc := range map[string]struct{ extra, want string }{
		"plugin in one stage": {`
  downloader {
    plugin "cache" {}

    plugin "cache" {
      bucket = "other"
    }
  }
`, "twice"},
		"step": {`
  pipeline {
    step "clean" "article" {}
    step "clean" "article" {}
  }
`, "twice"},
	} {
		t.Run(name, func(t *testing.T) { refuses(t, tc.extra, tc.want) })
	}
}

func TestDuplicateItemIsRefused(t *testing.T) {
	doc := parse(t, `
job "j" {
  start = ["https://example.com/"]
  item "article" {
    property "a" {
      type = str
    }
  }
  item "article" {
    property "b" {
      type = str
    }
  }
}
`)
	if err := doc.Validate(); err == nil {
		t.Fatal("accepted the same item twice")
	}
}

func TestDuplicatePropertyIsRefused(t *testing.T) {
	doc := parse(t, `
job "j" {
  start = ["https://example.com/"]
  item "article" {
    property "title" {
      type = str
    }
    property "title" {
      type = str
    }
  }
}
`)
	if err := doc.Validate(); err == nil {
		t.Fatal("accepted the same property twice")
	}
}

func TestUnnamedJobIsRefused(t *testing.T) {
	doc := parse(t, `
job "" {
  start = ["https://example.com/"]
  item "a" {
    property "p" {
      type = str
    }
  }
}
`)
	if err := doc.Validate(); err == nil {
		t.Fatal("accepted a job with no name")
	}
}

func TestJobWithNoItemsIsRefused(t *testing.T) {
	doc := parse(t, "job \"j\" {\n  start = [\"https://example.com/\"]\n}\n")
	if err := doc.Validate(); err == nil {
		t.Fatal("accepted a job with nothing to extract")
	}
}

func TestItemWithNoPropertiesIsRefused(t *testing.T) {
	doc := parse(t, "job \"j\" {\n  start = [\"https://example.com/\"]\n  item \"a\" {}\n}\n")
	if err := doc.Validate(); err == nil {
		t.Fatal("accepted an item with nothing to look for")
	}
}

func TestBadItemTypeIsRefused(t *testing.T) {
	doc := parse(t, `
job "j" {
  start = ["https://example.com/"]
  item "a" {
    type = "carrier-pigeon"
    property "p" {
      type = str
    }
  }
}
`)
	if err := doc.Validate(); err == nil {
		t.Fatal("accepted an item type that is not one")
	}
}

func TestBadPropertyTypeAndTransformAreRefused(t *testing.T) {
	doc := parse(t, `
job "j" {
  start = ["https://example.com/"]
  item "a" {
    property "p" {
      type       = "carrier-pigeon"
      transforms = ["fold"]
    }
  }
}
`)
	err := doc.Validate()
	if err == nil {
		t.Fatal("accepted a type and a transform that are not")
	}
	if !strings.Contains(err.Error(), "type") || !strings.Contains(err.Error(), "transform") {
		t.Errorf("error does not name both: %v", err)
	}
}

// Requires has to be a list of references, and every wrong shape has to say so.

func TestRequiresMustBeReferences(t *testing.T) {
	for name, expr := range map[string]string{
		"string":   `["clean.article"]`,
		"not list": `clean.article`,
		"too deep": `[a.b.c]`,
		"bare":     `[clean]`,
	} {
		t.Run(name, func(t *testing.T) {
			src := "job \"j\" {\n  start = [\"https://example.com/\"]\n  item \"a\" {\n    property \"p\" {\n      type = str\n    }\n  }\n" +
				"  pipeline {\n    step \"one\" \"x\" {\n      requires = " + expr + "\n    }\n  }\n}\n"
			if _, err := engine.Parse([]byte(src), "job.hcl"); err == nil {
				t.Fatalf("accepted requires = %s", expr)
			}
		})
	}
}

func TestStepsCanHaveNoRequires(t *testing.T) {
	j := mustValidate(t, `
  pipeline {
    step "clean" "article" {}
  }
`)
	steps := j.Steps()
	if len(steps) != 1 {
		t.Fatalf("got %d steps", len(steps))
	}
	if got := steps[0].Requirements(); len(got) != 0 {
		t.Errorf("requirements = %v, want none", got)
	}
	if steps[0].Address() != "clean.article" {
		t.Errorf("address = %q", steps[0].Address())
	}

	waves, err := j.Waves()
	if err != nil {
		t.Fatalf("waves: %v", err)
	}
	if len(waves) != 1 || len(waves[0]) != 1 {
		t.Errorf("waves = %v", waves)
	}
}

func TestNoPipelineIsNotAnError(t *testing.T) {
	j := mustValidate(t, "")

	waves, err := j.Waves()
	if err != nil {
		t.Fatalf("waves: %v", err)
	}
	if len(waves) != 0 {
		t.Errorf("waves = %v for a job with no pipeline", waves)
	}
	ordered, err := j.Order()
	if err != nil {
		t.Fatalf("order: %v", err)
	}
	if len(ordered) != 0 {
		t.Errorf("order = %v", ordered)
	}
	if w, err := j.Width(); err != nil || w != 0 {
		t.Errorf("width = %d, %v", w, err)
	}
}

// TestOrderAndWavesRefuseADanglingReference covers the second line of defence:
// validation should already have caught this, and these must not resolve it
// anyway.
func TestOrderAndWavesRefuseADanglingReference(t *testing.T) {
	j := job(t, `
  pipeline {
    step "one" "x" {
      requires = [missing.step]
    }
  }
`)
	if _, err := j.Order(); err == nil {
		t.Error("Order resolved a dangling reference")
	}
	if _, err := j.Waves(); err == nil {
		t.Error("Waves resolved a dangling reference")
	}
	if _, err := j.Width(); err == nil {
		t.Error("Width resolved a dangling reference")
	}
}

func TestExporterAddress(t *testing.T) {
	j := parse(t, document).Jobs[0]
	if got := j.Exporters[0].Address(); got != "json.article" {
		t.Errorf("address = %q", got)
	}
}

func TestMonitoringAccessorsOnAnAbsentBlock(t *testing.T) {
	var m *engine.Monitoring
	if m.MetricsOn() {
		t.Error("metrics defaulted on")
	}
	if !m.LoggingOn() {
		t.Error("logging defaulted off")
	}
	if m.LogLevel() != engine.DefaultLogLevel {
		t.Errorf("level = %q", m.LogLevel())
	}
}

// TestLoggingCanBeTurnedOff is the awkward case the accessor exists for: false
// and unset are the same bool, and unset has to mean on.
func TestLoggingCanBeTurnedOff(t *testing.T) {
	on := mustValidate(t, "\n  monitoring {\n    level = \"debug\"\n  }\n")
	if !on.Monitoring.LoggingOn() {
		t.Error("a block that set only a level turned logging off")
	}

	off := mustValidate(t, "\n  monitoring {\n    metrics = true\n    logging = false\n  }\n")
	if off.Monitoring.LoggingOn() {
		t.Error("an explicit logging = false was ignored")
	}
	if !off.Monitoring.MetricsOn() {
		t.Error("metrics = true was ignored")
	}
}

func TestSpecNamesAndValidation(t *testing.T) {
	spec := parse(t, document).Jobs[0].Spec()
	if got := strings.Join(spec.Names(), ","); got != "article" {
		t.Errorf("names = %q", got)
	}

	empty := &engine.Spec{Job: "j"}
	if err := empty.Validate(); err == nil {
		t.Error("a spec with no items validated")
	}
}

func TestParseSpecRejectsRubbish(t *testing.T) {
	if _, err := engine.ParseSpec([]byte("item {"), "spec.hcl"); err == nil {
		t.Error("parsed an unterminated block")
	}
	if _, err := engine.ParseSpec([]byte("wizardry {}\n"), "spec.hcl"); err == nil {
		t.Error("parsed a block that is not in the schema")
	}
}

func TestSpecWithADuplicateItemIsRefused(t *testing.T) {
	spec, err := engine.ParseSpec([]byte(`
item "article" {
  property "a" {
    type = "str"
  }
}

item "article" {
  property "b" {
    type = "str"
  }
}
`), "spec.hcl")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := spec.Validate(); err == nil {
		t.Fatal("accepted the same item twice")
	}
}

func TestPlacementCatalogue(t *testing.T) {
	if _, ok := engine.DefaultOrder(engine.StageDownloader, "cache"); !ok {
		t.Error("cache has no catalogued position")
	}
	if _, ok := engine.DefaultOrder(engine.StageDownloader, "somebody-elses"); ok {
		t.Error("an uncatalogued name has a position")
	}
	if len(engine.PlacementNames(engine.StageDownloader)) == 0 {
		t.Error("the downloader catalogue is empty")
	}
	if len(engine.PipelineKindNames()) == 0 {
		t.Error("no pipeline kinds are catalogued")
	}

	// robots and friends are attributes now, not plugins.
	for _, gone := range []string{"robots", "useragent", "timeout", "maxsize"} {
		if _, ok := engine.DefaultOrder(engine.StageDownloader, gone); ok {
			t.Errorf("%q is still catalogued as a downloader plugin", gone)
		}
	}
}

func TestStageValidity(t *testing.T) {
	if !engine.StageScheduler.ValidPlugin() {
		t.Error("the scheduler cannot take plugins")
	}
	if engine.StageScheduler.ValidExternal() {
		t.Error("the scheduler can be handed over")
	}
	if !engine.StagePipeline.ValidExternal() {
		t.Error("the pipeline cannot be handed over")
	}
	if engine.StagePipeline.ValidPlugin() {
		t.Error("the pipeline takes plugins")
	}
	if engine.Stage("wizardry").ValidPlugin() || engine.Stage("wizardry").ValidExternal() {
		t.Error("a stage that does not exist is valid")
	}
}

func TestTypeValidity(t *testing.T) {
	if !engine.TypeStr.Valid() || !engine.TypeObject.Valid() {
		t.Error("a real type is invalid")
	}
	if engine.Type("carrier-pigeon").Valid() {
		t.Error("a type that does not exist is valid")
	}
	if len(engine.TypeNames()) != len(engine.Types) {
		t.Error("TypeNames does not list every type")
	}
	if len(engine.TransformNames()) != len(engine.Transforms) {
		t.Error("TransformNames does not list every transform")
	}
}

func TestParseRejectsUnreadableDocuments(t *testing.T) {
	if _, err := engine.Parse([]byte("job {"), "job.hcl"); err == nil {
		t.Error("parsed an unterminated block")
	}
	if _, err := engine.Parse([]byte("wizardry {}\n"), "job.hcl"); err == nil {
		t.Error("parsed a block that is not in the schema")
	}
}

func TestChangesHelpers(t *testing.T) {
	var none engine.Changes
	if none.Any() {
		t.Error("an empty change set reports changes")
	}
	if none.Costly().Any() {
		t.Error("an empty change set reports costly changes")
	}
}

// The remaining diff paths: removals, additions and the parse failures that a
// resubmission has to survive reporting.

func TestDiffReportsRemovals(t *testing.T) {
	with := job(t, `
  downloader {
    plugin "cache" {}
  }

  pipeline {
    step "clean" "article" {}
  }

  exporter "json" "article" {
    dir = "./out"
  }
`)
	without := job(t, "")

	changes := engine.Diff(with, without)
	paths := map[string]bool{}
	for _, c := range changes {
		paths[c.Path] = true
		if c.To != "" && c.From == "" {
			t.Errorf("%s reads as an addition, want a removal", c.Path)
		}
	}
	for _, want := range []string{"plugin.downloader.cache", "step.clean.article", "exporter.json.article"} {
		if !paths[want] {
			t.Errorf("removal of %s was not reported: %v", want, changes)
		}
	}
}

func TestDiffReportsAdditions(t *testing.T) {
	changes := engine.Diff(job(t, ""), job(t, `
  pipeline {
    step "clean" "article" {}
  }

  exporter "json" "article" {
    dir = "./out"
  }
`))

	var added int
	for _, c := range changes {
		if c.From == "" && c.To == "added" {
			added++
		}
	}
	if added != 2 {
		t.Errorf("got %d additions, want 2: %v", added, changes)
	}
}

func TestDiffReportsChangedBlocks(t *testing.T) {
	step := func(script string) *engine.Job {
		return job(t, "\n  pipeline {\n    step \"python\" \"enrich\" {\n      script = \""+script+"\"\n    }\n  }\n")
	}
	changes := engine.Diff(step("./a.py"), step("./b.py"))
	if len(changes) != 1 || changes[0].To != "changed" {
		t.Fatalf("a changed step was not reported: %v", changes)
	}

	exporter := func(dir string) *engine.Job {
		return job(t, "\n  exporter \"json\" \"article\" {\n    dir = \""+dir+"\"\n  }\n")
	}
	changes = engine.Diff(exporter("./a"), exporter("./b"))
	if len(changes) != 1 || changes[0].To != "changed" {
		t.Fatalf("a changed exporter was not reported: %v", changes)
	}
}

func TestDiffReportsItemsAddedAndRemoved(t *testing.T) {
	two := parse(t, `
job "j" {
  start = ["https://example.com/"]
  item "article" {
    property "p" {
      type = str
    }
  }
  item "comment" {
    property "q" {
      type = str
    }
  }
}
`).Jobs[0]
	one := job(t, "")

	added := engine.Diff(one, two)
	if !added.Costly().Any() {
		t.Error("adding an item was free")
	}
	removed := engine.Diff(two, one)
	if !removed.Costly().Any() {
		t.Error("removing an item was free")
	}
}

func TestDiffReportsStartURLsRemoved(t *testing.T) {
	starts := func(list string) *engine.Job {
		return parse(t, "job \"j\" {\n  start = ["+list+"]\n  item \"a\" {\n    property \"p\" {\n      type = str\n    }\n  }\n}\n").Jobs[0]
	}

	changes := engine.Diff(
		starts(`"https://example.com/", "https://example.com/more"`),
		starts(`"https://example.com/"`),
	)
	if len(changes) != 1 {
		t.Fatalf("changes = %v", changes)
	}
	// Removing a start URL changes nothing already fetched, so it is free even
	// though adding one is not.
	if !changes[0].Effect.Free() {
		t.Errorf("removing a start URL cost %s", changes[0].Effect)
	}
}

// TestDiffSurvivesUnparseableDurations: a job that failed validation can still
// be diffed, because a client is owed a reply either way.
func TestDiffSurvivesUnparseableDurations(t *testing.T) {
	bad := job(t, `
  scheduler {
    rate     = "whenever"
    max_time = "a while"
  }

  downloader {
    timeout = "soon"
  }
`)
	good := job(t, "")

	changes := engine.Diff(good, bad)
	if !changes.Any() {
		t.Fatal("no changes reported at all")
	}
	var sawInvalid bool
	for _, c := range changes {
		if c.To == "invalid" {
			sawInvalid = true
		}
	}
	if !sawInvalid {
		t.Errorf("an unreadable duration was not reported as invalid: %v", changes)
	}
}

func TestResolvedFillsPluginOrderAndEnabled(t *testing.T) {
	j := mustValidate(t, `
  downloader {
    plugin "cache" {}

    plugin "retry" {
      enabled = false
    }
  }
`).Resolved()

	if len(j.Downloader.Plugins) != 2 {
		t.Fatalf("got %d plugins", len(j.Downloader.Plugins))
	}
	for _, p := range j.Downloader.Plugins {
		if p.Enabled == nil {
			t.Errorf("%s was left without an explicit enabled", p.Name)
		}
		if p.Name == "cache" && p.Order != 900 {
			t.Errorf("cache resolved to order %d, want the catalogued 900", p.Order)
		}
		if p.Name == "retry" && *p.Enabled {
			t.Error("an explicit enabled = false was overwritten")
		}
	}
}

func TestResolvedItemTypeIsKept(t *testing.T) {
	j := parse(t, `
job "j" {
  start = ["https://example.com/"]
  item "a" {
    type = list
    property "p" {
      type = str
    }
  }
}
`).Jobs[0].Resolved()

	if j.Items[0].Type != "list" {
		t.Errorf("item type = %q, want the declared list", j.Items[0].Type)
	}
}

func TestMutationDispositions(t *testing.T) {
	m := mustValidate(t, `
  mutation {
    costly         = "apply"
    stale_records  = "discard"
    out_of_scope   = "keep"
    orphaned_cache = "accept"
  }
`).Mutation

	if m.StaleRecordsPolicy() != engine.RecordsDiscard {
		t.Errorf("stale_records = %q", m.StaleRecordsPolicy())
	}
	if m.OutOfScopePolicy() != engine.ScopeKeep {
		t.Errorf("out_of_scope = %q", m.OutOfScopePolicy())
	}
	if m.OrphanedCachePolicy() != engine.CacheAccept {
		t.Errorf("orphaned_cache = %q", m.OrphanedCachePolicy())
	}
}

// TestReseedProducesNoAction: adding work does not invalidate any, so there is
// nothing to dispose of.
func TestReseedProducesNoAction(t *testing.T) {
	starts := func(list string) *engine.Job {
		return parse(t, "job \"j\" {\n  start = ["+list+"]\n  item \"a\" {\n    property \"p\" {\n      type = str\n    }\n  }\n"+
			"  mutation {\n    costly = \"apply\"\n  }\n}\n").Jobs[0]
	}

	review := starts(`"https://example.com/", "https://example.com/more"`).
		Review(starts(`"https://example.com/"`))

	if !review.OK() {
		t.Fatalf("refused: %v", review.Refused)
	}
	if len(review.Actions) != 0 {
		t.Errorf("a reseed produced actions: %v", review.Actions)
	}
}

func TestReextractAndCacheDispositionsAreReported(t *testing.T) {
	shape := func(required bool, bucket string) *engine.Job {
		src := fmt.Sprintf(`
job "j" {
  start = ["https://example.com/"]
  item "article" {
    property "title" {
      type     = str
      required = %t
    }
  }

  downloader {
    plugin "cache" {
      bucket = %q
    }
  }

  mutation {
    costly         = "apply"
    stale_records  = "reextract"
    orphaned_cache = "accept"
  }
}
`, required, bucket)
		return parse(t, src).Jobs[0]
	}

	review := shape(true, "moved").Review(shape(false, "pages"))
	if !review.OK() {
		t.Fatalf("refused: %v", review.Refused)
	}

	seen := map[engine.Effect]string{}
	for _, a := range review.Actions {
		seen[a.Effect] = a.Do
	}
	if seen[engine.EffectReextract] != engine.RecordsReextract {
		t.Errorf("reextract action = %q", seen[engine.EffectReextract])
	}
	if seen[engine.EffectCacheMoved] != engine.CacheAccept {
		t.Errorf("cache action = %q", seen[engine.EffectCacheMoved])
	}
}

// TestOrderAndWavesCatchACycleThemselves is the second line of defence:
// validation reports cycles, and these must not resolve one anyway if they are
// called on a document nobody validated.
func TestOrderAndWavesCatchACycleThemselves(t *testing.T) {
	j := job(t, `
  pipeline {
    step "one" "x" {
      requires = [two.y]
    }

    step "two" "y" {
      requires = [one.x]
    }
  }
`)

	if _, err := j.Order(); err == nil {
		t.Error("Order resolved a cycle")
	} else if !strings.Contains(err.Error(), "one.x") {
		t.Errorf("error does not name the steps stuck in it: %v", err)
	}

	if _, err := j.Waves(); err == nil {
		t.Error("Waves resolved a cycle")
	} else if !strings.Contains(err.Error(), "two.y") {
		t.Errorf("error does not name the steps stuck in it: %v", err)
	}
}

// TestAddressOfRejectsDeepTraversals covers the reference shapes that are not
// kind.name.
func TestAddressOfRejectsDeepTraversals(t *testing.T) {
	for name, expr := range map[string]string{
		"indexed": `[clean["article"]]`,
		"deep":    `[a.b.c.d]`,
	} {
		t.Run(name, func(t *testing.T) {
			src := "job \"j\" {\n  start = [\"https://example.com/\"]\n  item \"a\" {\n    property \"p\" {\n      type = str\n    }\n  }\n" +
				"  pipeline {\n    step \"one\" \"x\" {\n      requires = " + expr + "\n    }\n  }\n}\n"
			if _, err := engine.Parse([]byte(src), "job.hcl"); err == nil {
				t.Fatalf("accepted requires = %s", expr)
			}
		})
	}
}

// TestExporterErrorNamesNothingWhenNothingIsDeclared covers the message a job
// with no items gets.
func TestExporterErrorNamesNothingWhenNothingIsDeclared(t *testing.T) {
	doc := parse(t, `
job "j" {
  start = ["https://example.com/"]

  exporter "json" "article" {
    dir = "./out"
  }
}
`)
	err := doc.Validate()
	if err == nil {
		t.Fatal("accepted an exporter with no items at all")
	}
	if !strings.Contains(err.Error(), "none") {
		t.Errorf("error does not say that nothing is declared: %v", err)
	}
}

// TestUnnamedPropertyIsRefused covers the empty-name branch.
func TestUnnamedPropertyIsRefused(t *testing.T) {
	doc := parse(t, `
job "j" {
  start = ["https://example.com/"]
  item "a" {
    property "" {
      type = str
    }
  }
}
`)
	if err := doc.Validate(); err == nil {
		t.Fatal("accepted a property with no name")
	}
}

// TestUnnamedItemIsRefused covers the same for items.
func TestUnnamedItemIsRefused(t *testing.T) {
	doc := parse(t, `
job "j" {
  start = ["https://example.com/"]
  item "" {
    property "p" {
      type = str
    }
  }
}
`)
	if err := doc.Validate(); err == nil {
		t.Fatal("accepted an item with no name")
	}
}

func TestDiffReportsAnAddedPlugin(t *testing.T) {
	changes := engine.Diff(job(t, ""), job(t, `
  downloader {
    plugin "cache" {
      bucket = "pages"
    }
  }
`))

	if len(changes) != 1 || changes[0].To != "added" {
		t.Fatalf("an added plugin was not reported: %v", changes)
	}
	// Adding a cache is not free: it says where bodies live from now on.
	if changes[0].Effect != engine.EffectCacheMoved {
		t.Errorf("effect = %s", changes[0].Effect)
	}
}

func TestResolvedKeepsABudget(t *testing.T) {
	j := mustValidate(t, `
  scheduler {
    max_time = "90m"
  }
`).Resolved()

	if j.Scheduler.MaxTime != "1h30m0s" {
		t.Errorf("max_time resolved to %q", j.Scheduler.MaxTime)
	}

	// A job with no budget keeps none, rather than gaining a made-up one.
	none := job(t, "").Resolved()
	if none.Scheduler.MaxTime != "" {
		t.Errorf("a job with no budget gained %q", none.Scheduler.MaxTime)
	}
}

// TestReviewOfFreeChangesIsQuiet: the common case must not go near the
// disposition machinery.
func TestReviewOfFreeChangesIsQuiet(t *testing.T) {
	budget := func(pages int) *engine.Job {
		return job(t, fmt.Sprintf("\n  scheduler {\n    max_pages = %d\n  }\n", pages))
	}

	review := budget(500).Review(budget(100))
	if !review.OK() {
		t.Fatalf("a free change was refused: %v", review.Refused)
	}
	if len(review.Actions) != 0 {
		t.Errorf("a free change produced actions: %v", review.Actions)
	}
	if !review.Changes.Any() {
		t.Error("the change was not reported at all")
	}
}

// TestChainOfAnUnvalidatedJob: Chain is a reader, so it answers even for a
// document nobody accepted, and an uncatalogued plugin with no order sorts
// first rather than panicking.
func TestChainOfAnUnvalidatedJob(t *testing.T) {
	j := job(t, `
  downloader {
    plugin "somebody-elses" {}
    plugin "cache" {}
  }
`)
	chain := j.Chain(engine.StageDownloader)
	if len(chain) != 2 {
		t.Fatalf("got %d plugins", len(chain))
	}
	if chain[0].Name != "somebody-elses" {
		t.Errorf("chain = %v, want the unplaced plugin first", engine.Names(chain))
	}
}

// TestExamplesAreEvidenceNotShape is the property that makes teaching cheap:
// adding an example says something about how to find a value, not about what
// is being extracted, so it must not force a re-extraction of records that are
// still correct.
func TestExamplesAreEvidenceNotShape(t *testing.T) {
	taught := func(examples string) *engine.Job {
		src := `
job "j" {
  start = ["https://example.com/"]
  item "article" {
    property "title" {
      type = str
      ` + examples + `
    }
  }
}
`
		return parse(t, src).Jobs[0]
	}

	without := taught("")
	with := taught(`examples = ["Hello World"]`)

	if without.Spec().Fingerprint() != with.Spec().Fingerprint() {
		t.Error("adding an example changed the fingerprint, which would force a re-extraction")
	}
	if engine.Diff(without, with).Costly().Any() {
		t.Error("adding an example was reported as a costly change")
	}

	// It still has to survive into what a spider is handed, or teaching it
	// would be teaching nothing.
	rendered := string(with.Spec().HCL())
	if !strings.Contains(rendered, "Hello World") {
		t.Errorf("the example did not reach the spec:\n%s", rendered)
	}
}

func TestExamplesSurviveTheSpecRoundTrip(t *testing.T) {
	j := parse(t, `
job "j" {
  start = ["https://example.com/"]
  item "article" {
    property "title" {
      type     = str
      examples = ["Hello World", "Another headline"]
    }
  }
}
`).Jobs[0]

	back, err := engine.ParseSpec(j.Spec().HCL(), "spec.hcl")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	item, _ := back.Item("article")
	if got := item.Properties[0].Examples; len(got) != 2 || got[0] != "Hello World" {
		t.Errorf("examples = %v", got)
	}
}

// Entity references. What is extracted is a name; what is kept is a link to the
// thing that name refers to.

func TestEntityReference(t *testing.T) {
	j := mustValidate(t, "")
	_ = j

	doc := parse(t, `
job "news" {
  start = ["https://example.com/"]

  item "article" {
    property "title" {
      type = str
    }

    property "author" {
      type   = entity
      entity = "person"
    }
  }
}
`)
	if err := doc.Validate(); err != nil {
		t.Fatalf("did not validate: %v", err)
	}

	author := doc.Jobs[0].Items[0].Properties[1]
	if author.Type != string(engine.TypeEntity) {
		t.Errorf("type = %q", author.Type)
	}
	if author.Entity != "person" {
		t.Errorf("entity = %q", author.Entity)
	}
}

func TestEntityMustSayWhichKind(t *testing.T) {
	refuses(t, `
  item "b" {
    property "author" {
      type = entity
    }
  }
`, "which kind")
}

func TestEntityFieldsNeedAnEntityType(t *testing.T) {
	refuses(t, `
  item "b" {
    property "author" {
      type   = str
      entity = "person"
    }
  }
`, "nothing would resolve it")

}

// TestEntityReferenceIsShape: unlike an example, which is evidence about how to
// find a value, an entity reference changes what is extracted, so it belongs in
// the fingerprint and a change to it is a re-extraction.
func TestEntityReferenceIsShape(t *testing.T) {
	shape := func(body string) *engine.Spec {
		src := `
job "j" {
  start = ["https://example.com/"]
  item "article" {
    property "author" {
      ` + body + `
    }
  }
}
`
		return parse(t, src).Jobs[0].Spec()
	}

	plain := shape(`type = str`)
	linked := shape(`type = entity` + "\n      " + `entity = "person"`)

	if plain.Fingerprint() == linked.Fingerprint() {
		t.Error("turning a property into an entity reference did not change the shape")
	}

}

// Relations are graph edges, not record fields. A publisher is the site rather
// than a byline, so it needs somewhere other than the text to come from.

const withRelation = `
job "news" {
  start = ["https://example.com/"]

  item "article" {
    property "title" {
      type = str
    }

    property "author" {
      type   = entity
      entity = "person"
    }

    relation "publisher" {
      entity   = "company"
      property = self.domain
      topic    = ["climate@7"]
    }
  }
}
`

func TestRelationParsesAndValidates(t *testing.T) {
	doc := parse(t, withRelation)
	if err := doc.Validate(); err != nil {
		t.Fatalf("did not validate: %v", err)
	}

	item := doc.Jobs[0].Items[0]
	if len(item.Relations) != 1 {
		t.Fatalf("got %d relations", len(item.Relations))
	}

	r := item.Relations[0]
	if r.Name != "publisher" || r.Entity != "company" {
		t.Errorf("relation = %+v", r)
	}
	// self.domain resolves to the field's own name, so a misspelling is caught
	// by the parser rather than becoming an empty value.
	if r.Property != engine.SourceDomain {
		t.Errorf("property = %q, want %q", r.Property, engine.SourceDomain)
	}
	if len(r.Topic) != 1 || r.Topic[0] != "climate@7" {
		t.Errorf("topic = %v", r.Topic)
	}
}

func TestRelationNeedsAnEntity(t *testing.T) {
	// Required rather than validated, so it is a parse error with a position.
	_, err := engine.Parse([]byte(minimal(`
  item "b" {
    property "p" {
      type = str
    }

    relation "publisher" {
      property = self.domain
    }
  }
`)), "job.hcl")
	if err == nil {
		t.Fatal("accepted an edge to nothing in particular")
	}
	if !strings.Contains(err.Error(), "job.hcl") {
		t.Errorf("error does not point at the file: %v", err)
	}
}

func TestUnknownSelfFieldIsCaughtByTheParser(t *testing.T) {
	_, err := engine.Parse([]byte(minimal(`
  item "b" {
    property "p" {
      type = str
    }

    relation "publisher" {
      entity   = "company"
      property = self.doamin
    }
  }
`)), "job.hcl")
	if err == nil {
		t.Fatal("accepted a field self does not have")
	}
}

func TestRelationTopicNeedsAVersion(t *testing.T) {
	refuses(t, `
  item "b" {
    property "p" {
      type = str
    }

    relation "publisher" {
      entity = "company"
      topic  = ["climate"]
    }
  }
`, "version")
}

func TestDuplicateRelationIsRefused(t *testing.T) {
	refuses(t, `
  item "b" {
    property "p" {
      type = str
    }

    relation "publisher" {
      entity = "company"
    }

    relation "publisher" {
      entity = "person"
    }
  }
`, "twice")
}

// TestRelationIsShape: changing what gets asserted about the world is a
// re-extraction, not a free edit.
func TestRelationIsShape(t *testing.T) {
	without := parse(t, minimal(`
  item "b" {
    property "p" {
      type = str
    }
  }
`)).Jobs[0].Spec()

	with := parse(t, withRelation).Jobs[0].Spec()

	if without.Fingerprint() == with.Fingerprint() {
		t.Error("adding a relation did not change the shape")
	}
}

// TestRelationsReachTheSpider: a relation is something the spider has to
// assert, so it travels in the spec like everything else it needs.
func TestRelationsReachTheSpider(t *testing.T) {
	spec := parse(t, withRelation).Jobs[0].Spec()

	rendered := string(spec.HCL())
	for _, want := range []string{`relation "publisher"`, "company", "domain", "climate@7"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("the spec lost %q:\n%s", want, rendered)
		}
	}

	back, err := engine.ParseSpec(spec.HCL(), "spec.hcl")
	if err != nil {
		t.Fatalf("the rendered spec does not parse: %v", err)
	}
	if back.Fingerprint() != spec.Fingerprint() {
		t.Error("relations did not survive the round trip")
	}

	item, _ := back.Item("article")
	if len(item.Relations) != 1 || item.Relations[0].Entity != "company" {
		t.Errorf("relations = %+v", item.Relations)
	}
}

// An item is a measurement when it flows: tags, fields and a time.

const priceItem = `
job "markets" {
  start = ["https://example.com/quotes"]

  item "price" {
    of   = "company"
    time = "observed"

    property "value" {
      type = float
    }

    property "volume" {
      type = int
    }

    property "currency" {
      type = str
      tag  = true
    }

    property "observed" {
      type = date
    }

    relation "exchange" {
      entity   = "exchange"
      property = self.domain
    }
  }
}
`

func TestTagsAndFields(t *testing.T) {
	doc := parse(t, priceItem)
	if err := doc.Validate(); err != nil {
		t.Fatalf("did not validate: %v", err)
	}

	item := doc.Jobs[0].Items[0]

	// Dimensions: what it observes, what it relates to, and the scalar
	// declared a dimension.
	if got := strings.Join(item.Tags(), ","); got != "currency,exchange,of" {
		t.Errorf("tags = %q", got)
	}
	// Measurements: everything else.
	if got := strings.Join(item.Fields(), ","); got != "observed,value,volume" {
		t.Errorf("fields = %q", got)
	}
	if item.Of != "company" || item.Time != "observed" {
		t.Errorf("of = %q time = %q", item.Of, item.Time)
	}
}

// TestIdentityIsDimensionsAndInstantForASeries, because that is what tells two
// points apart: same sensor at two times, or two sensors at one time.
func TestIdentityIsDimensionsAndInstantForASeries(t *testing.T) {
	doc := parse(t, priceItem)
	if err := doc.Validate(); err != nil {
		t.Fatalf("did not validate: %v", err)
	}

	item := doc.Jobs[0].Items[0]
	if got := strings.Join(item.Identity(), ","); got != "currency,exchange,of,observed" {
		t.Errorf("identity = %q", got)
	}
}

// TestAPlainItemHasNoDeclaredIdentity. Dimensions are not identity, and reading
// them as one deduplicated a crawl of articles down to one per byline.
func TestAPlainItemHasNoDeclaredIdentity(t *testing.T) {
	j := mustValidate(t, `
  item "byline" {
    property "author" {
      type   = entity
      entity = "person"
    }
  }
`)
	item := j.Items[1]
	if got := strings.Join(item.Tags(), ","); got != "author" {
		t.Fatalf("tags = %q, want the entity reference", got)
	}
	if got := item.Identity(); len(got) != 0 {
		t.Errorf("identity = %v, want nothing: an item with no instant declares none", got)
	}
}

// TestEntityPropertiesAreTagsAlready: an entity reference is bounded and
// indexed by definition, so declaring it one is redundant and refused rather
// than quietly accepted.
func TestEntityPropertiesAreTagsAlready(t *testing.T) {
	j := mustValidate(t, `
  item "byline" {
    property "author" {
      type   = entity
      entity = "person"
    }
  }
`)
	if got := strings.Join(j.Items[1].Tags(), ","); got != "author" {
		t.Errorf("tags = %q, want the entity reference", got)
	}

	refuses(t, `
  item "b" {
    property "author" {
      type   = entity
      entity = "person"
      tag    = true
    }
  }
`, "already")
}

// TestATagHasToBeOneValue is the cardinality guard in its structural form: a
// dimension nobody can group by is not a dimension.
func TestATagHasToBeOneValue(t *testing.T) {
	refuses(t, `
  item "b" {
    property "author" {
      tag = true

      property "name" {
        type = str
      }
    }
  }
`, "one value")
}

// TestEventTimeMustBeADateThisItemExtracts, or the event carries a timestamp
// from nowhere.
func TestEventTimeMustBeADateThisItemExtracts(t *testing.T) {
	refuses(t, `
  item "b" {
    time = "published"

    property "title" {
      type = str
    }
  }
`, "no such property")

	refuses(t, `
  item "c" {
    time = "published"

    property "published" {
      type = str
    }
  }
`, "rather than date")
}

func TestEventShapeIsShape(t *testing.T) {
	base := parse(t, priceItem).Jobs[0].Spec()

	// of and time change what the event is, so both are re-extractions.
	noOf := parse(t, strings.Replace(priceItem, `    of   = "company"`+"\n", "", 1)).Jobs[0].Spec()
	if base.Fingerprint() == noOf.Fingerprint() {
		t.Error("what an item observes is not part of its shape")
	}

	noTime := parse(t, strings.Replace(priceItem, `    time = "observed"`+"\n", "", 1)).Jobs[0].Spec()
	if base.Fingerprint() == noTime.Fingerprint() {
		t.Error("which property is the event time is not part of its shape")
	}
}

func TestEventShapeReachesTheSpider(t *testing.T) {
	spec := parse(t, priceItem).Jobs[0].Spec()

	rendered := string(spec.HCL())
	for _, want := range []string{`of = "company"`, `time = "observed"`, "tag = true"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("the spec lost %q:\n%s", want, rendered)
		}
	}

	back, err := engine.ParseSpec(spec.HCL(), "spec.hcl")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if back.Fingerprint() != spec.Fingerprint() {
		t.Error("the event shape did not survive the round trip")
	}
}

// TestThePipelineBlockIsValidatedToo.
//
// It was not. `external_timeout = "eventually"` in a pipeline validated clean,
// where the same mistake in a downloader or a spider was refused with a named
// field, and Resolved() then swallowed the parse error so `scour job show` printed
// a pipeline with no timeout and the stored job lost the setting. A validator
// that reports every problem at once reported none for this one.
func TestThePipelineBlockIsValidatedToo(t *testing.T) {
	refuses(t, "\n  pipeline {\n    external         = true\n    external_timeout = \"eventually\"\n  }\n",
		"pipeline.external_timeout")
	refuses(t, "\n  pipeline {\n    external         = true\n    external_timeout = \"-10m\"\n  }\n",
		"negative")
}
