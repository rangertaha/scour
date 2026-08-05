// SPDX-License-Identifier: GPL-3.0-or-later

package pipeline_test

import (
	"context"
	"errors"
	"github.com/rangertaha/scour/internal/registry/registrytest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rangertaha/scour/internal/engine"
	"github.com/rangertaha/scour/internal/pipeline"
	"github.com/rangertaha/scour/internal/record"

	_ "github.com/rangertaha/scour/internal/pipeline/steps"
)

var fetched = time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)

func job(t *testing.T, blocks string) *engine.Job {
	t.Helper()

	src := `
job "news" {
  start = ["https://example.com/"]

  item "article" {
    property "title" {
      type     = str
      required = true
    }

    property "author" {
      type   = entity
      entity = "person"
    }

    property "score" {
      type = float
    }
  }
` + blocks + `
}
`
	doc, err := engine.Parse([]byte(src), "job.hcl")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := doc.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	return doc.Jobs[0]
}

func rec(url string, values map[string]string) *record.Record {
	return &record.Record{
		Item:    "article",
		URL:     url,
		Spec:    "abc123",
		Fetched: fetched,
		Values:  values,
	}
}

func run(t *testing.T, j *engine.Job, records ...*record.Record) []*record.Record {
	t.Helper()

	p, err := pipeline.New(context.Background(), j)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	out, err := p.Run(context.Background(), records)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return out
}

func TestCleanTidiesAndDrops(t *testing.T) {
	out := run(t, job(t, `
  pipeline {
    step "clean" "article" {
      drop       = ["score"]
      drop_empty = true
    }
  }
`), rec("https://example.com/a", map[string]string{
		"title":  "  Lots   of\n  space  ",
		"score":  "0.9",
		"author": "",
	}))

	if len(out) != 1 {
		t.Fatalf("got %d records", len(out))
	}
	if got := out[0].Get("title"); got != "Lots of space" {
		t.Errorf("title = %q", got)
	}
	if _, ok := out[0].Values["score"]; ok {
		t.Error("a dropped value survived")
	}
	if _, ok := out[0].Values["author"]; ok {
		t.Error("an empty value survived drop_empty")
	}
}

// TestValidateDropsWhatIsNotARecord. Exporting an item missing a required
// property produces a row with a hole in it that nothing downstream can tell
// from a page that genuinely had no title.
func TestValidateDropsWhatIsNotARecord(t *testing.T) {
	out := run(t, job(t, `
  pipeline {
    step "validate" "article" {}
  }
`),
		rec("https://example.com/a", map[string]string{"title": "Real"}),
		rec("https://example.com/b", map[string]string{"author": "Nobody"}))

	if len(out) != 1 || out[0].URL != "https://example.com/a" {
		t.Errorf("kept %d records: %v", len(out), out)
	}
}

// TestValidateMayFailTheRunInstead, for the job that would rather stop than
// export a corpus with holes in it.
func TestValidateMayFailTheRunInstead(t *testing.T) {
	p, err := pipeline.New(context.Background(), job(t, `
  pipeline {
    step "validate" "article" {
      drop = false
    }
  }
`))
	if err != nil {
		t.Fatal(err)
	}

	_, err = p.Run(context.Background(), []*record.Record{
		rec("https://example.com/b", map[string]string{"author": "Nobody"}),
	})
	if err == nil {
		t.Fatal("a record missing a required property passed")
	}
	if !strings.Contains(err.Error(), "title") || !strings.Contains(err.Error(), "validate.article") {
		t.Errorf("the error does not say what failed where: %v", err)
	}
}

// TestDedupeIsByWhatIdentifiesTheItem, not by the URL: the same article at two
// URLs is one article.
func TestDedupeIsByWhatIdentifiesTheItem(t *testing.T) {
	out := run(t, job(t, `
  pipeline {
    step "dedupe" "article" {
      by = ["title"]
    }
  }
`),
		rec("https://example.com/a", map[string]string{"title": "One"}),
		rec("https://example.com/a?utm=x", map[string]string{"title": "One"}),
		rec("https://example.com/b", map[string]string{"title": "Two"}))

	if len(out) != 2 {
		t.Errorf("kept %d records, want the two distinct articles", len(out))
	}
}

