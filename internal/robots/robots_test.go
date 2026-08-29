// SPDX-License-Identifier: GPL-3.0-or-later

package robots_test

import (
	"strings"
	"testing"
	"time"

	"github.com/rangertaha/scour/internal/robots"
)

const us = "scour (+https://github.com/rangertaha/scour)"

// TestTheExamplesInTheSpecification. RFC 9309 §5 writes these out, and a parser
// that disagrees with them is not implementing the protocol whatever else it
// does.
func TestTheExamplesInTheSpecification(t *testing.T) {
	// RFC 9309 §5.1, simple example.
	simple := robots.Parse([]byte(`
User-Agent: *
Disallow: *.gif$
Disallow: /example/
Allow: /publications/

User-Agent: foobot
Disallow:/
Allow:/example/page.html
Allow:/example/allowed.gif
`))

	for path, want := range map[string]bool{
		"/publications/":     true,
		"/example/":          false,
		"/example/page.html": false,
		"/anything.gif":      false,
		"/anything.gif?v=1":  true, // the $ anchors the end
		"/somewhere/else":    true,
		// The gif rule disallows gifs everywhere, but "/publications/" is 14
		// characters against its 6, and the longest match wins. A site that
		// meant otherwise has to say so with a longer pattern.
		"/publications/report.gif": true,
	} {
		if got := simple.Allowed(us, path); got != want {
			t.Errorf("* group: %s = %v, want %v", path, got, want)
		}
	}

	for path, want := range map[string]bool{
		"/example/page.html":   true,
		"/example/allowed.gif": true,
		"/example/other.html":  false,
		"/":                    false,
		"/publications/":       false, // foobot has its own group and does not see the *
	} {
		if got := simple.Allowed("foobot", path); got != want {
			t.Errorf("foobot: %s = %v, want %v", path, got, want)
		}
	}
}

// TestLongestMatchWins is the rule the whole file turns on: a site writes a
// broad disallow and then allows the one directory it wants crawled.
func TestLongestMatchWins(t *testing.T) {
	rules := robots.Parse([]byte(`
User-agent: *
Disallow: /
Allow: /public/
Disallow: /public/private/
`))

	for path, want := range map[string]bool{
		"/":                      false,
		"/anything":              false,
		"/public/":               true,
		"/public/page.html":      true,
		"/public/private/":       false,
		"/public/private/x.html": false,
	} {
		if got := rules.Allowed(us, path); got != want {
			t.Errorf("%s = %v, want %v", path, got, want)
		}
	}
}

// TestAllowWinsATie, which is what makes an equally specific allow usable at
// all. RFC 9309 §2.2.2.
func TestAllowWinsATie(t *testing.T) {
	rules := robots.Parse([]byte(`
User-agent: *
Disallow: /page
Allow: /page
`))
	if !rules.Allowed(us, "/page") {
		t.Error("a tie went to the disallow")
	}
}

func TestWildcardsAndAnchors(t *testing.T) {
	rules := robots.Parse([]byte(`
User-agent: *
Disallow: /*.pdf$
Disallow: /a/*/b
Disallow: /search?
`))

	for path, want := range map[string]bool{
		"/report.pdf":      false,
		"/deep/report.pdf": false,
		"/report.pdf?v=2":  true, // anchored, so the query saves it
		"/report.pdfx":     true,
		"/a/one/b":         false,
		"/a/one/two/b":     false,
		"/a/b":             true, // the * needs something between the slashes
		"/search?q=x":      false,
		"/searching":       true,
	} {
		if got := rules.Allowed(us, path); got != want {
			t.Errorf("%s = %v, want %v", path, got, want)
		}
	}
}

// TestAnchoredPatternsCannotReuseCharacters: "/a*a$" must not match "/a" by
// letting the prefix and the suffix land on the same byte.
func TestAnchoredPatternsCannotReuseCharacters(t *testing.T) {
	rules := robots.Parse([]byte("User-agent: *\nDisallow: /a*a$\n"))

	if !rules.Allowed(us, "/a") {
		t.Error("the prefix and the anchor matched the same character")
	}
	if rules.Allowed(us, "/aa") {
		t.Error("/aa did not match /a*a$")
	}
	if rules.Allowed(us, "/abca") {
		t.Error("/abca did not match /a*a$")
	}
}

