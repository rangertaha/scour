// SPDX-License-Identifier: GPL-3.0-or-later

package topic_test

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/hcl/v2/hclparse"

	"github.com/rangertaha/scour/internal/chain"
	"github.com/rangertaha/scour/internal/classify"
	"github.com/rangertaha/scour/internal/classify/store"
	"github.com/rangertaha/scour/internal/engine"
	"github.com/rangertaha/scour/internal/frontier"
	"github.com/rangertaha/scour/internal/frontier/sqlite"
	"github.com/rangertaha/scour/internal/plugin"
	"github.com/rangertaha/scour/internal/scheduler"
	"github.com/rangertaha/scour/internal/scheduler/topic"

	_ "github.com/rangertaha/scour/internal/classify/terms"
)

var origin = time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)

// The two URLs the whole package is about: one whose slug says what it is
// about, and one whose slug says it is about something else.
const (
	onTopic  = "https://example.com/climate/emissions-fall-again"
	offTopic = "https://example.com/sport/late-goal"
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

// topiced is a job whose scheduler runs this middleware over the given
// classifier, with whatever else the test wants to set.
func topiced(t *testing.T, dir, settings string) *engine.Job {
	t.Helper()

	return job(t, fmt.Sprintf(`
  scheduler {
    plugin "topic" {
      subject = "climate@7"
      dir     = %q
%s
    }
  }
`, dir, settings))
}

// stage builds a scheduler on a real SQLite frontier, because the two are only
// worth testing together: this middleware sets a number, and the frontier is
// the only thing that says what the number was for.
func stage(t *testing.T, j *engine.Job) *scheduler.Stage {
	t.Helper()

	s, err := scheduler.New(context.Background(), j,
		scheduler.Options{Dir: t.TempDir()},
		func(cfg frontier.Config) (frontier.Frontier, error) { return sqlite.Open(cfg) })
	if err != nil {
		t.Fatalf("new scheduler: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	return s
}

func submit(t *testing.T, s *scheduler.Stage, reqs ...*scheduler.Request) int {
	t.Helper()

	added, _, err := s.Submit(context.Background(), reqs...)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	return added
}

// urls builds the requests a spider produces for links it found on a page.
//
// With a parent, because that is what the spider sets on every link it reports
// and because it is what tells a discovered URL from a seed: a seed is never
// judged by a classifier, so a fixture with no parent would be testing the
// exemption rather than the scoring.
func urls(us ...string) []*scheduler.Request {
	reqs := make([]*scheduler.Request, 0, len(us))
	for _, u := range us {
		reqs = append(reqs, &scheduler.Request{
			URL:        u,
			Parent:     "https://example.com/",
			Discovered: origin,
		})
	}
	return reqs
}

// TestTheOnTopicURLIsFetchedFirst, which is the whole of what this middleware
// is for: the score it sets is what the ordering policy sorts by, so a crawl
// spends its budget on what is most likely to be worth it.
//
// The off-topic URL is offered first and discovered first, so nothing but the
// score can be putting the other one in front.
func TestTheOnTopicURLIsFetchedFirst(t *testing.T) {
	dir := trained(t, "climate", "emissions", "renewable")
	s := stage(t, topiced(t, dir, ""))
	ctx := context.Background()

	if n := submit(t, s, urls(offTopic, onTopic)...); n != 2 {
		t.Fatalf("queued %d of two", n)
	}

	first, err := s.Next(ctx, origin, time.Minute)
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	if first.URL != onTopic {
		t.Errorf("the frontier handed out %s first; the topic score did not order it", first.URL)
	}
	if err := s.Done(ctx, first.Hash, first.Attempt); err != nil {
		t.Fatalf("done: %v", err)
	}

	// A minute later, because one host is paced and the point here is the
	// order, not the politeness.
	second, err := s.Next(ctx, origin.Add(time.Minute), time.Minute)
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	if second.Score >= first.Score {
		t.Errorf("scored %s at %.3f and %s at %.3f; a slug about the subject did not score higher",
			first.URL, first.Score, second.URL, second.Score)
	}
}

// TestWithNoThresholdNothingIsDroppedButTheScoreIsStillSet, which is what a
// focused crawl usually wants: a slug is weak evidence, and ranking a bad guess
// last can be recovered from where discarding it cannot.
func TestWithNoThresholdNothingIsDroppedButTheScoreIsStillSet(t *testing.T) {
	dir := trained(t, "climate", "emissions")
	s := stage(t, topiced(t, dir, ""))
	ctx := context.Background()

	if n := submit(t, s, urls(offTopic, onTopic)...); n != 2 {
		t.Fatalf("a job with no threshold queued %d of two", n)
	}

	got, err := s.Next(ctx, origin, time.Minute)
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	if got.Score <= 0 {
		t.Errorf("%s came back scored %.3f; nothing was scored at all", got.URL, got.Score)
	}
}

// TestLeastRefusesWhatIsClearlyOffTopic, for a job confident enough in its
// classifier to spend nothing on the rest.
func TestLeastRefusesWhatIsClearlyOffTopic(t *testing.T) {
	dir := trained(t, "climate", "emissions", "renewable")
	s := stage(t, topiced(t, dir, `      least   = 0.3`))

	if n := submit(t, s, urls(onTopic)...); n != 1 {
		t.Errorf("an on-topic URL was refused")
	}
	if n := submit(t, s, urls(offTopic)...); n != 0 {
		t.Errorf("an off-topic URL was queued")
	}

	// And a refusal is a refusal rather than a failure, with the subject named,
	// because a score of 0.02 means nothing without saying against what.
	_, _, err := s.Submit(context.Background(), urls(offTopic)...)
	if err != nil {
		t.Fatalf("a dropped URL was reported as an error: %v", err)
	}
}

// config builds what the plugin loader hands this middleware, so that a test
// can reach the error a drop produces.
//
// The stage swallows drops, and rightly: refusing most of what a crawl finds is
// not a failure. That is exactly why the message is out of reach from Submit,
// and why this goes round it.
func config(t *testing.T, dir string, least float64) plugin.Config {
	t.Helper()

	src := fmt.Sprintf("subject = \"climate@7\"\ndir = %q\nleast = %v\n", dir, least)
	parsed, diags := hclparse.NewParser().ParseHCL([]byte(src), "plugin.hcl")
	if diags.HasErrors() {
		t.Fatalf("parse: %v", diags)
	}
	return plugin.Config{Name: topic.Name, Order: 450, Job: "news", Body: parsed.Body}
}

// TestADropSaysWhatItWasScoredAgainst, because a score of 0.02 means nothing
// without saying against what, and by which training of it.
func TestADropSaysWhatItWasScoredAgainst(t *testing.T) {
	dir := trained(t, "climate", "emissions", "renewable")

	built, err := topic.New(context.Background(), config(t, dir, 0.3))
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	handler := built(scheduler.HandlerFunc(
		func(_ context.Context, req *scheduler.Request) (*scheduler.Request, error) { return req, nil }))

	// With a parent, because a seed is exempt: what is under test here is what
	// a drop says, and only a discovered URL can be dropped.
	_, err = handler.Handle(context.Background(), &scheduler.Request{
		URL:    offTopic,
		Parent: "https://example.com/",
	})
	if !chain.Dropped(err) {
		t.Fatalf("err = %v, want the off-topic URL dropped", err)
	}
	if !strings.Contains(err.Error(), "climate@7") {
		t.Errorf("the drop does not say what it was scored against: %v", err)
	}
}

// TestAScoreSomethingElseSetIsNotClobbered. The rule is the larger of the two,
// so this can promote a URL and never demote one another scorer deliberately
// promoted, and so two scorers sharing an order give the same answer whichever
// runs last.
func TestAScoreSomethingElseSetIsNotClobbered(t *testing.T) {
	dir := trained(t, "climate", "emissions", "renewable")
	s := stage(t, topiced(t, dir, ""))
	ctx := context.Background()

	// An off-topic slug that something upstream was sure about. The topic
	// score is near zero, and it must not pull the request down.
	//
	// With a parent, which is what makes this test about anything: a request
	// without one is returned before it is ever scored - a start URL has no
	// slug to read and no parent to borrow words from - so the max this pins
	// was never reached, and changing it to a plain assignment left the test
	// passing. Every other test in this file goes through the urls helper,
	// which sets a parent for exactly this reason.
	if _, _, err := s.Submit(ctx, &scheduler.Request{
		URL:        offTopic,
		Parent:     "https://example.com/",
		Score:      0.9,
		Discovered: origin,
	}); err != nil {
		t.Fatal(err)
	}

	got, err := s.Next(ctx, origin, time.Minute)
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	if diff := got.Score - 0.9; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("score = %.3f, want the 0.9 something else had already set", got.Score)
	}
}

// TestWeightScalesWhatTheSubjectContributes, so a job can blend a subject
// against whatever else scores a request.
func TestWeightScalesWhatTheSubjectContributes(t *testing.T) {
	dir := trained(t, "climate", "emissions", "renewable")
	ctx := context.Background()

	score := func(settings string) float64 {
		s := stage(t, topiced(t, dir, settings))
		if n := submit(t, s, urls(onTopic)...); n != 1 {
			t.Fatalf("queued %d", n)
		}
		got, err := s.Next(ctx, origin, time.Minute)
		if err != nil {
			t.Fatalf("next: %v", err)
		}
		return got.Score
	}

	full := score("")
	half := score(`      weight  = 0.5`)

	if full <= 0 {
		t.Fatalf("an on-topic URL scored %.3f at full weight", full)
	}
	if diff := half - full/2; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("weight 0.5 scored %.4f, want half of %.4f", half, full)
	}
}

// TestAVersionIsRequired. A job whose behaviour changed when somebody
// retrained, with nothing in the document to show why, is the trap.
func TestAVersionIsRequired(t *testing.T) {
	dir := trained(t, "climate")

	_, err := scheduler.New(context.Background(), job(t, fmt.Sprintf(`
  scheduler {
    plugin "topic" {
      subject = "climate"
      dir     = %q
    }
  }
`, dir)), scheduler.Options{Dir: t.TempDir()},
		func(cfg frontier.Config) (frontier.Frontier, error) { return sqlite.Open(cfg) })
	if err == nil {
		t.Fatal("accepted a classifier reference with no version")
	}
	if !strings.Contains(err.Error(), "version") {
		t.Errorf("the error does not say what is missing: %v", err)
	}
}

// TestAClassifierNobodyTrainedIsRefusedWhenTheChainIsBuilt, not on the first
// URL of a run.
func TestAClassifierNobodyTrainedIsRefusedWhenTheChainIsBuilt(t *testing.T) {
	dir := trained(t, "climate")

	_, err := scheduler.New(context.Background(), job(t, fmt.Sprintf(`
  scheduler {
    plugin "topic" {
      subject = "insolvency@1"
      dir     = %q
    }
  }
`, dir)), scheduler.Options{Dir: t.TempDir()},
		func(cfg frontier.Config) (frontier.Frontier, error) { return sqlite.Open(cfg) })
	if err == nil {
		t.Fatal("built a chain around a classifier nobody has trained")
	}
	if !strings.Contains(err.Error(), "insolvency@1") {
		t.Errorf("the error does not name it: %v", err)
	}
}

// TestAThresholdIsAScore, and a weight is not a punishment.
func TestAThresholdIsAScore(t *testing.T) {
	dir := trained(t, "climate")

	for _, settings := range []string{
		`      least   = -0.5`,
		`      least   = 1.5`,
		`      weight  = -1`,
	} {
		_, err := scheduler.New(context.Background(), topiced(t, dir, settings),
			scheduler.Options{Dir: t.TempDir()},
			func(cfg frontier.Config) (frontier.Frontier, error) { return sqlite.Open(cfg) })
		if err == nil {
			t.Errorf("accepted %s", strings.TrimSpace(settings))
		}
	}
}

// TestTheMiddlewareIsInTheChain, without a job having to say where.
func TestTheMiddlewareIsInTheChain(t *testing.T) {
	dir := trained(t, "climate")
	s := stage(t, topiced(t, dir, ""))

	if got := strings.Join(s.Middleware(), " "); got != "topic" {
		t.Errorf("Middleware() = %q", got)
	}
}

// TestAURLIsScoredOnItsSlug, which is the only evidence there is before
// anything has been fetched.
func TestAURLIsScoredOnItsSlug(t *testing.T) {
	for _, c := range []struct {
		why    string
		raw    string
		parent string
		want   string
	}{
		{
			why:  "a slug is words, however it punctuates them",
			raw:  "https://example.com/climate/emissions-fall_again.html",
			want: "climate emissions fall again html",
		},
		{
			why:  "the scheme and host say the same thing about every URL on a site",
			raw:  "https://news.example.com/sport/late-goal",
			want: "sport late goal",
		},
		{
			why:  "a query can carry the subject too",
			raw:  "https://example.com/search?q=renewable+energy&page=2",
			want: "search renewable energy page",
		},
		{
			why:  "a fragment never reaches the server, so it describes nothing fetchable",
			raw:  "https://example.com/climate#comments",
			want: "climate",
		},
		{
			why:    "the page a link was found on is evidence about the link",
			raw:    "https://example.com/2026/08/story",
			parent: "https://example.com/climate/index",
			want:   "2026 08 story climate index",
		},
		{
			why:  "a start URL was found on nothing",
			raw:  "https://example.com/",
			want: "",
		},
	} {
		t.Run(c.why, func(t *testing.T) {
			if got := topic.Text(c.raw, c.parent); got != c.want {
				t.Errorf("Text(%q, %q) = %q, want %q", c.raw, c.parent, got, c.want)
			}
		})
	}
}

// TestASeedIsNeverJudged.
//
// A start URL is usually a bare host, so there is no slug to read and no parent
// page to borrow words from: the text scored is the empty string, which every
// scorer answers zero for. A job with `least` above zero therefore dropped its
// own seed - and a drop is not an error, so Seed reported nothing queued, the
// frontier was empty, and the run finished having fetched no pages with nothing
// anywhere saying why.
//
// Nobody linked to a seed. The operator wrote it down, which is a stronger
// statement about what the crawl is for than anything a classifier can infer
// from a URL with no words in it.
func TestASeedIsNeverJudged(t *testing.T) {
	dir := trained(t, "climate", "emissions", "renewable")

	built, err := topic.New(context.Background(), config(t, dir, 0.3))
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	handler := built(scheduler.HandlerFunc(
		func(_ context.Context, req *scheduler.Request) (*scheduler.Request, error) { return req, nil }))

	for _, seed := range []string{"https://example.com/", "https://example.com"} {
		out, err := handler.Handle(context.Background(), &scheduler.Request{URL: seed})
		if err != nil {
			t.Errorf("a start URL was dropped by the classifier: %v", err)
			continue
		}
		if out == nil {
			t.Errorf("%q came back as nothing", seed)
		}
	}

	// And a URL somebody's page linked to is still judged.
	if _, err := handler.Handle(context.Background(), &scheduler.Request{
		URL:    offTopic,
		Parent: "https://example.com/",
	}); !chain.Dropped(err) {
		t.Errorf("an off-topic discovered URL was not dropped: %v", err)
	}
}