// TestRankOrdersNumbersAsNumbers. A price of 9 sorting after 10 is the kind of
// thing nobody notices until a report is wrong.
func TestRankOrdersNumbersAsNumbers(t *testing.T) {
	out := run(t, job(t, `
  pipeline {
    step "rank" "article" {
      by         = "score"
      descending = true
      limit      = 2
    }
  }
`),
		rec("https://example.com/a", map[string]string{"title": "a", "score": "9"}),
		rec("https://example.com/b", map[string]string{"title": "b", "score": "10"}),
		rec("https://example.com/c", map[string]string{"title": "c", "score": "2"}))

	if len(out) != 2 {
		t.Fatalf("limit kept %d", len(out))
	}
	if out[0].Get("score") != "10" || out[1].Get("score") != "9" {
		t.Errorf("order = %s then %s", out[0].Get("score"), out[1].Get("score"))
	}
}

// TestIndependentStepsRunAtTheSameTime is the whole reason this is a graph.
func TestIndependentStepsRunAtTheSameTime(t *testing.T) {
	var (
		running  atomic.Int32
		together atomic.Bool
		wg       sync.WaitGroup
	)
	wg.Add(2)

	watcher := func(_ context.Context, cfg pipeline.Config) (pipeline.Step, error) {
		return pipeline.Func(func(_ context.Context, records []*record.Record) ([]*record.Record, error) {
			if running.Add(1) > 1 {
				together.Store(true)
			}
			// Each waits for the other, so a runner that ran them in sequence
			// would deadlock rather than merely be slow. A test that measured
			// time would be a test that fails on a busy machine.
			wg.Done()
			wg.Wait()
			running.Add(-1)
			return records, nil
		}), nil
	}
	register(t, "test-watch-one", watcher)
	register(t, "test-watch-two", watcher)

	j := job(t, `
  pipeline {
    step "clean" "article" {}

    step "test-watch-one" "left" {
      requires = [clean.article]
    }

    step "test-watch-two" "right" {
      requires = [clean.article]
    }
  }
`)

	p, err := pipeline.New(context.Background(), j)
	if err != nil {
		t.Fatal(err)
	}
	if p.Width() != 2 {
		t.Errorf("width = %d, want the two independent steps", p.Width())
	}
	if p.Waves() != 2 {
		t.Errorf("waves = %d", p.Waves())
	}

	done := make(chan error, 1)
	go func() {
		_, err := p.Run(context.Background(), []*record.Record{
			rec("https://example.com/a", map[string]string{"title": "One"}),
		})
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the two independent steps did not run at the same time")
	}

	if !together.Load() {
		t.Error("the steps never overlapped")
	}
}

// TestAStepWorksOnACopy, so the graph's order is not observable between steps
// that are supposed to be independent.
func TestAStepWorksOnACopy(t *testing.T) {
	register(t, "test-scribble", func(_ context.Context, cfg pipeline.Config) (pipeline.Step, error) {
		return pipeline.Func(func(_ context.Context, records []*record.Record) ([]*record.Record, error) {
			for _, r := range records {
				r.Values["title"] = "scribbled"
			}
			return records, nil
		}), nil
	})

	original := rec("https://example.com/a", map[string]string{"title": "Real"})

	run(t, job(t, `
  pipeline {
    step "clean" "article" {}

    step "test-scribble" "one" {
      requires = [clean.article]
    }

    step "test-scribble" "two" {
      requires = [clean.article]
    }
  }
`), original)

	if original.Get("title") != "Real" {
		t.Errorf("the record handed in was edited: %q", original.Get("title"))
	}
}

