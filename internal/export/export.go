// SPDX-License-Identifier: GPL-3.0-or-later

// Package export writes extracted records somewhere useful.
//
// A crawler that can only be queried through its own command line is a dead
// end: the records are the product, and they belong in whatever the rest of a
// pipeline reads. The formats here are deliberately boring, because the point
// is to hand off rather than to be interesting.
//
// Records are grouped by the domain they came from. One file per site is what
// makes an export diffable and re-runnable: a site that changed shows up as a
// changed file rather than as a diff across everything ever crawled.
package export

import (
	"context"
	"net/url"
	"sort"
	"strings"

	"github.com/rangertaha/scour/internal/registry"

	"github.com/rangertaha/scour/internal/store"
)

// Config is what an exporter is built from.
type Config struct {
	// Dir is where file-based exporters write.
	Dir string
	// URL is where the webhook exporter posts.
	URL string
	// TokenEnv names the environment variable holding a bearer token for the
	// webhook. As with the server's own auth, the secret is not written into
	// configuration.
	TokenEnv string
	// Timestamp names the export, so re-running on the same day overwrites
	// rather than accumulating. It is passed in rather than read from the
	// clock so that a caller can make an export reproducible.
	Timestamp string
}

// Result reports what an export did.
type Result struct {
	// Records is how many rows were written.
	Records int
	// Destinations are the files written or endpoints posted to, in order.
	Destinations []string
}

// Exporter writes records out.
type Exporter interface {
	// Name identifies the format.
	Name() string
	// Export writes an item's records, grouped as the implementation sees
	// fit, and reports where they went.
	Export(ctx context.Context, item string, rows []store.RecordRow) (*Result, error)
}

// Default is the format an export writes when none is named.
const Default = "csv"

// reg holds the implementations. See internal/registry for the shape every
// extension point in scour shares, and for how to add one.
var reg = registry.New[Config, Exporter]("exporter").Default(Default)

// Register adds an implementation, from init.
func Register(name string, f registry.Factory[Config, Exporter]) { reg.Register(name, f) }

// New builds a registered implementation.
func New(name string, cfg Config) (Exporter, error) { return reg.New(name, cfg) }

// Names lists what is registered.
func Names() []string { return reg.Names() }

// Has reports whether a name is registered.
func Has(name string) bool { return reg.Has(name) }

// byDomain groups records by the host they were extracted from.
//
// A record whose URL cannot be parsed is not dropped. Losing data because a
// URL is odd would be the worst possible trade in an exporter, so it is filed
// under a name that is obviously not a domain.
func byDomain(rows []store.RecordRow) map[string][]store.RecordRow {
	out := map[string][]store.RecordRow{}
	for _, row := range rows {
		out[domainOf(row.URL)] = append(out[domainOf(row.URL)], row)
	}
	return out
}

const unknownDomain = "unknown"

func domainOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return unknownDomain
	}
	return strings.ToLower(u.Hostname())
}

// columns is the union of every property name across the records, sorted.
//
// Taking the union rather than the first record's keys matters because
// extraction is per page: one page may carry a field another omits, and a
// column that appeared only in row 500 would otherwise be silently dropped.
func columns(rows []store.RecordRow) []string {
	seen := map[string]bool{}
	for _, row := range rows {
		for name := range row.Values {
			seen[name] = true
		}
	}

	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// domains returns the group keys in a stable order, so an export writes the
// same files in the same order every time.
func domains(groups map[string][]store.RecordRow) []string {
	out := make([]string, 0, len(groups))
	for domain := range groups {
		out = append(out, domain)
	}
	sort.Strings(out)
	return out
}
