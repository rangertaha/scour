// SPDX-License-Identifier: GPL-3.0-or-later

package engine_test

import (
	"strings"
	"testing"

	"github.com/rangertaha/scour/internal/engine"
)

// document is the job from NOTES.md, kept identical on purpose.
//
// If the notes and the parser disagree, one of them is wrong, and a document
// nobody can run is a worse thing to have written down than no document at
// all.
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

  engine {
    limits {
      max_pages = 500
      max_depth = 4
      max_time  = "90m"
    }

    politeness {
      rate        = "2s"
      concurrency = 2
      robots      = true
    }
  }

  monitoring {
    metrics = false
    logging = false
    level   = "info"
  }

  plugin "downloader" "cache" {
    order   = 900
    backend = "s3"
    bucket  = "pages"
  }

  plugin "downloader" "retry" {
    order = 550
    times = 3
  }

  plugin "spider" "depth" {
    order = 900
  }

  plugin "scheduler" "priority" {
    order = 1
  }

  pipelines "clean" "article" {
    rule {}
    rule {}
  }

  pipelines "rank" "article" {
    requires = [clean.article]
  }

  pipelines "python" "enrich" {
    requires = [clean.article, rank.article]
    script   = "./enrich.py"
  }

  pipelines "bash" "notify" {
    requires = [python.enrich]
  }

  exporter "json"   "article" { dir    = "./out" }
  exporter "csv"    "article" { dir    = "./out" }
  exporter "nats"   "article" { stream = "ITEMS" }
  exporter "sqlite" "article" { file   = "./items.db" }
}
`

func parse(t *testing.T, src string) *engine.Document {
	t.Helper()
	doc, err := engine.Parse([]byte(src), "job.hcl")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return doc
}

func TestTheDocumentedJobParsesAndValidates(t *testing.T) {
	doc := parse(t, document)

	if err := doc.Validate(); err != nil {
		t.Fatalf("the job in NOTES.md does not validate:\n%v", err)
	}
	if got := doc.Names(); len(got) != 1 || got[0] != "news" {
		t.Fatalf("names = %v", got)
	}
}

func TestScopeAndSchema(t *testing.T) {
	job := parse(t, document).Jobs[0]

	if len(job.Domains) != 1 || job.Domains[0] != "example.com" {
		t.Errorf("domains = %v", job.Domains)
	}
	if len(job.Included) != 1 || job.Included[0] != "*.example.com" {
		t.Errorf("included = %v", job.Included)
	}

	article := job.Items[0]
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

	// Nesting is demonstrated on an object, which is the only type that can
	// hold it.
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

	title := article.Properties[2]
	if len(title.Transforms) != 2 || title.Transforms[1] != engine.TransformTrim {
		t.Errorf("title transforms = %v", title.Transforms)
	}
}

func TestEngineSettingsAndDefaults(t *testing.T) {
	job := parse(t, document).Jobs[0]

	if got := job.Engine.Limits.Pages(); got != 500 {
		t.Errorf("max_pages = %d", got)
	}
	d, err := job.Engine.Limits.MaxTimeDuration()
	if err != nil {
		t.Fatalf("max_time: %v", err)
	}
	if d.String() != "1h30m0s" {
		t.Errorf("max_time = %s", d)
	}
	if !job.Engine.Politeness.ObeysRobots() {
		t.Error("robots = true did not survive")
	}

	// A job that says nothing still gets sane numbers, and the nil receivers
	// are the point: an absent block is not a special case at the call site.
	var absent *engine.Limits
	if absent.Depth() != engine.DefaultMaxDepth {
		t.Errorf("absent limits gave depth %d", absent.Depth())
	}
	var polite *engine.Politeness
	if !polite.ObeysRobots() || polite.Agent() == "" {
		t.Error("absent politeness is not polite")
	}
}

// TestChainOrder is the property the order numbers exist for.
func TestChainOrder(t *testing.T) {
	job := parse(t, document).Jobs[0]

	chain := job.Chain(engine.StageDownloader)
	if len(chain) != 2 {
		t.Fatalf("got %d downloader plugins, want 2", len(chain))
	}
	if chain[0].Name != "retry" || chain[1].Name != "cache" {
		t.Errorf("chain = %s, %s; want retry (550) before cache (900)", chain[0].Name, chain[1].Name)
	}
}

// TestChainUsesBuiltinOrder covers a plugin that does not say where it goes.
func TestChainUsesBuiltinOrder(t *testing.T) {
	doc := parse(t, `
