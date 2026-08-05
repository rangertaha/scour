// SPDX-License-Identifier: GPL-3.0-or-later

// Package robots reads robots.txt and answers whether a path may be fetched.
//
// It implements RFC 9309, the Robots Exclusion Protocol, plus the two
// extensions everybody's file uses anyway: wildcards in patterns, and
// crawl-delay.
//
// # Why this is written rather than imported
//
// Being wrong here has a victim. A parser that is subtly too permissive crawls
// what a site asked it not to, on somebody else's machine, under our name, and
// nothing in our own output looks wrong when it happens. That is a thing to
// read the specification for and hold to a test suite, not a thing to take on
// trust because a dependency is popular. It is also small: the whole of RFC
// 9309 is a few hundred lines with the edge cases written down.
//
// # What it does not do
//
// It does not fetch. What a 404 means, how long an answer is good for and what
// to do when a server will not answer are the crawler's decisions, not the
// file's, and they are made in the downloader where the fetching happens.
//
// It parses `sitemap` lines only far enough to ignore them. A sitemap is a
// source of URLs, which is a seeding decision belonging to whatever fills the
// frontier; crawl-delay is kept because it is the same kind of thing as the
// rest of the file, an instruction about how to behave toward this host.
package robots

import (
	"strconv"
	"strings"
	"time"
)

// MaxSize is how much of a robots.txt is read. RFC 9309 requires at least 500
// KiB to be parsed and permits the rest to be ignored, which is what stops a
// file that is really a video from being a way to stall a crawler.
const MaxSize = 500 * 1024

// Rules are the groups in one robots.txt.
//
// The zero value allows everything, which is what an empty file, a file of
// comments, and a 404 all mean.
type Rules struct {
	groups []group
}

// group is one or more user-agent lines and the rules that follow them.
type group struct {
	// agents are the product tokens this group is addressed to, lowercased.
	agents []string

	rules []rule

	delay    time.Duration
	hasDelay bool
}

// rule is one allow or disallow line.
type rule struct {
	allow bool

	// pattern is the path pattern, which may contain * and may end in $.
	pattern string
}

// Parse reads a robots.txt.
//
// It never fails. A robots.txt is written by hand, served by whatever the site
// runs, and is frequently an HTML error page; a parser that refused one would
// be deciding to crawl a site that tried to tell it something. Lines it cannot
// read are skipped, which is what RFC 9309 requires.
func Parse(body []byte) *Rules {
	if len(body) > MaxSize {
		body = body[:MaxSize]
	}

	var (
		rules   Rules
		current *group
		inRules bool // whether the group being built has seen a rule yet
	)

	for _, line := range strings.Split(string(body), "\n") {
		field, value, ok := split(line)
		if !ok {
			continue
		}

		switch field {
		case "user-agent":
			if value == "" {
				continue
			}
			// Consecutive user-agent lines address one group. A user-agent
			// line after a rule starts a new one.
			if current == nil || inRules {
				rules.groups = append(rules.groups, group{})
				current = &rules.groups[len(rules.groups)-1]
				inRules = false
			}
			current.agents = append(current.agents, strings.ToLower(value))

		case "allow", "disallow":
			if current == nil {
				// Rules before any user-agent line are addressed to nobody.
				continue
			}
			inRules = true
			// An empty disallow is the documented way to say "nothing is
			// disallowed", so it is the absence of a rule rather than a rule
			// matching everything.
			if value == "" {
				continue
			}
			current.rules = append(current.rules, rule{allow: field == "allow", pattern: value})

		case "crawl-delay":
			if current == nil {
				continue
			}
			inRules = true
			if d, err := strconv.ParseFloat(value, 64); err == nil && d >= 0 {
				current.delay = time.Duration(d * float64(time.Second))
				current.hasDelay = true
			}
		}
	}

	return &rules
}

