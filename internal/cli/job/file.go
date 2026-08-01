// SPDX-License-Identifier: GPL-3.0-or-later

package job

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/rangertaha/scour/internal/cli"
	"github.com/rangertaha/scour/internal/content"
	"github.com/rangertaha/scour/internal/store"
)

// file is a job as a config file.
//
// A job built from flags and a job built from a file are the same job, which is
// what makes the round trip worth having: `job config` writes a starting point,
// `job add -f` applies it, and `job show --toml` prints an existing job back in
// the same form. Anything assembled by flags can then go under version control,
// and anything under version control can be applied to a fresh machine.
type file struct {
	Name     string   `toml:"name"`
	Item     string   `toml:"item"`
	Depth    int      `toml:"depth"`
	MaxPages int      `toml:"max_pages"`
	MaxTime  string   `toml:"max_time"`
	Types    []string `toml:"types"`

	Domains []domainTarget `toml:"domain"`
	URLs    []urlTarget    `toml:"url"`
}

type domainTarget struct {
	Value      string `toml:"value"`
	Subdomains bool   `toml:"subdomains"`
	Depth      int    `toml:"depth"`
}

type urlTarget struct {
	Value string `toml:"value"`
	Depth int    `toml:"depth"`
}

// parseFile reads a job config, reporting an unreadable or malformed one.
func parseFile(path string) (*file, error) {
	var f file
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
			path, plural("key", len(keys)), strings.Join(keys, ", "))
	}
	return &f, nil
}

// validate reports everything wrong with a job config rather than the first
// thing. A config checker that stops at one fault turns fixing a file into as
// many runs as it has mistakes.
func (f *file) validate() []string {
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
	if _, err := f.maxTime(); err != nil {
		problems = append(problems, err.Error())
	}

	if len(f.Types) > 0 {
		if _, err := content.New(f.Types, nil); err != nil {
			problems = append(problems, "types: "+err.Error())
		}
	}

	if len(f.Domains) == 0 && len(f.URLs) == 0 {
		problems = append(problems, "no targets: a job with nowhere to look fetches nothing, so add a [[domain]] or a [[url]]")
	}
	for i, d := range f.Domains {
		if _, err := cli.NormaliseDomain(d.Value); err != nil {
			problems = append(problems, fmt.Sprintf("domain %d: %v", i+1, err))
		}
		if d.Depth < 0 {
			problems = append(problems, fmt.Sprintf("domain %d: depth is %d", i+1, d.Depth))
		}
	}
	for i, u := range f.URLs {
		if _, err := cli.NormaliseURL(u.Value); err != nil {
			problems = append(problems, fmt.Sprintf("url %d: %v", i+1, err))
		}
		if u.Depth < 0 {
			problems = append(problems, fmt.Sprintf("url %d: depth is %d", i+1, u.Depth))
		}
	}
	return problems
}

// maxTime is the run's time bound. An empty or absent value is no bound, which
// is the same thing a zero means everywhere else a bound is set.
func (f *file) maxTime() (time.Duration, error) {
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

// fileOf renders a stored job back as a config, so a job assembled by flags can
// be written to a file and kept.
func fileOf(job *store.Job, item string) *file {
	f := &file{
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
			f.Domains = append(f.Domains, domainTarget{
				Value: t.Value, Subdomains: t.Subdomains, Depth: t.Depth,
			})
		case store.TargetURL:
			f.URLs = append(f.URLs, urlTarget{Value: t.Value, Depth: t.Depth})
		}
	}
	sort.Slice(f.Domains, func(i, j int) bool { return f.Domains[i].Value < f.Domains[j].Value })
	sort.Slice(f.URLs, func(i, j int) bool { return f.URLs[i].Value < f.URLs[j].Value })
	return f
}

// render writes a job as TOML with the comments that say what each key means.
//
// Hand-written rather than marshalled, because an encoder drops the comments,
// and a config file a reader cannot understand without the documentation open
// beside it is most of the reason config files get copied wrong.
func (f *file) render() string {
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

// sample is the starting point `scour job config` prints. It is a real config
// rather than an empty one: a sample nobody can run teaches nothing.
func sample() *file {
	return &file{
		Name:     "uk",
		Item:     "vehicle",
		Depth:    3,
		MaxPages: 0,
		MaxTime:  "0s",
		Types:    []string{content.HTML, content.Feed},
		Domains: []domainTarget{
			{Value: "example.co.uk", Subdomains: true},
		},
		URLs: []urlTarget{
			{Value: "https://www.example.co.uk/used/"},
		},
	}
}

func plural(word string, n int) string {
	if n == 1 {
		return word
	}
	return word + "s"
}
