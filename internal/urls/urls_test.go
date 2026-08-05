// SPDX-License-Identifier: GPL-3.0-or-later

package urls_test

import (
	"strings"
	"testing"

	"github.com/rangertaha/scour/internal/urls"
)

// The default is conservative on purpose: only transformations that cannot
// change what a server returns. Every case here is one a server is not entitled
// to disagree with.
func TestWhatIsAlwaysSafe(t *testing.T) {
	for name, tc := range map[string]struct{ in, want string }{
		"scheme case":     {"HTTP://example.com/a", "http://example.com/a"},
		"host case":       {"http://Example.COM/a", "http://example.com/a"},
		"path case kept":  {"http://example.com/A", "http://example.com/A"},
		"fragment":        {"http://example.com/a#top", "http://example.com/a"},
		"default port":    {"http://example.com:80/a", "http://example.com/a"},
		"tls port":        {"https://example.com:443/a", "https://example.com/a"},
		"other port kept": {"http://example.com:8080/a", "http://example.com:8080/a"},
		"empty path":      {"http://example.com", "http://example.com/"},
		"dot segments":    {"http://example.com/a/./b/../c", "http://example.com/a/c"},
		"query kept":      {"http://example.com/a?b=2&a=1", "http://example.com/a?b=2&a=1"},
		"trailing slash":  {"http://example.com/a/", "http://example.com/a/"},
		"credentials":     {"http://user:pass@example.com/a", "http://example.com/a"},
		"spaces":          {"  http://example.com/a  ", "http://example.com/a"},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := urls.Normalise(tc.in, urls.Options{})
			if err != nil {
				t.Fatalf("normalise: %v", err)
			}
			if got != tc.want {
				t.Errorf("got  %q\nwant %q", got, tc.want)
			}
		})
	}
}

// TestCredentialsNeverSurvive is worth its own test. A URL that carries a
// password would put it in the frontier, in a log line and in a cache key.
func TestCredentialsNeverSurvive(t *testing.T) {
	got, err := urls.Normalise("https://alice:hunter2@example.com/secret?x=1", urls.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "hunter2") || strings.Contains(got, "alice") {
		t.Errorf("credentials survived normalisation: %s", got)
	}
}

// TestWhatAJobHasToAskFor: every one of these changes a URL a server is
// entitled to treat as different, so none of them happen unless told.
func TestWhatAJobHasToAskFor(t *testing.T) {
	const in = "http://example.com/A/?b=2&a=1&utm_source=news"

	if got, _ := urls.Normalise(in, urls.Options{}); got != in {
		t.Errorf("the default changed something: %s", got)
	}

	for name, tc := range map[string]struct {
		opts urls.Options
		want string
	}{
		"strip tracking": {urls.Options{StripQuery: urls.Tracking}, "http://example.com/A/?b=2&a=1"},
		"sort query":     {urls.Options{SortQuery: true}, "http://example.com/A/?a=1&b=2&utm_source=news"},
		"lower path":     {urls.Options{LowerPath: true}, "http://example.com/a/?b=2&a=1&utm_source=news"},
		"trailing slash": {urls.Options{StripTrailingSlash: true}, "http://example.com/A?b=2&a=1&utm_source=news"},
		"all of it":      {urls.Options{StripQuery: urls.Tracking, SortQuery: true, LowerPath: true, StripTrailingSlash: true}, "http://example.com/a?a=1&b=2"},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := urls.Normalise(in, tc.opts)
			if err != nil {
				t.Fatalf("normalise: %v", err)
			}
			if got != tc.want {
				t.Errorf("got  %q\nwant %q", got, tc.want)
			}
		})
	}
}

// TestStrippingEveryParameterLeavesNoQuery, rather than a dangling question
// mark that would make one page look like two.
func TestStrippingEveryParameterLeavesNoQuery(t *testing.T) {
	got, err := urls.Normalise("http://example.com/a?utm_source=x&fbclid=y",
		urls.Options{StripQuery: urls.Tracking})
	if err != nil {
		t.Fatal(err)
	}
	if got != "http://example.com/a" {
		t.Errorf("got %q", got)
	}
}

