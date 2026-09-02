// SPDX-License-Identifier: GPL-3.0-or-later

package scope_test

import (
	"testing"

	"github.com/rangertaha/scour/internal/scope"
	"github.com/rangertaha/scour/internal/urls"
)

func build(t *testing.T, domains, included, excluded []string) *scope.Scope {
	t.Helper()

	s, err := scope.New(domains, included, excluded, urls.Options{})
	if err != nil {
		t.Fatalf("scope: %v", err)
	}
	return s
}

// TestDomainsAreSitesAndSubdomains, which is what anybody writing one means.
func TestDomainsAreSitesAndSubdomains(t *testing.T) {
	s := build(t, []string{"example.com"}, nil, nil)

	for url, want := range map[string]bool{
		"https://example.com/a":           true,
		"https://news.example.com/a":      true,
		"https://a.b.example.com/a":       true,
		"https://example.com:8080/a":      true,
		"https://elsewhere.example/a":     false,
		"https://notexample.com/a":        false,
		"https://example.com.evil.test/a": false,
	} {
		if got := s.Allows(url); got != want {
			t.Errorf("Allows(%q) = %v, want %v", url, got, want)
		}
	}
}

// TestASubdomainSuffixIsNotASubdomain is the one that matters: a check written
// with HasSuffix and no dot lets example.com.evil.test through, which is a way
// to make a crawler fetch anything at all.
func TestASubdomainSuffixIsNotASubdomain(t *testing.T) {
	s := build(t, []string{"example.com"}, nil, nil)

	for _, url := range []string{
		"https://example.com.evil.test/a",
		"https://evilexample.com/a",
		"https://myexample.com/a",
	} {
		if s.Allows(url) {
			t.Errorf("a host that merely looks like the domain was allowed: %s", url)
		}
	}
}

// TestAStarredDomainIsTheSameThing, because a job that writes "*.example.com"
// means the site and reading it any other way would surprise them.
func TestAStarredDomainIsTheSameThing(t *testing.T) {
	s := build(t, []string{"*.example.com"}, nil, nil)

	if !s.Allows("https://example.com/a") {
		t.Error("the domain itself was excluded by its own wildcard")
	}
	if !s.Allows("https://news.example.com/a") {
		t.Error("a subdomain was excluded")
	}
}

func TestIncludedNarrows(t *testing.T) {
	s := build(t, []string{"example.com"}, []string{"*/news/*"}, nil)

	if !s.Allows("https://example.com/news/2026/story") {
		t.Error("an included URL was refused")
	}
	if s.Allows("https://example.com/about") {
		t.Error("a URL matching nothing included was allowed")
	}
}

// TestExcludedWinsOverEverything. A job that has said never means never,
// whatever else it also said.
func TestExcludedWinsOverEverything(t *testing.T) {
	s := build(t, []string{"example.com"}, []string{"*"}, []string{"*/print/*"})

	if !s.Allows("https://example.com/news/story") {
		t.Error("an ordinary URL was refused")
	}
	if s.Allows("https://example.com/print/story") {
		t.Error("an excluded URL was allowed by an inclusion")
	}
}

// TestAPatternWithNoWildcardIsAPrefix, because "excluded = [that subtree]" is
// how anybody writes it.
func TestAPatternWithNoWildcardIsAPrefix(t *testing.T) {
	s := build(t, nil, nil, []string{"https://example.com/print"})

	if s.Allows("https://example.com/print/anything") {
		t.Error("the subtree was not excluded")
	}
	if !s.Allows("https://example.com/news") {
		t.Error("something outside the subtree was excluded")
	}
}

// TestPatternsMatchTheHostToo, so "*.example.com" reads the way it is written
// even though a URL starts with a scheme.
func TestPatternsMatchTheHostToo(t *testing.T) {
	s := build(t, nil, []string{"*.example.com"}, nil)

	if !s.Allows("https://news.example.com/deep/page") {
		t.Error("a host pattern did not match a URL on that host")
	}
	if s.Allows("https://elsewhere.example/a") {
		t.Error("a host pattern matched another host")
	}
}

// TestNoScopeAllowsEverything, which is what `scour scrape` on one URL needs: it
// has nothing to be outside of.
func TestNoScopeAllowsEverything(t *testing.T) {
	s := build(t, nil, nil, nil)
	if !s.Allows("https://anywhere.example/a") {
		t.Error("an empty scope refused something")
	}

	var none *scope.Scope
	if !none.Allows("https://anywhere.example/a") {
		t.Error("no scope at all refused something")
	}
	if none.Domains() != nil {
		t.Error("no scope at all has domains")
	}
}

// TestAPatternThatIsNotOneIsRefusedWhenBuilt, rather than quietly matching
// nothing for the length of the crawl.
func TestAPatternThatIsNotOneIsRefusedWhenBuilt(t *testing.T) {
	if _, err := scope.New(nil, []string{"[unclosed"}, nil, urls.Options{}); err == nil {
		t.Error("accepted an included pattern that is not one")
	}
	if _, err := scope.New(nil, nil, []string{"[unclosed"}, urls.Options{}); err == nil {
		t.Error("accepted an excluded pattern that is not one")
	}
}

func TestDomainsAreReportedBack(t *testing.T) {
	s := build(t, []string{"Example.COM", " ", "*.other.example"}, nil, nil)

	got := s.Domains()
	if len(got) != 2 || got[0] != "example.com" || got[1] != "other.example" {
		t.Errorf("domains = %v", got)
	}
}

