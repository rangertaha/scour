// SPDX-License-Identifier: GPL-3.0-or-later

package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/hashicorp/hcl/v2/hclparse"
)

// Spec is what a spider needs and nothing else: the shapes to extract.
//
// A spider is handed this rather than the job it came from. It has no business
// knowing where bodies are cached, what the crawl budget is, which exporters
// are attached or what happens on resubmission, and handing it the whole job
// would make every one of those look like something it might depend on.
//
// # Why it is separable
//
// A spider somebody else wrote, in a language that is not Go, is a subscriber
// on the bus. It cannot import this package, so the spec has to travel as
// bytes, and it has to be the same bytes whoever is reading. [Spec.HCL] renders
// it as the text a person would have written, which is the form the job was
// submitted in and therefore the form an author can check against what they
// meant.
//
// # Why it is versioned
//
// Resubmitting a job mutates it, so the shape can change while the crawl runs.
// A record extracted under one shape and attributed to another is wrong in a
// way nothing downstream can detect, so every spec carries a [Spec.Fingerprint]
// and the record says which one it was read under.
type Spec struct {
	// Job is the name of the job this came from.
	Job string `json:"job" hcl:"job,optional"`

	// Items are the shapes to extract.
	Items []*Item `json:"items" hcl:"item,block"`
}

// Spec returns what a spider needs to extract this job's items.
func (j *Job) Spec() *Spec {
	return &Spec{Job: j.Name, Items: j.Items}
}

// Item returns one shape by name.
func (s *Spec) Item(name string) (*Item, bool) {
	for _, item := range s.Items {
		if item.Name == name {
			return item, true
		}
	}
	return nil, false
}

// Names lists the shapes, in the order they were written.
func (s *Spec) Names() []string {
	out := make([]string, 0, len(s.Items))
	for _, item := range s.Items {
		out = append(out, item.Name)
	}
	return out
}

// Fingerprint identifies this shape, and changes exactly when the shape does.
//
// Not a version number a person maintains, because they would forget. Not the
// bytes of the document either: reordering properties or renaming a job does
// not change what is extracted, and a fingerprint that moved when they did
// would make every cosmetic edit look like a schema change and force a
// re-extraction nobody needed.
func (s *Spec) Fingerprint() string {
	prints := make([]string, 0, len(s.Items))
	for _, item := range s.Items {
		prints = append(prints, item.Name+"="+item.fingerprint())
	}
	// Sorted, so the order items appear in does not change the answer.
	sort.Strings(prints)

	sum := sha256.Sum256([]byte(strings.Join(prints, "\n")))
	return hex.EncodeToString(sum[:16])
}

// HCL renders the spec as the text a person would have written.
//
// Types and transforms come out quoted rather than as the bare words a document
// may use. Both parse to the same value, and quoted is the better choice for
// generated text: a reader in another language can take it as a string without
// having to know which bare words this version happens to predeclare.
func (s *Spec) HCL() []byte {
	var b strings.Builder

	fmt.Fprintf(&b, "# Extraction spec for job %s, fingerprint %s.\n", HCLString(s.Job), s.Fingerprint())
	b.WriteString("# Generated. The job document is the source.\n")

	// The job's name as an attribute and not only in the comment above it.
	//
	// [Spec.Job] is declared `hcl:"job,optional"`, so ParseSpec reads one -
	// and nothing ever wrote one, so every spec a spider parsed came back with
	// an empty Job. A comment is for a person; the field is what the format
	// promises, and the two disagreed. See
	// TestARenderedSpecReadsBackAsItself.
	if s.Job != "" {
		fmt.Fprintf(&b, "\njob = %s\n", HCLString(s.Job))
	}

	for _, item := range s.Items {
		b.WriteString("\n")
		writeItem(&b, item, 0)
	}
	return []byte(b.String())
}

func writeItem(b *strings.Builder, item *Item, depth int) {
	pad := strings.Repeat("  ", depth)

	fmt.Fprintf(b, "%sitem %s {\n", pad, HCLString(item.Name))

	// The resolved type, never the raw field. A spec is what a spider in
	// another language is handed, and it is the whole of what that spider
	// gets: a property that relied on a default came out with no type at all,
	// so the reader had to know scour's defaulting rules to make sense of a
	// document whose point is that it does not need them. An object property
	// was the worst of it, since its type is inferred from having children and
	// nothing in the spec said so.
	writeAttr(b, depth+1, "type", string(item.ItemType()))
	writeAttr(b, depth+1, "of", item.Of)
	writeAttr(b, depth+1, "time", item.Time)
	writeAttr(b, depth+1, "description", item.Description)
	for _, p := range item.Properties {
		b.WriteString("\n")
		writeProperty(b, p, depth+1)
	}
	for _, r := range item.Relations {
		b.WriteString("\n")
		writeRelation(b, r, depth+1)
	}
	fmt.Fprintf(b, "%s}\n", pad)
}

