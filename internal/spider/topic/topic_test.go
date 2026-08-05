// SPDX-License-Identifier: GPL-3.0-or-later

package topic_test

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/rangertaha/scour/internal/chain"
	"github.com/rangertaha/scour/internal/classify"
	"github.com/rangertaha/scour/internal/classify/store"
	"github.com/rangertaha/scour/internal/downloader"
	"github.com/rangertaha/scour/internal/engine"
	"github.com/rangertaha/scour/internal/spider"
	"github.com/rangertaha/scour/internal/spider/topic"

	_ "github.com/rangertaha/scour/internal/classify/terms"
)

// trained puts a classifier where a job can reach it.
func trained(t *testing.T, terms ...string) string {
	t.Helper()

	dir := filepath.Join(t.TempDir(), "topics")
	classifiers, err := store.Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := classifiers.Put("terms", classify.Config{
		Name:    "climate",
		Version: 7,
		Terms:   terms,
	}); err != nil {
		t.Fatalf("put: %v", err)
	}
	return dir
}

func job(t *testing.T, blocks string) *engine.Job {
	t.Helper()

	src := `
job "news" {
  start = ["https://example.com/"]

  item "article" {
    property "title" {
      type = str
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

func page(body string) *downloader.Response {
	return &downloader.Response{
		Request: &downloader.Request{URL: "https://example.com/a"},
		URL:     "https://example.com/a",
		Status:  200,
		Header:  http.Header{"Content-Type": {"text/html; charset=utf-8"}},
		Body:    []byte(body),
	}
}

const onTopic = `<html><head><meta property="og:title" content="Emissions fall"></head>
<body><article><p>Carbon emissions and climate policy and renewable energy.
Emissions again, and climate, and more emissions.</p></article></body></html>`

const offTopic = `<html><head><meta property="og:title" content="A football match"></head>
<body><article><p>The match was decided by a late goal from the midfielder.</p></article></body></html>`

func stage(t *testing.T, dir string, blocks string) *spider.Stage {
	t.Helper()

	s, err := spider.New(context.Background(), job(t, blocks), spider.Options{})
	if err != nil {
		t.Fatalf("new spider: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// TestAnOffTopicPageIsDropped, which is the whole point of a focused crawl.
func TestAnOffTopicPageIsDropped(t *testing.T) {
	dir := trained(t, "climate", "emissions", "renewable")

	s := stage(t, dir, fmt.Sprintf(`
  spider {
    plugin "topic" {
      subject = "climate@7"
      least   = 0.3
      dir     = %q
    }
  }
`, dir))

	if _, err := s.Handle(context.Background(), page(onTopic)); err != nil {
		t.Errorf("an on-topic page was dropped: %v", err)
	}

	_, err := s.Handle(context.Background(), page(offTopic))
	if !chain.Dropped(err) {
		t.Fatalf("err = %v, want the off-topic page dropped", err)
	}
	if !strings.Contains(err.Error(), "climate@7") {
		t.Errorf("the drop does not say what it was scored against: %v", err)
	}
}

// TestTheScoreIsRecordedOnEveryItem, so somebody who disagrees with the
// threshold can filter the records rather than re-crawl.
func TestTheScoreIsRecordedOnEveryItem(t *testing.T) {
	dir := trained(t, "climate", "emissions", "renewable")

	s := stage(t, dir, fmt.Sprintf(`
  spider {
    plugin "topic" {
      subject = "climate@7"
      dir     = %q
    }
  }
`, dir))

	out, err := s.Handle(context.Background(), page(onTopic))
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Items) == 0 {
		t.Fatal("nothing was extracted")
	}

	got, ok := out.Items[0].Get(topic.Property)
	if !ok {
		t.Fatalf("no score on the item: %v", out.Items[0].Values)
	}
	score, err := strconv.ParseFloat(got.Text, 64)
	if err != nil {
		t.Fatalf("the score is not a number: %q", got.Text)
	}
	if score <= 0 || score > 1 {
		t.Errorf("score = %v, and a score is between 0 and 1", score)
	}
	// Which classifier produced it, because 0.71 is meaningless without
	// knowing what was being scored and by which training of it.
	if got.From != "climate@7" {
		t.Errorf("the score does not say what produced it: %q", got.From)
	}
}

// TestWithNoThresholdNothingIsDropped, which is what a job measuring a
// classifier before trusting it wants.
func TestWithNoThresholdNothingIsDropped(t *testing.T) {
	dir := trained(t, "climate", "emissions")

	s := stage(t, dir, fmt.Sprintf(`
  spider {
    plugin "topic" {
      subject = "climate@7"
      dir     = %q
    }
  }
`, dir))

	out, err := s.Handle(context.Background(), page(offTopic))
	if err != nil {
		t.Fatalf("a page was dropped by a job with no threshold: %v", err)
	}
	if _, ok := out.Items[0].Get(topic.Property); !ok {
		t.Error("the score was not recorded")
	}
}

// TestAVersionIsRequired. A job whose behaviour changed when somebody
// retrained, with nothing in the document to show why, is the trap.
func TestAVersionIsRequired(t *testing.T) {
	dir := trained(t, "climate")

	_, err := spider.New(context.Background(), job(t, fmt.Sprintf(`
  spider {
    plugin "topic" {
      subject = "climate"
      dir     = %q
    }
  }
`, dir)), spider.Options{})
	if err == nil {
		t.Fatal("accepted a classifier reference with no version")
	}
	if !strings.Contains(err.Error(), "version") {
		t.Errorf("the error does not say what is missing: %v", err)
	}
}

// TestAClassifierNobodyTrainedIsRefusedWhenTheChainIsBuilt, not on the first
// page of a run.
func TestAClassifierNobodyTrainedIsRefusedWhenTheChainIsBuilt(t *testing.T) {
	dir := trained(t, "climate")

	_, err := spider.New(context.Background(), job(t, fmt.Sprintf(`
  spider {
    plugin "topic" {
      subject = "insolvency@1"
      dir     = %q
    }
  }
`, dir)), spider.Options{})
	if err == nil {
		t.Fatal("built a chain around a classifier nobody has trained")
	}
	if !strings.Contains(err.Error(), "insolvency@1") {
		t.Errorf("the error does not name it: %v", err)
	}
}

func TestAThresholdIsAScore(t *testing.T) {
	dir := trained(t, "climate")

	for _, least := range []string{"-0.5", "1.5"} {
		_, err := spider.New(context.Background(), job(t, fmt.Sprintf(`
  spider {
    plugin "topic" {
      subject = "climate@7"
      least   = %s
      dir     = %q
    }
  }
`, least, dir)), spider.Options{})
		if err == nil {
			t.Errorf("accepted a threshold of %s", least)
		}
	}
}

// TestANodeRunningNoTopicedJobsLoadsNothing. That is why this is a plugin: most
// crawls do not want it, and the ones that do should not make everybody else
// carry it.
func TestANodeRunningNoTopicedJobsLoadsNothing(t *testing.T) {
	s, err := spider.New(context.Background(), job(t, ""), spider.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if len(s.Middleware()) != 0 {
		t.Errorf("a job with no topic block built %v", s.Middleware())
	}
	if _, err := s.Handle(context.Background(), page(offTopic)); err != nil {
		t.Errorf("a page was scored by a job that asked for no scoring: %v", err)
	}
}