// TestAPortIsNotPartOfASitesName. A job that wrote one meant the site, and a
// domain that kept its port would match nothing at all: the URL's host has its
// port taken off before the comparison, so the domain must too. Every crawl
// against a test server hits this, which is how it was found.
func TestAPortIsNotPartOfASitesName(t *testing.T) {
	s := build(t, []string{"127.0.0.1:8080"}, nil, nil)

	for _, url := range []string{
		"http://127.0.0.1:8080/a",
		"http://127.0.0.1:9090/a",
		"http://127.0.0.1/a",
	} {
		if !s.Allows(url) {
			t.Errorf("Allows(%q) = false", url)
		}
	}
	if s.Allows("http://elsewhere.example/a") {
		t.Error("another host was allowed")
	}
}

// TestAPortDoesNotDefeatAPattern.
//
// A port is not part of a site's name, and a pattern written by a person never
// carries one. Scope.host has stripped it from `domains` since the day that was
// noticed; `included` and `excluded` were left globbing against a host that
// still had it, and no test put a port in front of a pattern.
//
// Both directions were wrong and one of them dangerously. A site served on :8080
// matched no `included` pattern, so a crawl of it included nothing. And an
// `excluded` pattern did not exclude the same host on a non-default port, so the
// crawl fetched the thing it had been told never to touch, while the identical
// URL on :443 was correctly refused.
func TestAPortDoesNotDefeatAPattern(t *testing.T) {
	included, err := scope.New(nil, []string{"*.example.com"}, nil, urls.Options{})
	if err != nil {
		t.Fatalf("scope: %v", err)
	}
	for _, u := range []string{
		"https://news.example.com/a",
		"https://news.example.com:8080/a",
	} {
		if !included.Allows(u) {
			t.Errorf("%q is outside a scope that includes *.example.com", u)
		}
	}

	excluded, err := scope.New([]string{"example.com"}, nil, []string{"*.internal.example.com"}, urls.Options{})
	if err != nil {
		t.Fatalf("scope: %v", err)
	}
	for _, u := range []string{
		"https://db.internal.example.com/x",
		"https://db.internal.example.com:8443/x",
	} {
		if excluded.Allows(u) {
			t.Errorf("%q is allowed by a scope that excludes *.internal.example.com, "+
				"so the crawl fetches what it was told never to", u)
		}
	}
}

// TestAPortDoesNotDefeatAPatternWithAPathInIt.
//
// The other half of [TestAPortDoesNotDefeatAPattern], which only ever wrote
// host-shaped patterns. A pattern with a path in it is compared against the
// whole normalised URL, and that carries the port: the strip reached the
// host-only comparisons and neither of the two that a pattern like
// `https://example.com/admin/*` actually uses.
//
// Dangerous in the same direction as before. A crawl of a site served on a
// non-default port fetched exactly the subtree the job had said never to
// touch, while the same URL on :443 was correctly refused.
func TestAPortDoesNotDefeatAPatternWithAPathInIt(t *testing.T) {
	for _, pattern := range []string{
		"https://example.com/admin/*", // globbed
		"https://example.com/admin",   // read as a prefix
	} {
		excluded, err := scope.New([]string{"example.com"}, nil, []string{pattern}, urls.Options{})
		if err != nil {
			t.Fatalf("scope: %v", err)
		}
		for _, u := range []string{
			"https://example.com/admin/secret",
			"https://example.com:8443/admin/secret",
		} {
			if excluded.Allows(u) {
				t.Errorf("%q is allowed by a scope that excludes %q, "+
					"so the crawl fetches what it was told never to", u, pattern)
			}
		}
	}

	included, err := scope.New([]string{"example.com"}, []string{"https://example.com/news/*"}, nil, urls.Options{})
	if err != nil {
		t.Fatalf("scope: %v", err)
	}
	for _, u := range []string{
		"https://example.com/news/a",
		"https://example.com:8080/news/a",
	} {
		if !included.Allows(u) {
			t.Errorf("%q is outside a scope that includes https://example.com/news/*, "+
				"so a crawl of a site on that port includes nothing", u)
		}
	}
}

// TestAPatternsHostIsFoldedLikeEveryOtherHost.
//
// Normalise lowercases the scheme and the host of every URL, and New folds
// every entry in `domains`. Patterns were left as written, so an exclusion
// with a capital in its host matched no URL the crawler can produce: the job
// named the host it must never fetch, nothing refused it, and nothing said so.
//
// The path is not folded, because a URL path is case-sensitive and
// `excluded = ["*/Print/*"]` means that path and not another.
func TestAPatternsHostIsFoldedLikeEveryOtherHost(t *testing.T) {
	excluded, err := scope.New([]string{"example.com"}, nil, []string{
		"*.Internal.example.com",
		"https://Example.com/Admin/*",
	}, urls.Options{})
	if err != nil {
		t.Fatalf("scope: %v", err)
	}
	for _, u := range []string{
		"https://db.internal.example.com/x",
		"https://example.com/Admin/secret",
	} {
		if excluded.Allows(u) {
			t.Errorf("%q is allowed by a scope that names its host, only in different case", u)
		}
	}

	// And the path keeps its case, or the exclusion would be wider than what
	// was written.
	if !excluded.Allows("https://example.com/admin/secret") {
		t.Error("an exclusion written /Admin/ also refused /admin/, which is a different path")
	}
}
