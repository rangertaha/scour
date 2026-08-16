// SPDX-License-Identifier: GPL-3.0-or-later

package spider_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/rangertaha/scour/internal/registry/registrytest"

	"github.com/rangertaha/scour/internal/chain"
	"github.com/rangertaha/scour/internal/downloader"
	"github.com/rangertaha/scour/internal/engine"
	"github.com/rangertaha/scour/internal/extract"
	"github.com/rangertaha/scour/internal/plugin"
	"github.com/rangertaha/scour/internal/spider"
	"github.com/rangertaha/scour/internal/urls"

	_ "github.com/rangertaha/scour/internal/spider/httperror"
)

const page = `<!doctype html>
<html>
<head>
  <meta property="og:title" content="Something happened">
  <meta property="article:published_time" content="2026-08-04T09:15:00Z">
</head>
<body>
  <article><p>The body of it.</p></article>
  <a href="/news/other.html">next</a>
  <a href="https://elsewhere.example/x">away</a>
  <a href="mailto:a@example.com">mail</a>
</body>
</html>`

func job(t *testing.T, blocks string) *engine.Job {
	t.Helper()

	src := `
job "news" {
  start = ["https://example.com/"]

  item "article" {
    property "title" {
      type = str
    }

    property "published_time" {
      type       = date
      transforms = [datetime]
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

func stage(t *testing.T, j *engine.Job) *spider.Stage {
	t.Helper()

	s, err := spider.New(context.Background(), j, spider.Options{})
	if err != nil {
		t.Fatalf("new spider: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	return s
}

func response(status int, body string, depth int) *downloader.Response {
	return &downloader.Response{
		Request: &downloader.Request{URL: "https://example.com/news/story.html", Depth: depth},
		URL:     "https://example.com/news/story.html",
		Status:  status,
		Header:  http.Header{"Content-Type": {"text/html; charset=utf-8"}},
		Body:    []byte(body),
	}
}

func TestAPageBecomesItemsAndLinks(t *testing.T) {
	out, err := stage(t, job(t, "")).Handle(context.Background(), response(200, page, 1))
	if err != nil {
		t.Fatalf("handle: %v", err)
	}

	if out.Found() != 1 {
		t.Fatalf("found %d items", out.Found())
	}
	article := out.Items[0]
	if got := article.Text("title"); got != "Something happened" {
		t.Errorf("title = %q", got)
	}
	if got := article.Text("published_time"); got != "2026-08-04T09:15:00Z" {
		t.Errorf("published = %q", got)
	}

	if len(out.Links) != 2 {
		t.Fatalf("links = %v", out.Links)
	}
	if out.Links[0].URL != "https://example.com/news/other.html" {
		t.Errorf("first link = %s", out.Links[0].URL)
	}
	if out.Links[1].URL != "https://elsewhere.example/x" {
		t.Errorf("second link = %s; scope is the scheduler's decision, not the spider's", out.Links[1].URL)
	}
}

// TestADiscoveredLinkIsOneDeeperAndKnowsItsParent, which is what makes a crawl
// path reconstructable and what max_depth is measured against.
func TestADiscoveredLinkIsOneDeeperAndKnowsItsParent(t *testing.T) {
	out, err := stage(t, job(t, "")).Handle(context.Background(), response(200, page, 3))
	if err != nil {
		t.Fatal(err)
	}

	for _, link := range out.Links {
		if link.Depth != 4 {
			t.Errorf("%s is at depth %d, want one deeper than the page", link.URL, link.Depth)
		}
		if link.Parent != "https://example.com/news/story.html" {
			t.Errorf("%s came from %q", link.URL, link.Parent)
		}
		if link.Hash == "" || link.Host == "" || link.Discovered.IsZero() {
			t.Errorf("%s is not ready for the frontier: %+v", link.URL, link)
		}
	}
}

// TestItemsSayWhichShapeTheyWereReadUnder. A record attributed to the wrong
// shape is wrong in a way nothing downstream can detect.
func TestItemsSayWhichShapeTheyWereReadUnder(t *testing.T) {
	s := stage(t, job(t, ""))

	out, err := s.Handle(context.Background(), response(200, page, 0))
	if err != nil {
		t.Fatal(err)
	}
	if out.Spec == "" {
		t.Fatal("the output does not say which shape it was read under")
	}
	if out.Spec != s.Fingerprint() || out.Spec != s.Spec().Fingerprint() {
		t.Error("the fingerprint disagrees with the spec it came from")
	}
}

// TestTheSpecIsWhatTravels, because a spider somebody else wrote is handed this
// as text and nothing else.
func TestTheSpecIsWhatTravels(t *testing.T) {
	s := stage(t, job(t, ""))

	rendered := s.Spec().HCL()
	back, err := engine.ParseSpec(rendered, "spec.hcl")
	if err != nil {
		t.Fatalf("the rendered spec does not parse: %v", err)
	}
	if back.Fingerprint() != s.Fingerprint() {
		t.Error("the spec did not survive the round trip")
	}
}

// TestHTTPErrorRefusesBeforeAnythingParses. Parsing an error page into an empty
// article is the failure that plugin exists to prevent.
func TestHTTPErrorRefusesBeforeAnythingParses(t *testing.T) {
	s := stage(t, job(t, `
  spider {
    plugin "httperror" {}
  }
`))

	_, err := s.Handle(context.Background(), response(404, page, 0))
	if !chain.Dropped(err) {
		t.Fatalf("err = %v, want a drop", err)
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("the error does not say what happened: %v", err)
	}

	if _, err := s.Handle(context.Background(), response(200, page, 0)); err != nil {
		t.Errorf("a good page was refused: %v", err)
	}
}

// TestAJobMayReadTheErrorPagesAnyway, which is what an archival crawl wants and
// what a crawl measuring rot wants exclusively.
func TestAJobMayReadTheErrorPagesAnyway(t *testing.T) {
	for name, blocks := range map[string]string{
		"listed": `
  spider {
    plugin "httperror" {
      allow = [404]
    }
  }
`,
		"all": `
  spider {
    plugin "httperror" {
      allow_all = true
    }
  }
`,
	} {
		t.Run(name, func(t *testing.T) {
			out, err := stage(t, job(t, blocks)).Handle(context.Background(), response(404, page, 0))
			if err != nil {
				t.Fatalf("a status the job allowed was refused: %v", err)
			}
			if out.Found() != 1 {
				t.Error("the page was not read")
			}
		})
	}
}

// TestWithoutThePluginEveryStatusIsRead, because dropping one is a decision and
// the default is to make no decisions the job did not ask for.
func TestWithoutThePluginEveryStatusIsRead(t *testing.T) {
	out, err := stage(t, job(t, "")).Handle(context.Background(), response(404, page, 0))
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	if out.Found() != 1 {
		t.Error("a 404 was silently dropped by a spider with no httperror plugin")
	}
}

// TestAMiddlewareSeesTheResultOnTheWayBack, which is what makes filtering
// discovered links possible at all.
func TestAMiddlewareSeesTheResultOnTheWayBack(t *testing.T) {
	register(t, "test-trim", func(_ context.Context, cfg plugin.Config) (spider.Wrapper, error) {
		return func(next spider.Handler) spider.Handler {
			return spider.HandlerFunc(func(ctx context.Context, resp *downloader.Response) (*spider.Output, error) {
				out, err := next.Handle(ctx, resp)
				if err != nil {
					return nil, err
				}
				kept := out.Links[:0]
				for _, link := range out.Links {
					if strings.Contains(link.URL, "example.com") {
						kept = append(kept, link)
					}
				}
				out.Links = kept
				return out, nil
			})
		}, nil
	})

	s := stage(t, job(t, `
  spider {
    plugin "test-trim" {
      order = 500
    }
  }
`))

	out, err := s.Handle(context.Background(), response(200, page, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Links) != 1 {
		t.Errorf("links = %v; the middleware could not filter them on the way back", out.Links)
	}
	if got := strings.Join(s.Middleware(), " "); got != "test-trim" {
		t.Errorf("Middleware() = %q", got)
	}
}

// TestABodyInAnotherEncodingIsStillRead, through the same function the
// downloader would have used. The cache holds what the server sent.
func TestABodyInAnotherEncodingIsStillRead(t *testing.T) {
	// "цена" in windows-1251, declared only in the Content-Type.
	body := append([]byte(`<html><head><meta property="og:title" content="`),
		0xf6, 0xe5, 0xed, 0xe0)
	body = append(body, []byte(`"></head><body></body></html>`)...)

	resp := &downloader.Response{
		Request: &downloader.Request{URL: "https://example.com/p"},
		URL:     "https://example.com/p",
		Status:  200,
		Header:  http.Header{"Content-Type": {"text/html; charset=windows-1251"}},
		Body:    body,
	}

	out, err := stage(t, job(t, "")).Handle(context.Background(), resp)
	if err != nil {
		t.Fatal(err)
	}
	if got := out.Items[0].Text("title"); got != "цена" {
		t.Errorf("title = %q", got)
	}
}

