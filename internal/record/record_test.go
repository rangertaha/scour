// SPDX-License-Identifier: GPL-3.0-or-later

package record_test

import (
	"strings"
	"testing"
	"time"

	"github.com/rangertaha/scour/internal/engine"
	"github.com/rangertaha/scour/internal/extract"
	"github.com/rangertaha/scour/internal/record"
)

var fetched = time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)

// TestNestedValuesAreFlattened. A CSV cannot express a tree and a pipeline step
// that had to walk one would be walking one in every implementation.
func TestNestedValuesAreFlattened(t *testing.T) {
	items := []*extract.Item{{
		Name: "article",
		Values: map[string]*extract.Value{
			"title": {Text: "One"},
			"author": {
				Text: "Alex Doe",
				Nested: map[string]*extract.Value{
					"name":    {Text: "Alex Doe"},
					"profile": {Text: "https://example.com/alex"},
				},
			},
		},
	}}

	records := record.From("https://example.com/a", "abc123", fetched, items)
	if len(records) != 1 {
		t.Fatalf("got %d records", len(records))
	}

	r := records[0]
	for name, want := range map[string]string{
		"title":          "One",
		"author":         "Alex Doe",
		"author.name":    "Alex Doe",
		"author.profile": "https://example.com/alex",
	} {
		if got := r.Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}

	if got := strings.Join(r.Names(), " "); got != "author author.name author.profile title" {
		t.Errorf("Names() = %q, want them sorted", got)
	}
}

// TestARecordCarriesWhichShapeItWasReadUnder, or a resubmission that changes
// the shape cannot find the stale ones.
func TestARecordCarriesWhichShapeItWasReadUnder(t *testing.T) {
	records := record.From("https://example.com/a", "fingerprint", fetched,
		[]*extract.Item{{Name: "article", Values: map[string]*extract.Value{"title": {Text: "One"}}}})

	r := records[0]
	if r.Spec != "fingerprint" || r.URL != "https://example.com/a" || !r.Fetched.Equal(fetched) {
		t.Errorf("record = %+v", r)
	}
}

// TestACloneIsACopy, because a pipeline step edits one rather than what it was
// handed.
func TestACloneIsACopy(t *testing.T) {
	r := &record.Record{Item: "article", Values: map[string]string{"title": "One"}}

	clone := r.Clone()
	clone.Values["title"] = "Two"
	clone.Item = "price"

	if r.Get("title") != "One" || r.Item != "article" {
		t.Errorf("the original changed: %+v", r)
	}

	// A record with no values at all still clones into something writable.
	empty := (&record.Record{}).Clone()
	empty.Values["a"] = "b"
}

// TestAMeasurementSplitsWhatTheShapeAlreadySaid. Entity references are the tags
// because entities are bounded by definition; free text never is.
func TestAMeasurementSplitsWhatTheShapeAlreadySaid(t *testing.T) {
	doc, err := engine.Parse([]byte(`
job "markets" {
  start = ["https://example.com/"]

  item "price" {
    of   = "company"
    time = "observed"

    property "value" {
      type = float
    }

    property "currency" {
      type = str
      tag  = true
    }

    property "observed" {
      type = date
    }

    relation "exchange" {
      entity   = "exchange"
      property = self.domain
    }
  }
}
`), "job.hcl")
	if err != nil {
		t.Fatal(err)
	}
	if err := doc.Validate(); err != nil {
		t.Fatal(err)
	}
	shape := doc.Jobs[0].Items[0]

	r := &record.Record{
		Item: "price", URL: "https://example.com/p", Fetched: fetched,
		Values: map[string]string{
			"value":    "178.23",
			"currency": "gbp",
			"observed": "2026-08-04T09:15:00Z",
			"of":       "acme",
			"exchange": "lse",
		},
	}

	m := r.Measure(shape)
	if m.Name != "price" {
		t.Errorf("name = %q", m.Name)
	}
	for name, want := range map[string]string{"of": "acme", "exchange": "lse", "currency": "gbp"} {
		if m.Tags[name] != want {
			t.Errorf("tag %s = %q, want %q", name, m.Tags[name], want)
		}
	}
	if m.Fields["value"] != "178.23" {
		t.Errorf("fields = %v", m.Fields)
	}
	if _, tagged := m.Tags["value"]; tagged {
		t.Error("a measurement became a dimension")
	}

	// Event time, never ingest time. A headline published at nine and crawled
	// at half eleven is an event at nine.
	if !m.Time.Equal(time.Date(2026, 8, 4, 9, 15, 0, 0, time.UTC)) {
		t.Errorf("time = %s, want the item's own", m.Time)
	}
}