func writeProperty(b *strings.Builder, p *Property, depth int) {
	pad := strings.Repeat("  ", depth)

	fmt.Fprintf(b, "%sproperty %s {\n", pad, HCLString(p.Name))
	writeAttr(b, depth+1, "type", string(p.PropertyType()))
	writeAttr(b, depth+1, "entity", p.Entity)
	writeAttr(b, depth+1, "description", p.Description)
	if p.Required {
		fmt.Fprintf(b, "%s  required = true\n", pad)
	}
	if p.Tag {
		fmt.Fprintf(b, "%s  tag = true\n", pad)
	}
	writeList(b, depth+1, "aliases", p.Aliases)
	writeList(b, depth+1, "examples", p.Examples)
	writeList(b, depth+1, "regexes", p.Regexes)
	writeList(b, depth+1, "transforms", p.Transforms)
	writeList(b, depth+1, "xpath", p.XPath)
	writeList(b, depth+1, "css", p.CSS)

	for _, nested := range p.Properties {
		b.WriteString("\n")
		writeProperty(b, nested, depth+1)
	}
	fmt.Fprintf(b, "%s}\n", pad)
}

func writeRelation(b *strings.Builder, r *Relation, depth int) {
	pad := strings.Repeat("  ", depth)

	fmt.Fprintf(b, "%srelation %s {\n", pad, HCLString(r.Name))
	writeAttr(b, depth+1, "entity", r.Entity)
	writeAttr(b, depth+1, "property", r.Property)
	writeList(b, depth+1, "topic", r.Topic)

	// What the edge says about itself, so a spider in another language reading
	// the rendered spec learns those properties exist. Omitting them meant the
	// spec described an edge with nothing on it.
	for _, p := range r.Properties {
		writeProperty(b, p, depth+1)
	}

	fmt.Fprintf(b, "%s}\n", pad)
}

func writeAttr(b *strings.Builder, depth int, name, value string) {
	if value == "" {
		return
	}
	// HCL quoting, not Go's. This renders a document a spider in another
	// language parses, and the two agree until a value contains `${`, which HCL
	// reads as an interpolation: an induced selector like
	// meta[name="og:title-${id}"] - which train.Write escapes correctly on its
	// way into the job document - came back out of `scour job spec` as a spec
	// that will not parse. Two writers were fixed for this and this is the
	// third; see [HCLString].
	fmt.Fprintf(b, "%s%s = %s\n", strings.Repeat("  ", depth), name, HCLString(value))
}

func writeList(b *strings.Builder, depth int, name string, values []string) {
	if len(values) == 0 {
		return
	}
	quoted := make([]string, 0, len(values))
	for _, v := range values {
		// See [writeAttr]: a css or xpath value is the likeliest place for a
		// `${` to turn up, because that is what an induced selector picks up
		// off a page whose template did not render.
		quoted = append(quoted, HCLString(v))
	}
	fmt.Fprintf(b, "%s%s = [%s]\n", strings.Repeat("  ", depth), name, strings.Join(quoted, ", "))
}

// ParseSpec reads a spec back, which is what a spider does with what it was
// handed.
func ParseSpec(src []byte, filename string) (*Spec, error) {
	parser := hclparse.NewParser()

	parsed, diags := parser.ParseHCL(src, filename)
	if diags.HasErrors() {
		return nil, diagError(diags)
	}

	var spec Spec
	if diags := gohcl.DecodeBody(parsed.Body, evalContext(""), &spec); diags.HasErrors() {
		return nil, diagError(diags)
	}
	return &spec, nil
}

// Validate checks a spec on its own, which is what a spider does with one it
// was handed rather than one it built.
func (s *Spec) Validate() error {
	var problems []error

	if len(s.Items) == 0 {
		problems = append(problems, fmt.Errorf("spec for job %q: no items", s.Job))
	}

	seen := map[string]bool{}
	for _, item := range s.Items {
		if seen[item.Name] {
			problems = append(problems, fmt.Errorf("item %q: declared twice", item.Name))
		}
		seen[item.Name] = true
		problems = append(problems, item.validate()...)
	}

	return joinErrors(problems)
}
