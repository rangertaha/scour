// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rangertaha/scour/internal/mocksite"
)

// End to end against the mock site, through the command a person types.
//
// Everything here was true of the code before it was written down; none of it
// is a change. What it is, is the difference between behaviour that happens to
// hold and behaviour somebody would notice breaking. The unit tests under
// internal/ ask each stage what it does with a page. These ask what `scour crawl`
// does with a website, which is the only question a user has.

// job builds a document for the site with one property and whatever else a
// test needs.
func (w *website) job(t *testing.T, property, extra string) string {
	t.Helper()

	return document(t, fmt.Sprintf(`
job "news" {
  domains = ["%s"]
  start   = ["%s/"]

  item "article" {
    property "title" {
      type = str
%s
    }
  }

%s
}
`, w.host(), w.URL, property, extra))
}

// TestEveryVocabularyIsRead.
//
// A publisher says what a page is in whichever of three vocabularies their CMS
// emits, and a crawler that reads one of them finds a fraction of the web. All
// three are documented as ways a value is found, and until this existed none of
// them was checked through the command that uses them.
func TestEveryVocabularyIsRead(t *testing.T) {
	site := newWebsite(t, mocksite.Options{})
	path := site.job(t, `      aliases = ["headline"]`, `  scheduler {
    rate = "1ms"
  }`)

	for _, one := range []struct {
		page, want, from string
	}{
		{"/article/og", "An Open Graph story", "<meta og:title>"},
		{"/article/jsonld", "A JSON-LD story", "json-ld headline"},
		{"/article/microdata", "A microdata story", "itemprop"},

		// Unclosed tags, a stray close and an unquoted attribute. Every
		// crawler meets this and a parser that gave up would lose the page.
		{"/article/messy", "A messy story", "<meta og:title>"},
	} {
		t.Run(strings.TrimPrefix(one.page, "/article/"), func(t *testing.T) {
			out, errOut, code := run(t, "scrape", "--url", site.URL+one.page, path)
			if code != 0 {
				t.Fatalf("exit %d\n%s%s", code, out, errOut)
			}
			if !strings.Contains(out, one.want) {
				t.Errorf("did not find %q:\n%s", one.want, out)
			}
			if !strings.Contains(out, one.from) {
				t.Errorf("the provenance does not say %q, so nobody can tell where it came from:\n%s", one.from, out)
			}
		})
	}
}

// TestAPropertyIsOnlyFoundUnderNamesItAnswersTo.
//
// There is no built-in synonym table: a property answers to its own name and to
// the aliases the document gives it, and nothing else. That is a deliberate
// choice and an easy one to mistake for a bug, because schema.org calls a
// headline `headline` and most people call the property `title`.
//
// Both halves are here on purpose. The first is what somebody hits when their
// crawl finds nothing on a site whose markup is perfectly good, and the answer
// is a line in their document rather than a defect in the crawler.
func TestAPropertyIsOnlyFoundUnderNamesItAnswersTo(t *testing.T) {
	site := newWebsite(t, mocksite.Options{})
	rate := `  scheduler {
    rate = "1ms"
  }`

	bare := site.job(t, "", rate)
	out, _, code := run(t, "scrape", "--url", site.URL+"/article/jsonld", bare)
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if strings.Contains(out, "A JSON-LD story") {
		t.Errorf("`title` matched schema.org `headline` with no alias saying so:\n%s", out)
	}

	aliased := site.job(t, `      aliases = ["headline"]`, rate)
	out, _, code = run(t, "scrape", "--url", site.URL+"/article/jsonld", aliased)
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	if !strings.Contains(out, "A JSON-LD story") {
		t.Errorf("an alias of `headline` did not reach the JSON-LD:\n%s", out)
	}
}

