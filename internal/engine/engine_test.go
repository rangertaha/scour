// SPDX-License-Identifier: GPL-3.0-or-later

package engine_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/rangertaha/scour/internal/engine"
)

// document is the job from NOTES.md. notes_test.go parses that file directly,
// so the two cannot drift; this copy is here to be read beside the assertions.
const document = `
job "news" {
  domains  = ["example.com"]
  start    = ["https://example.com/topic"]
  included = ["*.example.com"]
  excluded = []

  item "article" {
    type        = object
    description = "A news article"

    property "url" {
      type       = str
      required   = true
      aliases    = ["uri", "link"]
      transforms = [absurl]
    }

    property "author" {
      type = object

      property "name" {
        type       = str
        transforms = [text, trim]
      }

      property "profile" {
        type = url
      }
    }

    property "title" {
      type       = str
      required   = true
      aliases    = ["headline"]
      transforms = [text, trim]
    }

    property "pubdate" {
      type       = date
      aliases    = ["published", "datePublished"]
      transforms = [datetime]
    }
  }

  scheduler {
    policy      = "priority"
    rate        = "2s"
    concurrency = 2
    max_depth   = 4
    max_pages   = 500
    max_time    = "90m"
  }

  downloader {
    robots     = true
    user_agent = "scour"
    timeout    = "30s"
    max_body   = 33554432

    plugin "cache" {
      order   = 900
      backend = "s3"
      bucket  = "pages"
    }

    plugin "retry" {
      order = 550
      times = 3
    }
  }

  spider {
    plugin "depth" {
      order = 900
    }
  }

  pipeline {
    step "clean" "article" {
      rule {}
      rule {}
    }

    step "rank" "article" {
      requires = [clean.article]
    }

    step "python" "enrich" {
      requires = [clean.article, rank.article]
      script   = "./enrich.py"
    }

    step "bash" "notify" {
      requires = [python.enrich]
      script   = "./notify.sh"
    }
  }

  monitoring {
    metrics = false
    logging = false
    level   = "info"
  }

  mutation {
    costly         = "apply"
    out_of_scope   = "drop"
    stale_records  = "reextract"
    orphaned_cache = "refuse"
  }

  exporter "json"   "article" { dir    = "./out" }
  exporter "csv"    "article" { dir    = "./out" }
  exporter "nats"   "article" { stream = "ITEMS" }
  exporter "sqlite" "article" { file   = "./items.db" }
}
`

// minimal is the smallest job that validates. Tests about one thing splice
// their blocks into it, so what they are testing is the only thing on screen.
const minimalJob = `
job "j" {
  start = ["https://example.com/"]

  item "article" {
    property "title" {
      type = str
    }
  }
%s
}
`

func minimal(extra string) string { return strings.Replace(minimalJob, "%s", extra, 1) }

func parse(t *testing.T, src string) *engine.Document {
	t.Helper()
	doc, err := engine.Parse([]byte(src), "job.hcl")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return doc
}

// job parses one job with extra blocks spliced into the minimal document.
func job(t *testing.T, extra string) *engine.Job {
	t.Helper()
	doc := parse(t, minimal(extra))
	if len(doc.Jobs) != 1 {
		t.Fatalf("got %d jobs, want 1", len(doc.Jobs))
	}
	return doc.Jobs[0]
}

func mustValidate(t *testing.T, extra string) *engine.Job {
	t.Helper()
	doc := parse(t, minimal(extra))
	if err := doc.Validate(); err != nil {
		t.Fatalf("did not validate: %v", err)
	}
	return doc.Jobs[0]
}

func refuses(t *testing.T, extra, want string) {
	t.Helper()
	err := parse(t, minimal(extra)).Validate()
	if err == nil {
		t.Fatal("accepted it")
	}
	if want != "" && !strings.Contains(err.Error(), want) {
		t.Errorf("error does not mention %q: %v", want, err)
	}
}

func addressesOf(steps []*engine.Step) []string {
	out := make([]string, 0, len(steps))
	for _, s := range steps {
		out = append(out, s.Address())
	}
	return out
}

// The documented job.

