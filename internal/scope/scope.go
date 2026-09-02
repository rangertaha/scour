// SPDX-License-Identifier: GPL-3.0-or-later

// Package scope decides whether a URL is one this job is allowed to fetch.
//
// A job says where it may go three ways, and they answer different questions:
//
//   - `domains` is which sites. A host matches if it is one of them or a
//     subdomain of one.
//   - `included` is which URLs, as patterns. If any are given, a URL has to
//     match one.
//   - `excluded` is which URLs never, and it wins over both of the others.
//
// The order is what makes it usable: a job says "this site, but not the
// printer-friendly copies" by naming the domain and excluding the pattern,
// rather than by writing out an inclusion for everything else.
//
// # One implementation, two stages that need it
//
// The scheduler drops an out-of-scope URL before it is queued. That covers
// every URL a crawl decides to fetch, because deciding is what the scheduler
// is for.
//
// The downloader covers the one it cannot: a redirect. A hop is chosen by
// whoever controls a page the crawl was already fetching, and it happens after
// queueing, so the scheduler has already had its say and cannot have another.
// [Scope.Allows] is called there on the target before the hop is taken. This
// comment used to claim the downloader checked, and nothing in it imported this
// package: a job naming one exclusion followed a redirect straight past it.
//
// The spider does not check, and does not need to. Its output is links, which
// go to the scheduler, which drops what is out of bounds before queueing. A
// check there would be the same rule applied twice on one path.
//
// One rule, one implementation, whoever asks: two subtly different scope checks
// is a crawl that leaves the site through whichever of them is looser.
//
// # Why the scope normalises the URL itself
//
// Because "one rule" turned out to mean the patterns and not the input. A
// scope compares against a normalised URL, `included` and `excluded` are
// globbed against the whole of it, and the job's own canonicalisation -
// `lower_path`, `strip_trailing_slash`, `sort_query` - changes what that
// string is. Three callers normalised before asking, and got it right once:
//
//   - the scheduler used the job's settings, which is the answer.
//   - the downloader's redirect follower used the defaults, so a job with
//     `lower_path = true` and `excluded = ["*/private/*"]` refused
//     /PRIVATE/secret when it queued one and followed a 302 to it.
//   - validation used the defaults, so a job whose start URLs are in scope
//     only after canonicalisation was refused outright, and one whose start
//     URLs leave scope only after it was accepted, stored, and crawled
//     nothing - the exact "success that did nothing" that check exists to
//     prevent.
//
// Fixing each site as it was found did not converge; a third turned up in the
// pass after the second was fixed. So [Scope.Allows] takes the URL as written
// and normalises it here, with the canonicalisation the scope was built with.
// There is no longer a way to ask a scope a question in terms it did not
// choose.
package scope

import (
	"fmt"
	"strings"

	"github.com/rangertaha/scour/internal/urls"
)

// Scope is a job's boundary.
//
// The zero value allows everything, which is what a job with no scope at all
// means: `scour scrape` on one URL has nothing to be outside of.
type Scope struct {
	// canon is how the job this scope belongs to decides two URLs are the same
	// page, applied to every URL before it is compared. See the package doc.
	canon urls.Options

	domains  []string
	included []string
	excluded []string
}

// New builds a scope. Patterns are checked here so a job with an unusable one
// is refused rather than quietly matching nothing.
//
// canon is the job's canonicalisation, from [engine.Job.Canonical]. It is a
// parameter rather than something the caller applies beforehand for the reason
// the package doc gives: three callers applied it beforehand and one of them
// got it right.
func New(domains, included, excluded []string, canon urls.Options) (*Scope, error) {
	s := &Scope{canon: canon}

	for _, d := range domains {
		d = strings.ToLower(strings.TrimSpace(d))
		if d == "" {
			continue
		}
		d = strings.TrimPrefix(d, "*.")

		// A port is not part of a site's name, and a job that wrote one meant
		// the site. Stripping it here is what makes the comparison symmetric:
		// the URL's host has its port taken off too, and a domain that kept one
		// would match nothing at all.
		s.domains = append(s.domains, urls.WithoutPort(d))
	}
	for _, list := range []struct {
		name string
		from []string
		to   *[]string
	}{
		{"included", included, &s.included},
		{"excluded", excluded, &s.excluded},
	} {
		for _, pattern := range list.from {
			pattern = strings.TrimSpace(pattern)
			if pattern == "" {
				continue
			}
			if strings.ContainsAny(pattern, "[]") {
				// Refused rather than treated as a literal, because somebody
				// writing brackets wants a character class and would never find
				// out they had not got one.
				return nil, fmt.Errorf(
					"scope: %s pattern %q: only * and ? are patterns here, and [ ] are not",
					list.name, pattern)
			}
			*list.to = append(*list.to, foldHost(pattern))
		}
	}
	return s, nil
}

