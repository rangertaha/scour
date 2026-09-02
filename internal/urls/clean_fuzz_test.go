// SPDX-License-Identifier: GPL-3.0-or-later

package urls_test

import (
	"net/url"
	"strings"
	"testing"

	"github.com/rangertaha/scour/internal/urls"
)

// removeDotSegments is what the standard library does with the same path, used
// here as an independent oracle. The second result is false for a path this
// cannot be asked about.
//
// net/url applies RFC 3986's remove_dot_segments when it resolves a reference,
// and an absolute-path reference is taken as it is and then reduced - which is
// exactly the operation under test. Two implementations of one spec agreeing
// is worth more than either agreeing with the test author.
//
// The reference carries both the decoded and the raw path, and the input has
// to already be in escaped form. clean is called on the escaped path of a
// parsed URL and does not escape anything itself, so handing net/url a path
// with a literal space in it compares an escaper against something that is
// not one: it answers "/%20" for "/ " and neither is wrong.
//
// # Where this oracle does not apply
//
// A path with an empty segment in it. net/url collapses one when a dot
// segment is resolved beside it - it answers "/" for "/..//" - and so does
// Python's urllib. A literal reading of section 5.2.4 gives "//", and so does
// Node: step 2C replaces the "/../" prefix and step 2E then moves the leading
// slash and everything up to the next one, which for "//" is the slash by
// itself. Implementations disagree here and the RFC does not.
//
// clean follows the RFC, because an empty segment being a segment is the whole
// reason it stopped calling path.Clean: a crawl that reads //a/p and /a/p as
// one page keeps one of two real pages. So the fuzz skips these rather than
// asserting the oracle's answer, and TestDotSegmentsFollowTheRFC pins them by
// hand instead. Do not "fix" clean to agree with net/url here.
func removeDotSegments(path string) (string, bool) {
	base, err := url.Parse("http://x")
	if err != nil {
		return "", false
	}

	parsed, err := url.Parse("http://x" + path)
	if err != nil || parsed.EscapedPath() != path {
		return "", false
	}
	return base.ResolveReference(&url.URL{Path: parsed.Path, RawPath: parsed.RawPath}).EscapedPath(), true
}

// FuzzCleanMatchesTheStandardLibrary.
//
// clean is a hand-written transcription of RFC 3986 section 5.2.4, on the
// input that decides a page's identity: every URL in every crawl goes through
// it, and two paths it wrongly reduces to one string are two pages the
// dupefilter keeps one of. It replaced a call to path.Clean, which is a
// different algorithm wearing a similar name, and the ways the two differ took
// three passes to find - so the replacement is held to an implementation
// nobody here wrote.
//
// Only absolute paths are compared, because that is the whole of what clean
// ever sees: it is called on the escaped path of a parsed absolute URL.
// [FuzzCleanHoldsItsInvariants] covers the rest of the input space.
func FuzzCleanMatchesTheStandardLibrary(f *testing.F) {
	for _, seed := range []string{
		"/", "/a", "/a/", "/a/b/c/./../../g", "/mid/content=5/../6",
		"//a/./p", "/a//./b", "//a/b/../c", "/../a", "/a/../../b",
		"/a/.", "/a/b/..", "/a/./", "/./", "/..", "/../..", "////",
		"/a/%2Fb/../c", "/a/b/../../../..", "/.../a", "/a/..b/../c",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, path string) {
		if !strings.HasPrefix(path, "/") {
			return
		}
		// See removeDotSegments: the implementations disagree about empty
		// segments and the RFC does not, so those are pinned by hand.
		if strings.Contains(path, "//") {
			return
		}

		want, ok := removeDotSegments(path)
		if !ok {
			return
		}
		if got := urls.Clean(path); got != want {
			t.Errorf("Clean(%q) = %q, and RFC 3986 remove_dot_segments gives %q", path, got, want)
		}
	})
}

// FuzzCleanHoldsItsInvariants over every input, absolute or not.
//
// Three properties, each of which is a way the crawl breaks rather than a way
// a test is unhappy: it must terminate (this is a loop that rewrites its own
// input, so an input it does not shrink hangs a crawl on one URL), it must
// produce an absolute path (everything downstream joins it to a host), and it
// must be idempotent (normalising is applied to a URL that may already have
// been normalised, and a second pass that moves it means two spellings of one
// page).
func FuzzCleanHoldsItsInvariants(f *testing.F) {
	for _, seed := range []string{
		"", ".", "..", "./a", "../a", "a/./b", "a/../b", "...", "/.",
		"/a/./b/../c", "//", "/a//b", "%2e%2e/", "/a/b/./../../c/./d",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, path string) {
		once := urls.Clean(path)
		if !strings.HasPrefix(once, "/") {
			t.Fatalf("Clean(%q) = %q, which is not an absolute path", path, once)
		}
		if twice := urls.Clean(once); twice != once {
			t.Errorf("Clean(%q) = %q, and cleaning that again gives %q", path, once, twice)
		}
	})
}