// TestAWaveKeepsWhatEveryStepKept, which makes two filters in one wave behave
// like the same two in sequence.
func TestAWaveKeepsWhatEveryStepKept(t *testing.T) {
	out := run(t, job(t, `
  pipeline {
    step "clean" "article" {}

    step "validate" "article" {
      requires = [clean.article]
    }

    step "dedupe" "article" {
      by       = ["title"]
      requires = [clean.article]
    }
  }
`),
		rec("https://example.com/a", map[string]string{"title": "One"}),
		rec("https://example.com/b", map[string]string{"title": "One"}),
		rec("https://example.com/c", map[string]string{"author": "no title"}))

	if len(out) != 1 {
		t.Errorf("kept %d, want the one record both steps kept: %v", len(out), out)
	}
}

// TestAStepOnlyTouchesItsOwnItem, so clean.article leaves a price alone.
func TestAStepOnlyTouchesItsOwnItem(t *testing.T) {
	price := &record.Record{
		Item: "price", URL: "https://example.com/p", Fetched: fetched,
		Values: map[string]string{"value": "  1.50  "},
	}

	out := run(t, job(t, `
  item "price" {
    property "value" {
      type = float
    }
  }

  pipeline {
    step "clean" "article" {}
  }
`), rec("https://example.com/a", map[string]string{"title": "  One  "}), price)

	var got string
	for _, r := range out {
		if r.Item == "price" {
			got = r.Get("value")
		}
	}
	if got != "  1.50  " {
		t.Errorf("the price was cleaned by a step named for the article: %q", got)
	}
}

// TestAKindNothingImplementsIsRefusedWhenBuilt, before the first record.
func TestAKindNothingImplementsIsRefusedWhenBuilt(t *testing.T) {
	_, err := pipeline.New(context.Background(), job(t, `
  pipeline {
    step "python" "enrich" {
      script = "./enrich.py"
    }
  }
`))
	if err == nil {
		t.Fatal("built a pipeline with a kind nothing implements")
	}
	if !strings.Contains(err.Error(), "python") || !strings.Contains(err.Error(), "clean") {
		t.Errorf("the error does not say what is missing or what there is: %v", err)
	}
}

// TestAStepThatWillNotBuildNamesItself.
func TestAStepThatWillNotBuildNamesItself(t *testing.T) {
	_, err := pipeline.New(context.Background(), job(t, `
  pipeline {
    step "rank" "article" {
      limit = -1
    }
  }
`))
	if err == nil {
		t.Fatal("accepted a limit that is not one")
	}
	if !strings.Contains(err.Error(), "rank.article") {
		t.Errorf("the error does not name the step: %v", err)
	}
}

// TestAFieldAStepDoesNotKnowIsRefused, with a position.
func TestAFieldAStepDoesNotKnowIsRefused(t *testing.T) {
	_, err := pipeline.New(context.Background(), job(t, `
  pipeline {
    step "dedupe" "article" {
      bye = ["title"]
    }
  }
`))
	if err == nil {
		t.Fatal("a typo was silently ignored")
	}
	if !strings.Contains(err.Error(), "job.hcl") {
		t.Errorf("the error has no position: %v", err)
	}
}

func TestAPipelineNeedsAJob(t *testing.T) {
	if _, err := pipeline.New(context.Background(), nil); err == nil {
		t.Error("built a pipeline for no job")
	}
}

func TestNoPipelineIsNotAnError(t *testing.T) {
	out := run(t, job(t, ""), rec("https://example.com/a", map[string]string{"title": "One"}))
	if len(out) != 1 {
		t.Errorf("a job with no pipeline changed its records")
	}
}

// TestACycleIsRefused, and the error names the steps stuck in it. Validation
// catches this first; this is the second line of defence for a job that
// arrived some other way.
func TestACycleIsRefused(t *testing.T) {
	doc, err := engine.Parse([]byte(`
job "news" {
  start = ["https://example.com/"]

  item "article" {
    property "title" {
      type = str
    }
  }

  pipeline {
    step "clean" "article" {
      requires = [dedupe.article]
    }

    step "dedupe" "article" {
      requires = [clean.article]
    }
  }
}
`), "job.hcl")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := pipeline.New(context.Background(), doc.Jobs[0]); err == nil {
		t.Error("built a pipeline with a cycle in it")
	}
}