func TestTheDocumentedJobParsesAndValidates(t *testing.T) {
	doc := parse(t, document)
	if err := doc.Validate(); err != nil {
		t.Fatalf("the documented job does not validate:\n%v", err)
	}
	if got := doc.Names(); len(got) != 1 || got[0] != "news" {
		t.Fatalf("names = %v", got)
	}
}

func TestScopeAndSchema(t *testing.T) {
	j := parse(t, document).Jobs[0]

	if len(j.Domains) != 1 || j.Domains[0] != "example.com" {
		t.Errorf("domains = %v", j.Domains)
	}
	if len(j.Included) != 1 || j.Included[0] != "*.example.com" {
		t.Errorf("included = %v", j.Included)
	}

	article := j.Items[0]
	if article.Type != "object" {
		t.Errorf("item type = %q, want the bare word object to resolve", article.Type)
	}
	if len(article.Properties) != 4 {
		t.Fatalf("got %d properties, want 4", len(article.Properties))
	}

	url := article.Properties[0]
	if !url.Required || url.Type != "str" {
		t.Errorf("url = %+v", url)
	}
	if len(url.Transforms) != 1 || url.Transforms[0] != engine.TransformAbsURL {
		t.Errorf("transforms = %v, want the bare word absurl to resolve", url.Transforms)
	}

	// Nesting is demonstrated on an object, the only type that can hold it.
	author := article.Properties[1]
	if author.Type != string(engine.TypeObject) {
		t.Errorf("author type = %q, want object", author.Type)
	}
	if len(author.Properties) != 2 {
		t.Fatalf("author has %d nested properties, want 2", len(author.Properties))
	}
	if author.Properties[1].Type != string(engine.TypeURL) {
		t.Errorf("author.profile type = %q", author.Properties[1].Type)
	}
}

// Stage blocks: a setting lives with the stage that enforces it.

func TestStageSettings(t *testing.T) {
	j := parse(t, document).Jobs[0]

	if j.Scheduler.Pages() != 500 || j.Scheduler.Depth() != 4 {
		t.Errorf("scheduler = %+v", j.Scheduler)
	}
	d, err := j.Scheduler.MaxTimeDuration()
	if err != nil {
		t.Fatalf("max_time: %v", err)
	}
	if d.String() != "1h30m0s" {
		t.Errorf("max_time = %s", d)
	}
	if rate, _ := j.Scheduler.RateDuration(); rate.String() != "2s" {
		t.Errorf("rate = %s", rate)
	}

	if !j.Downloader.ObeysRobots() {
		t.Error("robots = true did not survive")
	}
	if j.Downloader.Agent() != "scour" {
		t.Errorf("user agent = %q", j.Downloader.Agent())
	}
	if j.Downloader.BodyBytes() != 33554432 {
		t.Errorf("max_body = %d", j.Downloader.BodyBytes())
	}
}

// TestAbsentStagesAreNotSpecialCases: every accessor is nil-safe, so a job that
// configures nothing needs no checks at the call site.
func TestAbsentStagesAreNotSpecialCases(t *testing.T) {
	var s *engine.Scheduler
	if s.Depth() != engine.DefaultMaxDepth || s.Parallelism() != engine.DefaultConcurrency {
		t.Error("an absent scheduler does not answer")
	}
	var d *engine.Downloader
	if !d.ObeysRobots() || d.Agent() == "" || d.BodyBytes() == 0 {
		t.Error("an absent downloader is not polite")
	}
	var sp *engine.Spider
	if sp.IsExternal() {
		t.Error("an absent spider claims to be external")
	}
}

// TestSchedulerCannotBeExternal: the block has no external attribute at all, so
// this is a parse error with a line and a column rather than a validation rule.
func TestSchedulerCannotBeExternal(t *testing.T) {
	_, err := engine.Parse([]byte(minimal(`
  scheduler {
    external = true
  }
`)), "job.hcl")

	if err == nil {
		t.Fatal("accepted an external scheduler")
	}
	if !strings.Contains(err.Error(), "external") || !strings.Contains(err.Error(), "job.hcl") {
		t.Errorf("error does not name the attribute and the place: %v", err)
	}
}