// split breaks a line into its field and value, dropping comments.
func split(line string) (field, value string, ok bool) {
	if i := strings.IndexByte(line, '#'); i >= 0 {
		line = line[:i]
	}

	name, rest, found := strings.Cut(line, ":")
	if !found {
		return "", "", false
	}

	field = strings.ToLower(strings.TrimSpace(name))
	if field == "" {
		return "", "", false
	}
	return field, strings.TrimSpace(rest), true
}

// Allowed reports whether agent may fetch path.
//
// The path is the URL's path and query, as it appears in the request. An empty
// path is treated as "/", because that is what a server is asked for.
func (r *Rules) Allowed(agent, path string) bool {
	if r == nil {
		return true
	}

	g := r.group(agent)
	if g == nil {
		return true
	}
	if path == "" {
		path = "/"
	}

	// The most specific rule wins, by pattern length, and allow wins a tie.
	// That order is the point of the rule: a site writes a broad disallow and
	// then allows the one directory it wants indexed.
	var (
		best    int
		allowed = true
	)
	for _, rule := range g.rules {
		if !matches(rule.pattern, path) {
			continue
		}
		if len(rule.pattern) > best || (len(rule.pattern) == best && rule.allow) {
			best, allowed = len(rule.pattern), rule.allow
		}
	}
	return allowed
}

// Delay is the crawl-delay this agent was asked for, and whether it was asked
// for at all. Nothing in the file is not the same as zero: a site that said
// nothing leaves the pacing to the crawler's own politeness.
func (r *Rules) Delay(agent string) (time.Duration, bool) {
	if r == nil {
		return 0, false
	}
	g := r.group(agent)
	if g == nil || !g.hasDelay {
		return 0, false
	}
	return g.delay, true
}

// group finds the rules addressed to this agent.
//
// Exact match first, then the longest prefix, then the catch-all. RFC 9309 asks
// only for a case-insensitive comparison of the product token, but every real
// file expects "Googlebot-News" to fall back to the "Googlebot" group, and a
// crawler that ignored that would follow rules written for somebody else.
func (r *Rules) group(agent string) *group {
	token := Token(agent)

	var (
		specific *group
		matched  int
		catchAll *group
	)

	for i := range r.groups {
		g := &r.groups[i]
		for _, want := range g.agents {
			switch {
			case want == "*":
				if catchAll == nil {
					catchAll = g
				}
			case want == token:
				return g
			case strings.HasPrefix(token, want) && len(want) > matched:
				specific, matched = g, len(want)
			}
		}
	}

	if specific != nil {
		return specific
	}
	return catchAll
}

// Token is the product token in a User-Agent string: the name a robots.txt
// addresses, without the version or the URL that usually follows it.
//
//	"scour (+https://github.com/rangertaha/scour)" -> "scour"
//	"Mozilla/5.0 (compatible; acme/2.1)"           -> "mozilla"
func Token(agent string) string {
	agent = strings.TrimSpace(agent)
	if i := strings.IndexAny(agent, " /\t"); i >= 0 {
		agent = agent[:i]
	}
	return strings.ToLower(agent)
}

// matches reports whether a path matches a robots.txt pattern.
//
// A pattern is a literal prefix, with two extensions that are not in the
// original standard and are in every file that matters: `*` stands for any
// sequence of characters, and a trailing `$` anchors the end.
func matches(pattern, path string) bool {
	if pattern == "" {
		return false
	}

	anchored := strings.HasSuffix(pattern, "$")
	if anchored {
		pattern = pattern[:len(pattern)-1]
	}

	parts := strings.Split(pattern, "*")

	// The first part is a prefix: robots.txt patterns always start at the
	// beginning of the path.
	if !strings.HasPrefix(path, parts[0]) {
		return false
	}
	rest := path[len(parts[0]):]

	for i := 1; i < len(parts); i++ {
		part := parts[i]

		if i == len(parts)-1 && anchored {
			// The last part has to land on the end of the path, and cannot
			// reuse characters an earlier part already matched.
			return len(rest) >= len(part) && strings.HasSuffix(rest, part)
		}

		at := strings.Index(rest, part)
		if at < 0 {
			return false
		}
		rest = rest[at+len(part):]
	}

	if anchored {
		return rest == ""
	}
	return true
}
