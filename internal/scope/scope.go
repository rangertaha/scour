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
// # One implementation, three stages
//
// The scheduler drops an out-of-scope URL before it is queued, the downloader
// before it is fetched, and the spider before a discovered link is reported.
// Three chances to enforce one rule, so the rule has to be one piece of code:
// three subtly different scope checks is a crawl that leaves the site through
// whichever of them is loosest.
package scope

import (
	"fmt"
	"strings"

	"github.com/rangertaha/scour/internal/urls"
)

// Scope is a job's boundary.
//
// The zero value allows everything, which is what a job with no scope at all
// means: `scour try` on one URL has nothing to be outside of.
type Scope struct {
	domains  []string
	included []string
	excluded []string
}

// New builds a scope. Patterns are checked here so a job with an unusable one
// is refused rather than quietly matching nothing.
func New(domains, included, excluded []string) (*Scope, error) {
	s := &Scope{}

	for _, d := range domains {
		d = strings.ToLower(strings.TrimSpace(d))
		if d == "" {
			continue
		}
		s.domains = append(s.domains, strings.TrimPrefix(d, "*."))
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
			*list.to = append(*list.to, pattern)
		}
	}
	return s, nil
}

// Allows reports whether a normalised URL is inside this scope.
func (s *Scope) Allows(normalised string) bool {
	if s == nil {
		return true
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
	host = strings.ToLower(host)
	if i := strings.LastIndex(host, ":"); i >= 0 {
		host = host[:i]
	}

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
	if glob(pattern, normalised) || glob(pattern, urls.Host(normalised)) {
		return true
	}

	// A pattern with no wildcard reads as a prefix, because
	// `excluded = ["https://example.com/print"]` plainly means that subtree.
	if !strings.ContainsAny(pattern, "*?") {
		return strings.HasPrefix(normalised, pattern)
	}
	return false
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
		case j < len(pattern) && (pattern[j] == '?' || pattern[j] == s[i]):
			i++
			j++
		case j < len(pattern) && pattern[j] == '*':
			star, mark = j, i
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