func TestExternalStages(t *testing.T) {
	j := mustValidate(t, `
  spider {
    external         = true
    external_timeout = "10m"
  }
`)
	if !j.IsExternal(engine.StageSpider) {
		t.Error("spider was not marked external")
	}
	if j.IsExternal(engine.StageDownloader) {
		t.Error("downloader was marked external without being asked")
	}
	if d, _ := j.Spider.ExternalWait(); d.String() != "10m0s" {
		t.Errorf("timeout = %s", d)
	}
}

// Chains.

func TestChainOrder(t *testing.T) {
	j := parse(t, document).Jobs[0]

	chain := j.Chain(engine.StageDownloader)
	if len(chain) != 2 {
		t.Fatalf("got %d downloader plugins, want 2", len(chain))
	}
	if chain[0].Name != "retry" || chain[1].Name != "cache" {
		t.Errorf("chain = %v; want retry (550) before cache (900)", engine.Names(chain))
	}
}

func TestChainUsesCataloguedOrder(t *testing.T) {
	j := mustValidate(t, `
  downloader {
    plugin "cache" {}
    plugin "offsite" {}
  }
`)
	chain := j.Chain(engine.StageDownloader)
	if len(chain) != 2 || chain[0].Name != "offsite" || chain[1].Name != "cache" {
		t.Errorf("chain = %v; want offsite (500) before cache (900)", engine.Names(chain))
	}

	// The default is not just used for sorting and then forgotten: whoever
	// builds the chain has to be able to read the position back out, because a
	// plugin's own configuration never mentions it.
	if chain[0].Position() != 500 || chain[1].Position() != 900 {
		t.Errorf("positions = %d, %d; want the catalogued 500 and 900",
			chain[0].Position(), chain[1].Position())
	}
}

// TestPositionReportsTheExplicitOrder: a document that sets order is what the
// position reports, since that is the whole point of setting it.
func TestPositionReportsTheExplicitOrder(t *testing.T) {
	j := mustValidate(t, `
  downloader {
    plugin "cache" {
      order = 10
    }
  }
`)
	if got := j.Chain(engine.StageDownloader)[0].Position(); got != 10 {
		t.Errorf("position = %d, want the 10 the document asked for", got)
	}
}

// TestPluginKnowsItsStageFromItsBlock: there is no stage label, so the block
// and the plugin cannot disagree about where it belongs.
func TestPluginKnowsItsStageFromItsBlock(t *testing.T) {
	j := mustValidate(t, `
  downloader {
    plugin "cache" {}
  }

  spider {
    plugin "depth" {}
  }
`)
	seen := map[string]engine.Stage{}
	for _, p := range j.Plugins() {
		seen[p.Name] = p.Stage()
	}
	if seen["cache"] != engine.StageDownloader {
		t.Errorf("cache is in stage %s", seen["cache"])
	}
	if seen["depth"] != engine.StageSpider {
		t.Errorf("depth is in stage %s", seen["depth"])
	}
}

// TestDisabledIsTheSameAsAbsent pins the rule: a job gets exactly the chain it
// lists, and enabled = false is one of two spellings for not listing a link.
func TestDisabledIsTheSameAsAbsent(t *testing.T) {
	absent := mustValidate(t, `
  downloader {
    plugin "retry" {}
  }
`)
	disabled := mustValidate(t, `
  downloader {
    plugin "retry" {}

    plugin "cache" {
      enabled = false
      bucket  = "pages"
    }
  }
`)

	a := engine.Names(absent.Chain(engine.StageDownloader))
	d := engine.Names(disabled.Chain(engine.StageDownloader))
	if strings.Join(a, ",") != strings.Join(d, ",") {
		t.Errorf("disabled chain %v differs from absent chain %v", d, a)
	}

	// The configuration survives being turned off, which is the only reason
	// both spellings exist.
	var found bool
	for _, p := range disabled.Plugins() {
		if p.Name == "cache" {
			found = true
		}
	}
	if !found {
		t.Error("a disabled plugin lost its configuration")
	}
}

