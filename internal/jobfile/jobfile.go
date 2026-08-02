// SPDX-License-Identifier: GPL-3.0-or-later

// Package jobfile is a job as a document: parsed, checked, and rendered back.
//
// It sits below both surfaces that carry a whole job in one request, because
// both have to agree on what a job document says. It began inside the command
// line's job package, where the HTTP API could not reach it without importing
// the CLI, and an API that reimplemented the parse and the checks would drift
// from the CLI within a release: the two would disagree about which configs are
// valid, which is the one thing a config format cannot afford.
package jobfile

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/rangertaha/scour/internal/content"
	"github.com/rangertaha/scour/internal/normalise"
	"github.com/rangertaha/scour/internal/store"
)

// File is a job as a config file.
//
// A job built from flags and a job built from a file are the same job, which is
// what makes the round trip worth having: `job config` writes a starting point,
// `job add -f` applies it, and `job show --toml` prints an existing job back in
// the same form. Anything assembled by flags can then go under version control,
// and anything under version control can be applied to a fresh machine.
//
// The JSON tags carry the same document over HTTP, where the body of a job
// create is this type and nothing else. One type for both means a config that
// applies through the CLI applies through the API, field for field, rather than
// through a parallel request struct that has to be kept in step by hand.
type File struct {
	Name     string   `toml:"name" json:"name"`
	Item     string   `toml:"item" json:"item"`
	Depth    int      `toml:"depth" json:"depth,omitempty"`
	MaxPages int      `toml:"max_pages" json:"max_pages,omitempty"`
	MaxTime  string   `toml:"max_time" json:"max_time,omitempty"`
	Types    []string `toml:"types" json:"types,omitempty"`

	Domains []DomainTarget `toml:"domain" json:"domains,omitempty"`
	URLs    []URLTarget    `toml:"url" json:"urls,omitempty"`
}

// DomainTarget is a whole site, and how far into it to go.
type DomainTarget struct {
	Value      string `toml:"value" json:"value"`
	Subdomains bool   `toml:"subdomains" json:"subdomains,omitempty"`
	Depth      int    `toml:"depth" json:"depth,omitempty"`
}

// URLTarget is a single page, and what sits under its directory.
type URLTarget struct {
	Value string `toml:"value" json:"value"`
	Depth int    `toml:"depth" json:"depth,omitempty"`
}

// Parse reads a job config, reporting an unreadable or malformed one.
func Parse(path string) (*File, error) {
	var f File
	meta, err := toml.DecodeFile(path, &f)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	// A misspelled key is the whole failure mode of a config file: it parses,
	// applies, and does nothing the author asked for. Undecoded keys are named
	// rather than ignored.
	if undecoded := meta.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, 0, len(undecoded))
		for _, k := range undecoded {
			keys = append(keys, k.String())
		}
		sort.Strings(keys)
		return nil, fmt.Errorf("%s: unknown %s: %s",
			path, Plural("key", len(keys)), strings.Join(keys, ", "))
	}
	return &f, nil
}

// Validate reports everything wrong with a job config rather than the first
// thing. A config checker that stops at one fault turns fixing a file into as
// many runs as it has mistakes.
//
// This is the check for a document that is meant to be the whole job, which is
// what a file is: a config describing a job with nowhere to look is a mistake in
// the file rather than a job waiting to be finished.
func (f *File) Validate() []string {
	problems := f.ValidateFields()
	if len(f.Domains) == 0 && len(f.URLs) == 0 {
		problems = append(problems, "no targets: a job with nowhere to look fetches nothing, so add a [[domain]] or a [[url]]")
	}
	return problems
}

// ValidateFields is Validate without the requirement that the job has somewhere
// to look.
//
// It exists for the HTTP API, where targets are a sub-resource and the request
// that adds them is a separate one: rejecting a create for having no targets
// there would make `POST /v1/jobs` unable to express `scour job add uk -i
// vehicle`, which is a legal and ordinary thing to do. Nothing is lost by
// allowing it, because a run of a job with no targets is refused when it is
// started, which is the moment the absence actually matters.
func (f *File) ValidateFields() []string {
	var problems []string

	if strings.TrimSpace(f.Name) == "" {
		problems = append(problems, "name is required: it is what every other job command takes")
	}
	if strings.TrimSpace(f.Item) == "" {
		problems = append(problems, "item is required: a job hunts for exactly one, and it must already exist")
	}
	if f.Depth < 0 {
		problems = append(problems, fmt.Sprintf("depth is %d: it counts links out from a target, so it cannot be negative", f.Depth))
	}
	if f.MaxPages < 0 {
		problems = append(problems, fmt.Sprintf("max_pages is %d: use 0 for no bound", f.MaxPages))
	}
	if _, err := f.TimeBound(); err != nil {
		problems = append(problems, err.Error())
	}

	if len(f.Types) > 0 {
		if _, err := content.New(f.Types, nil); err != nil {
			problems = append(problems, "types: "+err.Error())
		}
	}

	for i, d := range f.Domains {
		if _, err := normalise.Domain(d.Value); err != nil {
			problems = append(problems, fmt.Sprintf("domain %d: %v", i+1, err))
		}
		if d.Depth < 0 {
			problems = append(problems, fmt.Sprintf("domain %d: depth is %d", i+1, d.Depth))
		}
	}
	for i, u := range f.URLs {
		if _, err := normalise.URL(u.Value); err != nil {
			problems = append(problems, fmt.Sprintf("url %d: %v", i+1, err))
		}
		if u.Depth < 0 {
			problems = append(problems, fmt.Sprintf("url %d: depth is %d", i+1, u.Depth))
		}
	}
	return problems
}

