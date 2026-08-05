// SPDX-License-Identifier: GPL-3.0-or-later

// Package urls decides when two addresses are the same page.
//
// It is the whole of what deduplication means. A crawler that treats
// `example.com/a`, `example.com/a?utm_source=x` and `example.com/a#top` as
// three pages fetches one page three times, on somebody else's server, and
// stores three copies of it. One that treats `?page=2` as noise loses half the
// site. The line between those two mistakes is this package, and it is drawn
// conservatively: only transformations that cannot change what a server returns
// are applied by default.
//
// # What is safe, and what only looks safe
//
// Lowercasing the scheme and host is safe: both are case-insensitive by
// specification. Dropping the fragment is safe: it is never sent to the server.
// Dropping a default port is safe. Resolving `.` and `..` is safe, because that
// is what a server does with them.
//
// Lowercasing the path is not safe, and neither is sorting query parameters,
// stripping a trailing slash, or removing an empty query. Every one of those
// changes a URL a server is entitled to treat as different, and some do. They
// are available, because on most sites they are right and the saving is large,
// but a job has to ask.
package urls

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"path"
	"sort"
	"strings"
)

// Options are the transformations a job is willing to treat as noise.
//
// The zero value applies only what cannot change a server's answer, which is
// the right default for a crawler that has never seen the site before.
type Options struct {
	// StripQuery removes named parameters entirely. Tracking parameters are
	// the reason: they are added by whoever linked to a page and say nothing
	// about which page it is.
	StripQuery []string

	// SortQuery orders the remaining parameters. Safe on nearly every site and
	// wrong on the ones that read the query as an ordered list.
	SortQuery bool

	// StripTrailingSlash treats /a/ and /a as one page. True of most servers
	// and not all of them.
	StripTrailingSlash bool

	// LowerPath treats /A and /a as one page. True on Windows origins and on
	// nothing else, so it is off unless a job knows.
	LowerPath bool
}

// Tracking is the set of parameters that are added by whoever linked to a page
// rather than by whoever wrote it. Offered as a default for [Options.StripQuery]
// so that every job does not have to rediscover the list.
var Tracking = []string{
	"utm_source", "utm_medium", "utm_campaign", "utm_term", "utm_content",
	"utm_id", "utm_source_platform", "utm_creative_format",
	"gclid", "gbraid", "wbraid", "dclid", "fbclid", "msclkid", "twclid",
	"mc_cid", "mc_eid", "igshid", "ref_src", "ref_url",
	"_ga", "_gl", "yclid", "ttclid",
}

// Normalise returns the canonical form of a URL, and the error a URL that is
// not one produces.
//
// The result is absolute, so a caller can compare it, hash it and fetch it
// without wondering which of those it is safe for.
func Normalise(raw string, opts Options) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("urls: %q: %w", raw, err)
	}
	return normalise(parsed, opts)
}

// Resolve turns a link found on a page into an absolute URL, normalised.
//
// This is what a spider does with every href it finds, and the reason it takes
// the page's own URL: most links on the web are relative, and a crawler that
// could not resolve them would only ever see the page it started on.
func Resolve(base, href string, opts Options) (string, error) {
	from, err := url.Parse(strings.TrimSpace(base))
	if err != nil {
		return "", fmt.Errorf("urls: base %q: %w", base, err)
	}
	link, err := from.Parse(strings.TrimSpace(href))
	if err != nil {
		return "", fmt.Errorf("urls: %q on %q: %w", href, base, err)
	}
	return normalise(link, opts)
}