// TestAnEmptyDisallowAllowsEverything. It is the documented way to say "nothing
// is disallowed", and reading it as a rule matching every path would lock a
// crawler out of the sites that were being most welcoming.
func TestAnEmptyDisallowAllowsEverything(t *testing.T) {
	rules := robots.Parse([]byte("User-agent: *\nDisallow:\n"))

	for _, path := range []string{"/", "/anything", "/deep/page.html"} {
		if !rules.Allowed(us, path) {
			t.Errorf("an empty disallow blocked %s", path)
		}
	}
}

// TestDisallowEverything, the other one-line file.
func TestDisallowEverything(t *testing.T) {
	rules := robots.Parse([]byte("User-agent: *\nDisallow: /\n"))

	for _, path := range []string{"/", "/anything", ""} {
		if rules.Allowed(us, path) {
			t.Errorf("Disallow: / let %q through", path)
		}
	}
}

// TestNothingAddressedToUs: a file that only speaks to other crawlers says
// nothing about us, and silence is permission.
func TestNothingAddressedToUs(t *testing.T) {
	rules := robots.Parse([]byte(`
User-agent: googlebot
Disallow: /
`))
	if !rules.Allowed(us, "/anything") {
		t.Error("followed rules written for somebody else")
	}
	if rules.Allowed("googlebot", "/anything") {
		t.Error("googlebot's own rules were not applied")
	}
}

// TestGroupSelection covers who a group is addressed to: exact first, then the
// longest prefix, then the catch-all.
func TestGroupSelection(t *testing.T) {
	rules := robots.Parse([]byte(`
User-agent: *
Disallow: /everyone

User-agent: acmebot
Disallow: /acme

User-agent: acmebot-news
Disallow: /acme-news
`))

	cases := map[string]struct{ blocked, allowed string }{
		"exact":     {"/acme-news", "/acme"},
		"prefix":    {"/acme", "/everyone"},
		"catch-all": {"/everyone", "/acme"},
	}

	for name, agent := range map[string]string{
		"exact":     "acmebot-news/1.0",
		"prefix":    "acmebot-images/1.0",
		"catch-all": "somebody-else/1.0",
	} {
		tc := cases[name]
		if rules.Allowed(agent, tc.blocked) {
			t.Errorf("%s: %s was allowed", name, tc.blocked)
		}
		if !rules.Allowed(agent, tc.allowed) {
			t.Errorf("%s: %s was blocked", name, tc.allowed)
		}
	}
}

// TestConsecutiveAgentsShareAGroup, and a user-agent line after a rule starts a
// new one. Getting this wrong merges two sites' worth of rules together.
func TestConsecutiveAgentsShareAGroup(t *testing.T) {
	rules := robots.Parse([]byte(`
User-agent: alphabot
User-agent: betabot
Disallow: /shared

User-agent: gammabot
Disallow: /gamma
`))

	for _, agent := range []string{"alphabot", "betabot"} {
		if rules.Allowed(agent, "/shared") {
			t.Errorf("%s was not in the shared group", agent)
		}
		if !rules.Allowed(agent, "/gamma") {
			t.Errorf("%s picked up the next group's rules", agent)
		}
	}
	if rules.Allowed("gammabot", "/gamma") {
		t.Error("gammabot's own group was not applied")
	}
	if !rules.Allowed("gammabot", "/shared") {
		t.Error("gammabot picked up the previous group's rules")
	}
}

// TestCaseAndSpacing. Field names are case-insensitive and hand-written files
// are untidy; the paths themselves are not case-insensitive, because URLs are
// not.
func TestCaseAndSpacing(t *testing.T) {
	rules := robots.Parse([]byte("  USER-AGENT :  Scour  \n\tDisallow  :  /Private  \n"))

	if rules.Allowed(us, "/Private/x") {
		t.Error("an untidy file was not read")
	}
	if !rules.Allowed(us, "/private/x") {
		t.Error("paths were matched case-insensitively, and URLs are not")
	}
}