// TestWithoutATimeTheFetchIsAllThereIs, which is worth being explicit about
// rather than quietly inventing one.
func TestWithoutATimeTheFetchIsAllThereIs(t *testing.T) {
	shape := &engine.Item{Name: "thing"}

	r := &record.Record{Item: "thing", Fetched: fetched, Values: map[string]string{"a": "b"}}
	if got := r.Measure(shape).Time; !got.Equal(fetched) {
		t.Errorf("time = %s, want the fetch time", got)
	}

	// And with no shape at all, everything is a field: nothing has said which
	// of them are dimensions, so claiming any of them are would be a guess.
	m := r.Measure(nil)
	if len(m.Tags) != 0 || m.Fields["a"] != "b" {
		t.Errorf("measurement = %+v", m)
	}
}

// TestAMeasurementCarriesNestedProperties.
//
// An object property has no value of its own: a record from `property "author"
// { property "name" {} }` carries `author.name` and never `author`. Tags and
// Fields named the parent, so nothing matched, and the measurement had no
// author in it at all, while the csv, parquet and sqlite exports of the same
// record each had an `author.name` column. The nats exporter is built on
// measurements, so every nested property a job declared was silently absent
// from what it published: the stream and the files disagreed about what the
// crawl had found.
func TestAMeasurementCarriesNestedProperties(t *testing.T) {
	doc, err := engine.Parse([]byte(`
job "news" {
  domains = ["example.com"]
  start   = ["https://example.com/"]

  item "article" {
    property "title" {
      type = str
    }

    property "author" {
      type = object

      property "name" {
        type = str
      }
    }
  }
}
`), "job.hcl")
	if err != nil {
		t.Fatal(err)
	}
	if err := doc.Validate(); err != nil {
		t.Fatal(err)
	}
	shape := doc.Jobs[0].Items[0]

	r := &record.Record{
		Item: "article", URL: "https://example.com/a", Spec: "abc",
		Fetched: time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC),
		Values:  map[string]string{"title": "One", "author.name": "Alex Doe"},
	}

	m := r.Measure(shape)
	if got := m.Fields["author.name"]; got != "Alex Doe" {
		t.Errorf("Fields[author.name] = %q, and a nested property was dropped: %v", got, m.Fields)
	}
	if got := m.Fields["title"]; got != "One" {
		t.Errorf("Fields[title] = %q", got)
	}
}

// TestARecordCarriesEveryDepthOfNesting.
//
// An extracted value's Nested holds values that have a Nested of their own, and
// engine.Item.Fields flattens a property tree of any depth, so a shape may name
// `author.address.city`. From flattened exactly one level, so a property three
// deep was extracted, reported found by the fill-rate, named by Fields, and
// never reached the record: the measurement came back with no field for it at
// all, and the csv, parquet and sqlite exports wrote an empty column.
//
// The depth-2 case was fixed once already, at depth 2 only. See
// TestAMeasurementCarriesNestedProperties.
func TestARecordCarriesEveryDepthOfNesting(t *testing.T) {
	records := record.From("https://example.com/a", "abc123", fetched, []*extract.Item{{
		Name: "article",
		Values: map[string]*extract.Value{
			"author": {
				Text: "Alex Doe",
				Nested: map[string]*extract.Value{
					"address": {
						Text: "Leeds",
						Nested: map[string]*extract.Value{
							"city":     {Text: "Leeds"},
							"postcode": {Text: "LS1 1AA"},
						},
					},
				},
			},
		},
	}})

	for name, want := range map[string]string{
		"author":                  "Alex Doe",
		"author.address":          "Leeds",
		"author.address.city":     "Leeds",
		"author.address.postcode": "LS1 1AA",
	} {
		if got := records[0].Values[name]; got != want {
			t.Errorf("Values[%q] = %q, want %q: %v", name, got, want, records[0].Values)
		}
	}
}
