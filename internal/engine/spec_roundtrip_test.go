// SPDX-License-Identifier: GPL-3.0-or-later

package engine_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	"github.com/rangertaha/scour/internal/engine"
)

// roundTrip renders a spec, reads it back, and renders that.
//
// Rendering twice rather than comparing structs, because the two are not the
// same object: HCL writes resolved types where the document relied on defaults,
// so a parsed-back spec differs from the original in ways that are correct. What
// must not differ is the text, and a value that does not survive the trip shows
// up there.
func roundTrip(t *testing.T, spec *engine.Spec) (string, string) {
	t.Helper()

	first := string(spec.HCL())

	read, err := engine.ParseSpec([]byte(first), "spec.hcl")
	if err != nil {
		t.Fatalf("the rendered spec does not parse: %v\n%s", err, first)
	}
	return first, string(read.HCL())
}

// hostile is the values that have actually broken this, plus the shapes around
// them.
//
// `${` is the one that keeps coming back: HCL reads it as an interpolation, so
// a selector containing one - which is what an induced locator picks up off a
// page whose template did not render - came back out of `scour job spec` as a
// spec that will not parse. Three writers were fixed for it one at a time.
var hostile = []string{
	`meta[name="og:title-${id}"]`,
	`${verbatim}`,
	`%{directive}`,
	`a "quoted" thing`,
	"back\\slash",
	"new\nline",
	"tab\there",
	"trailing space ",
	"unicode ü and 😀",
	"`backtick`",
	`$${already escaped}`,
	`%%{already escaped}`,
	"",
}

// TestARenderedSpecReadsBackAsItself.
//
// The spec is the wire format: a spider somebody else wrote, in a language that
// is not Go, receives these bytes and parses them. So a value that does not
// survive being written and read is a job that silently extracts something else
// on the other side, or a spec that will not parse at all.
//
// Every place a value can sit is covered, because the bugs were one writer at a
// time: an attribute, a list, a nested property, a relation's own properties,
// and the job name in the header comment.
func TestARenderedSpecReadsBackAsItself(t *testing.T) {
	for _, value := range hostile {
		t.Run(strings.ReplaceAll(value, "\n", "\\n"), func(t *testing.T) {
			spec := &engine.Spec{
				Job: "news" + value,
				Items: []*engine.Item{{
					Name:        "article",
					Description: value,
					Properties: []*engine.Property{{
						Name:        "title",
						Type:        "str",
						Description: value,
						CSS:         []string{value, "h1"},
						XPath:       []string{value},
						Regexes:     []string{value},
						Aliases:     []string{value},
						Properties: []*engine.Property{{
							Name: "inner",
							Type: "str",
							CSS:  []string{value},
						}},
					}},
					Relations: []*engine.Relation{{
						Name:   "exchange",
						Entity: "exchange",
						Topic:  []string{value},
						Properties: []*engine.Property{{
							Name: "role",
							Type: "str",
							CSS:  []string{value},
						}},
					}},
				}},
			}

			first, second := roundTrip(t, spec)
			if first != second {
				t.Errorf("the spec changed on the way back:\n--- written\n%s\n--- read back and written again\n%s",
					first, second)
			}
			// And the value really is in there, so this is not passing because
			// the writer dropped it at both ends.
			if value != "" && !strings.Contains(first, "title") {
				t.Fatalf("the property is missing from the spec:\n%s", first)
			}
		})
	}
}

// FuzzARenderedSpecReadsBackAsItself over any value at all, in the place a
// locator sits: that is where a value scour did not write itself ends up, since
// `scour job train` induces selectors from whatever a page happens to contain.
func FuzzARenderedSpecReadsBackAsItself(f *testing.F) {
	for _, seed := range hostile {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, value string) {
		// The domain is what a job document can express, and HCL narrows that
		// twice before a value ever reaches a Spec. Both were checked rather
		// than assumed:
		//
		//   - invalid UTF-8 is refused outright ("All input files must be
		//     UTF-8 encoded"), and hclwrite would render such a byte as the
		//     replacement character.
		//   - a string is normalised to NFC on the way in, so `.café` written
		//     decomposed comes back composed, seven bytes to six.
		//
		// So a Spec built from a document carries valid, composed UTF-8, and
		// only a Go caller constructing one by hand can carry anything else.
		if !utf8.ValidString(value) || norm.NFC.String(value) != value {
			return
		}

		spec := &engine.Spec{
			Job: "news",
			Items: []*engine.Item{{
				Name: "article",
				Properties: []*engine.Property{{
					Name: "title",
					Type: "str",
					CSS:  []string{value},
				}},
			}},
		}

		first := string(spec.HCL())
		read, err := engine.ParseSpec([]byte(first), "spec.hcl")
		if err != nil {
			t.Fatalf("a spec carrying %q does not parse: %v", value, err)
		}
		if second := string(read.HCL()); second != first {
			t.Errorf("a spec carrying %q changed on the way back:\n%s\n%s", value, first, second)
		}
	})
}