// TestAnEncodingDeclaredOnlyInTheHeaderSurvives.
//
// A windows-1251 page that says so in Content-Type and nowhere else. This is
// the case the whole two-key cache entry exists for: lose the headers and the
// text decodes into mojibake, and nothing about the result looks wrong.
func TestAnEncodingDeclaredOnlyInTheHeaderSurvives(t *testing.T) {
	site := newWebsite(t, mocksite.Options{})
	path := site.job(t, "", `  scheduler {
    rate = "1ms"
  }`)

	out, errOut, code := run(t, "scrape", "--url", site.URL+"/article/cyrillic", path)
	if code != 0 {
		t.Fatalf("exit %d\n%s%s", code, out, errOut)
	}
	if !strings.Contains(out, "Привет") {
		t.Errorf("the page did not decode as windows-1251:\n%s", out)
	}
}

// TestRedirectsAreFollowedToWhereTheyLand, including a relative Location.
//
// A relative one is resolved against where the body came from rather than
// against what was asked for, which is what makes a chain of them land in the
// right place. The chain here goes absolute, then relative, then arrives.
func TestRedirectsAreFollowedToWhereTheyLand(t *testing.T) {
	site := newWebsite(t, mocksite.Options{})
	path := site.job(t, "", `  scheduler {
    rate = "1ms"
  }`)

	for _, one := range []struct{ from, want string }{
		{"/moved", "An Open Graph story"},
		{"/chain/1", "The end of the chain"},
	} {
		out, errOut, code := run(t, "scrape", "--url", site.URL+one.from, path)
		if code != 0 {
			t.Fatalf("%s: exit %d\n%s%s", one.from, code, out, errOut)
		}
		if !strings.Contains(out, one.want) {
			t.Errorf("%s did not land on %q:\n%s", one.from, one.want, out)
		}
	}

	if n := site.Asked("/chain/3"); n != 1 {
		t.Errorf("the relative hop was asked for %d times, want 1%s", n, site.asking())
	}
}

// TestARedirectLoopIsGivenUpOnAndSaysWhere.
//
// A page that redirects to itself is not rare and a crawler that followed it
// would never stop. Giving up is half the requirement; the other half is the
// message, because "too many redirects" without the trail leaves an operator
// with no idea which URL did it.
func TestARedirectLoopIsGivenUpOnAndSaysWhere(t *testing.T) {
	site := newWebsite(t, mocksite.Options{})
	path := site.job(t, "", `  scheduler {
    rate = "1ms"
  }`)

	out, errOut, code := run(t, "scrape", "--url", site.URL+"/loop", path)
	if code == 0 {
		t.Fatalf("a redirect loop was reported as a success:\n%s", out)
	}

	said := out + errOut
	if !strings.Contains(said, "too many redirects") {
		t.Errorf("the failure does not say what happened:\n%s", said)
	}
	if !strings.Contains(said, "/loop") {
		t.Errorf("the failure does not name the URL that looped:\n%s", said)
	}
}

// TestACrawlStaysWhereItIsAllowed.
//
// Two promises in one run, because they fail the same way: quietly, on
// somebody else's machine. robots.txt disallows /private/, and the index links
// to a page under it and to a page on another host. Neither may be fetched, and
// the site counts requests rather than refusing them so that asking is visible
// rather than hidden behind a status.
func TestACrawlStaysWhereItIsAllowed(t *testing.T) {
	site := newWebsite(t, mocksite.Options{})
	path := site.job(t, "", `  scheduler {
    rate = "1ms"
  }`)

	out, errOut, code := run(t, "crawl", path)
	if code != 0 {
		t.Fatalf("exit %d\n%s%s", code, out, errOut)
	}

	if n := site.Asked("/private/secret"); n != 0 {
		t.Errorf("robots.txt disallows /private/ and the crawl asked for it %d times%s", n, site.asking())
	}
	if n := site.Asked("/robots.txt"); n != 1 {
		t.Errorf("robots.txt was read %d times, want once for the host%s", n, site.asking())
	}
}