func TestNothingIsAddedThatWasNotAskedFor(t *testing.T) {
	j := mustValidate(t, "")
	for _, stage := range []engine.Stage{
		engine.StageScheduler, engine.StageDownloader, engine.StageSpider,
	} {
		if got := j.Chain(stage); len(got) != 0 {
			t.Errorf("%s chain is %v for a job that listed nothing", stage, engine.Names(got))
		}
	}
}

func TestUncataloguedPluginNeedsAnExplicitOrder(t *testing.T) {
	refuses(t, `
  downloader {
    plugin "somebody-elses" {}
  }
`, "explicit one")

	mustValidate(t, `
  downloader {
    plugin "somebody-elses" {
      order = 42
    }
  }
`)
}

// The pipeline graph.

func TestPipelineOrderRespectsDependencies(t *testing.T) {
	j := parse(t, document).Jobs[0]

	ordered, err := j.Order()
	if err != nil {
		t.Fatalf("order: %v", err)
	}
	position := map[string]int{}
	for i, s := range ordered {
		position[s.Address()] = i
	}

	for _, pair := range [][2]string{
		{"clean.article", "rank.article"},
		{"rank.article", "python.enrich"},
		{"python.enrich", "bash.notify"},
	} {
		if position[pair[0]] >= position[pair[1]] {
			t.Errorf("%s should run before %s", pair[0], pair[1])
		}
	}
}

// TestWavesRunIndependentStepsTogether is the point of the graph: work that
// does not depend on other work happens at the same time.
func TestWavesRunIndependentStepsTogether(t *testing.T) {
	j := mustValidate(t, `
  pipeline {
    step "clean" "article" {}

    step "rank" "article" {
      requires = [clean.article]
    }

    step "translate" "article" {
      requires = [clean.article]
    }

    step "summarise" "article" {
      requires = [clean.article]
    }

    step "export" "article" {
      requires = [rank.article, translate.article, summarise.article]
    }
  }
`)

	waves, err := j.Waves()
	if err != nil {
		t.Fatalf("waves: %v", err)
	}
	if len(waves) != 3 {
		t.Fatalf("got %d waves, want 3", len(waves))
	}
	if len(waves[0]) != 1 || waves[0][0].Address() != "clean.article" {
		t.Errorf("wave 0 = %v", addressesOf(waves[0]))
	}
	if len(waves[1]) != 3 {
		t.Errorf("wave 1 = %v, want the three independent steps together", addressesOf(waves[1]))
	}
	if len(waves[2]) != 1 {
		t.Errorf("wave 2 = %v", addressesOf(waves[2]))
	}

	width, err := j.Width()
	if err != nil {
		t.Fatalf("width: %v", err)
	}
	if width != 3 {
		t.Errorf("width = %d, want 3", width)
	}
}

func TestWavesAreDeterministic(t *testing.T) {
	j := parse(t, document).Jobs[0]

	first, err := j.Waves()
	if err != nil {
		t.Fatalf("waves: %v", err)
	}
	for range 20 {
		again, err := j.Waves()
		if err != nil {
			t.Fatalf("waves: %v", err)
		}
		if len(again) != len(first) {
			t.Fatal("wave count changed between runs")
		}
		for i := range first {
			if strings.Join(addressesOf(first[i]), ",") != strings.Join(addressesOf(again[i]), ",") {
				t.Fatalf("wave %d changed between runs", i)
			}
		}
	}
}

func TestPipelineCycleIsRefused(t *testing.T) {
	refuses(t, `
  pipeline {
    step "one" "x" {
      requires = [three.z]
    }

    step "two" "y" {
      requires = [one.x]
    }

    step "three" "z" {
      requires = [two.y]
    }
  }
`, "cycle")
}

func TestPipelineSelfDependencyIsRefused(t *testing.T) {
	refuses(t, `
  pipeline {
    step "one" "x" {
      requires = [one.x]
    }
  }
`, "itself")
}

func TestPipelineUndeclaredDependencyIsRefused(t *testing.T) {
	refuses(t, `
  pipeline {
    step "one" "x" {
      requires = [missing.step]
    }
  }
`, "missing.step")
}

// Vocabulary.