// TestAStepThatFailsStopsTheRun, because a step that cannot do its job is not
// one to silently skip.
func TestAStepThatFailsStopsTheRun(t *testing.T) {
	boom := errors.New("the interpreter is not installed")
	register(t, "test-broken", func(_ context.Context, cfg pipeline.Config) (pipeline.Step, error) {
		return pipeline.Func(func(context.Context, []*record.Record) ([]*record.Record, error) {
			return nil, boom
		}), nil
	})

	p, err := pipeline.New(context.Background(), job(t, `
  pipeline {
    step "test-broken" "one" {}
  }
`))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := p.Run(context.Background(), nil); !errors.Is(err, boom) {
		t.Errorf("err = %v", err)
	}
}

func TestOrderAndRegistered(t *testing.T) {
	p, err := pipeline.New(context.Background(), job(t, `
  pipeline {
    step "clean" "article" {}

    step "validate" "article" {
      requires = [clean.article]
    }
  }
`))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(p.Order(), " "); got != "clean.article validate.article" {
		t.Errorf("Order() = %q", got)
	}

	if !pipeline.Has("clean") || pipeline.Has("carrier-pigeon") {
		t.Errorf("Registered() = %v", pipeline.Registered())
	}
}

// TestTwoTransformingStepsInOneWaveKeepBothTransformations.
//
// The regression test for a silent total data loss. `merge` used to identify a
// record by its values, and a step transforms values, so a step's output was a
// different record from its input. Two independent `clean` steps, which is the
// most natural pipeline anybody would write for a job with two items, produced
// four identities where there were two records, neither reached every step's
// output, and the wave returned nothing at all. The crawl exported zero records
// and reported that it had finished.
//
// Writing the same two steps with `requires` on the second worked, so the
// output depended on whether the author happened to serialise steps that did
// not need serialising.
func TestTwoTransformingStepsInOneWaveKeepBothTransformations(t *testing.T) {
	out := run(t, job(t, `
  item "price" {
    property "value" {
      type = float
    }
  }

  pipeline {
    step "clean" "article" {}
    step "clean" "price" {}
  }
`),
		rec("https://example.com/a", map[string]string{"title": "  One  "}),
		&record.Record{Item: "price", URL: "https://example.com/p", Fetched: fetched,
			Values: map[string]string{"value": "  1.50  "}})

	if len(out) != 2 {
		t.Fatalf("%d records survived a wave of two transforming steps, want both", len(out))
	}

	got := map[string]string{}
	for _, r := range out {
		got[r.Item] = r.Get("title") + r.Get("value")
	}
	// Each step's transformation survived, not just one of them.
	if got["article"] != "One" {
		t.Errorf("the article was not cleaned: %q", got["article"])
	}
	if got["price"] != "1.50" {
		t.Errorf("the price was not cleaned: %q", got["price"])
	}
}

// TestAFilterAndATransformInOneWaveBothApply, which is the mixed case the old
// identity could not express at all.
func TestAFilterAndATransformInOneWaveBothApply(t *testing.T) {
	out := run(t, job(t, `
  pipeline {
    step "clean" "article" {}

    step "validate" "article" {
      require = ["title"]
    }
  }
`),
		rec("https://example.com/a", map[string]string{"title": "  One  "}),
		rec("https://example.com/b", map[string]string{"author": "no title"}))

	if len(out) != 1 {
		t.Fatalf("kept %d records, want the one that passed the filter", len(out))
	}
	if out[0].Get("title") != "One" {
		t.Errorf("the transformation was lost when it ran beside a filter: %q", out[0].Get("title"))
	}
}

