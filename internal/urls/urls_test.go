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

	// Pinned rather than compared with itself. Hash(u) != Hash(u) is false
	// whatever Hash does, so the assertion it looked like was never made: a
	// hash is stable if it is the same in the next process and the next
	// release, and only a written-down value says that.
	const want = "2dce0a4c50441bfccfa9caf4b58c3cba"
	if got := urls.Hash(u); got != want {
		t.Errorf("Hash(%q) = %q, want %q. A frontier keyed by this cannot be resumed if it moves", u, got, want)
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

// TestAnEncodedParameterNameSurvives.
//
// The regression test for silent data loss. ParseQuery unescapes names on the
// way in, and the rebuild looked them up escaped, so `filter%5Bcat%5D` matched
// neither `filter%255Bcat%255D` nor itself and the parameter was dropped. Two
// consequences, both bad and both silent: the crawler fetched a URL nobody had
// linked, and two pages differing only in that parameter collapsed to one hash
// so one of them was never fetched at all.
//
// `filter[x]=` is what a JavaScript client encodes, so this is the common case
// rather than an exotic one.
func TestAnEncodedParameterNameSurvives(t *testing.T) {
	for name, in := range map[string]string{
		"brackets":  "https://example.com/search?filter%5Bcat%5D=news&page=2",
		"a plus":    "https://example.com/search?a+b=1&c=2",
		"a percent": "https://example.com/search?name%20with%20spaces=1&c=2",
	} {
		t.Run(name, func(t *testing.T) {
			got, err := urls.Normalise(in, urls.Options{})
			if err != nil {
				t.Fatalf("normalise: %v", err)
			}
			if got != in {
				t.Errorf("got  %q\nwant %q, unchanged: the default must not drop a parameter", got, in)
			}
		})
	}
}

// TestTwoPagesDifferingOnlyInAnEncodedParameterAreTwoPages, which is the half
// of that bug a crawl would notice last.
func TestTwoPagesDifferingOnlyInAnEncodedParameterAreTwoPages(t *testing.T) {
	news, err := urls.Normalise("https://example.com/s?filter%5Bcat%5D=news", urls.Options{})
	if err != nil {
		t.Fatal(err)
	}
	sport, err := urls.Normalise("https://example.com/s?filter%5Bcat%5D=sport", urls.Options{})
	if err != nil {
		t.Fatal(err)
	}

	if urls.Hash(news) == urls.Hash(sport) {
		t.Errorf("%q and %q hashed the same, so one of them would never be fetched", news, sport)
	}
}

// TestNormalisingIsClosedOverItsOwnOutput.
//
// url.Parse accepts ":80" as a host: it validates the port and is content for
// the name to be empty. The default-port strip ran after the empty-host check,
// so "http://:80/a" became "http:///a" and was returned as a success - a URL
// nothing can fetch, queued with a real hash, and one that fails the very check
// it had just passed. The one function every URL in the crawler goes through
// was neither idempotent nor closed over its own output.
func TestNormalisingIsClosedOverItsOwnOutput(t *testing.T) {
	for _, raw := range []string{
		"http://:80/a",
		"https://:443/a",
		"http://:8080/a",
		"https://example.com//assets/a.png",
		"https://example.com//",
		"https://example.com/a/./b",
		"https://example.com:8080/a",
		"HTTPS://Example.COM/A",
	} {
		got, err := urls.Normalise(raw, urls.Options{})
		if err != nil {
			// Refusing is a fine answer; returning something unusable is not.
			continue
		}

		again, err := urls.Normalise(got, urls.Options{})
		if err != nil {
			t.Errorf("%q normalised to %q, which does not normalise: %v", raw, got, err)
			continue
		}
		if again != got {
			t.Errorf("%q is not idempotent: %q then %q", raw, got, again)
		}
	}
}

// TestAnEncodedSeparatorIsNotASeparator.
//
// u.Path is decoded, so %2F is an ordinary slash in it, and Go keeps u.RawPath
// only while it still unescapes to u.Path. Rewriting u.Path alone invalidated
// RawPath, and the URL was then rebuilt by escaping u.Path, which does not
// escape a slash: one path segment became two.
//
// Two genuinely different resources therefore produced one hash, and the
// dupefilter fetched one of them. The same shape as the encoded-bracket
// regression this package already pins, on the path rather than the query.
func TestAnEncodedSeparatorIsNotASeparator(t *testing.T) {
	for _, tc := range []struct{ encoded, plain string }{
		{"https://example.com/a/./b%2Fc", "https://example.com/a/b/c"},
		{"https://example.com/./x%2Fy", "https://example.com/x/y"},
	} {
		a, err := urls.Normalise(tc.encoded, urls.Options{})
		if err != nil {
			t.Fatalf("%q: %v", tc.encoded, err)
		}
		b, err := urls.Normalise(tc.plain, urls.Options{})
		if err != nil {
			t.Fatalf("%q: %v", tc.plain, err)
		}
		if a == b {
			t.Errorf("%q and %q both normalise to %q, so one of the two is never fetched",
				tc.encoded, tc.plain, a)
		}
		if urls.Hash(a) == urls.Hash(b) {
			t.Errorf("%q and %q hash the same", a, b)
		}
	}
}

// TestADoubledSlashIsPartOfThePath.
//
// Path rewrites run on the escaped form so an encoded separator survives them,
// and the escaped form was written back by parsing it as a URL reference.
// url.Parse reads a leading `//` as a scheme-relative authority, so
// `//assets/a.png` came back with a host of `assets` and a path of `/a.png`:
// https://example.com//assets/a.png became https://example.com/a.png, a
// different page and usually one that does not exist.
//
// It collided too - //a/p and //b/p both became /p, one hash, and the
// dupefilter dropped one of two real pages, which is the failure the escaped
// rewrite was introduced to prevent. A doubled slash is something a template
// produces by accident all the time.
func TestADoubledSlashIsPartOfThePath(t *testing.T) {
	// Asserted as the whole URL, not as "it still contains //" - which every
	// one of these passes on the `//` in `https://`, so the check was vacuous
	// for exactly the inputs it was added for. Resolving the dot segments went
	// on collapsing the empty one for another two passes underneath it.
	for raw, want := range map[string]string{
		"https://example.com//assets/a.png":   "https://example.com//assets/a.png",
		"https://example.com//a/./p":          "https://example.com//a/p",
		"https://example.com//assets/./a.png": "https://example.com//assets/a.png",
		"https://example.com//a/b/../c":       "https://example.com//a/c",
		"https://example.com/a/./p":           "https://example.com/a/p",
	} {
		got, err := urls.Normalise(raw, urls.Options{})
		if err != nil {
			t.Fatalf("%q: %v", raw, err)
		}
		if got != want {
			t.Errorf("%q normalised to %q, want %q", raw, got, want)
		}
	}

	// The collision the whole rewrite exists to prevent, reached by resolving
	// a dot segment rather than by parsing the path as a reference.
	dotted, err := urls.Normalise("https://example.com//a/./p", urls.Options{})
	if err != nil {
		t.Fatal(err)
	}
	plain, err := urls.Normalise("https://example.com/a/p", urls.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if dotted == plain {
		t.Errorf("//a/./p and /a/p both normalised to %q, so two pages share one hash", plain)
	}

	a, err := urls.Normalise("https://example.com//a/p", urls.Options{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := urls.Normalise("https://example.com//b/p", urls.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Errorf("two different pages both normalise to %q", a)
	}
}

// TestAHostThatIsOnlyAPortIsNoHost.
//
// url.Parse validates a port and is content for the name to be empty, and the
// default-port strip only fires for :80 and :443, so "http://:8080/a" passed
// the emptiness check with a host of ":8080". It is idempotent, so the
// round-trip check cannot see it, and it queues with a real hash: the
// downloader then fails it with "no Host in request URL" on every attempt until
// the frontier abandons it.
func TestAHostThatIsOnlyAPortIsNoHost(t *testing.T) {
	for _, raw := range []string{"http://:8080/a", "https://:8443/", "http://:80/a"} {
		if got, err := urls.Normalise(raw, urls.Options{}); err == nil {
			t.Errorf("%q was accepted as %q, and nothing can fetch it", raw, got)
		}
	}
}

// TestDotSegmentsFollowTheRFC, including the two examples RFC 3986 section
// 5.2.4 works through itself.
//
// Written out here because the implementation is written out too: it used to
// call path.Clean, which is a different algorithm wearing a similar name, and
// the ways the two differ are the ways a URL stops naming the resource it named.
func TestDotSegmentsFollowTheRFC(t *testing.T) {
	for in, want := range map[string]string{
		// The RFC's own worked examples.
		"/a/b/c/./../../g":    "/a/g",
		"/mid/content=5/../6": "/mid/6",

		// A trailing slash is not a dot segment and survives resolving one.
		"/a/./":    "/a/",
		"/a/b/../": "/a/",
		"/a/.":     "/a/",
		"/a/b/..":  "/a/",

		// An empty segment is a segment.
		"//a/./p":    "//a/p",
		"/a//./b":    "/a//b",
		"//a/b/../c": "//a/c",
		"/a//b/../c": "/a//c",

		// More `..` than there are segments walks up to the root and stops.
		"/../a":      "/a",
		"/a/../../b": "/b",

		// A `..` beside an empty segment. net/url and Python's urllib both
		// answer "/" here; Node and a literal reading of section 5.2.4 answer
		// "//": step 2C replaces the "/../" prefix, leaving "//", and step 2E
		// then moves the leading slash and everything up to the next one,
		// which for "//" is the slash by itself. Pinned by hand because the
		// implementations disagree and the RFC does not, and because an empty
		// segment being a segment is the whole reason this stopped calling
		// path.Clean. FuzzCleanMatchesTheStandardLibrary skips this shape for
		// the same reason.
		"/..//":    "//",
		"/a/..//b": "//b",
		"//..":     "/",

		// A relative path is made absolute, which is what every caller here
		// wants: this only ever sees the path of an absolute URL.
		"./a":  "/a",
		"../a": "/a",
	} {
		if got := urls.Clean(in); got != want {
			t.Errorf("Clean(%q) = %q, want %q", in, got, want)
		}
	}
}