job "j" {
  start = ["https://example.com/"]
  item "a" {
    property "p" {
      type = str
    }
  }

  plugin "downloader" "cache" {}
  plugin "downloader" "robots" {}
}
`)
	if err := doc.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}

	chain := doc.Jobs[0].Chain(engine.StageDownloader)
	if chain[0].Name != "robots" || chain[1].Name != "cache" {
		t.Errorf("chain = %s, %s; want robots (100) before cache (900)", chain[0].Name, chain[1].Name)
	}
}

func TestDisabledPluginLeavesTheChain(t *testing.T) {
	doc := parse(t, `
job "j" {
  start = ["https://example.com/"]
  item "a" {
    property "p" {
      type = str
    }
  }

  plugin "downloader" "cache" {
    enabled = false
  }
}
`)
	if len(doc.Jobs[0].Chain(engine.StageDownloader)) != 0 {
		t.Error("a disabled plugin is still in the chain")
	}
}

func TestPipelineOrderRespectsDependencies(t *testing.T) {
	job := parse(t, document).Jobs[0]

	ordered, err := job.Order()
	if err != nil {
		t.Fatalf("order: %v", err)
	}

	position := map[string]int{}
	for i, p := range ordered {
		position[p.Address()] = i
	}

	for _, pair := range [][2]string{
		{"clean.article", "rank.article"},
		{"clean.article", "python.enrich"},
		{"rank.article", "python.enrich"},
		{"python.enrich", "bash.notify"},
	} {
		if position[pair[0]] >= position[pair[1]] {
			t.Errorf("%s should run before %s", pair[0], pair[1])
		}
	}
}

func TestPipelineCycleIsRefused(t *testing.T) {
	doc := parse(t, `
job "j" {
  start = ["https://example.com/"]
  item "a" {
    property "p" {
      type = str
    }
  }

  pipelines "one" "x" {
    requires = [three.z]
  }
  pipelines "two" "y" {
    requires = [one.x]
  }
  pipelines "three" "z" {
    requires = [two.y]
  }
}
`)
	err := doc.Validate()
	if err == nil {
		t.Fatal("accepted a cycle")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Errorf("error does not name the problem: %v", err)
	}
	if _, err := doc.Jobs[0].Order(); err == nil {
		t.Error("Order resolved a graph with a cycle in it")
	}
}

func TestPipelineSelfDependencyIsRefused(t *testing.T) {
	doc := parse(t, `
job "j" {
  start = ["https://example.com/"]
  item "a" {
    property "p" {
      type = str
    }
  }

  pipelines "one" "x" {
    requires = [one.x]
  }
}
`)
	if err := doc.Validate(); err == nil {
		t.Fatal("accepted a step that requires itself")
	}
}

func TestPipelineUndeclaredDependencyIsRefused(t *testing.T) {
	doc := parse(t, `
job "j" {
  start = ["https://example.com/"]
  item "a" {
    property "p" {
      type = str
    }
  }

  pipelines "one" "x" {
    requires = [missing.step]
  }
}
`)
	err := doc.Validate()
	if err == nil {
		t.Fatal("accepted a dependency on a step that does not exist")
	}
	if !strings.Contains(err.Error(), "missing.step") {
		t.Errorf("error does not name the dependency: %v", err)
	}
}

// TestUnknownVocabularyIsCaughtByTheParser is why bare words are predeclared:
// a typo gets a line and a column.
func TestUnknownVocabularyIsCaughtByTheParser(t *testing.T) {
	for name, src := range map[string]string{
		"type": `