func TestCommentsAndRubbish(t *testing.T) {
	rules := robots.Parse([]byte(`
# a comment
User-agent: *      # trailing comment
Disallow: /private # and here

this line has no colon
: neither does this one have a field
User-agent:
Disallow: /nobody-was-addressed
Sitemap: https://example.com/sitemap.xml
Unknown-Directive: whatever
`))

	if rules.Allowed(us, "/private/x") {
		t.Error("the rule between the rubbish was lost")
	}
	if !rules.Allowed(us, "/public") {
		t.Error("something in the rubbish became a rule")
	}
	// A user-agent line with no value addresses nobody, so what follows it
	// belongs to the group that was already open rather than to a new one.
	if rules.Allowed(us, "/nobody-was-addressed") {
		t.Error("a rule after an empty user-agent line was lost")
	}
}

// TestNothingAtAllAllowsEverything: an empty file, a file of comments, and no
// file at all are the same answer.
func TestNothingAtAllAllowsEverything(t *testing.T) {
	for name, body := range map[string]string{
		"empty":    "",
		"comments": "# nothing to see\n# really\n",
		"html":     "<!DOCTYPE html><html><body>404 Not Found</body></html>",
	} {
		t.Run(name, func(t *testing.T) {
			if !robots.Parse([]byte(body)).Allowed(us, "/anything") {
				t.Error("blocked")
			}
		})
	}

	var none *robots.Rules
	if !none.Allowed(us, "/anything") {
		t.Error("no rules at all blocked something")
	}
	if d, ok := none.Delay(us); ok || d != 0 {
		t.Errorf("no rules at all asked for a delay of %s", d)
	}
}

// TestRulesBeforeAnyAgentAreAddressedToNobody. RFC 9309 has no such thing as a
// global rule, and reading one as global would apply it to every crawler.
func TestRulesBeforeAnyAgentAreAddressedToNobody(t *testing.T) {
	rules := robots.Parse([]byte(`
Disallow: /orphaned
Crawl-delay: 30

User-agent: *
Disallow: /private
`))

	if !rules.Allowed(us, "/orphaned") {
		t.Error("a rule before any user-agent line was applied")
	}
	if rules.Allowed(us, "/private") {
		t.Error("the real group was lost")
	}
	if _, ok := rules.Delay(us); ok {
		t.Error("an orphaned crawl-delay was applied")
	}
}

func TestCrawlDelay(t *testing.T) {
	rules := robots.Parse([]byte(`
User-agent: *
Crawl-delay: 2.5

User-agent: acmebot
Crawl-delay: 10
`))

	d, ok := rules.Delay(us)
	if !ok {
		t.Fatal("no delay was asked for")
	}
	if d != 2500*time.Millisecond {
		t.Errorf("delay = %s", d)
	}

	if d, _ := rules.Delay("acmebot"); d != 10*time.Second {
		t.Errorf("acmebot's delay = %s", d)
	}
}

// TestADelayThatIsNotANumberIsIgnored, rather than becoming zero and reading as
// permission to go as fast as we like.
func TestADelayThatIsNotANumberIsIgnored(t *testing.T) {
	for name, value := range map[string]string{
		"words":    "slowly",
		"negative": "-5",
		"empty":    "",
	} {
		t.Run(name, func(t *testing.T) {
			rules := robots.Parse([]byte("User-agent: *\nCrawl-delay: " + value + "\n"))
			if _, ok := rules.Delay(us); ok {
				t.Error("was read as a delay")
			}
		})
	}
}