func TestUnknownVocabularyIsCaughtByTheParser(t *testing.T) {
	for name, body := range map[string]string{
		"type":      `type = stir`,
		"transform": `transforms = [datetim]`,
	} {
		t.Run(name, func(t *testing.T) {
			src := "job \"j\" {\n  item \"a\" {\n    property \"p\" {\n      " + body + "\n    }\n  }\n}\n"
			_, err := engine.Parse([]byte(src), "job.hcl")
			if err == nil {
				t.Fatal("accepted a word that is not in the vocabulary")
			}
			if !strings.Contains(err.Error(), "job.hcl") {
				t.Errorf("error does not point at the file: %v", err)
			}
		})
	}
}

func TestNestedPropertyTypeMismatch(t *testing.T) {
	doc := parse(t, `
job "j" {
  start = ["https://example.com/"]
  item "a" {
    property "p" {
      type = str
      property "child" {}
    }
  }
}
`)
	err := doc.Validate()
	if err == nil {
		t.Fatal("accepted nested properties on a string")
	}
	if !strings.Contains(err.Error(), "object") {
		t.Errorf("error does not say what would be allowed: %v", err)
	}
}

// Validation.

func TestBadStartURLs(t *testing.T) {
	for name, target := range map[string]string{
		"file":    "file:///etc/passwd",
		"no host": "https://",
		"garbage": "://nonsense",
	} {
		t.Run(name, func(t *testing.T) {
			doc := parse(t, "job \"j\" {\n  start = [\""+target+"\"]\n  item \"a\" {\n    property \"p\" {\n      type = str\n    }\n  }\n}\n")
			if err := doc.Validate(); err == nil {
				t.Fatal("accepted it")
			}
		})
	}
}

func TestSchedulerPolicyIsChecked(t *testing.T) {
	refuses(t, "\n  scheduler {\n    policy = \"vibes\"\n  }\n", "scheduler.policy")
}

func TestConcurrencyIsCapped(t *testing.T) {
	refuses(t, "\n  scheduler {\n    concurrency = 999\n  }\n", "single host")
}

func TestMonitoringLevelIsChecked(t *testing.T) {
	refuses(t, "\n  monitoring {\n    level = \"shouty\"\n  }\n", "monitoring.level")
}

func TestDuplicateJobNamesRefused(t *testing.T) {
	doc := parse(t, `
job "news" {
  start = ["https://example.com/"]
  item "a" {
    property "p" {
      type = str
    }
  }
}

job "news" {
  start = ["https://other.example/"]
  item "a" {
    property "p" {
      type = str
    }
  }
}
`)
	if err := doc.Validate(); err == nil {
		t.Fatal("accepted the same job name twice")
	}
}

// TestEveryProblemAtOnce is the usability property with teeth.
func TestEveryProblemAtOnce(t *testing.T) {
	doc := parse(t, `
job "first" {
  item "a" {
    property "p" {
      type = int
      property "child" {}
    }
  }

  scheduler {
    concurrency = 999
  }
}

job "second" {
  start = ["file:///etc/passwd"]
  item "b" {
    property "q" {
      type = str
    }
  }

  monitoring {
    level = "shouty"
  }
}
`)
	err := doc.Validate()
	if err == nil {
		t.Fatal("accepted a document full of problems")
	}
	for _, want := range []string{
		"first", "second", "start URLs", "concurrency", "object", "http and https", "monitoring.level",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q:\n%v", want, err)
		}
	}
}

func TestEmptyDocument(t *testing.T) {
	if err := parse(t, "").Validate(); err == nil {
		t.Fatal("accepted a document with no jobs")
	}
}

// Exporters.

func TestExportersAreScopedToAnItem(t *testing.T) {
	j := parse(t, document).Jobs[0]

	got := j.ExportersFor("article")
	if len(got) != 4 {
		t.Fatalf("article has %d exporters, want 4", len(got))
	}
	if got[0].Format != "json" || got[3].Format != "sqlite" {
		t.Error("exporters are not in document order")
	}
	if len(j.ExportersFor("comment")) != 0 {
		t.Error("an item that was never declared has exporters")
	}
}

func TestExporterForAnUndeclaredItemIsRefused(t *testing.T) {
	refuses(t, "\n  exporter \"json\" \"comment\" {\n    dir = \"./out\"\n  }\n", "comment")
}

