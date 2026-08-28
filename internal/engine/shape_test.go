// SPDX-License-Identifier: GPL-3.0-or-later

package engine_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/rangertaha/scour/internal/engine"
)

// A shape is what the extractor will do, not what the document happened to
// write. These hold the two places that kept confusing the two.

// shaped is a job whose items and properties lean on the defaults.
const shaped = `
job "news" {
  domains = ["example.com"]
  start   = ["https://example.com/"]

  item "article" {
    property "title" {}

    property "body" {
      property "text" {}
    }
  }
}
`

// TestWritingOutADefaultIsNotAChange.
//
// The fingerprint used the raw Type field while every reader of it used the
// defaulted ItemType/PropertyType, so making a default explicit read as a
// schema change: Diff reported `item.article: schema -> changed`,
// EffectReextract made it costly, and the default mutation policy refused the
// resubmission of a document that would extract exactly the same records.
// Writing a default out to be explicit is a thing people do to documents.
func TestWritingOutADefaultIsNotAChange(t *testing.T) {
	implicit, err := engine.Parse([]byte(shaped), "job.hcl")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	explicit, err := engine.Parse([]byte(strings.Replace(shaped,
		`property "title" {}`,
		"property \"title\" {\n      type = str\n    }", 1)), "job.hcl")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if changes := engine.Diff(implicit.Jobs[0], explicit.Jobs[0]); changes.Any() {
		t.Errorf("writing out the default type reads as %d change(s): %v", len(changes), changes)
	}
	if review := explicit.Jobs[0].Review(implicit.Jobs[0]); !review.OK() {
		t.Errorf("and resubmitting it would be refused: %v", review.Refused)
	}
}

// TestASpecStatesEveryType.
//
// The spec is the whole of what a spider in another language is handed, so a
// value it leaves out is one that reader cannot recover: it would have to know
// scour's defaulting rules to read a document whose point is that it does not
// need them. An object property was the worst of it, because its type is
// inferred from having children and nothing in the spec said so.
//
// Walked rather than spot-checked, so a renderer that starts dropping a
// resolved value somewhere else fails here too.
func TestASpecStatesEveryType(t *testing.T) {
	doc, err := engine.Parse([]byte(shaped), "job.hcl")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	spec, err := engine.ParseSpec(doc.Jobs[0].Spec().HCL(), "spec.hcl")
	if err != nil {
		t.Fatalf("the rendered spec does not parse: %v", err)
	}

	var checked int
	var walk func(where string, props []*engine.Property)
	walk = func(where string, props []*engine.Property) {
		for _, p := range props {
			checked++
			if p.Type == "" {
				t.Errorf("%s.%s has no type in the spec, so a spider in another "+
					"language cannot know what it is", where, p.Name)
			}
			walk(where+"."+p.Name, p.Properties)
		}
	}

	for _, item := range spec.Items {
		checked++
		if item.Type == "" {
			t.Errorf("item %q has no type in the spec", item.Name)
		}
		walk(item.Name, item.Properties)
	}

	if checked == 0 {
		t.Fatal("the spec has no items or properties, so this check is not checking anything")
	}
}

// TestAnUnusableScopeIsRefusedAtSubmission.
//
// Validation used to stay quiet about this, on the grounds that the scheduler
// reports it when it builds one. That held while the only way to run a job was
// `scour crawl`, where validating and running are one command a second apart.
//
// A cluster broke it. `scour job create` validates and stores; the scheduler is
// not built until `scour job start`, which may be days later and on another
// machine. So a document with an unusable pattern was accepted, stored, and
// listed as a healthy job nobody had started yet, and the reason it could never
// start was a round trip away.
func TestAnUnusableScopeIsRefusedAtSubmission(t *testing.T) {
	doc, err := engine.Parse([]byte(`
job "news" {
  domains  = ["example.com"]
  included = ["*/products[0-9]*"]
  start    = ["https://example.com/"]

  item "article" {
    property "title" {}
  }
}
`), "job.hcl")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	err = doc.Validate()
	if err == nil {
		t.Fatal("a document whose scope cannot be built was accepted")
	}
	if !strings.Contains(err.Error(), "news") {
		t.Errorf("the refusal does not name the job: %v", err)
	}
}

// TestAPropertyNamingAnEntityKindIsOne.
//
// `entity` is not a modifier on some other type, it is what the property is,
// and a document that names a kind and no type used to resolve as a string:
// validation accepted it, the entities step skipped it, and the record filed it
// as an ordinary field. The operator wrote down which kind of thing the value
// referred to and nothing ever resolved it, silently. Validation's message for
// the neighbouring case says what goes wrong in so many words - "so nothing
// would resolve it" - and this variant did it without saying anything.
//
// The nested case is the one the book documents: a reference is a name that
// refers to something, and its children describe the thing referred to rather
// than the item, which is how `author.role` is the person's role. Reading
// "object" off the children took that away from the shape it was added for.
func TestAPropertyNamingAnEntityKindIsOne(t *testing.T) {
	for name, body := range map[string]string{
		"on its own": `property "author" {
      entity = "person"
    }`,
		"with children, which describe the thing referred to": `property "author" {
      entity = "person"

      property "role" {
        type = str
      }
    }`,
	} {
		t.Run(name, func(t *testing.T) {
			doc, err := engine.Parse([]byte(`
job "news" {
  domains = ["example.com"]
  start   = ["https://example.com/"]

  item "article" {
    `+body+`
  }
}
`), "job.hcl")
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if err := doc.Validate(); err != nil {
				t.Fatalf("validate: %v", err)
			}

			p := doc.Jobs[0].Items[0].Properties[0]
			if got := p.PropertyType(); got != engine.TypeEntity {
				t.Errorf("a property naming the entity kind %q resolves as %q, "+
					"so nothing will ever resolve it", p.Entity, got)
			}

			// And every reader agrees, because each used to ask the raw field
			// its own way. An entity reference is a dimension, not a
			// measurement, and filing it under Fields is how it reaches a
			// time-series store as a value nobody can group by.
			// A reference with children contributes its children under dotted
			// names, which is why this asks for the prefix rather than the
			// bare name.
			item := doc.Jobs[0].Items[0]
			tagged := slices.ContainsFunc(item.Tags(), func(t string) bool {
				return t == "author" || strings.HasPrefix(t, "author.")
			})
			if !tagged {
				t.Errorf("an entity reference is not among the item's tags: %v", item.Tags())
			}
			for _, field := range item.Fields() {
				if field == "author" || strings.HasPrefix(field, "author.") {
					t.Errorf("an entity reference is filed as a measurement: %v", item.Fields())
				}
			}
		})
	}
}