// TestMaxDepthStopsTheLadder, at the depth the document asked for.
//
// The site serves an endless ladder: every /deep/N links to /deep/N+1. A budget
// that did not hold would crawl until something else stopped it, which is the
// failure mode that costs somebody a bandwidth bill.
func TestMaxDepthStopsTheLadder(t *testing.T) {
	site := newWebsite(t, mocksite.Options{})
	path := site.job(t, "", `  scheduler {
    rate      = "1ms"
    max_depth = 2
  }`)

	if out, errOut, code := run(t, "crawl", path); code != 0 {
		t.Fatalf("exit %d\n%s%s", code, out, errOut)
	}

	// The index is depth 0, so /deep/1 and /deep/2 are within two and /deep/3
	// is not.
	for _, reachable := range []string{"/deep/1", "/deep/2"} {
		if site.Asked(reachable) == 0 {
			t.Errorf("%s is inside max_depth and was never fetched%s", reachable, site.asking())
		}
	}
	if n := site.Asked("/deep/3"); n != 0 {
		t.Errorf("max_depth = 2 and /deep/3 was fetched %d times%s", n, site.asking())
	}
}

// TestMaxPagesStopsAndLeavesTheRestQueued.
//
// A page budget is not a smaller crawl: it is a crawl that stopped, and what it
// had discovered stays for the next run. A budget that dropped the queue would
// make resuming pointless, and the summary has to say which of the two happened
// because a script cannot tell otherwise.
func TestMaxPagesStopsAndLeavesTheRestQueued(t *testing.T) {
	site := newWebsite(t, mocksite.Options{})
	path := site.job(t, "", `  scheduler {
    rate      = "1ms"
    max_pages = 3
  }`)

	out, errOut, code := run(t, "crawl", path)
	if code != 0 {
		t.Fatalf("exit %d\n%s%s", code, out, errOut)
	}

	if !strings.Contains(out, "budget spent") {
		t.Errorf("the summary does not say the budget stopped it:\n%s", out)
	}
	if !strings.Contains(out, "still waiting") {
		t.Errorf("the summary does not say what is left for the next run:\n%s", out)
	}
	if got := site.Total(); got != 3 {
		t.Errorf("fetched %d pages under a budget of 3%s", got, site.asking())
	}
}

// TestTheDupefilterCollapsesATrackedURL.
//
// The index links to one page twice, once with a tracking parameter. Without
// the plugin those are two pages, which is the conservative default; with it
// they are one. Both halves matter: a crawler that always stripped would merge
// pages that a site genuinely distinguishes by query string.
func TestTheDupefilterCollapsesATrackedURL(t *testing.T) {
	rate := `  scheduler {
    rate = "1ms"
  }`
	stripping := `  scheduler {
    rate = "1ms"

    plugin "dupefilter" {
      strip_tracking = true
    }
  }`

	plain := newWebsite(t, mocksite.Options{})
	if out, errOut, code := run(t, "crawl", plain.job(t, "", rate)); code != 0 {
		t.Fatalf("exit %d\n%s%s", code, out, errOut)
	}
	if n := plain.Asked("/article/og"); n != 2 {
		t.Errorf("without the dupefilter /article/og was asked %d times, want both spellings%s", n, plain.asking())
	}

	stripped := newWebsite(t, mocksite.Options{})
	if out, errOut, code := run(t, "crawl", stripped.job(t, "", stripping)); code != 0 {
		t.Fatalf("exit %d\n%s%s", code, out, errOut)
	}
	if n := stripped.Asked("/article/og"); n != 1 {
		t.Errorf("with strip_tracking the same page was asked %d times%s", n, stripped.asking())
	}
}

// TestPagesThatFailDoNotStopTheCrawl.
//
// The site serves a 404, a 500 and a body that is not HTML, all linked from the
// index. A crawl meets these constantly and has to come out the other side: the
// run finishes, the pages that worked are extracted, and nothing about the
// exit code suggests the operator should look into it.
func TestPagesThatFailDoNotStopTheCrawl(t *testing.T) {
	site := newWebsite(t, mocksite.Options{})
	path := site.job(t, "", `  scheduler {
    rate = "1ms"
  }`)

	out, errOut, code := run(t, "crawl", path)
	if code != 0 {
		t.Fatalf("exit %d\n%s%s", code, out, errOut)
	}
	if !strings.Contains(out, "finished") {
		t.Errorf("the crawl did not finish:\n%s", out)
	}

	for _, awkward := range []string{"/missing", "/boom", "/not-html"} {
		if site.Asked(awkward) == 0 {
			t.Errorf("%s was never fetched, so this proves nothing%s", awkward, site.asking())
		}
	}
	if !strings.Contains(out, "items") {
		t.Errorf("nothing was extracted despite the pages that worked:\n%s", out)
	}
}