func TestDuplicateExporterIsRefused(t *testing.T) {
	refuses(t, `
  exporter "json" "article" {
    dir = "./a"
  }

  exporter "json" "article" {
    dir = "./b"
  }
`, "twice")
}

// Defaults.

func TestEmptyJobResolvesToSomethingSane(t *testing.T) {
	j := job(t, "").Resolved()

	if j.Scheduler.MaxDepth != engine.DefaultMaxDepth {
		t.Errorf("depth = %d", j.Scheduler.MaxDepth)
	}
	if j.Scheduler.Policy != engine.DefaultPolicy {
		t.Errorf("policy = %q", j.Scheduler.Policy)
	}
	if j.Downloader.MaxBody != engine.DefaultMaxBody {
		t.Errorf("max_body = %d", j.Downloader.MaxBody)
	}
	if !j.Downloader.ObeysRobots() {
		t.Error("robots defaulted off, which would harm somebody else's server")
	}
	if j.Downloader.UserAgent == "" {
		t.Error("no user agent")
	}
	if !j.Monitoring.LoggingOn() {
		t.Error("logging defaulted off, so a job that fails says nothing")
	}
	if j.Mutation.Costly != engine.CostlyRefuse {
		t.Errorf("costly = %q, want the cautious default", j.Mutation.Costly)
	}
	if j.Items[0].Properties[0].Type != string(engine.DefaultPropertyType) {
		t.Errorf("property type = %q", j.Items[0].Properties[0].Type)
	}
}

func TestNestedPropertyDefaultsToObject(t *testing.T) {
	j := parse(t, `
job "j" {
  start = ["https://example.com/"]
  item "a" {
    property "author" {
      property "name" {}
    }
  }
}
`).Jobs[0].Resolved()

	author := j.Items[0].Properties[0]
	if author.Type != string(engine.TypeObject) {
		t.Errorf("a property with children resolved to %q, want object", author.Type)
	}
	if author.Properties[0].Type != string(engine.TypeStr) {
		t.Errorf("nested leaf resolved to %q", author.Properties[0].Type)
	}
}

func TestResolvedDoesNotMutate(t *testing.T) {
	j := job(t, "")
	_ = j.Resolved()

	if j.Scheduler != nil || j.Downloader != nil || j.Mutation != nil || j.Monitoring != nil {
		t.Error("Resolved filled in the job it was called on")
	}
}

func TestResolvedKeepsWhatWasAsked(t *testing.T) {
	j := mustValidate(t, `
  scheduler {
    max_depth = 42
  }

  downloader {
    robots = false
  }

  monitoring {
    level = "debug"
  }
`).Resolved()

	if j.Scheduler.MaxDepth != 42 {
		t.Errorf("depth = %d, want the submitted 42", j.Scheduler.MaxDepth)
	}
	if j.Downloader.ObeysRobots() {
		t.Error("an explicit robots = false was overwritten")
	}
	if j.Monitoring.LogLevel() != "debug" {
		t.Errorf("level = %q", j.Monitoring.LogLevel())
	}
}

func TestEveryDefaultIsListed(t *testing.T) {
	d := engine.Defaults()
	if len(d) == 0 {
		t.Fatal("no defaults are listed")
	}
	for _, want := range []string{
		"scheduler.max_depth", "scheduler.policy", "downloader.robots",
		"downloader.max_body", "monitoring.level", "mutation.costly",
		"item.property.type",
	} {
		if _, ok := d[want]; !ok {
			t.Errorf("%s has no documented default", want)
		}
	}
}

// The spec a spider is handed.

func TestSpecIsJustTheShapes(t *testing.T) {
	j := parse(t, document).Jobs[0]
	spec := j.Spec()

	if spec.Job != "news" || len(spec.Items) != len(j.Items) {
		t.Errorf("spec = %+v", spec)
	}
	if _, ok := spec.Item("article"); !ok {
		t.Error("article is not in the spec")
	}
	if _, ok := spec.Item("nothing"); ok {
		t.Error("an item that does not exist was found")
	}
}