func normalise(u *url.URL, opts Options) (string, error) {
	// Scheme and host are case-insensitive by specification, so folding them
	// cannot change what a server returns.
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)

	switch u.Scheme {
	case "http", "https":
	case "":
		return "", fmt.Errorf("urls: %q has no scheme", u.String())
	default:
		// Not a refusal on politeness grounds but on plain capability: this
		// crawler speaks HTTP and a URL it cannot fetch is not a URL it should
		// be queueing.
		return "", fmt.Errorf("urls: %q: scheme %q is not one this fetches", u.String(), u.Scheme)
	}
	if u.Host == "" {
		return "", fmt.Errorf("urls: %q has no host", u.String())
	}

	// A default port is what the scheme already means.
	if (u.Scheme == "http" && strings.HasSuffix(u.Host, ":80")) ||
		(u.Scheme == "https" && strings.HasSuffix(u.Host, ":443")) {
		u.Host = u.Host[:strings.LastIndex(u.Host, ":")]
	}

	// Never sent to the server, so it cannot identify a page.
	u.Fragment = ""
	u.RawFragment = ""

	// Dot segments, resolved the way a server resolves them. url.Parse does not
	// do this: it only happens when one URL is resolved against another, and a
	// URL that arrived already absolute never was.
	u.Path = clean(u.Path)
	if opts.LowerPath {
		u.Path = strings.ToLower(u.Path)
	}
	if opts.StripTrailingSlash && len(u.Path) > 1 {
		u.Path = strings.TrimRight(u.Path, "/")
		if u.Path == "" {
			u.Path = "/"
		}
	}

	if u.RawQuery != "" {
		u.RawQuery = query(u.RawQuery, opts)
	}

	// Not the credentials. A URL that carries them is a URL that would put them
	// in a database, a log line and a cache key, and the frontier is not where
	// a password should live.
	u.User = nil

	return u.String(), nil
}

// clean applies RFC 3986's remove_dot_segments, keeping the trailing slash that
// path.Clean throws away. /a/ and /a are different pages to some servers, and
// resolving dots must not be the thing that decides they are not.
func clean(p string) string {
	if p == "" {
		return "/"
	}
	if !strings.Contains(p, "./") && !strings.HasSuffix(p, "/.") && !strings.HasSuffix(p, "/..") {
		return p
	}

	trailing := strings.HasSuffix(p, "/")
	cleaned := path.Clean(p)
	if trailing && !strings.HasSuffix(cleaned, "/") {
		cleaned += "/"
	}
	if !strings.HasPrefix(cleaned, "/") {
		cleaned = "/" + cleaned
	}
	return cleaned
}

func query(raw string, opts Options) string {
	values, err := url.ParseQuery(raw)
	if err != nil {
		// Unparseable, so it is left exactly as it arrived: a query this does
		// not understand is still one the server might.
		return raw
	}

	for _, name := range opts.StripQuery {
		delete(values, name)
	}
	if len(values) == 0 {
		return ""
	}

	if opts.SortQuery {
		// url.Values.Encode already sorts by key, and sorts each key's values
		// too, which is the part that makes ?a=2&a=1 and ?a=1&a=2 one page.
		for _, list := range values {
			sort.Strings(list)
		}
		return values.Encode()
	}

	// Order preserved, which means rebuilding it by hand: Encode always sorts.
	var b strings.Builder
	for _, pair := range strings.Split(raw, "&") {
		name, _, _ := strings.Cut(pair, "=")
		if _, kept := values[url.QueryEscape(name)]; !kept {
			if _, kept := values[name]; !kept {
				continue
			}
		}
		if b.Len() > 0 {
			b.WriteByte('&')
		}
		b.WriteString(pair)
	}
	return b.String()
}

// Hash identifies a normalised URL for deduplication.
//
// A digest rather than the URL, because this is a primary key in every store
// that holds one and a URL has no length limit worth trusting. Truncated to 128
// bits: a crawl that reached the birthday bound on that would have to hold
// about 10^19 URLs.
func Hash(normalised string) string {
	sum := sha256.Sum256([]byte(normalised))
	return hex.EncodeToString(sum[:16])
}

// Host is what politeness is paced against: the host and port, lowercased,
// without the userinfo.
func Host(normalised string) string {
	parsed, err := url.Parse(normalised)
	if err != nil {
		return ""
	}
	return strings.ToLower(parsed.Host)
}

// Domain is the registrable part of a host, as far as this needs one: the last
// two labels.
//
// Deliberately not a public-suffix lookup. That needs a list that goes stale,
// and the only thing this is used for is grouping hosts under a job's
// `domains`, where a job says what it means anyway.
func Domain(host string) string {
	host = strings.ToLower(host)
	if i := strings.LastIndex(host, ":"); i >= 0 {
		host = host[:i]
	}
	labels := strings.Split(host, ".")
	if len(labels) < 2 {
		return host
	}
	return strings.Join(labels[len(labels)-2:], ".")
}