// TestTwoStepsEditingOneRecordResolveTheSameWayEveryRun.
//
// A job saying two contradictory things about one item is a job with a problem,
// and neither ordering is more correct than the other. What matters is that the
// answer is not the scheduler's whim: a crawl that produced different output on
// alternate runs would be impossible to debug.
func TestTwoStepsEditingOneRecordResolveTheSameWayEveryRun(t *testing.T) {
	register(t, "test-shout", func(_ context.Context, cfg pipeline.Config) (pipeline.Step, error) {
		return pipeline.Func(func(_ context.Context, records []*record.Record) ([]*record.Record, error) {
			for _, r := range records {
				if r.Item == "article" {
					r.Values["title"] = strings.ToUpper(r.Values["title"])
				}
			}
			return records, nil
		}), nil
	})
	register(t, "test-whisper", func(_ context.Context, cfg pipeline.Config) (pipeline.Step, error) {
		return pipeline.Func(func(_ context.Context, records []*record.Record) ([]*record.Record, error) {
			for _, r := range records {
				if r.Item == "article" {
					r.Values["title"] = strings.ToLower(r.Values["title"])
				}
			}
			return records, nil
		}), nil
	})

	j := job(t, `
  pipeline {
    step "test-shout" "one" {}
    step "test-whisper" "two" {}
  }
`)

	var first string
	for attempt := range 12 {
		out := run(t, j, rec("https://example.com/a", map[string]string{"title": "MiXeD"}))
		if len(out) != 1 {
			t.Fatalf("attempt %d kept %d records", attempt, len(out))
		}
		if attempt == 0 {
			first = out[0].Get("title")
			continue
		}
		if out[0].Get("title") != first {
			t.Fatalf("run %d produced %q where the first produced %q: the answer depends on which goroutine finished",
				attempt, out[0].Get("title"), first)
		}
	}
	// Wave order, so the earlier step wins.
	if first != "MIXED" {
		t.Errorf("title = %q, want the earlier step in wave order to have won", first)
	}
}