job "j" {
  item "a" {
    property "p" {
      type = stir
    }
  }
}`,
		"transform": `
job "j" {
  item "a" {
    property "p" {
      transforms = [datetim]
    }
  }
}`,
	} {
		t.Run(name, func(t *testing.T) {
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

// TestQuotedVocabularyStillWorks keeps "str" and str the same value.
func TestQuotedVocabularyStillWorks(t *testing.T) {
	doc := parse(t, `
job "j" {
  start = ["https://example.com/"]
  item "a" {
    property "p" {
      type       = "str"
      transforms = ["trim"]
    }
  }
}
`)
	if err := doc.Validate(); err != nil {
		t.Fatalf("quoted forms were refused: %v", err)
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

func TestUnknownPluginNeedsAnExplicitOrder(t *testing.T) {
	src := `
job "j" {
  start = ["https://example.com/"]
  item "a" {
    property "p" {
      type = str
    }
  }

  plugin "downloader" "somebody-elses" {
    %s
  }
}
`
	if err := parse(t, strings.Replace(src, "%s", "", 1)).Validate(); err == nil {
		t.Fatal("accepted a third-party plugin with no order")
	}
	if err := parse(t, strings.Replace(src, "%s", "order = 42", 1)).Validate(); err != nil {
		t.Fatalf("refused a third-party plugin that said where it goes: %v", err)
	}
}

func TestUnknownStageIsRefused(t *testing.T) {
	doc := parse(t, `
job "j" {
  start = ["https://example.com/"]
  item "a" {
    property "p" {
      type = str
    }
  }

  plugin "wizard" "spells" {
    order = 1
  }
}
`)
	if err := doc.Validate(); err == nil {
		t.Fatal("accepted a plugin for a stage that does not exist")
	}
}

// TestSchedulerCannotBeExternal is the politeness argument in test form.
func TestSchedulerCannotBeExternal(t *testing.T) {
	doc := parse(t, `
job "j" {
  start = ["https://example.com/"]
  item "a" {
    property "p" {
      type = str
    }
  }

  engine {
    components {
      external = ["scheduler"]
    }
  }
}
`)
	err := doc.Validate()
	if err == nil {
		t.Fatal("accepted an external scheduler")
	}
	if !strings.Contains(err.Error(), "extended") {
		t.Errorf("error does not explain the alternative: %v", err)
	}
}

func TestExternalSpiderIsAccepted(t *testing.T) {
	doc := parse(t, `
job "j" {
  start = ["https://example.com/"]
  item "a" {
    property "p" {
      type = str
    }
  }

  engine {
    components {
      external = ["spider"]
      timeout  = "10m"
    }
  }
}
`)
	if err := doc.Validate(); err != nil {
		t.Fatalf("refused an external spider: %v", err)
	}
	if !doc.Jobs[0].Engine.Components.IsExternal(engine.StageSpider) {
		t.Error("spider was not marked external")
	}
}

func TestBadStartURLs(t *testing.T) {
	for name, target := range map[string]string{
		"file":    "file:///etc/passwd",
		"no host": "https://",
		"garbage": "://nonsense",
	} {
		t.Run(name, func(t *testing.T) {
			src := `
job "j" {
  start = ["` + target + `"]
  item "a" {
    property "p" {
      type = str
    }
  }
}
`
			if err := parse(t, src).Validate(); err == nil {
				t.Fatal("accepted it")
			}
		})
	}
}

func TestSeveralJobsAreIndependent(t *testing.T) {
	doc := parse(t, `
job "news" {
  start = ["https://example.com/"]
  item "article" {
    property "title" {
      type = str
    }
  }

  plugin "downloader" "cache" {
    backend = "s3"
  }
}