// TestNoDelayIsNotZeroDelay. A site that said nothing has left the pacing to
// us, and a caller has to be able to tell that from a site that said zero.
func TestNoDelayIsNotZeroDelay(t *testing.T) {
	silent := robots.Parse([]byte("User-agent: *\nDisallow: /private\n"))
	if _, ok := silent.Delay(us); ok {
		t.Error("a file with no crawl-delay reported one")
	}

	explicit := robots.Parse([]byte("User-agent: *\nCrawl-delay: 0\n"))
	d, ok := explicit.Delay(us)
	if !ok || d != 0 {
		t.Errorf("an explicit zero came back as %s, %v", d, ok)
	}
}

// TestOnlyTheFirstHalfMegabyteIsRead. RFC 9309 caps it, which is what stops a
// file that is really a video from being a way to stall a crawler.
func TestOnlyTheFirstHalfMegabyteIsRead(t *testing.T) {
	var b strings.Builder
	b.WriteString("User-agent: *\nDisallow: /private\n")
	b.WriteString(strings.Repeat("# padding\n", robots.MaxSize/10))
	b.WriteString("Disallow: /beyond-the-limit\n")

	rules := robots.Parse([]byte(b.String()))
	if rules.Allowed(us, "/private/x") {
		t.Error("the rule inside the limit was lost")
	}
	if !rules.Allowed(us, "/beyond-the-limit") {
		t.Error("a rule past the limit was read")
	}
}

func TestToken(t *testing.T) {
	for agent, want := range map[string]string{
		"scour (+https://github.com/rangertaha/scour)": "scour",
		"Mozilla/5.0 (compatible; acme/2.1)":           "mozilla",
		"AcmeBot":                                      "acmebot",
		"  spaced  ":                                   "spaced",
		"":                                             "",
	} {
		if got := robots.Token(agent); got != want {
			t.Errorf("Token(%q) = %q, want %q", agent, got, want)
		}
	}
}

// TestGroupsAddressingOneAgentAreCombined.
//
// RFC 9309 section 2.2.1 requires it, and the first version of this returned on
// the first matching group. A file with two `User-agent: *` blocks, which is
// what hand-editing and tooling that appends both produce, had every rule after
// the first block silently discarded, and a path the site had explicitly
// refused was crawled.
func TestGroupsAddressingOneAgentAreCombined(t *testing.T) {
	rules := robots.Parse([]byte(`
User-agent: *
Disallow: /tmp/

User-agent: *
Disallow: /admin/
`))

	for _, path := range []string{"/tmp/x", "/admin/secret"} {
		if rules.Allowed(us, path) {
			t.Errorf("%s was allowed: a second block addressing the same agent was discarded", path)
		}
	}
	if !rules.Allowed(us, "/public") {
		t.Error("combining the groups refused something neither of them did")
	}
}

// TestGroupsAddressingOneNamedAgentAreCombinedToo, since a repeated named token
// is the same mistake with a different word.
func TestGroupsAddressingOneNamedAgentAreCombinedToo(t *testing.T) {
	rules := robots.Parse([]byte(`
User-agent: scour
Disallow: /one/

User-agent: scour
Disallow: /two/

User-agent: *
Disallow: /
`))

	for _, path := range []string{"/one/x", "/two/x"} {
		if rules.Allowed(us, path) {
			t.Errorf("%s was allowed", path)
		}
	}
	// Still our own groups rather than the catch-all, which refuses everything.
	if !rules.Allowed(us, "/three") {
		t.Error("the catch-all was applied to an agent that has its own groups")
	}
}

// TestTheFirstCrawlDelayWins when two combined groups disagree: a file
// contradicting itself is a file, and the earlier line is what somebody reading
// top to bottom expects.
func TestTheFirstCrawlDelayWins(t *testing.T) {
	rules := robots.Parse([]byte("User-agent: *\nCrawl-delay: 2\n\nUser-agent: *\nCrawl-delay: 9\n"))

	if d, ok := rules.Delay(us); !ok || d != 2*time.Second {
		t.Errorf("delay = %s, %v", d, ok)
	}
}

