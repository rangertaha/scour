// SPDX-License-Identifier: GPL-3.0-or-later

package pipeline_test

import (
	"context"
	"errors"
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
	pipeline.Register("test-watch-one", watcher)
	pipeline.Register("test-watch-two", watcher)

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
	pipeline.Register("test-scribble", func(_ context.Context, cfg pipeline.Config) (pipeline.Step, error) {
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
	pipeline.Register("test-broken", func(_ context.Context, cfg pipeline.Config) (pipeline.Step, error) {
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