job "products" {
  start = ["https://shop.example/"]
  item "product" {
    property "price" {
      type = str
    }
  }
}
`)
	if err := doc.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}

	if len(doc.Jobs[0].Chain(engine.StageDownloader)) != 1 {
		t.Error("the first job lost its plugin")
	}
	if len(doc.Jobs[1].Chain(engine.StageDownloader)) != 0 {
		t.Error("the second job inherited the first job's plugin")
	}
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

  engine {
    politeness {
      concurrency = 999
    }
  }
}

job "second" {
  start = ["file:///etc/passwd"]
  item "b" {
    property "q" {
      type = str
    }
  }

  plugin "wizard" "spells" {
    order = 1
  }
}
`)
	err := doc.Validate()
	if err == nil {
		t.Fatal("accepted a document full of problems")
	}

	for _, want := range []string{
		"first", "second", "start URLs", "concurrency", "object", "http and https", "not a stage",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q:\n%v", want, err)
		}
	}
}

func TestEmptyDocument(t *testing.T) {
	doc := parse(t, "")
	if err := doc.Validate(); err == nil {
		t.Fatal("accepted a document with no jobs")
	}
}

// TestBuiltinCatalogueMatchesTheNotes guards the numbers the notes quote.
func TestBuiltinCatalogueMatchesTheNotes(t *testing.T) {
	for _, want := range []struct {
		stage engine.Stage
		name  string
		order int
	}{
		{engine.StageDownloader, "robots", 100},
		{engine.StageDownloader, "compression", 590},
		{engine.StageDownloader, "charset", 600},
		{engine.StageDownloader, "cache", 900},
		{engine.StageSpider, "httperror", 50},
		{engine.StageSpider, "depth", 900},
	} {
		got, ok := engine.DefaultOrder(want.stage, want.name)
		if !ok {
			t.Errorf("%s/%s is not a built-in", want.stage, want.name)
			continue
		}
		if got != want.order {
			t.Errorf("%s/%s order = %d, want %d", want.stage, want.name, got, want.order)
		}
	}

	// The ordering argument itself: decompress, then transcode, then cache.
	compression, _ := engine.DefaultOrder(engine.StageDownloader, "compression")
	charset, _ := engine.DefaultOrder(engine.StageDownloader, "charset")
	cacheOrder, _ := engine.DefaultOrder(engine.StageDownloader, "cache")
	if !(compression < charset && charset < cacheOrder) {
		t.Errorf("bodies must be decompressed (%d) and transcoded (%d) before they are cached (%d)",
			compression, charset, cacheOrder)
	}
}

// TestExportersAreScopedToAnItem is the requirement: an exporter names the
// item it writes, and every exporter for that item gets a copy.
func TestExportersAreScopedToAnItem(t *testing.T) {
	job := parse(t, document).Jobs[0]

	got := job.ExportersFor("article")
	if len(got) != 4 {
		t.Fatalf("article has %d exporters, want 4", len(got))
	}
	if got[0].Format != "json" || got[3].Format != "sqlite" {
		t.Errorf("exporters = %s..%s, want document order", got[0].Format, got[3].Format)
	}
	if len(job.ExportersFor("comment")) != 0 {
		t.Error("an item that was never declared has exporters")
	}
}

func TestExporterForAnUndeclaredItemIsRefused(t *testing.T) {
	doc := parse(t, `
job "j" {
  start = ["https://example.com/"]
  item "article" {
    property "p" {
      type = str
    }
  }

  exporter "json" "comment" {
    dir = "./out"
  }
}
`)
	err := doc.Validate()
	if err == nil {
		t.Fatal("accepted an exporter for an item that is not extracted")
	}
	if !strings.Contains(err.Error(), "comment") || !strings.Contains(err.Error(), "article") {
		t.Errorf("error should name the missing item and what is declared: %v", err)
	}
}

func TestSameFormatForTwoItemsIsFine(t *testing.T) {
	doc := parse(t, `
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

  exporter "json" "article" {
    dir = "./articles"
  }
  exporter "json" "comment" {
    dir = "./comments"
  }
}
`)
	if err := doc.Validate(); err != nil {
		t.Fatalf("refused one format writing two items: %v", err)
	}
	if len(doc.Jobs[0].ExportersFor("article")) != 1 || len(doc.Jobs[0].ExportersFor("comment")) != 1 {
		t.Error("exporters did not land on the right items")
	}
}

