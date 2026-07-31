// SPDX-License-Identifier: GPL-3.0-or-later

package crawl

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/rangertaha/scour/internal/store"
)

// scope decides whether a URL is inside an entity's targets.
//
// This used to be a regular expression per target, handed to colly as
// URLFilters. That is fine for the handful of targets a person types and
// hopeless for a list. Each compiled expression measured about 5KB, so a real
// list of 978,991 news sites wanted roughly 4.6GB before a single page was
// fetched, and colly tests a URL against the filters in order, so every
// discovered link cost up to 978,991 match attempts. The crawl spent eleven
// gigabytes of resident memory and never fetched anything.
//
// A target's scope is really a statement about a host, so the host is the index.
// Checking a URL walks the labels of its hostname, which is a handful of map
// lookups whatever the list's length, and the memory is the hostnames
// themselves rather than an automaton apiece.
type scope struct {
	// hosts holds domain targets that do not cover subdomains. The stored key
	// is the target as given; a leading "www." on the candidate is ignored,
	// which is what the old expression's (www\.)? did.
	hosts map[string]bool

	// trees holds domain targets that do cover subdomains, matched against the
	// candidate host and every suffix of it.
	trees map[string]bool

	// dirs holds URL targets as a host mapped to the path prefixes allowed
	// under it. A URL target is narrower than a domain target: it keeps the
	// crawl under the seed's own directory.
	dirs map[string][]string
}

// newScope builds the scope for a set of targets.
func newScope(targets []store.Target) (*scope, error) {
	s := &scope{
		hosts: make(map[string]bool),
		trees: make(map[string]bool),
		dirs:  make(map[string][]string),
	}
	for _, t := range targets {
		switch t.Kind {
		case store.TargetDomain:
			host := strings.ToLower(strings.TrimSpace(t.Value))
			if host == "" {
				continue
			}
			if t.Subdomains {
				s.trees[host] = true
			} else {
				s.hosts[host] = true
			}

		case store.TargetURL:
			u, err := url.Parse(t.Value)
			if err != nil {
				return nil, fmt.Errorf("target %q: %w", t.Value, err)
			}
			host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
			if host == "" {
				continue
			}
			dir := u.Path
			if i := strings.LastIndex(dir, "/"); i >= 0 {
				dir = dir[:i+1]
			}
			if dir == "" {
				dir = "/"
			}
			// A host reached by many URLs collects one entry per distinct
			// directory, not per URL, which is what keeps a list of a million
			// article URLs down to the handful of sections they sit in.
			if !contains(s.dirs[host], dir) {
				s.dirs[host] = append(s.dirs[host], dir)
			}
		}
	}
	return s, nil
}

// empty reports whether the scope names nothing, in which case the crawl is
// unrestricted rather than closed.
func (s *scope) empty() bool {
	return len(s.hosts) == 0 && len(s.trees) == 0 && len(s.dirs) == 0
}

// allows reports whether a URL is inside the scope.
func (s *scope) allows(rawURL string) bool {
	if s.empty() {
		return true
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return false
	}

	if s.hosts[host] || (strings.HasPrefix(host, "www.") && s.hosts[host[len("www."):]]) {
		return true
	}

	path := u.Path
	if path == "" {
		path = "/"
	}
	// A subdomain target covers the host and everything under it, and a URL
	// target covers any number of leading labels, so both are answered by
	// walking the candidate's own suffixes rather than by scanning the targets.
	for h := host; h != ""; {
		if s.trees[h] {
			return true
		}
		for _, dir := range s.dirs[h] {
			if strings.HasPrefix(path, dir) {
				return true
			}
		}
		i := strings.IndexByte(h, '.')
		if i < 0 {
			break
		}
		h = h[i+1:]
	}
	return false
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