// TestARedirectDuplicateIsCollapsedRatherThanFatal.
//
// Two frontier URLs that redirect to one page are two entries and one fetched
// page, so the same item is extracted from it twice and two records with one
// identity reach the pipeline. That is a crawl doing its job, not a bug, and it
// used to abort the run and throw away every record the crawl had produced.
// They are the same page, so the first one stands.
func TestARedirectDuplicateIsCollapsedRatherThanFatal(t *testing.T) {
	p, err := pipeline.New(context.Background(), job(t, `
  pipeline {
    step "clean" "article" {}
    step "validate" "article" {}
  }
`))
	if err != nil {
		t.Fatal(err)
	}

	out, err := p.Run(context.Background(), []*record.Record{
		rec("https://example.com/a", map[string]string{"title": "One"}),
		rec("https://example.com/a", map[string]string{"title": "Two"}),
	})
	if err != nil {
		t.Fatalf("a page reached twice by two URLs killed the run: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("len(out) = %d, want the two to be one record", len(out))
	}
	if got := out[0].Values["title"]; got != "One" {
		t.Errorf("title = %q, want the first of the two", got)
	}
}

// TestAStepThatInventsADuplicateIsRefusedLoudly.
//
// Uniqueness after a wave is the pipeline's own invariant rather than the
// crawl's: a step that invented a second record for one item on one page leaves
// the merge guessing which of them a later step's output referred to, and an
// exporter two rows nobody can tell apart. Refusing beats guessing, which is the
// lesson of the bug the merge was rewritten for.
func TestAStepThatInventsADuplicateIsRefusedLoudly(t *testing.T) {
	register(t, "test-twin", func(_ context.Context, cfg pipeline.Config) (pipeline.Step, error) {
		return pipeline.Func(func(_ context.Context, records []*record.Record) ([]*record.Record, error) {
			out := make([]*record.Record, 0, len(records)*2)
			for _, r := range records {
				out = append(out, r, r.Clone())
			}
			return out, nil
		}), nil
	})

	p, err := pipeline.New(context.Background(), job(t, `
  pipeline {
    step "test-twin" "article" {}
  }
`))
	if err != nil {
		t.Fatal(err)
	}

	_, err = p.Run(context.Background(), []*record.Record{
		rec("https://example.com/a", map[string]string{"title": "One"}),
	})
	if err == nil {
		t.Fatal("a step that invented a duplicate was merged rather than refused")
	}
	if !strings.Contains(err.Error(), "identity") {
		t.Errorf("the error does not say what is wrong: %v", err)
	}
	if !strings.Contains(err.Error(), "test-twin") {
		t.Errorf("the error does not say which step: %v", err)
	}
}

// TestIdentityIsStableUnderTransformation, stated where it is relied on.
func TestIdentityIsStableUnderTransformation(t *testing.T) {
	before := rec("https://example.com/a", map[string]string{"title": "  One  "})

	after := before.Clone()
	after.Values["title"] = "One"

	if before.Identity() != after.Identity() {
		t.Error("cleaning a record changed which record it is")
	}
	other := rec("https://example.com/b", map[string]string{"title": "One"})
	if before.Identity() == other.Identity() {
		t.Error("two records from different pages share an identity")
	}
}

// TestRankKeepsItsOrderingWhenItSharesAWave.
//
// A step that reorders is a step whose entire output is its ordering, and the
// merge used to take the order from the wave's input unconditionally: every
// `rank` that shared a wave with anything was silently undone, and with a limit
// set it kept an arbitrary n records rather than the top n. Nothing failed and
// nothing was logged; the exported file was just in the wrong order.
func TestRankKeepsItsOrderingWhenItSharesAWave(t *testing.T) {
	p, err := pipeline.New(context.Background(), job(t, `
  pipeline {
    step "rank" "article" {
      by         = "score"
      descending = true
    }
    step "validate" "article" {}
  }
`))
	if err != nil {
		t.Fatal(err)
	}

	out, err := p.Run(context.Background(), []*record.Record{
		rec("https://example.com/a", map[string]string{"title": "A", "score": "2"}),
		rec("https://example.com/b", map[string]string{"title": "B", "score": "9"}),
		rec("https://example.com/c", map[string]string{"title": "C", "score": "5"}),
	})
	if err != nil {
		t.Fatal(err)
	}

	var got []string
	for _, r := range out {
		got = append(got, r.Values["score"])
	}
	if want := []string{"9", "5", "2"}; !slices.Equal(got, want) {
		t.Errorf("scores in order = %v, want %v: the rank was undone by its wave", got, want)
	}
}

// TestARecordAStepAddedSurvivesItsWave.
//
// The merge keeps a record only if every step kept it, which is what makes two
// filters in one wave behave like the same two in sequence. A record one step
// invented was held to that rule too, and the other steps of the wave had never
// seen it, so it was always dropped: the same step alone in its own wave kept
// it, and nothing said why.
func TestARecordAStepAddedSurvivesItsWave(t *testing.T) {
	register(t, "test-invent", func(_ context.Context, cfg pipeline.Config) (pipeline.Step, error) {
		return pipeline.Func(func(_ context.Context, records []*record.Record) ([]*record.Record, error) {
			extra := records[0].Clone()
			extra.URL = "https://example.com/invented"
			return append(slices.Clone(records), extra), nil
		}), nil
	})

	p, err := pipeline.New(context.Background(), job(t, `
  pipeline {
    step "test-invent" "article" {}
    step "validate" "article" {}
  }
`))
	if err != nil {
		t.Fatal(err)
	}

	out, err := p.Run(context.Background(), []*record.Record{
		rec("https://example.com/a", map[string]string{"title": "A"}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("len(out) = %d, want the invented record to have survived", len(out))
	}
	if out[1].URL != "https://example.com/invented" {
		t.Errorf("out[1].URL = %q, want the invented one", out[1].URL)
	}
}

// register puts a step kind in the global table for the length of one test.
//
// Every test that needs a step of its own goes through this rather than calling
// [pipeline.Register] directly, because the table is global and registering the
// same name twice panics: a test that registered without removing made this
// whole package impossible to run under `go test -count=2` or, once shuffling
// reordered it, under `-shuffle=on` either. Running the suite repeatedly is how
// a flaky test is found, so a package that cannot be is a package whose
// flakiness nobody will see. The gate runs -count=2 for that reason, which is
// what makes the next test that forgets fail the build rather than ship.
func register(t *testing.T, kind string, f func(context.Context, pipeline.Config) (pipeline.Step, error)) {
	t.Helper()
	pipeline.Register(kind, f)
	t.Cleanup(func() { pipeline.Unregister(kind) })
}

// TestMain fails the package if a test left a name in the global table. See
// [registrytest].
func TestMain(m *testing.M) { registrytest.Main(m, pipeline.Registered) }