func TestDuplicateExporterIsRefused(t *testing.T) {
	doc := parse(t, `
job "j" {
  start = ["https://example.com/"]
  item "article" {
    property "p" {
      type = str
    }
  }

  exporter "json" "article" {
    dir = "./a"
  }
  exporter "json" "article" {
    dir = "./b"
  }
}
`)
	if err := doc.Validate(); err == nil {
		t.Fatal("accepted the same format twice for one item")
	}
}

// TestWavesRunIndependentStepsTogether is the point of the graph: work that
// does not depend on other work happens at the same time.
func TestWavesRunIndependentStepsTogether(t *testing.T) {
	doc := parse(t, `
job "j" {
  start = ["https://example.com/"]
  item "article" {
    property "p" {
      type = str
    }
  }

  pipelines "clean" "article" {}

  pipelines "rank" "article" {
    requires = [clean.article]
  }
  pipelines "translate" "article" {
    requires = [clean.article]
  }
  pipelines "summarise" "article" {
    requires = [clean.article]
  }

  pipelines "export" "article" {
    requires = [rank.article, translate.article, summarise.article]
  }
}
`)
	if err := doc.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	job := doc.Jobs[0]

	waves, err := job.Waves()
	if err != nil {
		t.Fatalf("waves: %v", err)
	}
	if len(waves) != 3 {
		t.Fatalf("got %d waves, want 3", len(waves))
	}

	if len(waves[0]) != 1 || waves[0][0].Address() != "clean.article" {
		t.Errorf("wave 0 = %v, want clean.article alone", addressesOf(waves[0]))
	}
	if len(waves[1]) != 3 {
		t.Errorf("wave 1 = %v, want the three independent steps together", addressesOf(waves[1]))
	}
	if len(waves[2]) != 1 || waves[2][0].Address() != "export.article" {
		t.Errorf("wave 2 = %v, want export.article", addressesOf(waves[2]))
	}

	width, err := job.Width()
	if err != nil {
		t.Fatalf("width: %v", err)
	}
	if width != 3 {
		t.Errorf("width = %d, want 3", width)
	}
}

func TestWavesAreDeterministic(t *testing.T) {
	job := parse(t, document).Jobs[0]

	first, err := job.Waves()
	if err != nil {
		t.Fatalf("waves: %v", err)
	}
	for range 20 {
		again, err := job.Waves()
		if err != nil {
			t.Fatalf("waves: %v", err)
		}
		if len(again) != len(first) {
			t.Fatalf("wave count changed between runs")
		}
		for i := range first {
			if strings.Join(addressesOf(first[i]), ",") != strings.Join(addressesOf(again[i]), ",") {
				t.Fatalf("wave %d changed between runs: %v then %v",
					i, addressesOf(first[i]), addressesOf(again[i]))
			}
		}
	}
}

func TestWavesRefuseACycle(t *testing.T) {
	doc := parse(t, `
job "j" {
  start = ["https://example.com/"]
  item "a" {
    property "p" {
      type = str
    }
  }

  pipelines "one" "x" {
    requires = [two.y]
  }
  pipelines "two" "y" {
    requires = [one.x]
  }
}
`)
	if _, err := doc.Jobs[0].Waves(); err == nil {
		t.Fatal("waves resolved a cycle")
	}
}

// TestWavesAgreeWithOrder keeps the two views of one graph consistent.
func TestWavesAgreeWithOrder(t *testing.T) {
	job := parse(t, document).Jobs[0]

	ordered, err := job.Order()
	if err != nil {
		t.Fatalf("order: %v", err)
	}
	waves, err := job.Waves()
	if err != nil {
		t.Fatalf("waves: %v", err)
	}

	flat := 0
	for _, wave := range waves {
		flat += len(wave)
	}
	if flat != len(ordered) {
		t.Errorf("waves hold %d steps, order holds %d", flat, len(ordered))
	}
}