// TimeBound is max_time parsed. An empty or absent value is no bound, which is
// the same thing a zero means everywhere else a bound is set.
func (f *File) TimeBound() (time.Duration, error) {
	s := strings.TrimSpace(f.MaxTime)
	if s == "" || s == "0" {
		return 0, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("max_time %q is not a duration: try 30m, 2h, 90s", f.MaxTime)
	}
	if d < 0 {
		return 0, fmt.Errorf("max_time %q is negative: use 0 for no bound", f.MaxTime)
	}
	return d, nil
}

// Of renders a stored job back as a config, so a job assembled by flags can be
// written to a file and kept.
func Of(job *store.Job, item string) *File {
	f := &File{
		Name:     job.Name,
		Item:     item,
		Depth:    job.Depth,
		MaxPages: job.MaxPages,
	}
	if job.MaxTime > 0 {
		f.MaxTime = time.Duration(job.MaxTime).String()
	}
	for _, t := range job.ContentTypes {
		f.Types = append(f.Types, t.Type)
	}
	sort.Strings(f.Types)

	for _, t := range job.Targets {
		switch t.Kind {
		case store.TargetDomain:
			f.Domains = append(f.Domains, DomainTarget{
				Value: t.Value, Subdomains: t.Subdomains, Depth: t.Depth,
			})
		case store.TargetURL:
			f.URLs = append(f.URLs, URLTarget{Value: t.Value, Depth: t.Depth})
		}
	}
	sort.Slice(f.Domains, func(i, j int) bool { return f.Domains[i].Value < f.Domains[j].Value })
	sort.Slice(f.URLs, func(i, j int) bool { return f.URLs[i].Value < f.URLs[j].Value })
	return f
}

// Render writes a job as TOML with the comments that say what each key means.
//
// Hand-written rather than marshalled, because an encoder drops the comments,
// and a config file a reader cannot understand without the documentation open
// beside it is most of the reason config files get copied wrong.
func (f *File) Render() string {
	var b strings.Builder

	b.WriteString("# A scour job: where to look for one item, and the bounds on looking.\n")
	b.WriteString("# Apply it with: scour job add -f <this file>\n\n")

	fmt.Fprintf(&b, "name = %q  # unique across all jobs; every job command takes it\n", f.Name)
	fmt.Fprintf(&b, "item = %q  # the item this job hunts for; it must already exist\n\n", f.Item)

	b.WriteString("# Bounds. Zero, or absent, means no bound.\n")
	fmt.Fprintf(&b, "depth     = %d      # how many links out from a target to follow\n", f.Depth)
	fmt.Fprintf(&b, "max_pages = %d      # stop a run after this many pages\n", f.MaxPages)
	maxTime := f.MaxTime
	if maxTime == "" {
		maxTime = "0s"
	}
	fmt.Fprintf(&b, "max_time  = %q   # stop a run after this long, keeping what it fetched\n\n", maxTime)

	b.WriteString("# Content types this job may fetch. Empty allows the configured default.\n")
	if len(f.Types) == 0 {
		b.WriteString("types = []\n")
	} else {
		quoted := make([]string, 0, len(f.Types))
		for _, t := range f.Types {
			quoted = append(quoted, fmt.Sprintf("%q", t))
		}
		fmt.Fprintf(&b, "types = [%s]\n", strings.Join(quoted, ", "))
	}

	if len(f.Domains) > 0 {
		b.WriteString("\n# Whole sites.\n")
		for _, d := range f.Domains {
			b.WriteString("\n[[domain]]\n")
			fmt.Fprintf(&b, "value      = %q\n", d.Value)
			fmt.Fprintf(&b, "subdomains = %t\n", d.Subdomains)
			fmt.Fprintf(&b, "depth      = %d  # 0 takes the job's depth\n", d.Depth)
		}
	}
	if len(f.URLs) > 0 {
		b.WriteString("\n# Single pages, and what sits under their directory.\n")
		for _, u := range f.URLs {
			b.WriteString("\n[[url]]\n")
			fmt.Fprintf(&b, "value = %q\n", u.Value)
			fmt.Fprintf(&b, "depth = %d  # 0 takes the job's depth\n", u.Depth)
		}
	}
	return b.String()
}

// Sample is the starting point `scour job config` prints. It is a real config
// rather than an empty one: a sample nobody can run teaches nothing.
func Sample() *File {
	return &File{
		Name:     "uk",
		Item:     "vehicle",
		Depth:    3,
		MaxPages: 0,
		MaxTime:  "0s",
		Types:    []string{content.HTML, content.Feed},
		Domains: []DomainTarget{
			{Value: "example.co.uk", Subdomains: true},
		},
		URLs: []URLTarget{
			{Value: "https://www.example.co.uk/used/"},
		},
	}
}

// Plural is exported because the callers that count a config's problems are in
// other packages now, and three copies of this would be three chances for one
// of them to say "1 problems".
func Plural(word string, n int) string {
	if n == 1 {
		return word
	}
	return word + "s"
}