// TestSortingMakesRepeatedParametersOnePage, which is the case ordinary sorting
// by key alone would miss.
func TestSortingMakesRepeatedParametersOnePage(t *testing.T) {
	first, _ := urls.Normalise("http://example.com/a?x=2&x=1", urls.Options{SortQuery: true})
	second, _ := urls.Normalise("http://example.com/a?x=1&x=2", urls.Options{SortQuery: true})
	if first != second {
		t.Errorf("%q and %q are the same page", first, second)
	}
}

// TestAQueryThatWillNotParseIsLeftAlone: one this does not understand is still
// one the server might.
func TestAQueryThatWillNotParseIsLeftAlone(t *testing.T) {
	got, err := urls.Normalise("http://example.com/a?%zz=1", urls.Options{SortQuery: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "%zz=1") {
		t.Errorf("got %q", got)
	}
}

func TestWhatIsNotAURL(t *testing.T) {
	for name, in := range map[string]string{
		"relative":   "/just/a/path",
		"no host":    "http:///a",
		"not http":   "ftp://example.com/a",
		"javascript": "javascript:alert(1)",
		"mailto":     "mailto:somebody@example.com",
		"data":       "data:text/html,<h1>hi</h1>",
		"file":       "file:///etc/passwd",
		"nonsense":   "://not a url",
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := urls.Normalise(in, urls.Options{}); err == nil {
				t.Errorf("accepted %q as %q", in, got)
			}
		})
	}
}

// TestResolveIsHowASpiderSeesALink. Most links on the web are relative, and a
// crawler that could not resolve them would only ever see the page it started
// on.
func TestResolveIsHowASpiderSeesALink(t *testing.T) {
	const page = "https://example.com/news/2026/story.html"

	for name, tc := range map[string]struct{ href, want string }{
		"sibling":       {"other.html", "https://example.com/news/2026/other.html"},
		"rooted":        {"/index.html", "https://example.com/index.html"},
		"up":            {"../2025/old.html", "https://example.com/news/2025/old.html"},
		"absolute":      {"https://elsewhere.example/x", "https://elsewhere.example/x"},
		"protocol free": {"//cdn.example/x", "https://cdn.example/x"},
		"query only":    {"?page=2", "https://example.com/news/2026/story.html?page=2"},
		"fragment only": {"#top", "https://example.com/news/2026/story.html"},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := urls.Resolve(page, tc.href, urls.Options{})
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			if got != tc.want {
				t.Errorf("got  %q\nwant %q", got, tc.want)
			}
		})
	}
}

// TestResolveRefusesWhatCannotBeFetched, so a spider does not queue a mailto
// link and a scheduler never has to think about one.
func TestResolveRefusesWhatCannotBeFetched(t *testing.T) {
	for _, href := range []string{"javascript:void(0)", "mailto:a@example.com", "tel:+441234"} {
		if got, err := urls.Resolve("https://example.com/a", href, urls.Options{}); err == nil {
			t.Errorf("resolved %q to %q", href, got)
		}
	}
	if _, err := urls.Resolve("://not a url", "a.html", urls.Options{}); err == nil {
		t.Error("resolved against a base that is not a URL")
	}
	if _, err := urls.Resolve("https://example.com/", "http://[::1", urls.Options{}); err == nil {
		t.Error("resolved a link that is not a URL")
	}
}

func TestHashIsStableAndShort(t *testing.T) {
	const u = "https://example.com/a"

	if urls.Hash(u) != urls.Hash(u) {
		t.Error("the same URL hashed to two things")
	}
	if urls.Hash(u) == urls.Hash(u+"b") {
		t.Error("two URLs hashed to one thing")
	}
	if got := len(urls.Hash(u)); got != 32 {
		t.Errorf("hash is %d characters, want 32", got)
	}
}

func TestHostAndDomain(t *testing.T) {
	for u, want := range map[string]string{
		"https://news.example.com/a": "news.example.com",
		"https://example.com:8080/a": "example.com:8080",
		"not a url":                  "",
	} {
		if got := urls.Host(u); got != want {
			t.Errorf("Host(%q) = %q, want %q", u, got, want)
		}
	}

	for host, want := range map[string]string{
		"news.example.com": "example.com",
		"example.com":      "example.com",
		"a.b.c.example.co": "example.co",
		"example.com:8080": "example.com",
		"localhost":        "localhost",
	} {
		if got := urls.Domain(host); got != want {
			t.Errorf("Domain(%q) = %q, want %q", host, got, want)
		}
	}
}