func TestPipelinePluginIsRedirected(t *testing.T) {
	doc := parse(t, `
job "j" {
  start = ["https://example.com/"]
  item "a" {
    property "p" {
      type = str
    }
  }

  plugin "pipeline" "clean" {
    order = 1
  }
}
`)
	err := doc.Validate()
	if err == nil {
		t.Fatal("accepted a pipeline plugin")
	}
	// The message has to say what to write instead: whoever wrote this had the
	// right idea and the wrong spelling.
	if !strings.Contains(err.Error(), "pipelines") || !strings.Contains(err.Error(), "requires") {
		t.Errorf("error does not point at the right spelling: %v", err)
	}
}

func addressesOf(steps []*engine.Pipeline) []string {
	out := make([]string, 0, len(steps))
	for _, s := range steps {
		out = append(out, s.Address())
	}
	return out
}

// Resubmitting the same job name mutates it. These cover what "applies the
// changes" means, which is not the same for every change.

func TestResubmittingAnIdenticalDocumentChangesNothing(t *testing.T) {
	running := parse(t, document).Jobs[0]
	submitted := parse(t, document).Jobs[0]

	if changes := engine.Diff(running, submitted); changes.Any() {
		t.Errorf("an identical resubmission reported %d changes: %v", len(changes), changes)
	}
}

func TestReorderingIsNotAChange(t *testing.T) {
	a := parse(t, `
job "j" {
  start    = ["https://a.example/", "https://b.example/"]
  domains  = ["a.example", "b.example"]
  item "article" {
    property "title" {
      type = str
    }
    property "body" {
      type = str
    }
  }
}
`).Jobs[0]
	b := parse(t, `
job "j" {
  start    = ["https://b.example/", "https://a.example/"]
  domains  = ["b.example", "a.example"]
  item "article" {
    property "body" {
      type = str
    }
    property "title" {
      type = str
    }
  }
}
`).Jobs[0]

	if changes := engine.Diff(a, b); changes.Any() {
		t.Errorf("reordering was read as a change: %v", changes)
	}
}

func TestRaisingABudgetIsFree(t *testing.T) {
	a := parse(t, `
job "j" {
  start = ["https://example.com/"]
  item "a" {
    property "p" {
      type = str
    }
  }
  engine {
    limits {
      max_pages = 100
    }
  }
}
`).Jobs[0]
	b := parse(t, `
job "j" {
  start = ["https://example.com/"]
  item "a" {
    property "p" {
      type = str
    }
  }
  engine {
    limits {
      max_pages = 500
    }
  }
}
`).Jobs[0]

	changes := engine.Diff(a, b)
	if len(changes) != 1 {
		t.Fatalf("got %d changes, want 1: %v", len(changes), changes)
	}
	if changes[0].Path != "engine.limits.max_pages" {
		t.Errorf("path = %q", changes[0].Path)
	}
	if !changes[0].Effect.Free() {
		t.Errorf("raising a page budget should be free, got %s", changes[0].Effect)
	}
	if changes.Costly().Any() {
		t.Errorf("a free change was reported as costly: %v", changes.Costly())
	}
}

// TestMovingTheCacheIsNotFree is the change that would otherwise look like a
// crawl that suddenly forgot everything it had done.
func TestMovingTheCacheIsNotFree(t *testing.T) {
	a := parse(t, `
job "j" {
  start = ["https://example.com/"]
  item "a" {
    property "p" {
      type = str
    }
  }
  plugin "downloader" "cache" {
    backend = "s3"
    bucket  = "pages"
  }
}
`).Jobs[0]
	b := parse(t, `
job "j" {
  start = ["https://example.com/"]
  item "a" {
    property "p" {
      type = str
    }
  }
  plugin "downloader" "cache" {
    backend = "s3"
    bucket  = "somewhere-else"
  }
}
`).Jobs[0]

	costly := engine.Diff(a, b).Costly()
	if len(costly) != 1 {
		t.Fatalf("got %d costly changes, want 1: %v", len(costly), costly)
	}
	if costly[0].Effect != engine.EffectCacheMoved {
		t.Errorf("effect = %s, want cache moved", costly[0].Effect)
	}
}

