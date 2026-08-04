// SPDX-License-Identifier: GPL-3.0-or-later

// Package templates holds the job documents scour ships as starting points.
//
// They are files rather than strings in Go, embedded at build time, for two
// reasons. A job document is what a person reads and edits, so it should be
// edited here the same way: with an editor that knows HCL, not through escaped
// quotes. And a file can be validated by a test exactly as a user's document
// is, which is what keeps a shipped template from being a shipped mistake.
package templates

import (
	"bytes"
	"embed"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
	"text/template"
)

//go:embed files/*.hcl
var files embed.FS

// Template is one starting point.
type Template struct {
	// Name is what `--template` takes.
	Name string
	// Summary is one line about what it is for.
	Summary string
}

// Default is the template used when none is named. Deliberately the plainest
// one: somebody who has not chosen yet is better served by a document they can
// read in one sitting than by the most complete one.
const Default = "basic"

// summaries is what each template is for.
//
// Kept here rather than parsed out of a comment in the file, because a summary
// is a promise about the template and should break the build when it stops
// being true, which a test can check and a comment cannot.
var summaries = map[string]string{
	"basic":   "The plainest job that works. Start here",
	"news":    "Articles: headline, byline, dates, body",
	"product": "A shop: name, price, availability, images",
	"listing": "A directory of entries: jobs, venues, courses",
}

// Names lists what there is, sorted, with the default first.
//
// The default first because a list is read top down and that is the one most
// people want; the rest sorted because any other order is one somebody has to
// maintain.
func Names() []string {
	rest := make([]string, 0, len(summaries))
	for name := range summaries {
		if name != Default {
			rest = append(rest, name)
		}
	}
	sort.Strings(rest)
	return append([]string{Default}, rest...)
}

// All returns every template with its summary, in [Names] order.
func All() []Template {
	out := make([]Template, 0, len(summaries))
	for _, name := range Names() {
		out = append(out, Template{Name: name, Summary: summaries[name]})
	}
	return out
}

// Has reports whether a template exists.
func Has(name string) bool {
	_, ok := summaries[name]
	return ok
}

// Render returns a template with the job named.
//
// An unknown name lists what there is, because the alternative is somebody
// guessing twice.
func Render(name, job string) ([]byte, error) {
	if name == "" {
		name = Default
	}
	if !Has(name) {
		return nil, fmt.Errorf("no template %q, have %s", name, strings.Join(Names(), ", "))
	}
	if strings.TrimSpace(job) == "" {
		job = "example"
	}

	src, err := files.ReadFile(path.Join("files", name+".hcl"))
	if err != nil {
		return nil, fmt.Errorf("template %q: %w", name, err)
	}

	// quote rather than %q in the file, so a job name with a quote in it
	// cannot break out and produce a document that parses as something else.
	t, err := template.New(name).Funcs(template.FuncMap{
		"quote": strconv.Quote,
	}).Parse(string(src))
	if err != nil {
		return nil, fmt.Errorf("template %q: %w", name, err)
	}

	var out bytes.Buffer
	if err := t.Execute(&out, struct{ Name string }{job}); err != nil {
		return nil, fmt.Errorf("template %q: %w", name, err)
	}
	return out.Bytes(), nil
}

// fileNames lists the embedded files, so a test can check the summaries and
// the files have not drifted apart.
func fileNames() ([]string, error) {
	entries, err := fs.ReadDir(files, "files")
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, strings.TrimSuffix(e.Name(), ".hcl"))
	}
	sort.Strings(out)
	return out, nil
}