func TestSpecRoundTrips(t *testing.T) {
	original := parse(t, document).Jobs[0].Spec()

	rendered := original.HCL()
	back, err := engine.ParseSpec(rendered, "spec.hcl")
	if err != nil {
		t.Fatalf("the rendered spec does not parse:\n%s\n%v", rendered, err)
	}
	if err := back.Validate(); err != nil {
		t.Fatalf("the rendered spec does not validate: %v", err)
	}
	if back.Fingerprint() != original.Fingerprint() {
		t.Errorf("fingerprint changed across the round trip:\n%s", rendered)
	}
}

// TestFingerprintTracksTheShape is what stops a record being attributed to a
// shape it was not read under.
func TestFingerprintTracksTheShape(t *testing.T) {
	shape := func(required bool) *engine.Spec {
		src := fmt.Sprintf(`
job "j" {
  start = ["https://example.com/"]
  item "article" {
    property "title" {
      type     = str
      required = %t
    }
  }
}
`, required)
		return parse(t, src).Jobs[0].Spec()
	}

	if shape(false).Fingerprint() == shape(true).Fingerprint() {
		t.Error("a changed property did not change the fingerprint")
	}
	if shape(false).Fingerprint() != shape(false).Fingerprint() {
		t.Error("the same shape produced two fingerprints")
	}
}

func TestFingerprintIgnoresCosmetics(t *testing.T) {
	order := func(first, second string) *engine.Spec {
		src := fmt.Sprintf(`
job "j" {
  start = ["https://example.com/"]
  item "article" {
    property %q {
      type = str
    }
    property %q {
      type = str
    }
  }
}
`, first, second)
		return parse(t, src).Jobs[0].Spec()
	}

	if order("title", "body").Fingerprint() != order("body", "title").Fingerprint() {
		t.Error("reordering properties changed the fingerprint")
	}
}

func TestSpecCarriesEverythingAnExtractorNeeds(t *testing.T) {
	spec := parse(t, `
job "j" {
  start = ["https://example.com/"]
  item "article" {
    property "title" {
      type       = str
      required   = true
      aliases    = ["headline"]
      regexes    = ["^.{3,}$"]
      transforms = [text, trim]
      xpath      = ["//h1"]
      css        = ["h1.title"]
    }
  }
}
`).Jobs[0].Spec()

	rendered := string(spec.HCL())
	for _, want := range []string{
		"headline", "^.{3,}$", "text", "trim", "//h1", "h1.title", "required = true",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("the rendered spec lost %q:\n%s", want, rendered)
		}
	}
}

// Resubmission: the same job name mutates, and the mutation block says at what
// cost.

func TestResubmittingAnIdenticalDocumentChangesNothing(t *testing.T) {
	running := parse(t, document).Jobs[0]
	submitted := parse(t, document).Jobs[0]

	if changes := engine.Diff(running, submitted); changes.Any() {
		t.Errorf("an identical resubmission reported %d changes: %v", len(changes), changes)
	}
}

func TestRaisingABudgetIsFree(t *testing.T) {
	budget := func(pages int) *engine.Job {
		return job(t, fmt.Sprintf("\n  scheduler {\n    max_pages = %d\n  }\n", pages))
	}

	changes := engine.Diff(budget(100), budget(500))
	if len(changes) != 1 || changes[0].Path != "scheduler.max_pages" {
		t.Fatalf("changes = %v", changes)
	}
	if !changes[0].Effect.Free() {
		t.Errorf("raising a page budget should be free, got %s", changes[0].Effect)
	}
}

func scopedJob(t *testing.T, domains, extra string) *engine.Job {
	t.Helper()
	src := fmt.Sprintf(`
job "j" {
  start   = ["https://example.com/"]
  domains = [%s]

  item "article" {
    property "title" {
      type = str
    }
  }
%s
}
`, domains, extra)
	return parse(t, src).Jobs[0]
}

func TestNarrowingScopeIsNotFree(t *testing.T) {
	costly := engine.Diff(
		scopedJob(t, `"a.example", "b.example"`, ""),
		scopedJob(t, `"a.example"`, ""),
	).Costly()

	if len(costly) != 1 || costly[0].Effect != engine.EffectRescope {
		t.Fatalf("removing a domain should rescope, got %v", costly)
	}
}