func TestChangingTheSchemaIsNotFree(t *testing.T) {
	a := parse(t, `
job "j" {
  start = ["https://example.com/"]
  item "article" {
    property "title" {
      type = str
    }
  }
}
`).Jobs[0]
	b := parse(t, `
job "j" {
  start = ["https://example.com/"]
  item "article" {
    property "title" {
      type     = str
      required = true
    }
  }
}
`).Jobs[0]

	costly := engine.Diff(a, b).Costly()
	if len(costly) != 1 || costly[0].Effect != engine.EffectReextract {
		t.Fatalf("changing a property should need reextraction, got %v", costly)
	}
}

func TestNarrowingScopeIsNotFree(t *testing.T) {
	a := parse(t, `
job "j" {
  start   = ["https://example.com/"]
  domains = ["example.com", "other.example"]
  item "a" {
    property "p" {
      type = str
    }
  }
}
`).Jobs[0]
	b := parse(t, `
job "j" {
  start   = ["https://example.com/"]
  domains = ["example.com"]
  item "a" {
    property "p" {
      type = str
    }
  }
}
`).Jobs[0]

	costly := engine.Diff(a, b).Costly()
	if len(costly) != 1 || costly[0].Effect != engine.EffectRescope {
		t.Fatalf("removing a domain should rescope, got %v", costly)
	}
}

func TestAddingAStartURLReseeds(t *testing.T) {
	a := parse(t, `
job "j" {
  start = ["https://example.com/"]
  item "a" {
    property "p" {
      type = str
    }
  }
}
`).Jobs[0]
	b := parse(t, `
job "j" {
  start = ["https://example.com/", "https://example.com/more"]
  item "a" {
    property "p" {
      type = str
    }
  }
}
`).Jobs[0]

	costly := engine.Diff(a, b).Costly()
	if len(costly) != 1 || costly[0].Effect != engine.EffectReseed {
		t.Fatalf("adding a start URL should reseed, got %v", costly)
	}
}

// TestPluginConfigChangeIsSeen proves the opaque body is still comparable:
// this package does not know what "times" means and must still notice it moved.
func TestPluginConfigChangeIsSeen(t *testing.T) {
	a := parse(t, `
job "j" {
  start = ["https://example.com/"]
  item "a" {
    property "p" {
      type = str
    }
  }
  plugin "downloader" "retry" {
    times = 3
  }
}
`).Jobs[0]
	b := parse(t, `
job "j" {
  start = ["https://example.com/"]
  item "a" {
    property "p" {
      type = str
    }
  }
  plugin "downloader" "retry" {
    times = 10
  }
}
`).Jobs[0]

	changes := engine.Diff(a, b)
	if len(changes) != 1 || changes[0].Path != "plugin.downloader.retry" {
		t.Fatalf("a change inside an opaque plugin body was missed: %v", changes)
	}
	if !changes[0].Effect.Free() {
		t.Errorf("a retry count should be free to change, got %s", changes[0].Effect)
	}
}

func TestRemovingAPluginIsSeen(t *testing.T) {
	a := parse(t, `
job "j" {
  start = ["https://example.com/"]
  item "a" {
    property "p" {
      type = str
    }
  }
  plugin "spider" "depth" {}
}
`).Jobs[0]
	b := parse(t, `
job "j" {
  start = ["https://example.com/"]
  item "a" {
    property "p" {
      type = str
    }
  }
}
`).Jobs[0]

	changes := engine.Diff(a, b)
	if len(changes) != 1 || changes[0].To != "" {
		t.Fatalf("removal was not reported: %v", changes)
	}
}
