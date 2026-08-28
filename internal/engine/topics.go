// SPDX-License-Identifier: GPL-3.0-or-later

package engine

import (
	"fmt"
	"strings"

	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/hashicorp/hcl/v2/hclparse"
)

// A labels document is what a topic is trained from.
//
// # Why the labels are a file and not state inside the tool
//
// Because they are a judgement somebody made, and judgements are the thing you
// most want to be able to read back, diff and argue with six months later. A
// classifier trained from a database nobody can see is one whose mistakes
// cannot be traced to the decision that caused them: it says a page is about
// climate and there is nowhere to go and look at why.
//
// So this is a file beside the job document, the same as everything else here.
// `scour topic propose` writes into it and a person edits it, which is exactly
// the loop `scour job train` already uses for locators: propose, review, write
// back. The tool is never the author of what it learns from.
//
// # Why seed terms are a bootstrap and not the classifier
//
// Labelling a few hundred pages by hand is what stops anybody training
// anything, so the terms give a first pass: a page holding enough of them is
// proposed as an example. That is a worse classifier than the one being
// trained, and it is meant to be. It is a starting point somebody corrects, and
// what they correct is what wins, because a model that only ever learned what
// it was already told would have been a term list all along.
//
// This is not a job document. A job says which trained topic it uses, by name
// and version; it never says what the topic was learned from, so that retraining
// cannot silently change what a job does.
type Topics struct {
	Topics []*TopicLabels `hcl:"topic,block" json:"topics"`
}

// TopicLabels is one subject and the pages that do and do not show it.
type TopicLabels struct {
	Name string `hcl:"name,label" json:"name"`

	// Terms bootstrap the first pass: pages holding enough of them are
	// proposed. They are a starting point, not the answer, and a topic that
	// has been labelled does not need them any more.
	Terms []string `hcl:"terms,optional" json:"terms,omitempty"`

	// About and Not are the pages, by URL, that a person says are and are not
	// the subject.
	//
	// URLs rather than cache keys, because a key is a hash and a file full of
	// hashes is a file nobody can review, which would defeat the whole reason
	// this is a file.
	About []string `hcl:"about,optional" json:"about,omitempty"`
	Not   []string `hcl:"not,optional" json:"not,omitempty"`
}

// Least is the share of a page's terms that has to match for the bootstrap to
// propose it.
//
// Deliberately not configurable. It is a threshold on a classifier nobody is
// meant to keep, and a knob here would invite tuning the bootstrap instead of
// correcting the labels.
const Least = 0.35

// ParseTopics reads a labels document.
func ParseTopics(src []byte, filename string) (*Topics, error) {
	parser := hclparse.NewParser()

	parsed, diags := parser.ParseHCL(src, filename)
	if diags.HasErrors() {
		return nil, diagError(diags)
	}

	var doc Topics
	if diags := gohcl.DecodeBody(parsed.Body, evalContext(), &doc); diags.HasErrors() {
		// The mistake worth naming, for the reason ParseService gives: both are
		// HCL, both are .hcl, and both live beside each other.
		var job Document
		if d := gohcl.DecodeBody(parsed.Body, evalContext(), &job); !d.HasErrors() && len(job.Jobs) > 0 {
			return nil, fmt.Errorf(
				"%s is a job document, and a labels document is a different thing: "+
					"it holds `topic` blocks saying which pages are and are not a subject", filename)
		}
		return nil, diagError(diags)
	}
	return &doc, nil
}

// Validate reports everything wrong at once, the way a job document does.
func (t *Topics) Validate() error {
	var problems []error

	if len(t.Topics) == 0 {
		problems = append(problems, fmt.Errorf(
			"a labels document has no topics in it. Add a `topic \"name\"` block"))
	}

	seen := map[string]bool{}
	for _, one := range t.Topics {
		name := strings.TrimSpace(one.Name)
		if name == "" {
			problems = append(problems, fmt.Errorf("a topic has no name"))
			continue
		}
		if seen[name] {
			problems = append(problems, fmt.Errorf("topic %q is declared twice", name))
		}
		seen[name] = true

		if len(one.Terms) == 0 && len(one.About) == 0 {
			problems = append(problems, fmt.Errorf(
				"topic %q has neither terms to bootstrap from nor pages labelled as the subject, "+
					"so there is nothing to learn from", name))
		}

		// A page on both lists is a person having changed their mind and not
		// finished, and guessing which they meant is exactly the kind of quiet
		// decision that makes a model untraceable.
		about := map[string]bool{}
		for _, url := range one.About {
			about[url] = true
		}
		for _, url := range one.Not {
			if about[url] {
				problems = append(problems, fmt.Errorf(
					"topic %q lists %s as both the subject and not the subject", name, url))
			}
		}
	}

	return joinErrors(problems)
}

// Topic returns one subject's labels by name.
func (t *Topics) Topic(name string) (*TopicLabels, bool) {
	for _, one := range t.Topics {
		if one.Name == name {
			return one, true
		}
	}
	return nil, false
}