// TestACrawlHonoursTheSitesCrawlDelay, through the command a person runs.
//
// `Crawl-delay` is read in the downloader and acted on in the scheduler, and
// for a long time nothing joined the two: robots.Rules.Delay had no caller
// anywhere, so a site asking to be crawled slowly was crawled at whatever the
// job's own rate said. Every layer under this has its own test and each one
// passed throughout.
//
// Bounded to three pages so that proving it costs two seconds rather than the
// whole site.
func TestACrawlHonoursTheSitesCrawlDelay(t *testing.T) {
	const delay = time.Second

	site := newWebsite(t, mocksite.Options{
		Robots: fmt.Sprintf("User-agent: *\nCrawl-delay: %d\n", int(delay.Seconds())),
	})

	// A rate far below what the site asked for, so a crawl honouring only the
	// job's own setting finishes in milliseconds and fails here.
	path := site.job(t, "", `  scheduler {
    rate      = "1ms"
    max_pages = 3
  }`)

	start := time.Now()
	out, errOut, code := run(t, "crawl", path)
	took := time.Since(start)
	if code != 0 {
		t.Fatalf("exit %d\n%s%s", code, out, errOut)
	}

	// Three pages, so two waits between them.
	if want := 2 * delay; took < want-200*time.Millisecond {
		t.Errorf("three pages took %s; the site asked for %s between requests, so it should have taken about %s\n%s",
			took, delay, want, out)
	}
}

// TestARedirectCannotCarryTheCrawlOutOfScope.
//
// A redirect target is the one URL a crawl fetches that neither the job nor a
// page the job chose to read picked out: the server on the other end picked it.
// The scheduler drops out-of-scope URLs before they are queued, and a redirect
// happens after queueing, inside the fetch, so nothing checked it at all. The
// scope package documents three stages that enforce it and only one imported it.
//
// Scope here is `excluded` rather than another host, and that is deliberate:
// two httptest servers both listen on 127.0.0.1, and a port is not part of a
// site's name, so to this crawler they are one site and always were. An
// exclusion is unambiguous, needs one server, and is the same rule.
func TestARedirectCannotCarryTheCrawlOutOfScope(t *testing.T) {
	var landed atomic.Int32
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprint(w, "User-agent: *\n")
		case "/":
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, `<html><head><meta property="og:title" content="Index"></head>
			  <body><a href="/leaves">away</a><a href="/forbidden/direct">direct</a></body></html>`)
		case "/leaves":
			http.Redirect(w, r, "/forbidden/landed", http.StatusFound)
		default:
			if strings.HasPrefix(r.URL.Path, "/forbidden/") {
				landed.Add(1)
			}
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, `<html><head><meta property="og:title" content="Forbidden"></head><body>x</body></html>`)
		}
	}))
	defer site.Close()

	path := document(t, fmt.Sprintf(`
job "news" {
  domains  = ["%s"]
  start    = ["%s/"]
  excluded = ["*/forbidden/*"]

  item "article" {
    property "title" {
      type = str
    }
  }

  scheduler {
    rate = "1ms"
  }
}
`, strings.TrimPrefix(site.URL, "http://"), site.URL))

	out, errOut, code := run(t, "crawl", path)
	if code != 0 {
		t.Fatalf("exit %d\n%s%s", code, out, errOut)
	}

	// Not once, by either route. The direct link is dropped by the scheduler,
	// which always worked; the redirect is dropped by the follower, which is
	// what this is about.
	if n := landed.Load(); n != 0 {
		t.Errorf("`excluded` names /forbidden/ and the crawl fetched it %d times;\n"+
			"a redirect can hand this crawler a URL its own job refused\n%s", n, out)
	}

	// And the crawl carried on: an out-of-scope redirect is an ordinary drop,
	// not a failure, because that is what the scheduler calls the same URL when
	// it arrives as a link.
	if !strings.Contains(out, "finished") {
		t.Errorf("the crawl did not finish:\n%s", out)
	}
}