// Allows reports whether a URL is inside this scope.
//
// The URL is taken as written and normalised here, with the canonicalisation
// the scope was built with. A URL that will not normalise is not allowed: it
// is not a page this crawl can hold, let alone one it may fetch.
//
// Normalising an already-normalised URL is what the scheduler does, and it
// costs a pass and changes nothing: normalisation is closed over its own
// output, which its own tests pin.
func (s *Scope) Allows(raw string) bool {
	if s == nil {
		return true
	}

	normalised, err := urls.Normalise(raw, s.canon)
	if err != nil {
		return false
	}

	// Excluded first and unconditionally. A job that has said never means
	// never, whatever else it also said.
	for _, pattern := range s.excluded {
		if matches(pattern, normalised) {
			return false
		}
	}

	if len(s.domains) > 0 && !s.host(urls.Host(normalised)) {
		return false
	}

	if len(s.included) > 0 {
		for _, pattern := range s.included {
			if matches(pattern, normalised) {
				return true
			}
		}
		return false
	}
	return true
}

// host reports whether a host is one of the job's domains or below one.
func (s *Scope) host(host string) bool {
	host = urls.WithoutPort(strings.ToLower(host))

	for _, domain := range s.domains {
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return true
		}
	}
	return false
}

// Domains is what the job named, which is what an error message quotes back.
func (s *Scope) Domains() []string {
	if s == nil {
		return nil
	}
	return append([]string(nil), s.domains...)
}

// foldHost lowercases the authority of a URL-shaped pattern and leaves the rest
// as written.
//
// [urls.Normalise] lowercases the scheme and the host of every URL, and New
// folds every entry in `domains`, so a pattern naming a host in another case
// could match no URL that ever reaches here.
//
// The path is deliberately not folded. A URL path is case-sensitive, so
// `excluded = ["*/Print/*"]` means that path and not `/print/`, and folding it
// would silently widen every exclusion somebody wrote.
//
// # Why only a pattern with a scheme
//
// Because the two cannot be told apart otherwise, and guessing got it wrong.
// This used to treat any pattern with no `/`, `?` or `#` as host-shaped, which
// is true of `*.example.com` and equally true of `*Print*` and `*.PDF`. So a
// wildcard path pattern was folded to `*print*`, matched nothing against a
// path that keeps its case, and the crawl fetched the subtree the job said
// never to touch - the exact failure the paragraph above says this avoids,
// arriving through the branch meant to prevent it. An `included = ["*News*"]`
// matched nothing either, so a crawl of that site included nothing.
//
// A pattern with a scheme has an authority that is unmistakably an authority.
// Everything else is left as written and the host comparison in [matches]
// folds instead, which is where the case-insensitivity actually belongs: a
// host is case-insensitive by specification and a path is not, so it is a
// property of what is being compared and not of the pattern.
func foldHost(pattern string) string {
	scheme := strings.Index(pattern, "://")
	if scheme < 0 {
		return pattern
	}

	host := scheme + len("://")
	end := strings.IndexAny(pattern[host:], "/?#")
	if end < 0 {
		return strings.ToLower(pattern)
	}
	return strings.ToLower(pattern[:host+end]) + pattern[host+end:]
}