// TestLinksAreNormalisedTheWayTheSchedulerWillDedupeThem, or the spider reports
// one spelling and the frontier stores another.
func TestLinksAreNormalisedTheWayTheSchedulerWillDedupeThem(t *testing.T) {
	const body = `<html><body>
	  <a href="/a?utm_source=news">one</a>
	  <a href="/a">two</a>
	</body></html>`

	s, err := spider.New(context.Background(), job(t, ""), spider.Options{
		Canon: urls.Options{StripQuery: urls.Tracking},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	out, err := s.Handle(context.Background(), &downloader.Response{
		Request: &downloader.Request{URL: "https://example.com/p"},
		URL:     "https://example.com/p",
		Status:  200,
		Body:    []byte(body),
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(out.Links) != 2 {
		t.Fatalf("links = %v", out.Links)
	}
	if out.Links[0].Hash != out.Links[1].Hash {
		t.Errorf("two spellings of one page got two hashes: %s and %s", out.Links[0].URL, out.Links[1].URL)
	}
}

// TestATaughtLocatorThatStopsMatchingFallsBackAndSaysSo.
//
// Falling back to semantics keeps a crawl working when a site changes one
// class, which is worth having. What must not happen is it being invisible:
// the value says it was found by a guess, so `scour try` shows the selector is
// no longer doing the work.
func TestATaughtLocatorThatStopsMatchingFallsBackAndSaysSo(t *testing.T) {
	j := job(t, "")
	j.Items[0].Properties[0].CSS = []string{".nothing-matches-this"}

	s, err := spider.New(context.Background(), j, spider.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	out, err := s.Handle(context.Background(), response(200, page, 0))
	if err != nil {
		t.Fatal(err)
	}

	title, ok := out.Items[0].Get("title")
	if !ok {
		t.Fatal("the page stopped producing a title entirely")
	}
	if title.How != extract.BySemantics {
		t.Errorf("found by %s; a selector that matches nothing appeared to still work", title.How)
	}
}

// TestAPageThatIsNotTheShapeIsNotAnError. Most of what a crawl fetches is not
// what it was looking for.
func TestAPageThatIsNotTheShapeIsNotAnError(t *testing.T) {
	out, err := stage(t, job(t, "")).Handle(context.Background(),
		response(200, `<html><body><p>Nothing here.</p><a href="/a">on</a></body></html>`, 0))
	if err != nil {
		t.Fatalf("handle: %v", err)
	}
	if out.Found() != 0 {
		t.Errorf("found %d items on a page that has none", out.Found())
	}
	if len(out.Links) != 1 {
		t.Error("the links were lost with the items")
	}
	if !out.Complete() {
		t.Error("a page with no items reported itself incomplete")
	}
}

func TestASpiderNeedsAJobAndAResponse(t *testing.T) {
	ctx := context.Background()

	if _, err := spider.New(ctx, nil, spider.Options{}); err == nil {
		t.Error("built a spider for no job")
	}
	if _, err := stage(t, job(t, "")).Handle(ctx, nil); err == nil {
		t.Error("read a response that does not exist")
	}
}

// TestABrokenLocatorIsRefusedWhenTheSpiderIsBuilt.
func TestABrokenLocatorIsRefusedWhenTheSpiderIsBuilt(t *testing.T) {
	j := job(t, "")
	j.Items[0].Properties[0].CSS = []string{"div["}

	_, err := spider.New(context.Background(), j, spider.Options{})
	if err == nil {
		t.Fatal("built a spider with a selector that is not one")
	}
	if !strings.Contains(err.Error(), "news") {
		t.Errorf("the error does not say which job: %v", err)
	}
}

func TestRegisteredListsWhatThisBuildHas(t *testing.T) {
	if !spider.Has("httperror") {
		t.Errorf("Registered() = %v", spider.Registered())
	}
	if spider.Has("somebody-elses") {
		t.Error("Has reports something nothing implements")
	}
}

// TestAnIncompleteItemSaysSo, which is what --strict reads and what a job whose
// required property stopped matching needs somebody to notice.
func TestAnIncompleteItemSaysSo(t *testing.T) {
	j := job(t, "")
	j.Items[0].Properties = append(j.Items[0].Properties, &engine.Property{
		Name:     "price",
		Type:     "str",
		Required: true,
		CSS:      []string{".nothing-matches-this"},
	})

	s, err := spider.New(context.Background(), j, spider.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	out, err := s.Handle(context.Background(), response(200, page, 0))
	if err != nil {
		t.Fatal(err)
	}
	if out.Complete() {
		t.Error("an item missing a required property reported itself complete")
	}
}

// register puts a spider middleware in the global table for the length of one test.
//
// Every test that needs one of its own goes through this rather than calling
// [spider.Register] directly, because the table is global and registering the
// same name twice panics: a test that registered without removing made this
// whole package impossible to run under `go test -count=2` or, once shuffling
// reordered it, under `-shuffle=on` either. Running the suite repeatedly is how
// a flaky test is found, so a package that cannot be is a package whose
// flakiness nobody will see. The gate runs -count=2 for that reason, which is
// what makes the next test that forgets fail the build rather than ship.
func register(t *testing.T, name string, f spider.Middleware) {
	t.Helper()
	spider.Register(name, f)
	t.Cleanup(func() { spider.Unregister(name) })
}

// TestMain fails the package if a test left a name in the global table. See
// [registrytest].
func TestMain(m *testing.M) { registrytest.Main(m, spider.Registered) }