func TestChangingTheSchemaIsNotFree(t *testing.T) {
	shape := func(required bool) *engine.Job {
		src := fmt.Sprintf(`
job "j" {
  start = ["https://example.com/"]
  item "article" {
    property "title" {
      type     = str
      required = %t
    }
  }
}
`, required)
		return parse(t, src).Jobs[0]
	}

	costly := engine.Diff(shape(false), shape(true)).Costly()
	if len(costly) != 1 || costly[0].Effect != engine.EffectReextract {
		t.Fatalf("changing a property should need reextraction, got %v", costly)
	}
}

// TestPluginConfigChangeIsSeen proves the opaque body stays comparable: this
// package does not know what "times" means and must still notice it moved.
func TestPluginConfigChangeIsSeen(t *testing.T) {
	retry := func(times int) *engine.Job {
		return job(t, fmt.Sprintf("\n  downloader {\n    plugin \"retry\" {\n      times = %d\n    }\n  }\n", times))
	}

	changes := engine.Diff(retry(3), retry(10))
	if len(changes) != 1 || !strings.Contains(changes[0].Path, "retry") {
		t.Fatalf("a change inside an opaque plugin body was missed: %v", changes)
	}
	if !changes[0].Effect.Free() {
		t.Errorf("a retry count should be free to change, got %s", changes[0].Effect)
	}
}

func TestMutationIsOptionalAndCautious(t *testing.T) {
	j := job(t, "")
	if j.Mutation != nil {
		t.Fatal("a job with no mutation block got one")
	}
	if j.Mutation.CostlyPolicy() != engine.CostlyRefuse {
		t.Errorf("costly = %q", j.Mutation.CostlyPolicy())
	}
	if j.Mutation.StaleRecordsPolicy() != engine.RecordsKeep {
		t.Error("the default disposition deletes records, which it must not")
	}
}

func TestCostlyChangeIsRefusedByDefault(t *testing.T) {
	review := scopedJob(t, `"a.example"`, "").Review(scopedJob(t, `"a.example", "b.example"`, ""))

	if review.OK() {
		t.Fatal("a narrowed scope was applied without being asked about")
	}
	if len(review.Actions) != 0 {
		t.Error("work was disposed of despite the submission being refused")
	}
}

func TestCostlyApplyProducesActions(t *testing.T) {
	running := scopedJob(t, `"a.example", "b.example"`, "")
	submitted := scopedJob(t, `"a.example"`, `
  mutation {
    costly       = "apply"
    out_of_scope = "drop"
  }
`)

	review := submitted.Review(running)
	if !review.OK() {
		t.Fatalf("refused despite costly = apply: %v", review.Refused)
	}
	if len(review.Actions) != 1 || review.Actions[0].Do != engine.ScopeDrop {
		t.Fatalf("actions = %v", review.Actions)
	}
}

// TestCacheRefusesEvenUnderApply: losing a corpus is worse than the other
// costly changes, so a job may allow the rest and still refuse that.
func TestCacheRefusesEvenUnderApply(t *testing.T) {
	cached := func(bucket string) *engine.Job {
		return job(t, fmt.Sprintf(`
  downloader {
    plugin "cache" {
      bucket = %q
    }
  }

  mutation {
    costly = "apply"
  }
`, bucket))
	}

	review := cached("somewhere-else").Review(cached("pages"))
	if review.OK() {
		t.Fatal("the cache moved and the job did not notice")
	}
	if review.Refused[0].Effect != engine.EffectCacheMoved {
		t.Errorf("refused = %v", review.Refused)
	}
}

func TestMutationValuesAreChecked(t *testing.T) {
	for field, value := range map[string]string{
		"costly":         "maybe",
		"out_of_scope":   "burn",
		"stale_records":  "shred",
		"orphaned_cache": "shrug",
	} {
		t.Run(field, func(t *testing.T) {
			refuses(t, fmt.Sprintf("\n  mutation {\n    %s = %q\n  }\n", field, value), "mutation."+field)
		})
	}
}