// matches applies a pattern to a URL.
//
// A pattern is matched against the whole URL and against the host alone, so
// that `*.example.com` reads the way anybody writing it expects even though a
// URL starts with a scheme.
//
// Globbing rather than a regular expression: a job document is written by
// people who want to exclude `*/print/*`, and a regular expression is a way to
// be wrong about that at three in the morning.
func matches(pattern, normalised string) bool {
	host := urls.Host(normalised)

	// Against the host with its port and without it, because a port is not
	// part of a site's name and a pattern written by a person never carries
	// one. [Scope.host] has stripped it from `domains` since the day that was
	// noticed; `included` and `excluded` were left comparing against a host
	// that still had it.
	//
	// Both directions were wrong and one of them dangerously. A site served on
	// :8080 matched no `included` pattern, so a crawl of it included nothing.
	// And `excluded = ["*.internal.example.com"]` did not exclude
	// `https://db.internal.example.com:8443/x`, so a crawl fetched the thing it
	// had been told never to touch - while the same URL on the default port was
	// correctly refused.
	// The URL without its port as well as with it, and not only the host,
	// because a pattern with a path in it is compared against the whole URL
	// and never reaches the two host comparisons below. That is where the
	// strip was missing: `excluded = ["https://example.com/admin/*"]` did not
	// exclude `https://example.com:8443/admin/secret`, while the same URL on
	// :443 was correctly refused, and a crawl of a site served on :8080
	// matched no `included` pattern and so included nothing.
	bare := withoutPort(normalised)

	// The host comparisons fold and the URL comparisons do not, because a host
	// is case-insensitive by specification and a path is not. See [foldHost].
	if glob(pattern, normalised) || glob(pattern, bare) ||
		globFold(pattern, host) || globFold(pattern, urls.WithoutPort(host)) {
		return true
	}

	// A pattern with no wildcard reads as a prefix, because
	// `excluded = ["https://example.com/print"]` plainly means that subtree.
	if !strings.ContainsAny(pattern, "*?") {
		return strings.HasPrefix(normalised, pattern) || strings.HasPrefix(bare, pattern)
	}
	return false
}

// withoutPort is a normalised URL with the port taken off its host, which is
// the form a pattern is written in. Everything after the host is untouched: a
// port only ever appears in the authority, and a `:` in a path is ordinary.
func withoutPort(normalised string) string {
	host := urls.Host(normalised)
	if host == "" {
		return normalised
	}
	bare := urls.WithoutPort(host)
	if bare == host {
		return normalised
	}
	return strings.Replace(normalised, host, bare, 1)
}

// globFold is [glob] against a host, which is case-insensitive.
func globFold(pattern, host string) bool {
	return glob(strings.ToLower(pattern), strings.ToLower(host))
}

// glob matches `*` and `?` against a whole string.
//
// Not [path.Match], and the difference is the whole point: there, `*` stops at
// a `/`, so `*/print/*` matches nothing on a URL that has four slashes in it
// before the path even starts. Anybody writing that pattern means "anywhere",
// and here it means that.
//
// `?` matches one byte rather than one rune, which for a URL is the same thing:
// anything outside ASCII is percent-encoded before it gets here.
func glob(pattern, s string) bool {
	var (
		star = -1 // where the last * was, or -1
		mark int  // how much of s that * had consumed
		i, j int
	)

	for i < len(s) {
		switch {
		// The wildcard is tested first, because `*` in a pattern is always a
		// wildcard and never a literal. Tested second, the literal comparison
		// matched a `*` in the pattern against a `*` in the URL and consumed
		// the wildcard: `*` did not match `*0`, and `*` is a legal character
		// in a path. So a site could put one in a URL and walk past an
		// exclusion written to catch it. There is no way to write a literal
		// `*` in a pattern, which is what "only * and ? are patterns here"
		// means. Found by fuzzing against the obvious implementation.
		case j < len(pattern) && pattern[j] == '*':
			star, mark = j, i
			j++
		case j < len(pattern) && (pattern[j] == '?' || pattern[j] == s[i]):
			i++
			j++
		case star >= 0:
			// Backtrack: let the last * swallow one more byte.
			mark++
			i, j = mark, star+1
		default:
			return false
		}
	}

	for j < len(pattern) && pattern[j] == '*' {
		j++
	}
	return j == len(pattern)
}