// TestAByteOrderMarkIsNotAField.
//
// U+FEFF left Unicode's White_Space property in 4.0.1, so strings.TrimSpace
// does not remove it. Without an explicit strip, the first line of a file
// written by a Windows editor reads as a field called "\ufeffuser-agent", it
// matches nothing, and every rule after it is discarded as addressed to nobody.
// The whole file becomes permission, which is the most dangerous way for a
// robots parser to fail.
func TestAByteOrderMarkIsNotAField(t *testing.T) {
	rules := robots.Parse([]byte("\ufeffUser-agent: *\nDisallow: /private/\n"))

	if rules.Allowed(us, "/private/x") {
		t.Error("a byte order mark turned the whole file into permission")
	}
	if !rules.Allowed(us, "/public") {
		t.Error("something outside the rule was refused")
	}
}

// TestANonASCIIRuleIsObeyed.
//
// RFC 9309 section 2.2.2 compares both sides percent-encoded, and only one side
// was. A path comes from a URL and is already encoded: a link to /müll/ is
// /m%C3%BCll/ by the time anything can follow it. The pattern is whatever the
// publisher typed, and a robots.txt served as UTF-8 says `Disallow: /müll/` in
// plain letters. Byte for byte those two differ at the first non-ASCII
// character, so the rule matched nothing, Allowed said yes, and the crawler
// fetched what the site had refused. Every pattern holding a space or any
// non-ASCII character behaved that way, which is most of the non-English web.
//
// This is the one kind of defect in the crawler that harms somebody else's
// server rather than its own output, which is why the downloader will not make
// robots optional either.
func TestANonASCIIRuleIsObeyed(t *testing.T) {
	rules := robots.Parse([]byte("User-agent: *\nDisallow: /müll/\nDisallow: /a b/\n"))

	for _, path := range []string{
		"/m%C3%BCll/page",
		"/a%20b/page",
	} {
		if rules.Allowed("scour", path) {
			t.Errorf("Allowed(%q) = true, and the site refused it", path)
		}
	}

	// A rule the publisher already wrote percent-encoded still works, so
	// encoding is not applied twice.
	encoded := robots.Parse([]byte("User-agent: *\nDisallow: /m%C3%BCll/\n"))
	if encoded.Allowed("scour", "/m%C3%BCll/page") {
		t.Error("a pattern written percent-encoded was double-encoded and stopped matching")
	}

	// And something the rules do not name is still allowed, so this is not
	// satisfied by refusing everything.
	if !rules.Allowed("scour", "/news/story") {
		t.Error("a path no rule names was refused")
	}
}

// TestAnEscapeMatchesWhateverCaseItIsWrittenIn.
//
// RFC 3986 says a percent-encoding compares case-insensitively, and both sides
// of this comparison are written by somebody else: the publisher's escapes in
// the rule, and the site's own links in the path. They were compared byte for
// byte, so `Disallow: /müll/` - stored as /m%C3%BCll/ - did not match a link
// written /m%c3%bcll/page, and the crawler fetched a path the site had refused.
// The mirror case, a lowercase rule and a normally-encoded link, failed the
// same way.
func TestAnEscapeMatchesWhateverCaseItIsWrittenIn(t *testing.T) {
	for name, file := range map[string]string{
		"a rule written in unicode": "User-agent: *\nDisallow: /müll/\n",
		"a rule written uppercase":  "User-agent: *\nDisallow: /m%C3%BCll/\n",
		"a rule written lowercase":  "User-agent: *\nDisallow: /m%c3%bcll/\n",
	} {
		t.Run(name, func(t *testing.T) {
			rules := robots.Parse([]byte(file))
			for _, path := range []string{"/m%C3%BCll/page", "/m%c3%bcll/page"} {
				if rules.Allowed("scour", path) {
					t.Errorf("%q is allowed, though the site disallowed that directory", path)
				}
			}
		})
	}

	// And the case of everything else still matters: a robots.txt path is
	// case-sensitive apart from its escapes.
	rules := robots.Parse([]byte("User-agent: *\nDisallow: /Private\n"))
	if !rules.Allowed("scour", "/private") {
		t.Error("/private was refused by a rule written /Private, so path case stopped mattering")
	}
}
